package settings

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeStrategySettingsKeepsLegacyTemplatesAsText(t *testing.T) {
	settings := normalizeStrategySettings(StrategySettings{
		RefreshInterval:          minRefreshIntervalSeconds,
		BalanceTemplateFormat:    "unsupported",
		MultiplierTemplateFormat: "",
	})
	if settings.BalanceTemplateFormat != NotificationTemplateFormatText {
		t.Fatalf("expected invalid balance format to fall back to text, got %q", settings.BalanceTemplateFormat)
	}
	if settings.MultiplierTemplateFormat != NotificationTemplateFormatText {
		t.Fatalf("expected missing multiplier format to fall back to text, got %q", settings.MultiplierTemplateFormat)
	}
}

func TestTelegramHTMLNotificationPreservesFormattingAndDropsUnsafeContent(t *testing.T) {
	telegramHTML := telegramHTMLForChannel(`<script>alert(1)</script><p onclick="alert(1)"><strong>余额</strong> <a href="https://example.com">详情</a><a href="javascript:alert(1)">危险</a></p>`)
	if !strings.Contains(telegramHTML, "<b>余额</b>") || !strings.Contains(telegramHTML, `<a href="https://example.com">详情</a>`) {
		t.Fatalf("expected supported Telegram HTML formatting, got %q", telegramHTML)
	}
	if strings.Contains(telegramHTML, "onclick") || strings.Contains(telegramHTML, "javascript:") || strings.Contains(telegramHTML, "alert(1)") {
		t.Fatalf("unsafe Telegram HTML attributes must be removed: %q", telegramHTML)
	}
}

func TestTelegramNotificationsKeepTextMarkdownAndHTMLPayloads(t *testing.T) {
	for _, tc := range []struct {
		format        NotificationTemplateFormat
		content, mode string
	}{
		{NotificationTemplateFormatText, "original text", ""},
		{NotificationTemplateFormatMarkdown, "**original text**", "Markdown"},
		{NotificationTemplateFormatHTML, "<strong>original text</strong>", "HTML"},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			var payload map[string]string
			service := NewService(&http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.telegram.org" || r.URL.Path != "/botfixture/sendMessage" {
					t.Fatal("unexpected notification endpoint")
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
			})}, nil)
			if err := service.sendTelegramMessage(context.Background(), "fixture", "original-chat", "", notificationMessage{Content: tc.content, Format: tc.format}); err != nil {
				t.Fatal(err)
			}
			expected := tc.content
			if tc.format == NotificationTemplateFormatHTML {
				expected = "<b>original text</b>"
			}
			if payload["chat_id"] != "original-chat" || payload["text"] != expected || payload["parse_mode"] != tc.mode {
				t.Fatal("Telegram message or format changed")
			}
		})
	}
}
