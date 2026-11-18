package api

import (
	"context"
	"github.com/kiss-kedaya/chanshui/internal/gateway"

	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"

	"sort"
	"strings"
	"time"
)

type boundedTransport struct {
	base http.RoundTripper
	sem  chan struct{}
}
type releasedBody struct {
	io.ReadCloser
	release func()
}

func (b *releasedBody) Close() error { err := b.ReadCloser.Close(); b.release(); return err }
func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.sem != nil {
		select {
		case t.sem <- struct{}{}:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	release := func() {
		if t.sem != nil {
			<-t.sem
		}
	}
	resp, e := t.base.RoundTrip(r)
	if e != nil {
		release()
		return nil, e
	}
	resp.Body = &releasedBody{resp.Body, release}
	return resp, nil
}
func publicIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func (s *Server) initTransport() {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxIdleConns = 256
	tr.MaxIdleConnsPerHost = 64
	tr.MaxConnsPerHost = 64
	tr.ResponseHeaderTimeout = 0
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if !s.opts.AllowPrivate && !publicIP(ip) {
				return nil, fmt.Errorf("不允许访问本地、内网或保留地址")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	s.transport = tr
	base := http.RoundTripper(tr)
	if s.opts.HTTP != nil && s.opts.HTTP.Transport != nil {
		base = s.opts.HTTP.Transport
	}
	s.httpClient = &http.Client{Transport: boundedTransport{base: base, sem: s.globalSem}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect blocked") }}
}
func (s *Server) client(q Request) (gateway.Client, error) {
	copy := *s.httpClient
	copy.Timeout = time.Duration(q.Timeout) * time.Second
	if q.Concurrency > 0 {
		copy.Transport = boundedTransport{base: copy.Transport, sem: make(chan struct{}, q.Concurrency)}
	}
	return gateway.Client{BaseURL: q.BaseURL, Key: q.Key, HTTP: &copy, Mode: q.Protocol, DefaultEffort: "medium"}, nil
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	var listed struct {
		Protocol string `json:"protocol,omitempty"`
		BaseURL  string `json:"base_url"`
		Key      string `json:"key"`
	}
	if !s.readJSON(w, r, &listed, true, false) {
		return
	}
	q := Request{Protocol: listed.Protocol, BaseURL: listed.BaseURL, Key: listed.Key}
	if e := s.validate(&q, false); e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	g, e := s.client(q)
	if e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	all, e := g.ListModels(r.Context())
	if e != nil {
		reply(w, 502, map[string]string{"error": e.Error()})
		return
	}
	ids := []string{}
	for _, id := range all {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	reply(w, 200, map[string]any{"models": ids, "upstream_count": len(all)})
}
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Request
		SingleAskInput
	}
	if !s.readJSON(w, r, &q, true, true) {
		return
	}
	if e := s.validate(&q.Request, true); e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	if e := ValidateSingleAsk(q.Model, &q.SingleAskInput); e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	g, e := s.client(q.Request)
	if e != nil {
		reply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	m, e := SingleAsk(r.Context(), g, q.Request, q.SingleAskInput)
	if e != nil {
		reply(w, 502, map[string]string{"error": redact(e.Error(), q.Key)})
		return
	}
	reply(w, 200, sanitize(m, q.Key))
}
