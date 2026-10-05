package connection_health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 主代理固定模型列表仅透出已识别原因，其他失败保持旧提示。
func TestHomeCostStageBModelListShowsKnownRefusalOnly(t *testing.T) {
	for _, item := range []struct {
		code   string
		status int
		want   string
	}{
		{"ANNOUNCEMENT_ACK_REQUIRED", 409, "admin.upstream.errors.announcementAckRequired"},
		{"INSUFFICIENT_BALANCE", 403, "admin.upstream.errors.upstreamInsufficientBalance"},
		{"API_KEY_QUOTA_EXHAUSTED", 429, "admin.upstream.errors.upstreamKeyQuotaExhausted"},
		{"INSUFFICIENT_QUOTA", 429, "admin.upstream.errors.upstreamKeyQuotaExhausted"},
		{"API_KEY_EXPIRED", 403, "admin.upstream.errors.upstreamKeyExpired"},
		{"UNKNOWN", 403, ErrorModelListUnavailable}, {"", 401, ErrorModelListUnavailable},
	} {
		t.Run(item.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(item.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": item.code, "message": "raw-fixture?token=secret"}})
			}))
			defer server.Close()
			_, err := NewModelDiscoveryRunner().ListModels(t.Context(), server.URL, "fixture-only")
			if err == nil || err.Error() != item.want {
				t.Errorf("reason=%v want=%s", err, item.want)
			}
		})
	}
}
