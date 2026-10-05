package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func homeCostBMetricsRecord(t *testing.T, metrics Metrics) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestHomeCostStageBUnknownCostRefusalAndRecovery(t *testing.T) {
	now := time.Date(2031, 2, 3, 16, 0, 1, 0, time.UTC)
	var recovered atomic.Bool
	var elapsed atomic.Int64
	clock := func() time.Time { return now.Add(time.Duration(elapsed.Load()) * time.Second) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000}})
		case "/api/log/self/stat":
			start, end, _ := businessDayUnixBounds(businesstime.DateAt(now))
			if r.URL.Query().Get("start_timestamp") != strconv.FormatInt(start, 10) || r.URL.Query().Get("end_timestamp") != strconv.FormatInt(end, 10) {
				t.Error("cost query ignored injected Singapore date")
			}
			if !recovered.Load() {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"code": "future_gate", "message": "unsafe-token=fixture-secret"})
			} else {
				writeJSON(w, map[string]any{"data": map[string]any{"quota": 1000000}})
			}
		default:
			writeJSON(w, map[string]any{"data": map[string]any{}})
		}
	}))
	defer server.Close()
	oldAt := now.Add(-time.Second) // same Singapore business day.
	amount := 3.0
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformNewAPI, BaseURL: server.URL, UserID: "1", AccessToken: "fixture"})
	site.Platform = PlatformNewAPI
	site.Metrics = Metrics{TodayConsume: metric(&amount), TodayConsumeDate: businesstime.DateAt(oldAt), TodayConsumeAt: &oldAt}
	cache := newSyncTestCache(site) // no collector in this status-only fixture.
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = clock
	svc := NewService(platform, nil, nil, cache)
	svc.now = clock
	defer svc.Close()
	response, err := svc.syncOnce(t.Context(), site.ID)
	if err != nil || response.Status != StatusConnected {
		t.Fatalf("cost-only rejection broke connection: %v", err)
	}
	record := homeCostBMetricsRecord(t, response.Metrics)
	if record["todayConsumeStatus"] != "unreadable" || record["todayConsumeUpstreamCode"] != "FUTURE_GATE" || record["todayConsumeHTTPStatus"] != float64(409) || record["todayConsumeErrorKey"] != ErrorRequest {
		t.Errorf("unknown refusal missing bounded metadata: %v", record)
	}
	if response.Metrics.TodayConsume.Value == nil || *response.Metrics.TodayConsume.Value != 3 || response.Metrics.TodayConsumeAt == nil || !response.Metrics.TodayConsumeAt.Equal(oldAt) {
		t.Error("same-day original observation lost")
	}
	recovered.Store(true)
	elapsed.Store(10)
	response, err = svc.syncOnce(t.Context(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	record = homeCostBMetricsRecord(t, response.Metrics)
	if record["todayConsumeStatus"] != "ok" || response.Metrics.TodayConsume.Value == nil || *response.Metrics.TodayConsume.Value != 2 || response.Metrics.TodayConsumeAt == nil || !response.Metrics.TodayConsumeAt.Equal(clock()) {
		t.Error("successful retry did not replace retained cost")
	}
	for _, key := range []string{"todayConsumeErrorKey", "todayConsumeUpstreamCode", "todayConsumeHTTPStatus", "todayConsumeFailedAt"} {
		if record[key] != nil {
			t.Errorf("recovery kept stale failure field %s", key)
		}
	}
}

func TestHomeCostStageBSuccessfulLoginDatesAllModesAndUpdate(t *testing.T) {
	now := time.Date(2031, 2, 3, 16, 0, 1, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			w.Header().Set("Set-Cookie", "fixture=fixture-cookie; Path=/")
			writeJSON(w, map[string]any{"data": map[string]any{"id": 1}})
		case "/api/v1/auth/login":
			writeJSON(w, map[string]any{"data": map[string]any{"access_token": "fixture-access"}})
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": []any{}})
		case "/api/log/self/stat":
			writeJSON(w, map[string]any{"data": map[string]any{"quota": 500000}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1}})
		default:
			writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000, "balance": 10}})
		}
	}))
	defer server.Close()
	for _, mode := range []struct {
		platform Platform
		auth     AuthMode
	}{{PlatformNewAPI, AuthModeUserKey}, {PlatformNewAPI, AuthModePassword}, {PlatformSub2API, AuthModeToken}, {PlatformSub2API, AuthModePassword}} {
		t.Run(string(mode.platform)+"/"+string(mode.auth), func(t *testing.T) {
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			svc := NewService(platform, nil, nil, newSyncTestCache())
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			defer svc.Close()
			dto := CreateRequest{Name: "验收-日期", SiteURL: server.URL, Platform: mode.platform, AuthMode: mode.auth, Account: "fixture-account", Password: "fixture-password", UserID: "1", AccessToken: "fixture-access", RechargeRate: 1}
			created, err := svc.Create(t.Context(), "user", dto)
			if err != nil {
				t.Fatal(err)
			}
			assertDate := func(response Response) {
				t.Helper()
				if response.Metrics.TodayConsumeDate != businesstime.DateAt(now) || response.Metrics.TodayConsumeAt == nil || !response.Metrics.TodayConsumeAt.Equal(now) {
					t.Error("successful login omitted dated observation")
				}
			}
			assertDate(created)
			updated, err := svc.Update(t.Context(), "user", created.ID, UpdateRequest{Name: dto.Name, SiteURL: dto.SiteURL, Platform: dto.Platform, AuthMode: dto.AuthMode, Account: dto.Account, Password: dto.Password, UserID: dto.UserID, AccessToken: dto.AccessToken, RechargeRate: 1})
			if err != nil {
				t.Fatal(err)
			}
			assertDate(updated)
		})
	}
}

func TestHomeCostStageBSub2APICostRefusalRecordsSnapshotButAuthDoesNot(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, failurePath := range []string{"/api/v1/usage/dashboard/stats", "/api/v1/auth/me"} {
		t.Run(failurePath, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failurePath {
					w.WriteHeader(http.StatusForbidden)
					writeJSON(w, map[string]any{"code": "INSUFFICIENT_BALANCE"})
					return
				}
				writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
			}))
			defer server.Close()
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-access"})
			cache := newKeySnapshotTestCache(site)
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			svc := NewService(platform, nil, nil, cache)
			svc.now = func() time.Time { return now }
			defer svc.Close()
			response, err := svc.sync(t.Context(), site.ID)
			if err != nil || response.Status != StatusError {
				t.Error("Sub2API whole-sync failure semantics changed")
			}
			if failurePath == "/api/v1/usage/dashboard/stats" {
				select {
				case snapshot := <-cache.writes:
					if snapshot.Complete || snapshot.FailureReason != "admin.upstream.errors.upstreamInsufficientBalance" || snapshot.FailureAt == nil {
						t.Error("cost refusal snapshot lost safe failure")
					}
				case <-time.After(time.Second):
					t.Error("known cost refusal did not write reason-only snapshot")
				}
			} else if snapshot, _ := cache.GetKeyUsageSnapshot(t.Context(), site.ID); snapshot != nil {
				t.Error("identity rejection guessed to be cost rejection")
			}
		})
	}
}
