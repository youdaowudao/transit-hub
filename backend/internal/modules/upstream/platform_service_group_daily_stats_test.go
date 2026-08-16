package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchGroupCostStatsForDateFallsBackToBoundedCompleteKeySet(t *testing.T) {
	var usageRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}, {"id": 2, "name": "free"}}})
		case "/api/v1/admin/groups/usage-summary":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{
				{"id": 11, "group_id": 1},
				{"id": 12, "group_id": 2},
			}, "total": 2})
		case "/api/v1/usage/stats":
			usageRequests++
			if r.URL.Query().Get("api_key_id") == "11" {
				writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 3.5}})
				return
			}
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 0.0}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}, {ID: "2", Name: "free"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err != nil {
		t.Fatalf("FetchGroupCostStatsForDate() error: %v", err)
	}
	if result.Source != GroupCostSourceKeyFallback || !result.Complete || len(result.Stats) != 2 || usageRequests != 2 {
		t.Fatalf("unexpected fallback result: %+v usageRequests=%d", result, usageRequests)
	}
	if result.SiteReportedRawCost == nil || *result.SiteReportedRawCost != 3.5 || result.UnattributedRawCost == nil || *result.UnattributedRawCost != 0 {
		t.Fatalf("unexpected reconciliation: %+v", result)
	}
}

func TestFetchGroupCostStatsForDateDoesNotUseAdminKeyForKeyFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/groups/available" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "unexpected fallback request", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AdminAPIKey: "admin-key"},
		[]GroupInfo{{ID: "1", Name: "vip"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err == nil {
		t.Fatal("admin key must not be used for user key fallback")
	}
}

func TestFetchGroupCostStatsForDateRejectsIncompleteKeyUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}, {"id": 2, "name": "free"}}})
		case "/api/v1/admin/groups/usage-summary":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v1/keys":
			if r.URL.Query().Get("page") == "2" {
				writeJSON(w, map[string]any{"data": []map[string]any{}, "total": 2})
				return
			}
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 11, "group_id": 1}}, "total": 2})
		case "/api/v1/usage/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 1.0}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}, {ID: "2", Name: "free"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err == nil {
		t.Fatal("incomplete key usage must remain unknown")
	}
}

func TestFetchGroupCostStatsForDateDoesNotFanOutOnSummaryNetworkFailure(t *testing.T) {
	var keyRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}}})
		case "/api/v1/admin/groups/usage-summary":
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		case "/api/v1/keys":
			keyRequests++
			writeJSON(w, map[string]any{"data": []map[string]any{}})
		}
	}))
	defer server.Close()

	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err == nil || keyRequests != 0 {
		t.Fatalf("network/5xx summary failure must not fan out to keys: err=%v keyRequests=%d", err, keyRequests)
	}
}

func TestFetchGroupCostStatsForDateTreatsCompleteEmptyKeySetAsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}}})
		case "/api/v1/admin/groups/usage-summary":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{}, "total": 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err != nil || !result.Complete || len(result.Stats) != 1 || result.Stats[0].TodayActualCost != 0 {
		t.Fatalf("complete empty key set must be a true zero: result=%+v err=%v", result, err)
	}
}

func TestFetchGroupCostStatsForDateRejectsUnknownKeyGroup(t *testing.T) {
	var usageRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}}})
		case "/api/v1/admin/groups/usage-summary":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 11, "group_id": 99}}, "total": 1})
		case "/api/v1/usage/stats":
			usageRequests++
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 1.0}})
		}
	}))
	defer server.Close()

	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		context.Background(),
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err == nil || usageRequests != 0 {
		t.Fatalf("unknown group mapping must reject whole batch before usage fan-out: err=%v usageRequests=%d", err, usageRequests)
	}
}

func TestFetchGroupCostStatsForDateHonorsContextOnSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/groups/available" {
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "vip"}}})
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchGroupCostStatsForDate(
		ctx,
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "user-token"},
		[]GroupInfo{{ID: "1", Name: "vip"}},
		"2026-08-16",
		GroupCostSamplingState{},
	)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("summary request did not honor context: err=%v elapsed=%s", err, time.Since(started))
	}
}

// TestFetchAdminGroupDailyStats_DispatchesByPlatform 验证平台中性包装方法按
// session.Platform 正确路由到 sub2api / new-api 具体实现，不重复实现底层抓取逻辑。
func TestFetchAdminGroupDailyStats_DispatchesByPlatform(t *testing.T) {
	t.Run("sub2api uses usage-summary endpoint", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/groups/available":
				writeJSON(w, map[string]any{"data": []map[string]any{
					{"id": 1, "name": "default"},
					{"id": 2, "name": "vip"},
				}})
			case "/api/v1/admin/groups/usage-summary":
				writeJSON(w, map[string]any{"data": []map[string]any{
					{"group_id": 1, "today_actual_cost": 12.5},
					{"group_id": 2, "today_cost": 7.25},
				}})
			default:
				t.Fatalf("unexpected sub2api path: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		service := NewPlatformService(NewHTTPClient(server.Client()))
		session := Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"}

		stats, err := service.FetchAdminGroupDailyStats(session, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stats) != 2 {
			t.Fatalf("expected 2 stats, got %d", len(stats))
		}
		byName := map[string]float64{}
		for _, s := range stats {
			byName[s.GroupName] = s.TodayActualCost
		}
		if byName["default"] != 12.5 {
			t.Errorf("default cost = %.2f, want 12.50", byName["default"])
		}
		if byName["vip"] != 7.25 {
			t.Errorf("vip cost = %.2f, want 7.25", byName["vip"])
		}
	})

	t.Run("new-api uses per-group log stat with quota conversion", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/log/self/stat" {
				t.Fatalf("unexpected new-api path: %s", r.URL.Path)
			}
			group := r.URL.Query().Get("group")
			quota := map[string]float64{"default": 100000, "vip": 250000}[group]
			writeJSON(w, map[string]any{"data": map[string]any{"quota": quota}})
		}))
		defer server.Close()

		service := NewPlatformService(NewHTTPClient(server.Client()))
		session := Session{Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "session=abc", UserID: "1", QuotaPerUnit: 100000}
		groups := []GroupInfo{{Name: "default"}, {Name: "vip"}}

		stats, err := service.FetchAdminGroupDailyStats(session, groups)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stats) != 2 {
			t.Fatalf("expected 2 stats, got %d", len(stats))
		}
		byName := map[string]float64{}
		for _, s := range stats {
			byName[s.GroupName] = s.TodayActualCost
		}
		// quota / quotaPerUnit: 100000/100000 = 1.0, 250000/100000 = 2.5
		if byName["default"] != 1.0 {
			t.Errorf("default amount = %.4f, want 1.0000 (quota/quotaPerUnit conversion)", byName["default"])
		}
		if byName["vip"] != 2.5 {
			t.Errorf("vip amount = %.4f, want 2.5000 (quota/quotaPerUnit conversion)", byName["vip"])
		}
	})
}

func TestFetchAdminGroupDailyStatsForDate_Sub2APIUsesRequestedBusinessDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{
				"id":    1,
				"group": map[string]any{"name": "vip"},
			}}})
		case "/api/v1/usage/stats":
			if got := r.URL.Query().Get("start_date"); got != "2026-07-31" {
				t.Errorf("start_date = %q, want 2026-07-31", got)
			}
			if got := r.URL.Query().Get("end_date"); got != "2026-07-31" {
				t.Errorf("end_date = %q, want 2026-07-31", got)
			}
			if got := r.URL.Query().Get("timezone"); got != "Asia/Shanghai" {
				t.Errorf("timezone = %q, want Asia/Shanghai", got)
			}
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 12.5}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	service := NewPlatformService(NewHTTPClient(server.Client()))
	stats, err := service.FetchAdminGroupDailyStatsForDate(
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"},
		nil,
		"2026-07-31",
	)
	if err != nil {
		t.Fatalf("FetchAdminGroupDailyStatsForDate() error: %v", err)
	}
	if len(stats) != 1 || stats[0].GroupName != "vip" || stats[0].TodayActualCost != 12.5 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestFetchSub2APIAdminGroupDailyStatsByIDForDateUsesStableIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/dashboard/groups" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		for key, want := range map[string]string{
			"start_date": "2026-08-13",
			"end_date":   "2026-08-13",
			"timezone":   "Asia/Shanghai",
		} {
			if got := r.URL.Query().Get(key); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
		}
		if got := r.Header.Get("x-api-key"); got != "admin-key" {
			t.Fatalf("x-api-key = %q, want admin-key", got)
		}
		writeJSON(w, map[string]any{"data": []map[string]any{
			{"id": 901, "group_id": 11, "group_name": "同名分组", "today_actual_cost": 12.5},
			{"id": 902, "group_id": 12, "group_name": "同名分组", "today_actual_cost": 7.25},
		}})
	}))
	defer server.Close()

	stats, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminGroupDailyStatsByIDForDate(
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AdminAPIKey: "admin-key"},
		"2026-08-13",
	)
	if err != nil {
		t.Fatalf("FetchSub2APIAdminGroupDailyStatsByIDForDate() error: %v", err)
	}
	if len(stats) != 2 || stats[0].GroupID != "11" || stats[1].GroupID != "12" {
		t.Fatalf("stable group IDs were lost: %+v", stats)
	}
	if stats[0].GroupName != "同名分组" || stats[1].GroupName != "同名分组" || stats[0].TodayActualCost != 12.5 || stats[1].TodayActualCost != 7.25 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestFetchSub2APIAdminGroupDailyStatsByIDForDateRejectsIncompleteRows(t *testing.T) {
	for name, rows := range map[string][]map[string]any{
		"missing group id": {{"today_actual_cost": 1.0}},
		"missing amount":   {{"group_id": 11}},
		"duplicate group": {
			{"group_id": 11, "today_actual_cost": 1.0},
			{"group_id": 11, "today_actual_cost": 2.0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"data": rows})
			}))
			defer server.Close()

			_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminGroupDailyStatsByIDForDate(
				Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"},
				"2026-08-13",
			)
			if err == nil {
				t.Fatal("expected incomplete row to fail")
			}
		})
	}
}

func TestFetchSub2APIAdminGroupDailyStatsByIDForDateAcceptsExplicitEmptyGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"groups": []map[string]any{}}})
	}))
	defer server.Close()

	stats, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminGroupDailyStatsByIDForDate(
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"},
		"2026-08-13",
	)
	if err != nil {
		t.Fatalf("explicit empty groups must be a valid zero-revenue response: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("stats = %+v, want empty", stats)
	}
}

func TestFetchSub2APIAdminGroupDailyStatsByIDForDateRejectsMissingGroupsField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"start_date": "2026-08-13"}})
	}))
	defer server.Close()

	_, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminGroupDailyStatsByIDForDate(
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"},
		"2026-08-13",
	)
	if err == nil {
		t.Fatal("missing groups field must fail")
	}
}

func TestFetchAdminGroupDailyStatsForDate_NewAPIUsesOneRequestedBusinessDay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/log/self/stat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("start_timestamp"); got != "1785427200" {
			t.Errorf("start_timestamp = %q, want Shanghai 2026-07-31 00:00:00", got)
		}
		if got := r.URL.Query().Get("end_timestamp"); got != "1785513599" {
			t.Errorf("end_timestamp = %q, want Shanghai 2026-07-31 23:59:59", got)
		}
		writeJSON(w, map[string]any{"data": map[string]any{"quota": 250000}})
	}))
	defer server.Close()

	service := NewPlatformService(NewHTTPClient(server.Client()))
	stats, err := service.FetchAdminGroupDailyStatsForDate(
		Session{Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "session=abc", UserID: "1", QuotaPerUnit: 100000},
		[]GroupInfo{{Name: "vip"}},
		"2026-07-31",
	)
	if err != nil {
		t.Fatalf("FetchAdminGroupDailyStatsForDate() error: %v", err)
	}
	if len(stats) != 1 || stats[0].GroupName != "vip" || stats[0].TodayActualCost != 2.5 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// TestSub2APICostParsers 验证 sub2api 各降级路径的字段解析覆盖文档要求的
// today_actual_cost / total_actual_cost / actual_cost 语义。
func TestSub2APICostParsers(t *testing.T) {
	t.Run("group usage summary prefers today_actual_cost", func(t *testing.T) {
		got := sub2APIUsageSummaryCost(map[string]any{"today_actual_cost": 9.5})
		if got != 9.5 {
			t.Errorf("got %.2f, want 9.50", got)
		}
	})

	t.Run("per-key usage stats prefers total_actual_cost", func(t *testing.T) {
		got := sub2APIUsageStatsCost(map[string]any{"total_actual_cost": 4.2})
		if got != 4.2 {
			t.Errorf("got %.2f, want 4.20", got)
		}
	})

	t.Run("per-key usage stats falls back to actual_cost", func(t *testing.T) {
		got := sub2APIUsageStatsCost(map[string]any{"actual_cost": 3.1})
		if got != 3.1 {
			t.Errorf("got %.2f, want 3.10", got)
		}
	})

	t.Run("admin dashboard groups fallback parses today_actual_cost", func(t *testing.T) {
		got := sub2APIGroupDailyCost(map[string]any{"today_actual_cost": 6.6})
		if got != 6.6 {
			t.Errorf("got %.2f, want 6.60", got)
		}
	})

	t.Run("admin dashboard groups fallback falls back to actual_cost", func(t *testing.T) {
		got := sub2APIGroupDailyCost(map[string]any{"actual_cost": 1.23})
		if got != 1.23 {
			t.Errorf("got %.2f, want 1.23", got)
		}
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
