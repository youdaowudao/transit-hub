package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type c41CredentialTransport func(*http.Request) (*http.Response, error)

func (f c41CredentialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMessagesC41K8AccountTypeAndAuthFromExistingExport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  any
		extra any
		want  string
	}{
		{"apikey default", "apikey", nil, "x_api_key"},
		{"apikey bearer", "apikey", map[string]any{"anthropic_apikey_auth_scheme": "authorization_bearer"}, "authorization_bearer"},
		{"trimmed", " apikey ", map[string]any{"anthropic_apikey_auth_scheme": " authorization_bearer "}, "authorization_bearer"},
		{"apikey explicit key", "apikey", map[string]any{"anthropic_apikey_auth_scheme": "x_api_key"}, "x_api_key"},
		{"apikey unknown", "apikey", map[string]any{"anthropic_apikey_auth_scheme": "unknown"}, "x_api_key"},
		{"apikey malformed", "apikey", map[string]any{"anthropic_apikey_auth_scheme": 1}, "x_api_key"},
		{"extra malformed", "apikey", "bad", "x_api_key"},
		{"extra empty", "apikey", map[string]any{}, "x_api_key"},
		{"oauth", "oauth", map[string]any{"anthropic_apikey_auth_scheme": "authorization_bearer"}, ""},
		{"missing type", nil, nil, ""},
		{"malformed type", 1, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: c41CredentialTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/api/v1/admin/accounts/data" || req.URL.Query().Get("ids") != "1" || req.URL.Query().Get("include_proxies") != "false" {
					t.Errorf("changed credential export path/query: %s", req.URL.RequestURI())
				}
				body, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"accounts": []any{map[string]any{"id": 1, "type": tc.kind, "extra": tc.extra, "credentials": map[string]any{"access_token": "fixture-only", "base_url": "https://fixture.invalid"}}}}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
			})}
			s := NewPlatformService(NewHTTPClient(client))
			cred, err := s.ResolveProbeCredentialContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "https://main.invalid", AccessToken: "fixture-only"}, AdminGroupAccountInfo{ID: "1"})
			if err != nil || calls != 1 {
				t.Fatalf("credential export err=%v calls=%d", err, calls)
			}
			field := reflect.ValueOf(cred).FieldByName("AnthropicAuth")
			got := ""
			if field.IsValid() {
				got = field.String()
			}
			if got != tc.want {
				t.Errorf("auth=%q want=%q", got, tc.want)
			}
		})
	}
}
