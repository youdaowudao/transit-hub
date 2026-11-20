package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type absorbProbeTransport func(*http.Request) (*http.Response, error)

func (f absorbProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAbsorbProbeNestedAddressWithoutCredentialDisclosure(t *testing.T) {
	account := parseSub2APIAccount(map[string]any{"id": float64(55), "credentials": map[string]any{"base_url": "https://nested.invalid/v1", "api_key": "fixture-key-never-stored"}})
	if account.BaseURL != "https://nested.invalid/v1" {
		t.Fatal("nested account address was not retained")
	}
}

func TestAbsorbProbeFallbackPreservesExportChecks(t *testing.T) {
	for _, tc := range []struct {
		name, payload, wantURL string
		valid                  bool
	}{
		{"list fallback", `{"data":{"accounts":[{"credentials":{"api_key":"fixture-key"}}]}}`, "https://listed.invalid/v1", true},
		{"credential address first", `{"data":{"accounts":[{"base_url":"https://record.invalid","credentials":{"api_key":"fixture-key","base_url":"https://export.invalid"}}]}}`, "https://export.invalid", true},
		{"record address before list", `{"data":{"accounts":[{"base_url":"https://record.invalid","credentials":{"api_key":"fixture-key"}}]}}`, "https://record.invalid", true},
		{"missing key cannot use list URL", `{"data":{"accounts":[{"credentials":{}}]}}`, "", false},
		{"multiple accounts cannot use list URL", `{"data":{"accounts":[{"credentials":{"api_key":"a"}},{"credentials":{"api_key":"b"}}]}}`, "", false},
		{"redacted export cannot use list URL", `{"data":{"accounts":[{"credentials":{"note":"redacted"}}]}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: absorbProbeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts/data" || r.URL.Query().Get("ids") != "55" {
					t.Fatal("unexpected probe request")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.payload)), Request: r}, nil
			})}
			service := NewPlatformService(NewHTTPClient(client))
			got, err := service.ResolveProbeCredentialContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "https://hub-fixture.invalid", AccessToken: "fixture-session"}, AdminGroupAccountInfo{ID: "55", BaseURL: "https://listed.invalid/v1"})
			if calls != 1 {
				t.Fatalf("expected one export request, got %d", calls)
			}
			if tc.valid {
				if err != nil || got.BaseURL != tc.wantURL || got.Key != "fixture-key" {
					t.Fatal("credential export did not preserve URL precedence and plaintext key")
				}
			} else if err == nil {
				t.Fatal("an invalid export bypassed the credential checks")
			}
		})
	}
}
