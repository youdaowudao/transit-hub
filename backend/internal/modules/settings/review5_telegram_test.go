package settings

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAbsorbReview5TelegramRedactionUsesOriginalLabels(t *testing.T) {
	for _, label := range []string{"api_key", "token", "password", "chat_id", "Bearer", "Basic"} {
		t.Run(label, func(t *testing.T) {
			target := absorbTelegramTarget()
			target.proxyURL = "http://" + label + ":fixture-proxy-password@proxy.fixture:8080"
			separator := "="
			if label == "Bearer" || label == "Basic" {
				separator = " "
			}
			const secret = "fixture-unregistered-credential"
			description := "明确拒绝：" + label + separator + secret + " 原因：目标暂不可用"
			masked := redactTelegramDescription(description, target)
			if strings.Contains(masked, secret) || !strings.Contains(masked, "原因：目标暂不可用") {
				t.Error("known credential masking broke generic label or safe cause")
			}
			body, _ := json.Marshal(map[string]any{"ok": false, "description": description})
			client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}
			target.proxyURL = ""
			target.botToken = label // Same collision through a known token, without any proxy network.
			result := NewService(client, nil).SendFrozenTelegramWithResult(t.Context(), target, "fixture", NotificationTemplateFormatText)
			if result.Status != DeliveryFailed || !result.Attempted || strings.Contains(result.Reason, secret) || !strings.Contains(result.Reason, "原因：目标暂不可用") {
				t.Error("public refusal lost safe cause or exposed generic credential")
			}
		})
	}
}

func TestAbsorbReview5TelegramRejectsAmbiguousConfirmation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"contradictory-ok", `{"ok":false,"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
		{"same-ok", `{"ok":true,"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
		{"case-ok", `{"OK":false,"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
		{"escaped-ok", `{"o\u006b":false,"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
		{"duplicate-result", `{"ok":true,"result":{"message_id":0},"result":{"message_id":7}}`, DeliveryUnknown},
		{"case-result", `{"ok":true,"Result":{"message_id":0},"result":{"message_id":7}}`, DeliveryUnknown},
		{"duplicate-message", `{"ok":true,"result":{"message_id":0,"message_id":7}}`, DeliveryUnknown},
		{"case-message", `{"ok":true,"result":{"MESSAGE_ID":0,"message_id":7}}`, DeliveryUnknown},
		{"normal-extra-fields", `{"ok":true,"result":{"message_id":7,"chat":{"id":1}},"future":1}`, DeliverySent},
		{"unrelated-repeated-fields", `{"ok":true,"result":{"message_id":7,"future":1,"future":2},"future":1,"future":2}`, DeliverySent},
		{"unrelated-nested-confirmation", `{"ok":true,"result":{"message_id":7,"future":{"message_id":0,"message_id":1}},"future":{"ok":false,"ok":true}}`, DeliverySent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			result := NewService(client, nil).SendFrozenTelegramWithResult(t.Context(), absorbTelegramTarget(), "fixture", NotificationTemplateFormatText)
			if result.Status != tc.want || !result.Attempted || calls != 1 {
				t.Errorf("status=%s attempted=%t calls=%d; want %s exactly once", result.Status, result.Attempted, calls, tc.want)
			}
		})
	}
}
