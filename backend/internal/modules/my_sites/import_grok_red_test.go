package my_sites

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Primary-agent business expectations, fixed before implementation against
// SPEC G01–G06. The existing transport stays in memory and opens no port.
func TestGrokREDSelectionEntirePipeline(t *testing.T) {
	for _, source := range []string{"grok", "openai", "anthropic", "gemini", "antigravity", "kimi", "zhipu", "deepseek", "minimax", "", "unknown"} {
		t.Run(source, func(t *testing.T) { c51AssertImportedPlatform(t, source, " GrOk ", "grok", "X") })
	}
	t.Run("cached-classification-fallback", func(t *testing.T) { c51AssertImportedPlatform(t, "grok", "", "grok", "X") })
}

func TestGrokREDSettingsModelsAndAssociation(t *testing.T) {
	for _, settings := range []string{
		`{}`,
		`{"priorityMode":"manual","priority":1,"concurrency":1,"passthrough":false,"poolMode":false,"upstreamBillingProbeEnabled":false}`,
		`{"priorityMode":"manual","priority":9,"concurrency":1000,"passthrough":false,"poolMode":true,"upstreamBillingProbeEnabled":true}`,
		`{"priorityMode":"automatic","priority":10,"concurrency":50,"passthrough":false,"poolMode":false,"upstreamBillingProbeEnabled":true}`,
		`{"priorityMode":"automatic","priority":2147483647,"concurrency":1000,"passthrough":false,"poolMode":true,"upstreamBillingProbeEnabled":false}`,
	} {
		for _, mapping := range []bool{false, true} {
			t.Run(settings+map[bool]string{false: "/mapping-off", true: "/mapping-on"}[mapping], func(t *testing.T) {
				remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
				s, repo := c5RedService(t, remote)
				c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
					if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
						remote.previews++
						var body map[string]any
						if json.NewDecoder(r.Body).Decode(&body) != nil || body["platform"] != "grok" || body["type"] != "apikey" || body["base_url"] != "https://c5-source.invalid" || body["api_key"] != "synthetic-c5-key" {
							t.Fatal("preview changed selected platform or source credentials")
						}
						return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"models": []string{"provider/custom-v2", " grok-live ", "provider/custom-v2"}}}), nil, true
					}
					return nil, nil, false
				})
				req := c5RedRequest(t, settings)
				req.GroupType, req.AddToPricingMapping = "grok", &mapping
				result, err := s.RealConnect(t.Context(), "user-1", req)
				if err != nil {
					t.Fatalf("Grok settings import must succeed: %v", err)
				}
				priority, concurrency, pool, probe := 100, 50, true, true
				var requested map[string]any
				if json.Unmarshal([]byte(settings), &requested) != nil {
					t.Fatal("invalid test input")
				}
				if v, ok := requested["priority"].(float64); ok {
					priority = int(v)
				}
				if v, ok := requested["concurrency"].(float64); ok {
					concurrency = int(v)
				}
				if v, ok := requested["poolMode"].(bool); ok {
					pool = v
				}
				if v, ok := requested["upstreamBillingProbeEnabled"].(bool); ok {
					probe = v
				}
				c := result.Configuration
				if c == nil || c.Platform != "grok" || c.Priority != priority || c.Concurrency != concurrency || c.PoolMode != pool || c.UpstreamBillingProbeEnabled != probe || c.Passthrough || c.ModelState != "synced" || !reflect.DeepEqual(c.Models, []string{"grok-live", "provider/custom-v2"}) {
					t.Fatal("Grok readback lost exact settings or nonstandard model IDs")
				}
				credentials := remote.created["credentials"].(map[string]any)
				if !reflect.DeepEqual(credentials["model_mapping"], map[string]any{"grok-live": "grok-live", "provider/custom-v2": "provider/custom-v2"}) || credentials["api_key"] != "synthetic-c5-key" || credentials["base_url"] != "https://c5-source.invalid" {
					t.Fatal("created whitelist or original source changed")
				}
				if _, exists := credentials["grok_passthrough"]; exists {
					t.Fatal("Grok invented a passthrough credential")
				}
				if extra, _ := remote.created["extra"].(map[string]any); extra["grok_passthrough"] != nil {
					t.Fatal("Grok invented passthrough configuration")
				}
				if repo.connection == nil || repo.connection.GroupType != "grok" || repo.connection.PricingMappingEnabled != mapping || !reflect.DeepEqual(repo.connection.OwnGroupIDs, []string{"7", "8"}) || result.ConfigurationStatus != "confirmed" || remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || repo.saveCalls != 1 {
					t.Fatal("Grok import did not save exactly one complete multi-group association")
				}
			})
		}
	}
}
