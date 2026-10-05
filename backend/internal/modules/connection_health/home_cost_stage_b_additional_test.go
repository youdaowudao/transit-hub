package connection_health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func TestHomeCostStageBModelRefusalParsingBoundaries(t *testing.T) {
	for _, item := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"top-code", 403, `{"code":"insufficient_balance","message":"raw-secret?token=secret"}`, "admin.upstream.errors.upstreamInsufficientBalance"},
		{"nested-code", 429, `{"error":{"code":"api_key_quota_exhausted","message":"raw-secret"}}`, "admin.upstream.errors.upstreamKeyQuotaExhausted"},
		{"numeric-code-falls-through", 403, `{"code":403,"error":{"type":"api_key_expired"}}`, "admin.upstream.errors.upstreamKeyExpired"},
		{"invalid-code", 403, `{"code":"INSUFFICIENT_BALANCE?token=secret"}`, ErrorModelListUnavailable},
		{"unknown-code", 409, `{"code":"UNKNOWN_REFUSAL","message":"raw-secret"}`, ErrorModelListUnavailable},
		{"invalid-json", 403, `not-json raw-secret`, ErrorModelListUnavailable},
		{"success-not-refusal", 200, `{"code":"INSUFFICIENT_BALANCE","data":[]}`, ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
					t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(item.status)
				_, _ = w.Write([]byte(item.body))
			}))
			defer server.Close()
			models, err := NewModelDiscoveryRunner().ListModels(t.Context(), server.URL, "fixture-only")
			if item.want == "" {
				if err != nil || len(models) != 0 {
					t.Fatalf("successful discovery changed: models=%+v err=%v", models, err)
				}
			} else if err == nil || err.Error() != item.want || strings.Contains(err.Error(), "raw-secret") || strings.Contains(err.Error(), "token=") {
				t.Fatalf("safe discovery reason=%v want=%s", err, item.want)
			}
			if requests.Load() != 1 {
				t.Fatalf("model discovery unexpectedly retried: requests=%d", requests.Load())
			}
		})
	}
}

func TestHomeCostStageBModelRefusalReachesHandlerWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"INSUFFICIENT_BALANCE","message":"raw-secret?token=secret"}`))
	}))
	defer server.Close()
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "验收-分组"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "100", Name: "验收-模型拒绝", BaseURL: server.URL, Models: "fixture-model"}}},
		credByAccount: map[string]upstream.ProbeCredential{"100": {BaseURL: server.URL, Key: "fixture-secret"}},
	}
	service := newModelDiscoveryTestService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}, newFakeRepository())
	handler := &Handler{service: service}
	request := httptest.NewRequest(http.MethodGet, "/api/connection-health/targets/newapi:ws1:100/models", nil)
	request.SetPathValue("id", "newapi:ws1:100")
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	handler.discoverTargetModels(response, request)
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, `"message":"admin.upstream.errors.upstreamInsufficientBalance"`) {
		t.Fatalf("model refusal lost at handler: status=%d body=%s", response.Code, body)
	}
	for _, secret := range []string{"raw-secret", "token=", "fixture-secret", server.URL} {
		if strings.Contains(body, secret) {
			t.Error("model refusal response leaked upstream details")
		}
	}
}
