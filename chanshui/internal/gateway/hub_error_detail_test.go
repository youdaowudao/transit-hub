package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHubCompatibleErrorKeepsRootAndPlainReasonsWithoutCredentials(t *testing.T) {
	for _, body := range []string{`{"message":"unsupported tool choice fixture-key"}`, `{"error":"unsupported tool choice fixture-key"}`, `unsupported tool choice fixture-key`} {
		c := Client{BaseURL: "https://fixture.invalid", Key: "fixture-key", Mode: "chat", PreserveErrorReason: true, RetryLimit: new(int), HTTP: &http.Client{Transport: aliasTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}}
		m, e := c.CallMessages(t.Context(), CallRequest{Model: "fixture", Prompt: "fixture", MaxTokens: 128})
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(m.Err, "unsupported tool choice") || strings.Contains(m.Err, "fixture-key") {
			t.Fatal("specific safe reason lost", m.Err)
		}
	}
}
