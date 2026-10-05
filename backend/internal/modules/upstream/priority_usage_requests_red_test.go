package upstream

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSafeHTTPDiagnosticAllowsFixedUsagePaths(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	for _, path := range []string{
		"/api/v1/usage/stats", "/api/v1/keys", "/api/v1/admin/keys",
		"/api/v1/admin/groups/usage-summary", "/api/v1/usage/dashboard/api-keys-usage",
	} {
		t.Run(path, func(t *testing.T) {
			logs.Reset()
			_, err := platform.httpClient.requestJSON("https://fixed-usage.test"+path+"?token=must-never-appear&api_key_id=123", requestOptions{})
			if err == nil {
				t.Fatal("fixture must produce a request failure")
			}
			want := "host=https://fixed-usage.test path=" + path
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("fixed usage path missing from failure diagnostic: want %q, log=%s", want, logs.String())
			}
			if strings.Contains(logs.String(), "must-never-appear") || strings.Contains(logs.String(), "api_key_id") || strings.Contains(logs.String(), "?token") {
				t.Fatalf("query parameters leaked into diagnostic: %s", logs.String())
			}
		})
	}
	for _, path := range []string{"/api/v1/keys/123", "/api/v1/admin/keys/123", "/api/v1/usage/stats-extra", "/api/v1/keys%2F123"} {
		if got := safeHTTPDiagnostic("https://fixed-usage.test" + path + "?token=must-never-appear"); got != "host=- path=-" {
			t.Errorf("dynamic or other path must remain hidden: path=%s diagnostic=%s", path, got)
		}
	}
}

func TestGroupCostSummaryAuthFailureWaits24Hours(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, class := range []string{"auth_401", "auth_403"} {
		t.Run(class, func(t *testing.T) {
			var state GroupCostSamplingState
			for attempt := 0; attempt < 3; attempt++ {
				state.RecordSummaryFailure(class, now)
				if got := state.SummaryNextAllowedAt.Sub(now); got != 24*time.Hour {
					t.Errorf("summary auth failure backoff=%s, want fixed 24h", got)
				}
				if state.SummaryAuthFailures != attempt+1 || state.LastReason != class {
					t.Fatalf("summary failure bookkeeping changed: %+v", state)
				}
			}
			if state.summaryAllowed(now.Add(24*time.Hour-time.Nanosecond)) || !state.summaryAllowed(now.Add(24*time.Hour)) {
				t.Error("summary must remain blocked until the exact 24h boundary")
			}
		})
	}
	var fallback GroupCostSamplingState
	fallback.RecordFallbackFailure("auth_403", now)
	if got := fallback.FallbackNextAllowedAt.Sub(now); got != 30*time.Minute {
		t.Errorf("first fallback auth backoff changed: %s", got)
	}
	fallback.RecordFallbackFailure("auth_403", now)
	if got := fallback.FallbackNextAllowedAt.Sub(now); got != time.Hour {
		t.Errorf("second fallback auth backoff changed: %s", got)
	}
	var other GroupCostSamplingState
	other.RecordSummaryFailure("invalid_response", now)
	if got := other.SummaryNextAllowedAt.Sub(now); got != 30*time.Minute {
		t.Errorf("non-auth summary backoff changed: %s", got)
	}
	if groupCostSamplingStateTTL != 25*time.Hour {
		t.Errorf("sampling state TTL=%s, want 25h", groupCostSamplingStateTTL)
	}
}
