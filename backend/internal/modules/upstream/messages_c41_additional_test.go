package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessagesC41AuthMissingFieldsAndSerialization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record map[string]any
		want   string
	}{
		{"no type", map[string]any{}, ""},
		{"non-string type", map[string]any{"type": true}, ""},
		{"OAuth", map[string]any{"type": "oauth"}, ""},
		{"case differs", map[string]any{"type": "APIKEY"}, ""},
		{"no extra", map[string]any{"type": "apikey"}, "x_api_key"},
		{"no scheme", map[string]any{"type": "apikey", "extra": map[string]any{}}, "x_api_key"},
		{"malformed extra", map[string]any{"type": "apikey", "extra": true}, "x_api_key"},
		{"malformed scheme", map[string]any{"type": "apikey", "extra": map[string]any{"anthropic_apikey_auth_scheme": true}}, "x_api_key"},
		{"trimmed bearer", map[string]any{"type": " apikey ", "extra": map[string]any{"anthropic_apikey_auth_scheme": " authorization_bearer "}}, "authorization_bearer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sub2APIAnthropicAuth(tc.record); got != tc.want {
				t.Errorf("auth=%q want=%q", got, tc.want)
			}
		})
	}
	raw, err := json.Marshal(ProbeCredential{AnthropicAuth: "authorization_bearer"})
	if err != nil || strings.Contains(string(raw), "AnthropicAuth") || strings.Contains(string(raw), "authorization_bearer") {
		t.Fatalf("ephemeral auth exposed: %s err=%v", raw, err)
	}
}
