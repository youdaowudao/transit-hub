package settings

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAbsorbReview4TelegramMasksOverlappingKnownValues(t *testing.T) {
	for _, tc := range []struct {
		name, token, chat, proxy, raw string
		fragments                     []string
	}{
		{"contained token first", "INNER:VALUE", "prefixINNER:VALUEsuffix", "", "prefixINNER:VALUEsuffix", []string{"prefix", "INNER:VALUE", "suffix"}},
		{"contained chat first", "prefixINNER:VALUEsuffix", "INNER:VALUE", "", "prefixINNER:VALUEsuffix", []string{"prefix", "INNER:VALUE", "suffix"}},
		{"crossing overlap", "abc", "cdef", "", "abcdef", []string{"abc", "cdef", "def"}},
		{"proxy password contains token", "INNER:VALUE", "fixture-chat", "http://fixture-user:prefixINNER%3AVALUEsuffix@proxy.fixture:8080", "prefixINNER:VALUEsuffix", []string{"prefix", "INNER:VALUE", "suffix"}},
		{"proxy password contains username", "fixture-token", "fixture-chat", "http://INNER:prefixINNERsuffix@proxy.fixture:8080", "prefixINNERsuffix", []string{"prefix", "INNER", "suffix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := TelegramTarget{ID: "fixture", Enabled: true, botToken: tc.token, chatID: tc.chat, proxyURL: tc.proxy}
			for _, encoded := range []string{tc.raw, url.QueryEscape(tc.raw), url.QueryEscape(url.QueryEscape(tc.raw))} {
				message := "明确拒绝：" + encoded + "；具体原因：目标暂不可用，请核查。"
				masked := redactTelegramDescription(message, target)
				for _, part := range tc.fragments {
					if strings.Contains(masked, part) {
						t.Errorf("overlapping credential remainder escaped masking: %q", part)
					}
				}
				if !strings.Contains(masked, "具体原因：目标暂不可用，请核查。") {
					t.Error("safe concrete refusal explanation was lost")
				}
				if tc.proxy == "" {
					body, _ := json.Marshal(map[string]any{"ok": false, "description": message})
					client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
					})}
					result := NewService(client, nil).SendFrozenTelegramWithResult(t.Context(), target, "fixture", NotificationTemplateFormatText)
					if result.Status != DeliveryFailed {
						t.Fatal("explicit refusal classification changed")
					}
					for _, part := range tc.fragments {
						if strings.Contains(result.Reason, part) {
							t.Errorf("public send result exposed overlapping credential remainder: %q", part)
						}
					}
				}
			}
		})
	}
}
