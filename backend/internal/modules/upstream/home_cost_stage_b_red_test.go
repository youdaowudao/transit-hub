package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

var homeCostRefusals = []struct {
	code   string
	status int
	key    string
}{
	{"ANNOUNCEMENT_ACK_REQUIRED", 409, "admin.upstream.errors.announcementAckRequired"},
	{"INSUFFICIENT_BALANCE", 403, "admin.upstream.errors.upstreamInsufficientBalance"},
	{"API_KEY_QUOTA_EXHAUSTED", 429, "admin.upstream.errors.upstreamKeyQuotaExhausted"},
	{"INSUFFICIENT_QUOTA", 429, "admin.upstream.errors.upstreamKeyQuotaExhausted"},
	{"API_KEY_EXPIRED", 403, "admin.upstream.errors.upstreamKeyExpired"},
}

// 主代理固定 B-2 原因、安全性与原有 mutation/remote 元数据的边界。
func TestHomeCostStageBRecognizesRefusalWithoutChangingMutationMetadata(t *testing.T) {
	for _, refusal := range homeCostRefusals {
		for _, envelope := range []string{"code", "error.code", "error.type"} {
			for _, businessFailure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/business=%v", refusal.code, envelope, businessFailure), func(t *testing.T) {
					body := map[string]any{"success": false, "message": "raw-message-fixture?token=fixture-secret", "reason": "FORBIDDEN"}
					if envelope == "code" {
						body["code"] = strings.ToLower(refusal.code)
					} else {
						body["error"] = map[string]any{strings.TrimPrefix(envelope, "error."): strings.ToLower(refusal.code)}
					}
					status := refusal.status
					if businessFailure {
						status = 200
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); writeJSON(w, body) }))
					defer server.Close()
					_, err := NewHTTPClient(server.Client()).requestJSON(server.URL, requestOptions{})
					var detail *RequestError
					if !errors.As(err, &detail) {
						t.Fatalf("expected structured rejection: %v", err)
					}
					if detail.UpstreamCode != refusal.code || siteErrorKey(err) != refusal.key || errorCategory(siteErrorKey(err)) != ErrorRequest {
						t.Errorf("code=%s reason=%s category=%s", detail.UpstreamCode, siteErrorKey(err), errorCategory(siteErrorKey(err)))
					}
					if !businessFailure && (detail.RemoteReason != "FORBIDDEN" || detail.MutationOutcome != MutationUncertain) {
						t.Error("existing remote reason or mutation outcome changed")
					}
				})
			}
		}
	}
}

func TestHomeCostStageBCodeParsingPreservesNumericAndRejectsUnsafeValues(t *testing.T) {
	for _, value := range []any{42, "invalid?token=fixture", "with-dash", strings.Repeat("A", 65), "汉字"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			writeJSON(w, map[string]any{"code": value})
		}))
		_, err := NewHTTPClient(server.Client()).requestJSON(server.URL, requestOptions{})
		server.Close()
		var detail *RequestError
		if !errors.As(err, &detail) || detail.UpstreamCode != "" {
			t.Fatal("unsafe or numeric code became an upstream string code")
		}
		if value == 42 && (detail.RemoteCode == nil || *detail.RemoteCode != 42) {
			t.Fatal("numeric code compatibility lost")
		}
	}
}

func TestHomeCostStageBCostUnreadableKeepsConnectionAndSkipsKnownRefusalCollection(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, sameDay := range []bool{true, false} {
		for _, hadSnapshot := range []bool{true, false} {
			t.Run(fmt.Sprintf("same-day=%v/snapshot=%v", sameDay, hadSnapshot), func(t *testing.T) {
				var keyReads atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/user/self":
						writeJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 1, "quota": 5000000}})
					case "/api/log/self/stat":
						w.WriteHeader(409)
						writeJSON(w, map[string]any{"success": false, "code": "ANNOUNCEMENT_ACK_REQUIRED", "message": "raw-message-fixture?token=fixture-secret"})
					case "/api/user/self/groups", "/api/user/groups", "/api/pricing":
						writeJSON(w, map[string]any{"success": true, "data": map[string]any{}})
					case "/api/token/":
						keyReads.Add(1)
						writeJSON(w, map[string]any{"success": true, "data": []any{}, "total": 0})
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformNewAPI, BaseURL: server.URL, UserID: "1", AccessToken: "fixture-only"})
				site.Platform = PlatformNewAPI
				oldAt := now.Add(-time.Hour)
				if !sameDay {
					oldAt = now.Add(-24 * time.Hour)
				}
				amount := 3.0
				site.Metrics = Metrics{TodayConsume: MetricValue{Value: &amount, Display: "3.00"}, TodayConsumeDate: businesstime.DateAt(oldAt), TodayConsumeAt: &oldAt}
				cache := newKeySnapshotTestCache(site)
				if hadSnapshot {
					_ = cache.SaveKeyUsageSnapshot(t.Context(), site.ID, fixtureKeySnapshot(now.Add(-time.Minute), 3))
					<-cache.writes
				}
				platform := NewPlatformService(NewHTTPClient(server.Client()))
				platform.now = func() time.Time { return now }
				svc := NewService(platform, nil, nil, cache)
				svc.now = func() time.Time { return now }
				t.Cleanup(svc.Close)
				var logs bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&logs)
				defer log.SetOutput(previous)
				response, err := svc.sync(t.Context(), site.ID)
				if err != nil || response.Status != StatusConnected || response.ErrorKey != nil {
					t.Fatalf("cost rejection changed connection: status=%s err=%v", response.Status, err)
				}
				encoded, _ := json.Marshal(response.Metrics)
				var record map[string]any
				_ = json.Unmarshal(encoded, &record)
				if record["todayConsumeStatus"] != "unreadable" || record["todayConsumeErrorKey"] != homeCostRefusals[0].key || record["todayConsumeUpstreamCode"] != "ANNOUNCEMENT_ACK_REQUIRED" || record["todayConsumeHTTPStatus"] != float64(409) || record["todayConsumeFailedAt"] == nil {
					t.Errorf("missing safe cost status: %s", encoded)
				}
				if sameDay {
					if response.Metrics.TodayConsume.Value == nil || *response.Metrics.TodayConsume.Value != 3 || response.Metrics.TodayConsumeAt == nil || !response.Metrics.TodayConsumeAt.Equal(oldAt) {
						t.Error("same-day value or original observation lost")
					}
				} else if response.Metrics.TodayConsume.Value != nil {
					t.Error("previous-day value became today's cost")
				}
				select {
				case snapshot := <-cache.writes:
					if snapshot.FailureReason != homeCostRefusals[0].key || snapshot.FailureAt == nil {
						t.Error("snapshot lost refusal reason")
					}
					if hadSnapshot && (!snapshot.Complete || len(snapshot.Items) != 1 || snapshot.Items[0].TodayAmount != 3) {
						t.Error("same-day complete snapshot overwritten")
					}
					if !hadSnapshot && (snapshot.Complete || len(snapshot.Items) != 0) {
						t.Error("reason-only snapshot became complete")
					}
				case <-time.After(2 * time.Second):
					t.Error("cost refusal did not update snapshot")
				}
				svc.Close()
				if keyReads.Load() != 0 {
					t.Errorf("known refusal still collected keys: %d", keyReads.Load())
				}
				if strings.Contains(string(encoded)+logs.String(), "raw-message-fixture") || strings.Contains(string(encoded)+logs.String(), "fixture-secret") {
					t.Error("upstream message or credentials entered metrics/logs")
				}
			})
		}
	}
}

func TestHomeCostStageBLoginWritesBusinessDateAndObservation(t *testing.T) {
	now := time.Date(2031, 2, 3, 16, 0, 1, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			writeJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 1, "quota": 5000000}})
		case "/api/log/self/stat":
			writeJSON(w, map[string]any{"success": true, "data": map[string]any{"quota": 500000}})
		default:
			writeJSON(w, map[string]any{"success": true, "data": map[string]any{}})
		}
	}))
	defer server.Close()
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, nil, nil, newSyncTestCache())
	svc.now = func() time.Time { return now }
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	defer svc.Close()
	response, err := svc.Create(t.Context(), "user", CreateRequest{Name: "验收-日期", SiteURL: server.URL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response.Metrics.TodayConsumeDate != businesstime.DateAt(now) || response.Metrics.TodayConsumeAt == nil || !response.Metrics.TodayConsumeAt.Equal(now) {
		t.Errorf("login omitted dated observation: date=%s at=%v", response.Metrics.TodayConsumeDate, response.Metrics.TodayConsumeAt)
	}
}
