package settings

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

const (
	smtpDialTimeout    = 10 * time.Second
	smtpOverallTimeout = 15 * time.Second
	smtpTestSubject    = "TransitHub SMTP Test"
	smtpTestHTMLBody   = "<p>TransitHub SMTP test email was sent successfully.</p>"
)

// smtpSendConfig 承载测试邮件发送所需的全部参数，全部来自已保存配置和固定测试邮件内容；
// 不接受任何来自请求体的 SMTP host/port/password（由 service 层保证，见 TestSMTPEmail）。
type smtpSendConfig struct {
	Host           string
	Port           int
	Username       string
	Password       string
	TLSMode        SmtpTLSMode
	FromEmail      string
	FromName       string
	RecipientEmail string
	Subject        string
	HTMLBody       string
}

// smtpSender 是发送测试邮件的抽象接口，便于测试注入本地 fake SMTP server。
type smtpSender interface {
	Send(ctx context.Context, cfg smtpSendConfig) error
}

// netSMTPSender 是基于标准库 net/smtp 的生产实现。
// rootCAs 仅供包内测试注入自签根证书池；生产构造路径 newNetSMTPSender 保持为 nil，
// 即使用系统根证书，不允许跳过证书校验（InsecureSkipVerify 恒为 false）。
type netSMTPSender struct {
	rootCAs *x509.CertPool
}

// newNetSMTPSender 是生产环境使用的构造函数：不接受自定义根证书池。
func newNetSMTPSender() *netSMTPSender {
	return &netSMTPSender{}
}

func (s *netSMTPSender) Send(ctx context.Context, cfg smtpSendConfig) error {
	if err := validateSMTPHeaderValue(cfg.FromName); err != nil {
		return err
	}
	if err := validateSMTPHeaderValue(cfg.FromEmail); err != nil {
		return err
	}
	if err := validateSMTPHeaderValue(cfg.RecipientEmail); err != nil {
		return err
	}
	if err := validateSMTPHeaderValue(cfg.Subject); err != nil {
		return err
	}
	if cfg.Username == "" && cfg.Password != "" {
		return ErrSMTPMissingConfig
	}

	deadline := time.Now().Add(smtpOverallTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	// ServerName 固定来自 host，不从 from email 推导；两种 TLS 模式都强制 TLS 1.2+ 和系统根证书校验。
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.Host,
		RootCAs:            s.rootCAs,
		InsecureSkipVerify: false,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: smtpDialTimeout}

	var conn net.Conn
	var err error
	if cfg.TLSMode == SmtpTLSModeImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("%w: dial: %v", ErrSMTPSendFailed, err)
	}
	defer conn.Close()
	// 连接级 deadline 覆盖 TLS 握手、SMTP 命令和整体发送，Go 标准库 net/smtp 没有原生 context 支持。
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("%w: set deadline: %v", ErrSMTPSendFailed, err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("%w: smtp handshake: %v", ErrSMTPSendFailed, err)
	}
	defer client.Close()

	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("%w: EHLO: %v", ErrSMTPSendFailed, err)
	}

	if cfg.TLSMode == SmtpTLSModeStarttls {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("%w: server does not support STARTTLS", ErrSMTPSendFailed)
		}
		// client.StartTLS 内部会在升级成功后自动丢弃升级前的 capability 状态并重新执行 EHLO
		// （见 Go 标准库 net/smtp 实现），满足升级后才能 AUTH/MAIL/RCPT/DATA 的要求。
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("%w: STARTTLS: %v", ErrSMTPSendFailed, err)
		}
	}

	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("%w: authentication: %v", ErrSMTPSendFailed, err)
		}
	}

	if err := client.Mail(cfg.FromEmail); err != nil {
		return fmt.Errorf("%w: MAIL FROM: %v", ErrSMTPSendFailed, err)
	}
	if err := client.Rcpt(cfg.RecipientEmail); err != nil {
		return fmt.Errorf("%w: RCPT TO: %v", ErrSMTPSendFailed, err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: DATA: %v", ErrSMTPSendFailed, err)
	}
	if _, err := writer.Write(buildSMTPTestMessage(cfg)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("%w: write message: %v", ErrSMTPSendFailed, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: finalize message: %v", ErrSMTPSendFailed, err)
	}

	// QUIT 失败不影响邮件已经通过 DATA 阶段成功发送的事实，不作为失败处理。
	_ = client.Quit()
	return nil
}

// validateSMTPHeaderValue 拒绝任何包含 CRLF 的值，防止邮件头注入；直接拒绝而不是替换，
// 避免静默篡改用户配置。
func validateSMTPHeaderValue(value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return ErrSMTPValidation
	}
	return nil
}

// buildSMTPTestMessage 构造 HTML 邮件。Subject/HTMLBody 为空时回退到固定 SMTP 测试内容，
// 保持既有 /api/settings/smtp/test-email 的静态测试邮件合同不变。
// 标题和显示名称按字符边界分段做 RFC 2047 B 编码，正文做 base64 编码，并补齐标准邮件头。
// 头部的值在 Send 中已经过 validateSMTPHeaderValue 的 CRLF 检查。
func buildSMTPTestMessage(cfg smtpSendConfig) []byte {
	subjectText := cfg.Subject
	if subjectText == "" {
		subjectText = smtpTestSubject
	}
	htmlBody := cfg.HTMLBody
	if htmlBody == "" {
		htmlBody = smtpTestHTMLBody
	}

	var b strings.Builder
	b.WriteString("From: " + formatFromAddress(cfg.FromName, cfg.FromEmail) + "\r\n")
	b.WriteString("To: " + (&mail.Address{Address: cfg.RecipientEmail}).String() + "\r\n")
	b.WriteString("Subject: " + encodeHeaderText(subjectText) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + newMessageID(cfg.FromEmail) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	for len(encoded) > base64LineLength {
		b.WriteString(encoded[:base64LineLength] + "\r\n")
		encoded = encoded[base64LineLength:]
	}
	b.WriteString(encoded + "\r\n")
	return []byte(b.String())
}

const (
	// base64LineLength 是正文 base64 每行的字符数（MIME 规定不超过 76）。
	base64LineLength = 76
	// headerWordBytes 是标题每个编码段最多装的原文字节数：base64 后 48 个字符，加上 =?UTF-8?B?…?= 共 60 个，
	// 每个编码段不超过 RFC 2047 规定的 75 个字符。
	headerWordBytes = 36
)

// encodeHeaderText 把头部文字编码成 RFC 2047 的 B 编码段，一段一行（折行续写）。纯 ASCII 原样返回。
// 切分只在字符边界上，不会把一个中文字拆到两段里。
func encodeHeaderText(text string) string {
	if isPrintableASCII(text) {
		return text
	}
	var words []string
	chunk := make([]byte, 0, headerWordBytes)
	for _, r := range text {
		encoded := string(r)
		if len(chunk) > 0 && len(chunk)+len(encoded) > headerWordBytes {
			words = append(words, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString(chunk)+"?=")
			chunk = chunk[:0]
		}
		chunk = append(chunk, encoded...)
	}
	if len(chunk) > 0 {
		words = append(words, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString(chunk)+"?=")
	}
	return strings.Join(words, "\r\n ")
}

// formatFromAddress 写发件人：名字是纯 ASCII 时交给 net/mail（它会按需加引号）；有中文时用 encodeHeaderText，
// 避免编码后挤在一行里过长。
func formatFromAddress(name string, address string) string {
	if name == "" || isPrintableASCII(name) {
		return (&mail.Address{Name: name, Address: address}).String()
	}
	return encodeHeaderText(name) + " <" + address + ">"
}

func isPrintableASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] > 0x7e {
			return false
		}
	}
	return true
}

// newMessageID 生成 <随机数@发件域名> 形式的 Message-ID。
func newMessageID(fromEmail string) string {
	domain := "transithub.invalid"
	if at := strings.LastIndex(fromEmail, "@"); at >= 0 && at < len(fromEmail)-1 {
		domain = fromEmail[at+1:]
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	return "<" + hex.EncodeToString(random) + "@" + domain + ">"
}
