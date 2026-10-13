package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

// 主代理冻结业务期望。模拟 transport 不启动监听、进程或真实上游。
func singaporeCostPlatform(t *testing.T, now time.Time, handle func(*http.Request) (int, any)) *PlatformService {
	t.Helper()
	client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, payload := handle(r)
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	p := NewPlatformService(NewHTTPClient(client))
	p.now = func() time.Time { return now }
	return p
}

func singaporeCostDateQuery(t *testing.T, r *http.Request, date string) {
	t.Helper()
	q := r.URL.Query()
	if q.Get("start_date") != date || q.Get("end_date") != date || q.Get("timezone") != "Asia/Singapore" || q.Get("period") != "" {
		t.Errorf("查询日必须是输入的新加坡日期，实际参数=%v", q)
	}
}

func TestSingaporeCostCriticalNativeGroups(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	// 真实排查的六站独立总额；仅投影公开的编号/费用，无凭据。
	for _, tc := range []struct {
		name   string
		amount float64
	}{
		{"340", .41558589}, {"可达鸭", .07410396}, {"智元AI", 2.72469103}, {"林", .15651599}, {"synoralink", 0}, {"透明人", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/api/v1/usage/stats":
					singaporeCostDateQuery(t, r, date)
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": tc.amount}}
				case "/api/v1/usage/dashboard/snapshot-v2":
					singaporeCostDateQuery(t, r, date)
					q := r.URL.Query()
					if q.Get("include_trend") != "false" || q.Get("include_model_stats") != "false" || q.Get("include_group_stats") != "true" {
						t.Error("原生分组开关不符")
					}
					return 200, map[string]any{"data": map[string]any{"start_date": date, "end_date": date, "groups": []any{map[string]any{"group_id": 1, "actual_cost": tc.amount}}}}
				case "/api/v1/groups/available":
					return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": "同名"}, map[string]any{"id": 2, "name": "同名"}}}
				case "/api/v1/keys":
					return 200, map[string]any{"data": []any{map[string]any{"id": 210, "group_id": 11}}, "total": 1}
				default:
					return 403, map[string]any{"message": "当前接口不可读"}
				}
			})
			result, err := p.FetchGroupCostStatsForDate(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, []GroupInfo{{ID: "1", Name: "同名"}, {ID: "2", Name: "同名"}}, date, GroupCostSamplingState{})
			if err != nil {
				t.Fatalf("不可见零Key不能阻断原生日志费用：%v; 请求=%v", err, paths)
			}
			want := []string{"/api/v1/usage/stats", "/api/v1/usage/dashboard/snapshot-v2", "/api/v1/usage/stats"}
			if !reflect.DeepEqual(paths, want) {
				t.Errorf("本成本入口只许三个用户请求：%v", paths)
			}
			if !result.Complete || result.Source != GroupCostSourceSummary || result.SiteReportedRawCost == nil || math.Abs(*result.SiteReportedRawCost-tc.amount) > 1e-9 || len(result.Stats) != 2 {
				t.Fatalf("原生稀疏结果及独立核验错误：%+v", result)
			}
		})
	}
}

func TestSingaporeCostCriticalGroupReconciliation(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, tc := range []struct {
		name       string
		a, b, g, u float64
		missing    bool
		failure    bool
	}{
		{"不可见正消费", 3, 3, 2, 1, false, false}, {"微小正消费不抹零", 2.0000000001, 2.0000000001, 2, .0000000001, false, false},
		{"消费变化", 2, 3, 3, 0, false, true}, {"稳定缺额", 3, 3, 2, 0, false, true}, {"缺groups不是零", 0, 0, 0, 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/usage/stats":
					reads++
					amount := tc.a
					if reads > 1 {
						amount = tc.b
					}
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": amount}}
				case "/api/v1/usage/dashboard/snapshot-v2":
					data := map[string]any{"start_date": date, "end_date": date}
					if !tc.missing {
						data["groups"] = []any{map[string]any{"group_id": 1, "actual_cost": tc.g}, map[string]any{"group_id": 99, "actual_cost": tc.u}}
					}
					return 200, map[string]any{"data": data}
				default:
					return 404, map[string]any{"message": "接口不存在"}
				}
			})
			r, err := p.FetchGroupCostStatsForDate(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, []GroupInfo{{ID: "1", Name: "可见"}}, date, GroupCostSamplingState{})
			if tc.failure {
				if err == nil {
					t.Fatalf("变化/缺额/缺字段不能发布：%+v", r)
				}
				return
			}
			if err != nil || r.Complete || r.UnattributedRawCost == nil || *r.UnattributedRawCost != tc.u || r.GroupAttributedRawCost == nil || *r.GroupAttributedRawCost != tc.g || r.SiteReportedRawCost == nil || *r.SiteReportedRawCost != tc.b {
				t.Fatalf("不可见费用须真实保留：%+v %v", r, err)
			}
		})
	}
}

func TestSingaporeCostCriticalMetricsUsesExplicitDate(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	reads := 0
	p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
		case "/api/v1/usage/dashboard/stats":
			return 200, map[string]any{"data": map[string]any{"today_actual_cost": .21543435, "total_cost": 7}}
		case "/api/v1/usage/stats":
			reads++
			singaporeCostDateQuery(t, r, date)
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": .726117134}}
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			return 200, map[string]any{"data": []any{}}
		default:
			return 404, map[string]any{}
		}
	})
	m, err := p.FetchMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	if err != nil || m.TodayConsume.Value == nil || *m.TodayConsume.Value != .726117134 || m.TodayConsumeDate != date || reads != 1 {
		t.Fatalf("站点费用仍采用服务器today：%+v %v reads=%d", m, err, reads)
	}
	encoded, _ := json.Marshal(m)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if fields["todayConsumeTimezone"] != "Asia/Singapore" {
		t.Fatal("显式日期成功必须携带费用时区证明")
	}
}

func TestSingaporeCostCriticalKeyRequiresIndependentTotals(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, tc := range []struct {
		name      string
		a, b, k   float64
		duplicate bool
		complete  bool
	}{
		{"稳定完整", 3, 3, 3, false, true}, {"已删Key缺消费", 4, 4, 3, false, false}, {"消费变化", 3, 4, 3, false, false}, {"重复ID", 3, 3, 3, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			totals := 0
			batches := 0
			keys := 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/keys":
					rows := []any{map[string]any{"id": 1, "name": "使用中"}}
					if tc.duplicate {
						rows = append(rows, rows[0])
					}
					return 200, map[string]any{"data": rows, "total": len(rows)}
				case "/api/v1/usage/dashboard/api-keys-usage":
					batches++
					return 200, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": tc.k}}}}
				case "/api/v1/usage/dashboard/stats":
					return 200, map[string]any{"data": map[string]any{"today_actual_cost": tc.b}}
				case "/api/v1/usage/stats":
					singaporeCostDateQuery(t, r, date)
					amount := tc.k
					if r.URL.Query().Get("api_key_id") == "" {
						totals++
						amount = tc.a
						if totals > 1 {
							amount = tc.b
						}
					} else {
						keys++
					}
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": amount}}
				default:
					return 404, map[string]any{}
				}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
			svc := NewService(p, nil, nil, newFakeSiteCache())
			svc.cache.(*fakeSiteCache).add(site)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			r, err := svc.KeyUsageForDate(t.Context(), "user", "workspace", date)
			if tc.complete {
				if err != nil || r.CompletedSites != 1 || len(r.Sites) != 1 || !r.Sites[0].Complete || totals != 2 || batches != 0 || keys != 1 {
					t.Fatalf("Key指定日/A/B/K未完成：%+v err=%v totals=%d batches=%d keys=%d", r, err, totals, batches, keys)
				}
			} else if r.CompletedSites != 0 || (len(r.Sites) > 0 && r.Sites[0].Complete) {
				t.Fatalf("不完整Key被认证：%+v err=%v", r, err)
			}
		})
	}
}

func TestSingaporeCostCriticalOldSnapshotCannotConfirm(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	cache := newKeySnapshotTestCache(site)
	// 本用例故意是旧记录，不复用可随迁移新增证明的通用fixture。
	amount := 9.0
	_ = cache.SaveKeyUsageSnapshot(t.Context(), site.ID, KeyUsageSnapshot{BusinessDate: businesstime.DateAt(now), StartedAt: now, AttemptStartedAt: now, CollectedAt: now, ConsumeDate: businesstime.DateAt(now), SyncedRawCost: &amount, CollectedRawCost: &amount, Complete: true, Items: []KeyUsageTodayStat{{KeyID: "1", TodayAmount: amount}}})
	svc := NewService(nil, nil, nil, cache)
	svc.now = func() time.Time { return now }
	r, err := svc.CachedKeyUsageForDate(t.Context(), "user", "workspace", businesstime.DateAt(now))
	if err != nil || r.CompletedSites != 0 || len(r.Sites) != 1 || r.Sites[0].Complete || len(r.Sites[0].Items) > 0 {
		t.Fatalf("旧快照没有时区证明仍进入费用：%+v %v", r, err)
	}
}

type singaporeCostCriticalRepository struct {
	durable              Site
	unsafeWrites, checks int
	saveError            error
}

func (r *singaporeCostCriticalRepository) ListSites(context.Context) ([]Site, error) {
	return []Site{r.durable}, nil
}
func (r *singaporeCostCriticalRepository) ListSitesForUser(context.Context, string) ([]Site, error) {
	return []Site{r.durable}, nil
}
func (r *singaporeCostCriticalRepository) DeleteSite(context.Context, string, string) error {
	return nil
}
func (r *singaporeCostCriticalRepository) SaveSite(_ context.Context, site Site) error {
	r.unsafeWrites++
	r.durable = site
	return nil
}
func (r *singaporeCostCriticalRepository) SaveExistingSiteIfUnchanged(_ context.Context, expected, candidate Site) (bool, error) {
	r.checks++
	if !reflect.DeepEqual(expected.Session, r.durable.Session) {
		return false, nil
	}
	if r.saveError != nil {
		return false, r.saveError
	}
	r.durable = candidate
	return true, nil
}

func TestSingaporeCostCriticalAllExistingWritesCheckDurableSession(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, entry := range []string{"settings", "enabled", "metadata", "sync-success", "sync-failure", "session-unchanged", "session-refresh"} {
		t.Run(entry, func(t *testing.T) {
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				if entry == "sync-failure" {
					return 401, map[string]any{"message": "登录资料已失效"}
				}
				switch r.URL.Path {
				case "/api/v1/auth/me":
					return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
				case "/api/v1/usage/dashboard/stats":
					return 200, map[string]any{"data": map[string]any{"today_actual_cost": 3}}
				case "/api/v1/usage/stats":
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 3}}
				default:
					return 200, map[string]any{"data": []any{}}
				}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
			site.BaseURL = site.Session.BaseURL
			site.Account = "原账号"
			site.RequestedPlatform = PlatformSub2API
			cache := newFakeSiteCache()
			cache.add(site)
			before, _ := json.Marshal(site)
			durable := *site
			session := *site.Session
			session.AccessToken = "fixture-new"
			durable.Session = &session
			repo := &singaporeCostCriticalRepository{durable: durable}
			svc := NewService(p, repo, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetBackgroundTasksDisabled(true)
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			var err error
			threshold := 7.0
			switch entry {
			case "settings":
				_, err = svc.UpdateSettings(t.Context(), "user", site.ID, SiteSettings{BalanceThreshold: &threshold})
			case "enabled":
				_, err = svc.UpdateEnabled(t.Context(), "user", site.ID, false)
			case "metadata":
				_, err = svc.Update(t.Context(), "user", site.ID, UpdateRequest{Name: "新名称", SiteURL: site.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: site.Account, RechargeRate: 1})
			case "sync-success", "sync-failure":
				_, err = svc.sync(t.Context(), site.ID)
			case "session-unchanged", "session-refresh":
				refreshed := *site.Session
				if entry == "session-refresh" {
					refreshed.AccessToken = "fixture-rotated"
				}
				var ok bool
				ok, err = svc.persistKeyUsageSession(*site, refreshed)
				if ok {
					t.Error("PG已不同仍返回会话可发布")
				}
			}
			current, _ := cache.Get(t.Context(), site.ID)
			after, _ := json.Marshal(current)
			if repo.unsafeWrites != 0 || repo.durable.Session.AccessToken != "fixture-new" || repo.checks == 0 {
				t.Errorf("旧请求覆回新PG或未做条件检查：unsafe=%d checks=%d", repo.unsafeWrites, repo.checks)
			}
			if entry != "sync-success" && entry != "sync-failure" && string(before) != string(after) {
				t.Error("条件失败改变缓存资料")
			}
			if entry != "session-unchanged" && entry != "session-refresh" && err == nil {
				t.Error("条件失败不能报告操作成功")
			}
		})
	}
}

func TestSingaporeCostCriticalIdentityEditRejected(t *testing.T) {
	for _, field := range []string{"url", "account", "platform", "mode"} {
		t.Run(field, func(t *testing.T) {
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
			site.BaseURL = site.Session.BaseURL
			site.Account = "原账号"
			site.RequestedPlatform = PlatformSub2API
			cache := newFakeSiteCache()
			cache.add(site)
			before, _ := json.Marshal(site)
			svc := NewService(nil, nil, nil, cache)
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			dto := UpdateRequest{Name: site.Name, SiteURL: site.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: site.Account, RechargeRate: 1}
			switch field {
			case "url":
				dto.SiteURL = "http://other.invalid"
			case "account":
				dto.Account = "其他账号"
			case "platform":
				dto.Platform = PlatformNewAPI
			case "mode":
				dto.AuthMode = AuthModePassword
			}
			_, err := svc.Update(t.Context(), "user", site.ID, dto)
			afterSite, _ := cache.Get(t.Context(), site.ID)
			after, _ := json.Marshal(afterSite)
			if err == nil || string(before) != string(after) {
				t.Fatalf("原编号换身份未拒绝或原值被改：err=%v unchanged=%v", err, string(before) == string(after))
			}
		})
	}
}

func TestSingaporeCostCriticalCredentialPrincipal(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"same", "different", "old-missing-id", "old-expired"} {
		t.Run(scenario, func(t *testing.T) {
			oldReads, newReads := 0, 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/auth/me":
					id := 1
					if r.Header.Get("Authorization") == "Bearer fixture-old" {
						oldReads++
						if scenario == "old-expired" {
							return 401, map[string]any{"message": "原登录已失效"}
						}
						if scenario == "old-missing-id" {
							return 200, map[string]any{"data": map[string]any{"balance": 20}}
						}
					} else {
						newReads++
						if scenario == "different" {
							id = 2
						}
					}
					return 200, map[string]any{"data": map[string]any{"id": id, "balance": 20}}
				case "/api/v1/usage/dashboard/stats":
					return 200, map[string]any{"data": map[string]any{"today_actual_cost": 3}}
				case "/api/v1/usage/stats":
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 3}}
				default:
					return 200, map[string]any{"data": []any{}}
				}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
			site.BaseURL = site.Session.BaseURL
			site.Account = "原账号"
			site.RequestedPlatform = PlatformSub2API
			cache := newFakeSiteCache()
			cache.add(site)
			before, _ := json.Marshal(site)
			svc := NewService(p, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetBackgroundTasksDisabled(true)
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			_, err := svc.Update(t.Context(), "user", site.ID, UpdateRequest{Name: site.Name, SiteURL: site.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: site.Account, AccessToken: "fixture-new", RechargeRate: 1})
			current, _ := cache.Get(t.Context(), site.ID)
			after, _ := json.Marshal(current)
			if scenario == "same" {
				if err != nil || current.Session.AccessToken != "fixture-new" || oldReads != 1 || newReads != 2 {
					t.Fatalf("同账号凭据更新需旧身份一次、新登录/身份各一次：err=%v old=%d new=%d", err, oldReads, newReads)
				}
				return
			}
			if err == nil || string(before) != string(after) {
				t.Fatalf("无法确认原身份须整次拒绝原值不改：scenario=%s err=%v unchanged=%v", scenario, err, string(before) == string(after))
			}
			if oldReads != 1 || (scenario != "different" && newReads != 0) {
				t.Error("原身份失败后仍执行新登录")
			}
		})
	}
}

func TestSingaporeCostCriticalOriginalSixSiteReplay(t *testing.T) {
	body, err := os.ReadFile("testdata/singapore_cost_six_sites.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name            string `json:"name"`
		Date            string `json:"date"`
		AvailableGroups []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"availableGroups"`
		OldKeys                             []map[string]any `json:"oldKeys"`
		AccountBefore, AccountAfter, Native map[string]any
	}
	if err = json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 6 {
		t.Fatal("fixture必须保留六站")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			now, err := time.ParseInLocation("2006-01-02", f.Date, businesstime.Location())
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(12 * time.Hour)
			totals, legacy, native := 0, 0, 0
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/usage/stats":
					singaporeCostDateQuery(t, r, f.Date)
					totals++
					if totals == 1 {
						return 200, f.AccountBefore
					}
					return 200, f.AccountAfter
				case "/api/v1/usage/dashboard/snapshot-v2":
					native++
					singaporeCostDateQuery(t, r, f.Date)
					return 200, f.Native
				case "/api/v1/groups/available":
					legacy++
					return 200, map[string]any{"data": f.AvailableGroups}
				case "/api/v1/keys":
					legacy++
					return 200, map[string]any{"data": f.OldKeys, "total": len(f.OldKeys)}
				default:
					legacy++
					return 403, map[string]any{"message": "未提供管理员费用权限"}
				}
			})
			groups := make([]GroupInfo, len(f.AvailableGroups))
			for i, g := range f.AvailableGroups {
				groups[i] = GroupInfo{ID: strconv.Itoa(g.ID), Name: g.Name}
			}
			r, err := p.FetchGroupCostStatsForDate(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, groups, f.Date, GroupCostSamplingState{})
			if err != nil || !r.Complete || len(r.Stats) != len(groups) || totals != 2 || native != 1 || legacy != 0 {
				t.Fatalf("六站原输入回放未完成 native=%d totals=%d legacy=%d result=%+v err=%v", native, totals, legacy, r, err)
			}
			want := map[string]float64{}
			for _, row := range f.Native["data"].(map[string]any)["groups"].([]any) {
				v := row.(map[string]any)
				want[strconv.Itoa(int(v["group_id"].(float64)))] = v["actual_cost"].(float64)
			}
			for _, g := range r.Stats {
				if g.TodayActualCost != want[g.GroupID] {
					t.Errorf("分组%s原消费费用改变：got=%v want=%v", g.GroupID, g.TodayActualCost, want[g.GroupID])
				}
			}
		})
	}
}

func TestSingaporeCostCriticalMalformedKeysNeverComplete(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"missing-id", "empty-id", "fraction-id", "non-object", "missing-cost", "negative-cost", "nan-cost", "truncated-total", "oversized-page"} {
		t.Run(scenario, func(t *testing.T) {
			rows := []any{map[string]any{"id": 1, "name": "使用中"}}
			total := 1
			costData := map[string]any{"total_actual_cost": 1.0}
			switch scenario {
			case "missing-id":
				rows = []any{map[string]any{"name": "缺编号"}}
			case "empty-id":
				rows = []any{map[string]any{"id": ""}}
			case "fraction-id":
				rows = []any{map[string]any{"id": 1.5}}
			case "non-object":
				rows = []any{"broken"}
			case "missing-cost":
				costData = map[string]any{"total_cost": 1.0}
			case "negative-cost":
				costData["total_actual_cost"] = -1.0
			case "nan-cost":
				costData["total_actual_cost"] = "NaN"
			case "truncated-total":
				total = 2
			case "oversized-page":
				rows = nil
				for i := 1; i <= 101; i++ {
					rows = append(rows, map[string]any{"id": i})
				}
				total = 101
			}
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/keys":
					if r.URL.Query().Get("page") != "1" {
						return 200, map[string]any{"data": []any{}, "total": total}
					}
					return 200, map[string]any{"data": rows, "total": total}
				case "/api/v1/usage/stats":
					if r.URL.Query().Get("api_key_id") == "" {
						return 200, map[string]any{"data": map[string]any{"total_actual_cost": 1.0}}
					}
					return 200, map[string]any{"data": costData}
				default:
					return 404, map[string]any{}
				}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
			cache := newFakeSiteCache()
			cache.add(site)
			svc := NewService(p, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			svc.SetBackgroundTasksDisabled(true)
			t.Cleanup(svc.Close)
			r, err := svc.KeyUsageForDate(t.Context(), "user", "workspace", businesstime.DateAt(now))
			if r.CompletedSites != 0 || len(r.Sites) != 1 || r.Sites[0].Complete || len(r.Sites[0].Items) != 0 {
				t.Fatalf("畸形/缺字段/截断Key不能返回完整或部分费用：scenario=%s completed=%d sites=%d err=%v", scenario, r.CompletedSites, len(r.Sites), err)
			}
		})
	}
}

func TestSingaporeCostCriticalNewAPIZeroTotalDoesNotCertifyMissingData(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"missing-quota", "missing-id", "duplicate-id", "truncated-total", "missing-array"} {
		t.Run(scenario, func(t *testing.T) {
			rows := []any{map[string]any{"id": 1, "name": "同名", "group": "vip"}}
			total := 1
			switch scenario {
			case "missing-id":
				rows = []any{map[string]any{"name": "同名"}}
			case "duplicate-id":
				rows = append(rows, rows[0])
				total = 2
			case "truncated-total":
				total = 2
			}
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/token/":
					if scenario == "missing-array" {
						return 200, map[string]any{"success": true, "data": map[string]any{"total": 0}}
					}
					if r.URL.Query().Get("p") != "1" {
						return 200, map[string]any{"success": true, "data": []any{}, "total": total}
					}
					return 200, map[string]any{"success": true, "data": rows, "total": total}
				case "/api/log/self/stat":
					if scenario == "missing-quota" && r.URL.Query().Get("token_name") != "" {
						return 200, map[string]any{"success": true, "data": map[string]any{}}
					}
					return 200, map[string]any{"success": true, "data": map[string]any{"quota": 0}}
				default:
					return 404, map[string]any{}
				}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", UserID: "1", AccessToken: "fixture-only", QuotaPerUnit: 500000})
			site.Platform = PlatformNewAPI
			cache := newFakeSiteCache()
			cache.add(site)
			svc := NewService(p, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			svc.SetBackgroundTasksDisabled(true)
			t.Cleanup(svc.Close)
			r, err := svc.KeyUsageForDate(t.Context(), "user", "workspace", businesstime.DateAt(now))
			if r.CompletedSites != 0 || len(r.Sites) != 1 || r.Sites[0].Complete || len(r.Sites[0].Items) != 0 {
				t.Fatalf("独立总额真零不能替缺失NewAPI数据认证：scenario=%s completed=%d sites=%d err=%v", scenario, r.CompletedSites, len(r.Sites), err)
			}
		})
	}
}

func TestSingaporeCostCriticalSyncFailedPGKeepsRotatedCacheToken(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			return 200, map[string]any{"data": map[string]any{"access_token": "fixture-new", "refresh_token": "fixture-new-refresh", "token_type": "Bearer", "expires_in": 3600}}
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
		case "/api/v1/usage/dashboard/stats":
			return 200, map[string]any{"data": map[string]any{"today_actual_cost": 9}}
		case "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 9}}
		default:
			return 200, map[string]any{"data": []any{}}
		}
	})
	expires := now.Add(-time.Second).UnixMilli()
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old", RefreshToken: "fixture-old-refresh", ExpiresAt: &expires})
	cache := newSyncTestCache(site)
	repo := &singaporeCostCriticalRepository{durable: *site, saveError: errors.New("fixture PG unavailable")}
	svc := NewService(p, repo, nil, cache)
	svc.now = func() time.Time { return now }
	svc.SetBackgroundTasksDisabled(true)
	t.Cleanup(svc.Close)
	_, err := svc.sync(t.Context(), site.ID)
	current, _ := cache.Get(t.Context(), site.ID)
	if err == nil || current == nil || current.Session == nil || current.Session.AccessToken != "fixture-new" || current.Session.RefreshToken != "fixture-new-refresh" {
		t.Fatalf("PG保存失败后的本任务瞬态撤销覆回旧Token：err=%v newSessionRetained=%v", err, current != nil && current.Session != nil && current.Session.AccessToken == "fixture-new")
	}
	if repo.durable.Session.AccessToken != "fixture-old" {
		t.Fatal("PG失败不得回写原PG或虚构持久成功")
	}
	if current.Status == StatusSyncing {
		t.Fatal("同步已经失败并结束，当前缓存不得继续保留本任务的同步中状态")
	}
}

func TestSingaporeCostCriticalNoUserIdentityMakesNoCostRequests(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, session := range []Session{{Platform: PlatformNewAPI}, {Platform: PlatformNewAPI, AccessToken: "fixture-only"}, {Platform: PlatformSub2API, AdminAPIKey: "fixture-only"}} {
		t.Run(string(session.Platform)+"/"+session.AccessToken, func(t *testing.T) {
			requests := 0
			p := singaporeCostPlatform(t, now, func(*http.Request) (int, any) {
				requests++
				return 200, map[string]any{"data": map[string]any{"quota": 0, "groups": []any{}}}
			})
			session.BaseURL = "http://fixture.invalid"
			_, err := p.FetchGroupCostStatsForDate(t.Context(), session, []GroupInfo{{ID: "1", Name: "vip"}}, businesstime.DateAt(now), GroupCostSamplingState{})
			if err == nil || requests != 0 {
				t.Fatalf("缺用户身份成本入口必须请求前拒绝：requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestSingaporeCostCriticalPagingKeepsFirstPageTotal(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, platform := range []Platform{PlatformSub2API, PlatformNewAPI} {
		for _, scenario := range []string{"truncated", "exact", "exceeds"} {
			t.Run(string(platform)+"/"+scenario, func(t *testing.T) {
				declared, lastCount := 150, 10
				if scenario == "exact" {
					lastCount = 50
				}
				if scenario == "exceeds" {
					declared = 105
				}
				p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
					if r.URL.Path == "/api/v1/keys" || r.URL.Path == "/api/token/" {
						page := r.URL.Query().Get("page")
						if platform == PlatformNewAPI {
							page = r.URL.Query().Get("p")
						}
						count, offset := 100, 0
						if page == "2" {
							count, offset = lastCount, 100
						}
						items := make([]any, 0, count)
						for i := 1; i <= count; i++ {
							id := offset + i
							items = append(items, map[string]any{"id": id, "name": "token-" + strconv.Itoa(id), "group": "default", "used_quota": 0})
						}
						data := map[string]any{"items": items}
						if page == "1" {
							data["total"] = declared
						}
						return 200, map[string]any{"data": data}
					}
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 0, "quota": 0}}
				})
				site := newTestSite("site", "user", "workspace", 1, &Session{Platform: platform, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-user", UserID: "1"})
				cache := newSyncTestCache(site)
				svc := NewService(p, nil, nil, cache)
				svc.now = func() time.Time { return now }
				svc.SetBackgroundTasksDisabled(true)
				t.Cleanup(svc.Close)
				result, err := svc.KeyUsageForDate(t.Context(), site.UserID, site.AdminAccountID, businesstime.DateAt(now))
				if err != nil || len(result.Sites) != 1 {
					t.Fatalf("真实Key入口错误：%+v %v", result, err)
				}
				wantComplete := scenario == "exact"
				if result.Sites[0].Complete != wantComplete {
					t.Fatalf("后页缺total不能取消首页声明：scenario=%s complete=%v total=%d rows=%d", scenario, result.Sites[0].Complete, declared, len(result.Sites[0].Items))
				}
				if !wantComplete && (len(result.Sites[0].Items) != 0 || result.CompletedSites != 0) {
					t.Fatal("截断或超额列表不可部分发布或确认")
				}
				if wantComplete && len(result.Sites[0].Items) != declared {
					t.Fatal("total仅首页出现且数量正确的列表应完整保留")
				}
			})
		}
	}
}

type singaporeCollectorFailureRepository struct {
	*singaporeCostCriticalRepository
	calls int
}

func (r *singaporeCollectorFailureRepository) SaveExistingSiteIfUnchanged(ctx context.Context, expected, candidate Site) (bool, error) {
	r.calls++
	if r.calls > 1 {
		return false, errors.New("fixture PG collector write unavailable")
	}
	return r.singaporeCostCriticalRepository.SaveExistingSiteIfUnchanged(ctx, expected, candidate)
}

func TestSingaporeCostCriticalCollectorSaveFailureReachesAccountRefresh(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	keyRequests := 0
	p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		if r.URL.Path == "/api/v1/keys" {
			keyRequests++
		}
		if r.URL.Path == "/api/v1/auth/me" {
			return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
		}
		if r.URL.Path == "/api/v1/usage/stats" {
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 0}}
		}
		return 200, map[string]any{"data": []any{}}
	})
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-user"})
	cache := newKeySnapshotTestCache(site)
	repo := &singaporeCollectorFailureRepository{singaporeCostCriticalRepository: &singaporeCostCriticalRepository{durable: cloneSite(site)}}
	svc := NewService(p, repo, nil, cache)
	svc.now = func() time.Time { return now }
	svc.SetBackgroundTasksDisabled(true)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(svc.Close)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := svc.SyncAndCollectKeyUsage(ctx, "user", "workspace", businesstime.DateAt(now), now.Add(-time.Minute))
	if err != nil || len(result.Sites) != 1 {
		t.Fatalf("账号刷新真实入口：%+v %v", result, err)
	}
	item := result.Sites[0]
	if item.Complete || result.CompletedSites != 0 || len(item.Items) != 0 {
		t.Fatal("collector保存失败不得发布费用")
	}
	if !strings.Contains(item.Error, "保存") || !strings.Contains(item.Error, "fixture PG collector write unavailable") {
		t.Fatalf("同步成功后的采集保存原因/实际结果被吞掉：%q", item.Error)
	}
	if keyRequests != 0 {
		t.Fatalf("保存身份未通过不应发Key请求：%d", keyRequests)
	}
	snapshot, _ := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	if snapshot == nil || !strings.Contains(snapshot.FailureReason, "保存") {
		t.Fatal("已有快照失败通道未保留存储原因")
	}
}

func TestSingaporeCostCriticalKnownRefusalCannotPublishLate(t *testing.T) {
	base := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, state := range []string{"new-session", "new-success", "new-workspace", "newer-key-success", "midnight"} {
		t.Run(state, func(t *testing.T) {
			failedAt := base.Add(-2 * time.Minute)
			now := base.Add(time.Minute)
			if state == "midnight" {
				failedAt = time.Date(2031, 2, 3, 15, 58, 0, 0, time.UTC)
				now = time.Date(2031, 2, 3, 16, 2, 0, 0, time.UTC)
			}
			original := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
			original.Metrics = Metrics{TodayConsumeDate: businesstime.DateAt(failedAt), TodayConsumeStatus: "unreadable", TodayConsumeFailedAt: &failedAt, TodayConsumeErrorDetail: "原请求费用拒绝"}
			current := cloneSite(original)
			if state == "new-session" {
				current.Session.AccessToken = "fixture-new"
			}
			if state == "new-success" {
				current.Metrics.TodayConsumeStatus = "ok"
				current.Metrics.TodayConsumeFailedAt = nil
				current.Metrics.TodayConsumeErrorDetail = ""
			}
			if state == "new-workspace" {
				current.AdminAccountID = "workspace-new"
			}
			cache := newKeySnapshotTestCache(&current)
			amount := 3.0
			goodAt := now.Add(-time.Second)
			good := KeyUsageSnapshot{BusinessDate: businesstime.DateAt(now), StartedAt: goodAt, AttemptStartedAt: goodAt, CollectedAt: goodAt, ConsumeDate: businesstime.DateAt(now), Timezone: businesstime.Timezone, Complete: true, SyncedRawCost: &amount, CollectedRawCost: &amount, Items: []KeyUsageTodayStat{{KeyID: "1", TodayAmount: 3}}}
			if err := cache.SaveKeyUsageSnapshot(t.Context(), original.ID, good); err != nil {
				t.Fatal(err)
			}
			svc := NewService(nil, nil, nil, cache)
			svc.now = func() time.Time { return now }
			t.Cleanup(svc.Close)
			svc.recordKnownKeyUsageCostFailure(cloneSite(original), keyUsageCostFailure{reason: original.Metrics.TodayConsumeErrorDetail, failedAt: failedAt})
			got, err := cache.GetKeyUsageSnapshot(t.Context(), original.ID)
			if err != nil || got == nil || !got.Complete || got.FailureReason != "" || !got.AttemptStartedAt.Equal(goodAt) || got.BusinessDate != good.BusinessDate {
				t.Fatalf("旧拒绝污染新身份/成功/日期：state=%s snapshot=%+v err=%v", state, got, err)
			}
		})
	}
}
