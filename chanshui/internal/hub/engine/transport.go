package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

const MaxResponseBytes = 8 << 20

var ErrResponseTooLarge = errors.New("上游响应超过原版8MiB上限，本题未完成；已停止读取，不使用截断答案判分")

type Credential struct {
	BaseURL       string `json:"baseURL"`
	Key           string `json:"key"`
	Protocol      string `json:"protocol"`
	AnthropicAuth string `json:"anthropicAuth"`
}
type Deployment struct {
	DisableBankSync     bool `json:"disableBankSync,omitempty"`
	AllowPrivateTargets bool `json:"allowPrivateTargets"`
}
type Transport struct {
	Base          http.RoundTripper
	sem           chan struct{}
	Attempts      atomic.Int64
	MaxAttempts   int
	BeforeAttempt func(context.Context) error
	attemptMu     sync.Mutex
}
type limitedBody struct {
	io.ReadCloser
	left    int
	release func()
	closed  atomic.Bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		var one [1]byte
		n, e := b.ReadCloser.Read(one[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, e
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= n
	return n, e
}
func (b *limitedBody) Close() error {
	e := b.ReadCloser.Close()
	if b.closed.CompareAndSwap(false, true) {
		b.release()
	}
	return e
}
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case t.sem <- struct{}{}:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	release := func() { <-t.sem }
	if err := r.Context().Err(); err != nil {
		release()
		return nil, err
	}
	// Admission and the durable per-item count are one ordered step. HTTP runs
	// outside this lock, so a slow response never blocks the next free slot.
	t.attemptMu.Lock()
	if err := r.Context().Err(); err != nil {
		t.attemptMu.Unlock()
		release()
		return nil, err
	}
	if t.MaxAttempts > 0 && t.Attempts.Load() >= int64(t.MaxAttempts) {
		t.attemptMu.Unlock()
		release()
		return nil, fmt.Errorf("已达到本次%d次上游尝试上限，未再发送请求", t.MaxAttempts)
	}
	if t.BeforeAttempt != nil {
		if e := t.BeforeAttempt(r.Context()); e != nil {
			t.attemptMu.Unlock()
			release()
			return nil, e
		}
	}
	t.Attempts.Add(1)
	t.attemptMu.Unlock()
	res, e := t.Base.RoundTrip(r)
	if e != nil {
		release()
		return nil, e
	}
	res.Body = &limitedBody{ReadCloser: res.Body, left: MaxResponseBytes, release: release}
	return res, nil
}
func publicIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func NewTransport(concurrency, maxAttempts int, allowPrivate bool, override http.RoundTripper) (*Transport, *http.Transport) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	concurrency = max(1, min(concurrency, 64))
	tr.MaxIdleConns = concurrency
	tr.MaxIdleConnsPerHost = concurrency
	tr.MaxConnsPerHost = concurrency
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("目标地址没有可用DNS结果")
		}
		for _, ip := range ips {
			if !allowPrivate && !publicIP(ip) {
				return nil, fmt.Errorf("不允许访问本地、内网或保留地址；如为隔离本地验收需显式配置可信测试开关")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	base := http.RoundTripper(tr)
	if override != nil {
		base = override
	}
	return &Transport{Base: base, sem: make(chan struct{}, concurrency), MaxAttempts: maxAttempts}, tr
}
func validateCredential(c Credential) error {
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("上游地址无效，请核对账号的HTTP/HTTPS API地址，地址不可包含用户名或查询凭据")
	}
	if strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("账号没有可用API密钥，未发送检测请求")
	}
	if c.Protocol != "chat" && c.Protocol != "anthropic" && c.Protocol != "openai" {
		return fmt.Errorf("账号协议不受支持，未发送请求")
	}
	return nil
}
func client(c Credential, t *Transport, retries int) gateway.Client {
	return gateway.Client{BaseURL: c.BaseURL, Key: c.Key, Mode: c.Protocol, DefaultEffort: "medium", RetryLimit: &retries, AnthropicAuth: c.AnthropicAuth, DiscardSSEFrames: false, PreserveErrorReason: true, HTTP: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("上游重定向已阻止，请核对账号API地址")
	}}}
}
func Models(ctx context.Context, c Credential, deployment Deployment, override http.RoundTripper, options ...store.Settings) ([]string, error) {
	if e := validateCredential(c); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	t, tr := NewTransport(1, 0, deployment.AllowPrivateTargets, override)
	defer tr.CloseIdleConnections()
	settings := store.DefaultSettings()
	if len(options) > 0 {
		settings = options[0]
	}
	g := client(c, t, settings.Retries)
	if settings.Protocol != "" {
		g.Mode = settings.Protocol
	}
	ids, e := g.ListModels(ctx)
	if e != nil {
		return nil, fmt.Errorf("读取模型列表未完成：%s；请核对账号API地址和上游提示后重新读取", strings.ReplaceAll(e.Error(), c.Key, "[密钥已隐藏]"))
	}
	return ids, nil
}
