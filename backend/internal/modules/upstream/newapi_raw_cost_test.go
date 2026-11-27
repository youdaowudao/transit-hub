package upstream

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestSingaporeCostNewAPIRawCostFallback(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	session := Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", UserID: "1", AccessToken: "fixture-only", QuotaPerUnit: 100}
	groups := []GroupInfo{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}}
	for _, mode := range []string{"healthy", "group_mismatch", "pending", "auth", "duplicate", "short_page", "wrong_total", "total_changed", "key_pending", "key_mismatch", "key_healthy", "key_mixed_auth", "key_unknown", "raw_unknown_group", "known_code"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			rawCalls, totalCalls, tokenCalls := 0, 0, 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				mu.Lock()
				defer mu.Unlock()
				q := r.URL.Query()
				switch r.URL.Path {
				case "/api/token/":
					tokenCalls++
					return 200, map[string]any{"data": map[string]any{"total": 2, "items": []any{map[string]any{"id": 1, "name": "one", "group": "a"}, map[string]any{"id": 2, "name": "zero", "group": "b"}}}}
				case "/api/log/self/stat":
					if q.Get("group") == "" && q.Get("token_name") == "" {
						totalCalls++
						quota := 100
						if mode == "total_changed" && totalCalls > 1 {
							quota = 101
						}
						return 200, map[string]any{"data": map[string]any{"quota": quota}}
					}
					if mode == "known_code" {
						return 200, map[string]any{"success": false, "code": "API_KEY_EXPIRED", "message": "Statistics are still being calculated. Try again later."}
					}
					if mode == "auth" || mode == "key_mixed_auth" && q.Get("token_name") == "zero" {
						return 401, map[string]any{"message": "denied"}
					}
					if mode == "pending" || mode == "duplicate" || mode == "short_page" || mode == "wrong_total" || mode == "key_pending" || mode == "key_mixed_auth" || mode == "key_unknown" || mode == "raw_unknown_group" {
						return 200, map[string]any{"success": false, "message": "Statistics are still being calculated. Try again later."}
					}
					quota := 0
					if q.Get("group") == "a" {
						quota = 100
					}
					if mode == "group_mismatch" || mode == "key_mismatch" {
						quota = 101
					}
					return 200, map[string]any{"data": map[string]any{"quota": quota}}
				case "/api/log/self":
					rawCalls++
					st, en, _ := businessDayUnixBounds(date)
					if q.Get("start_timestamp") != fmt.Sprint(st) || q.Get("end_timestamp") != fmt.Sprint(en) || q.Get("type") != "2" {
						t.Error("raw date differs")
					}
					quota := 100
					if mode == "wrong_total" {
						quota = 99
					}
					row := map[string]any{"type": 2, "created_at": st, "quota": quota, "group": "a", "token_id": 1, "token_name": "one", "request_id": "r1"}
					if mode == "key_unknown" {
						row["token_id"] = 99
					}
					if mode == "raw_unknown_group" {
						row["group"] = "old-group"
					}
					rows := []any{row}
					total := 1
					if mode == "duplicate" {
						rows = append(rows, row)
						total = 2
					}
					if mode == "short_page" {
						total = 2
					}
					return 200, map[string]any{"success": true, "data": map[string]any{"total": total, "page": 1, "items": rows}}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					return 404, nil
				}
			})
			if mode == "key_pending" || mode == "key_mismatch" || mode == "key_healthy" || mode == "key_mixed_auth" || mode == "key_unknown" {
				items, total, _, err := p.fetchDatedKeyCollection(context.Background(), session, groups, date)
				if mode == "key_mixed_auth" || mode == "key_unknown" {
					if err == nil || len(items) != 0 || mode == "key_mixed_auth" && rawCalls != 0 || mode == "key_unknown" && rawCalls != 1 {
						t.Fatalf("unsafe key fallback: mode=%s raw=%d err=%v", mode, rawCalls, err)
					}
					return
				}
				if mode == "key_healthy" {
					if err != nil || len(items) != 2 || total != 1 || rawCalls != 0 || tokenCalls != 1 {
						t.Fatalf("healthy key path changed: raw=%d total=%g err=%v", rawCalls, total, err)
					}
					return
				}
				if err != nil || total != 1 || len(items) != 2 || items[0].KeyID != "1" || items[0].TodayAmount != 1 || items[1].KeyID != "2" || items[1].TodayAmount != 0 || rawCalls != 1 || tokenCalls != 1 {
					t.Fatalf("key fallback must retain catalog and zeros: items=%+v total=%g raw=%d tokens=%d err=%v", items, total, rawCalls, tokenCalls, err)
				}
				return
			}
			result, err := p.FetchGroupCostStatsForDate(context.Background(), session, groups, date, GroupCostSamplingState{})
			switch mode {
			case "healthy":
				if err != nil || rawCalls != 0 || len(result.Stats) != 2 {
					t.Fatalf("normal path changed: raw=%d result=%+v err=%v", rawCalls, result, err)
				}
			case "raw_unknown_group":
				if err != nil || result.Complete || result.UnattributedRawCost == nil || *result.UnattributedRawCost != 1 || result.Stats[0].TodayActualCost != 0 {
					t.Fatalf("historical group must remain unassigned: %+v err=%v", result, err)
				}
			case "group_mismatch", "pending":
				if err != nil || rawCalls != 1 || len(result.Stats) != 2 || result.Stats[0].TodayActualCost != 1 || result.Stats[1].TodayActualCost != 0 || result.Source == "group_summary" {
					t.Fatalf("raw group fallback failed: raw=%d result=%+v err=%v", rawCalls, result, err)
				}
			default:
				if err == nil {
					t.Fatal("invalid or changed totals must not publish")
				}
				if (mode == "auth" || mode == "total_changed" || mode == "known_code") && rawCalls != 0 {
					t.Fatal("unrelated failure triggered fallback")
				}
			}
		})
	}
}

func TestSingaporeCostRawLogPagination(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	start, _, _ := businessDayUnixBounds(date)
	session := Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", UserID: "1", AccessToken: "fixture-only", QuotaPerUnit: 100}
	for _, mode := range []string{"complete", "duplicate_cross_page", "changed_count", "count_cap", "missing_id", "wrong_date", "false_success"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				pages++
				if r.URL.Path != "/api/log/self" || r.URL.Query().Get("p") != fmt.Sprint(pages) || r.URL.Query().Get("page_size") != "100" {
					t.Errorf("unexpected page request: %s", r.URL.Path)
				}
				total := 101
				if mode == "count_cap" {
					total = 10000
				}
				if mode == "changed_count" && pages == 2 {
					total = 102
				}
				n := 100
				if pages == 2 {
					n = 1
				}
				rows := make([]any, n)
				for i := range rows {
					id := fmt.Sprintf("%d-%d", pages, i)
					if mode == "duplicate_cross_page" && pages == 2 {
						id = "1-0"
					}
					if mode == "missing_id" {
						id = ""
					}
					at := start
					if mode == "wrong_date" {
						at--
					}
					rows[i] = map[string]any{"type": 2, "created_at": at, "quota": 1, "group": "a", "token_id": 1, "request_id": id}
				}
				payload := map[string]any{"success": true, "data": map[string]any{"total": total, "page": pages, "items": rows}}
				if mode == "false_success" {
					delete(payload, "success")
				}
				return 200, payload
			})
			result, err := p.fetchNewAPIRawUsage(context.Background(), session, date)
			if mode == "complete" {
				if err != nil || pages != 2 || result.groups["a"] != 101 || result.keys["1"] != 101 {
					t.Fatalf("pagination failed: %+v pages=%d err=%v", result, pages, err)
				}
			} else if err == nil {
				t.Fatalf("incomplete raw logs accepted: %s", mode)
			}
		})
	}
}

func TestSingaporeCostRawLogConversionRejectsInvalid(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	start, _, _ := businessDayUnixBounds(date)
	session := Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", UserID: "1", AccessToken: "fixture-only", QuotaPerUnit: 1e-310}
	groups := []GroupInfo{{ID: "a", Name: "a"}}
	p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/token/":
			return 200, map[string]any{"data": map[string]any{"total": 1, "items": []any{map[string]any{"id": 1, "name": "one", "group": "a"}}}}
		case "/api/log/self/stat":
			if r.URL.Query().Get("group") == "" && r.URL.Query().Get("token_name") == "" {
				return 200, map[string]any{"data": map[string]any{"quota": 0}}
			}
			return 200, map[string]any{"success": false, "message": "Statistics are still being calculated. Try again later."}
		case "/api/log/self":
			return 200, map[string]any{"success": true, "data": map[string]any{"total": 1, "items": []any{map[string]any{"type": 2, "created_at": start, "quota": 1, "group": "a", "token_id": 1, "request_id": "r1"}}}}
		default:
			return 404, nil
		}
	})
	if result, err := p.FetchGroupCostStatsForDate(context.Background(), session, groups, date, GroupCostSamplingState{}); err == nil {
		t.Fatalf("positive unconvertible quota published as zero group cost: %+v", result)
	}
	if items, _, _, err := p.fetchDatedKeyCollection(context.Background(), session, groups, date); err == nil {
		t.Fatalf("positive unconvertible quota published as zero Key cost: %+v", items)
	}
}

func TestSingaporeCostRawFallbackDirectReason(t *testing.T) {
	old := datedCostError(PlatformNewAPI, "metrics", strings.Repeat("旧统计失败，", 50))
	fallback := datedCostError(PlatformNewAPI, "metrics", "原始消费记录重复")
	if got := costFailureCause(rawCostFallbackError(old, fallback)); !strings.Contains(got, "原始消费记录重复") {
		t.Fatalf("direct fallback failure was truncated: %s", got)
	}
}
