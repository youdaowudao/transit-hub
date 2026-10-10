package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/shared/authctx"
)

type modelControlSummaryFailure struct{ modelControlRepository }

func (r modelControlSummaryFailure) ListModelControlTargets(context.Context, string, string) ([]modelControlTarget, error) {
	return nil, errors.New("fixture C3 storage failure")
}
func TestModelControlSummariesAllReadTransportsAndFailures(t *testing.T) {
	for _, transport := range []string{"get", "json", "sse", "recent"} {
		for _, failed := range []bool{false, true} {
			name := transport + "/success"
			if failed {
				name = transport + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				f := newC3REDFixture(t)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := f.service.Shutdown(ctx); err != nil {
						t.Error(err)
					}
				})
				f.round("1", "A", 1, 2, 0)
				f.manage("1", "A")
				f.close("1", "A")
				if failed {
					f.service.modelControls = modelControlSummaryFailure{f.service.modelControls}
				}
				path := "/api/connection-health/admin-groups"
				method := http.MethodGet
				if transport == "json" || transport == "sse" {
					method = http.MethodPost
					path += "/refresh"
				}
				if transport == "recent" {
					path = "/api/connection-health/question-answer-recent-summaries?targetId=" + c3REDTarget("1")
				}
				request := httptest.NewRequest(method, path, nil)
				request = request.WithContext(authctx.WithUserID(request.Context(), c3REDUser))
				if transport == "sse" {
					request.Header.Set("Accept", "text/event-stream")
				}
				response := httptest.NewRecorder()
				f.mux.ServeHTTP(response, request)
				if response.Code != 200 {
					t.Fatalf("transport=%s status=%d body=%s", transport, response.Code, response.Body.String())
				}
				if transport == "recent" {
					var payload map[string]any
					if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					items := payload["items"].([]any)
					if len(items) != 1 {
						t.Fatal("recent items lost")
					}
					item := items[0].(map[string]any)
					if item["recentQuestionAnswer"] == nil {
						t.Fatal("C3 failure erased old recent summary")
					}
					assertModelControlSummary(t, item["modelControl"], payload["modelControlError"], failed)
					return
				}
				var groups []any
				if transport == "get" {
					if err := json.Unmarshal(response.Body.Bytes(), &groups); err != nil {
						t.Fatal(err)
					}
				} else {
					raw := response.Body.Bytes()
					if transport == "sse" {
						found := false
						for _, block := range strings.Split(string(raw), "\n\n") {
							if strings.HasPrefix(block, "event: terminal\n") {
								raw = []byte(strings.TrimPrefix(block, "event: terminal\ndata: "))
								found = true
								break
							}
						}
						if !found {
							t.Fatalf("SSE terminal absent: %s", response.Body.String())
						}
					}
					var payload map[string]any
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					groups, _ = payload["groups"].([]any)
					if groups == nil {
						if result, ok := payload["result"].(map[string]any); ok {
							groups, _ = result["groups"].([]any)
						}
					}
				}
				if len(groups) != 1 {
					t.Fatalf("groups=%v body=%s", groups, response.Body.String())
				}
				group := groups[0].(map[string]any)
				if failed {
					if group["modelSupply"] != nil {
						t.Fatal("failed summary still produced supply")
					}
				} else {
					supply, ok := group["modelSupply"].(map[string]any)
					if !ok {
						t.Fatal("supply missing from group transport")
					}
					items := supply["items"].([]any)
					if len(items) != 1 {
						t.Fatal("supply has unrelated models", items)
					}
					item := items[0].(map[string]any)
					if item["modelName"] != "A" || item["open"] != float64(1) || item["closed"] != float64(1) || item["unknown"] != float64(0) {
						t.Fatal("wrong supply", item)
					}
				}
				accounts := group["accounts"].([]any)
				found := false
				for _, raw := range accounts {
					item := raw.(map[string]any)
					if item["targetId"] != c3REDTarget("1") {
						continue
					}
					found = true
					if item["recentQuestionAnswer"] == nil {
						t.Fatal("C3 summary failure erased recent QA")
					}
					assertModelControlSummary(t, item["modelControl"], group["modelControlError"], failed)
				}
				if !found {
					t.Fatal("target missing from read transport")
				}
			})
		}
	}
}
func assertModelControlSummary(t *testing.T, summary, reason any, failed bool) {
	t.Helper()
	if failed {
		if summary != nil || reason == nil || reason == "" {
			t.Fatalf("summary failure not isolated: summary=%v reason=%v", summary, reason)
		}
		return
	}
	if reason != nil && reason != "" {
		t.Fatalf("unexpected summary error=%v", reason)
	}
	value, ok := summary.(map[string]any)
	if !ok || value["closed"] != float64(1) || value["attention"] != float64(0) {
		t.Fatalf("wrong persistent summary=%v", summary)
	}
}
