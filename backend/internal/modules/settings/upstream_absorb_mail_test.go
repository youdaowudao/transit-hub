package settings

import (
	"encoding/base64"
	"io"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAbsorbSMTPStandardsAndExactContent(t *testing.T) {
	for _, display := range []string{"TransitHub", "Sales, support", strings.Repeat("中文🙂服务", 20)} {
		t.Run(display[:min(len(display), 8)], func(t *testing.T) {
			subject := strings.Repeat("中文活动🙂", 30)
			body := strings.Repeat("<p>完整正文，保留空格 🙂</p>\n", 50)
			raw := buildSMTPTestMessage(smtpSendConfig{FromEmail: "mailer@example.com", FromName: display, RecipientEmail: "recipient@example.com", Subject: subject, HTMLBody: body})
			message, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || decoded != subject {
				t.Fatal("subject did not round-trip exactly")
			}
			from, err := mail.ParseAddress(message.Header.Get("From"))
			if err != nil || from.Address != "mailer@example.com" || from.Name != display {
				t.Fatal("sender address/display name changed")
			}
			if _, err := mail.ParseDate(message.Header.Get("Date")); err != nil {
				t.Fatal("missing or invalid Date header")
			}
			id := message.Header.Get("Message-ID")
			if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
				t.Fatal("missing standard Message-ID")
			}
			if message.Header.Get("Content-Transfer-Encoding") != "base64" {
				t.Fatal("body is not MIME base64")
			}
			content, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
			if err != nil || string(content) != body {
				t.Fatal("HTML body did not round-trip exactly")
			}
			parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
			for _, line := range strings.Split(parts[1], "\r\n") {
				if len(line) > 76 {
					t.Fatal("MIME body line exceeds 76 characters")
				}
			}
			for _, word := range strings.Fields(message.Header.Get("Subject")) {
				if len(word) > 75 {
					t.Fatal("encoded header word exceeds RFC length")
				}
				decoded, err := new(mime.WordDecoder).Decode(word)
				if err != nil || !utf8.ValidString(decoded) {
					t.Fatal("a header word split a Unicode character")
				}
			}
		})
	}
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		m, _ := mail.ReadMessage(strings.NewReader(string(buildSMTPTestMessage(smtpSendConfig{FromEmail: "mailer@example.com", RecipientEmail: "recipient@example.com"}))))
		id := m.Header.Get("Message-ID")
		if seen[id] {
			t.Fatal("Message-ID was reused")
		}
		seen[id] = true
	}
}

func TestAbsorbSMTPEncodedFromPhysicalLineLimits(t *testing.T) {
	for _, tc := range []struct{ name, display, address string }{
		{"twelve-three-byte-name", strings.Repeat("中", 12), "mailer@example.com"},
		{"ordinary-long-address", strings.Repeat("中", 12), strings.Repeat("a", 48) + "@example.com"},
		{"multiple-encoded-words", strings.Repeat("中", 36), "mailer@example.com"},
		{"unicode-chunk-boundary", strings.Repeat("中", 11) + "🙂" + strings.Repeat("文", 12), "mailer@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := strings.Repeat("活动🙂", 20)
			body := "<p>完整邮件正文</p>"
			raw := buildSMTPTestMessage(smtpSendConfig{FromName: tc.display, FromEmail: tc.address, RecipientEmail: "recipient@example.com", Subject: subject, HTMLBody: body})
			headers := strings.SplitN(string(raw), "\r\n\r\n", 2)[0]
			for _, line := range strings.Split(headers, "\r\n") {
				if strings.Contains(line, "=?UTF-8?B?") && len(line) > 76 {
					t.Errorf("physical line containing encoded-word has %d characters, want <=76: %q", len(line), line)
				}
			}
			message, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			from, err := mail.ParseAddress(message.Header.Get("From"))
			if err != nil || from.Name != tc.display || from.Address != tc.address {
				t.Fatal("folded From name/address did not round-trip")
			}
			decoded, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || decoded != subject {
				t.Fatal("folded subject did not round-trip")
			}
			for _, header := range []string{message.Header.Get("From"), message.Header.Get("Subject")} {
				for _, word := range strings.Fields(header) {
					if !strings.HasPrefix(word, "=?UTF-8?B?") {
						continue
					}
					if len(word) > 75 {
						t.Fatal("encoded-word exceeds75 characters")
					}
					decoded, err := new(mime.WordDecoder).Decode(word)
					if err != nil || !utf8.ValidString(decoded) {
						t.Fatal("encoded-word split a Unicode character")
					}
				}
			}
			content, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
			if err != nil || string(content) != body {
				t.Fatal("folded header changed MIME body")
			}
		})
	}
}
