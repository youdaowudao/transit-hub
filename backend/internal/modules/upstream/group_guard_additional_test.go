package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func groupGuardSite(t *testing.T) *Site {
	t.Helper()
	site := newTestSite("site", "user", "workspace", 1, &Session{AuthMode: AuthModeUserKey, Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only", UserID: "1", QuotaPerUnit: 500000})
	site.Platform = PlatformNewAPI
	site.RequestedPlatform = PlatformNewAPI
	site.Account = "1"
	at := int64(1000)
	site.LastSyncedAt = &at
	site.Metrics.Groups = []GroupInfo{{ID: "a", Name: "a", Multiplier: float64Pointer(1), MultiplierMode: "fixed"}}
	site.Metrics.Group = site.Metrics.Groups[0]
	return site
}
func TestGroupGuardAdditionalPostgresReturnsOwnUpdateVersionAndKeepsNewGroups(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	site := groupGuardSite(t)
	inserted, err := repo.SaveSiteWithSnapshotVersion(t.Context(), *site)
	if err != nil || inserted == "" {
		t.Fatalf("insert version missing: %v", err)
	}
	var actualVersion string
	if err = pool.QueryRow(t.Context(), `SELECT xmin::text FROM upstream_sites WHERE id='site'`).Scan(&actualVersion); err != nil || actualVersion != inserted {
		t.Fatal("version did not belong to insert")
	}
	baseline := cloneSite(site)
	candidate := cloneSite(site)
	candidate.Metrics.Groups[0].Multiplier = float64Pointer(2)
	candidate.Metrics.Group = candidate.Metrics.Groups[0]
	at := int64(2000)
	candidate.LastSyncedAt = &at
	actual, version, saved, accepted, err := repo.SaveExistingSiteWithGroupGuard(t.Context(), *site, candidate, baseline)
	if err != nil || !saved || !accepted || version == "" || version == inserted {
		t.Fatalf("current group save result saved=%v accepted=%v err=%v", saved, accepted, err)
	}
	older := cloneSite(site)
	older.Metrics.Groups[0].Multiplier = float64Pointer(9)
	older.Metrics.Group = older.Metrics.Groups[0]
	older.Metrics.Balance = metric(float64Pointer(7))
	oldAt := int64(3000)
	older.LastSyncedAt = &oldAt
	older.Status = StatusError
	olderActual, oldVersion, saved, accepted, err := repo.SaveExistingSiteWithGroupGuard(t.Context(), actual, older, baseline)
	if err != nil || !saved || accepted || oldVersion == version {
		t.Fatalf("late result saved=%v accepted=%v err=%v", saved, accepted, err)
	}
	if !reflect.DeepEqual(canonicalGroups(olderActual.Metrics.Groups), canonicalGroups(actual.Metrics.Groups)) || !reflect.DeepEqual(olderActual.LastSyncedAt, actual.LastSyncedAt) || olderActual.Status != actual.Status || olderActual.Metrics.Balance.Value == nil || *olderActual.Metrics.Balance.Value != 7 {
		t.Fatal("late result replaced new groups or discarded independent balance")
	}
	if _, err = pool.Exec(t.Context(), `UPDATE upstream_sites SET remark='settings-only' WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT xmin::text FROM upstream_sites WHERE id='site'`).Scan(&actualVersion); err != nil || actualVersion == oldVersion {
		t.Fatal("settings change did not invalidate source version")
	}
}

type additionalSnapshotWriter struct {
	calls   atomic.Int32
	failure error
	version string
	cleanup bool
}

func (w *additionalSnapshotWriter) SaveSiteSnapshot(_ context.Context, _, _, _, _ string, _ Platform, _ []SnapshotGroup, _ int64, version string, cleanup bool) error {
	w.calls.Add(1)
	w.version = version
	w.cleanup = cleanup
	return w.failure
}

type additionalUncertainSnapshotError struct{}

func (*additionalUncertainSnapshotError) Error() string                 { return "injected commit connection lost" }
func (*additionalUncertainSnapshotError) UncertainSnapshotResult() bool { return true }
func groupGuardFixturePlatform(t *testing.T, onGroups func()) *PlatformService {
	return singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/status":
			return 200, map[string]any{"data": map[string]any{"quota_per_unit": 500000}}
		case "/api/user/self":
			return 200, map[string]any{"data": map[string]any{"id": 1, "quota": 500000, "used_quota": 500000, "group": "a"}}
		case "/api/log/self/stat":
			return 200, map[string]any{"data": map[string]any{"quota": 3500000}}
		case "/api/user/self/groups":
			if onGroups != nil {
				onGroups()
			}
			return 200, map[string]any{"data": map[string]any{"a": map[string]any{"ratio": 1}}}
		case "/api/pricing":
			return 200, map[string]any{"group_ratio": map[string]any{"a": 1}, "usable_group": map[string]any{"a": "a"}}
		default:
			t.Errorf("unexpected group guard request path %s", r.URL.Path)
			return 500, map[string]any{}
		}
	})
}
func TestGroupGuardAdditionalSyncAndSameSessionReloginRejectLateGroupsPostgres(t *testing.T) {
	for _, entry := range []string{"sync", "relogin"} {
		t.Run(entry, func(t *testing.T) {
			pool := singaporeCostPGPool(t)
			repo := NewRepository(pool)
			site := groupGuardSite(t)
			if err := repo.SaveSite(t.Context(), *site); err != nil {
				t.Fatal(err)
			}
			readStarted, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			platform := groupGuardFixturePlatform(t, func() {
				if reads.Add(1) == 1 {
					close(readStarted)
					<-release
				}
			})
			writer := &additionalSnapshotWriter{}
			cache := newSyncTestCache(site)
			svc := NewService(platform, repo, writer, cache)
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			callbacks := make(chan struct{}, 1)
			svc.AfterSync = func(context.Context, string, string, string, string, Metrics, Metrics, Status, Status) {
				callbacks <- struct{}{}
			}
			loginCallbacks := atomic.Int32{}
			svc.SetSiteLoginSuccessCallback(func(string, string, string) { loginCallbacks.Add(1) })
			done := make(chan struct {
				response Response
				err      error
			}, 1)
			go func() {
				var response Response
				var err error
				if entry == "sync" {
					response, err = svc.syncOnce(context.Background(), site.ID)
				} else {
					response, err = svc.Update(context.Background(), "user", site.ID, UpdateRequest{Name: site.Name, SiteURL: site.BaseURL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
				}
				done <- struct {
					response Response
					err      error
				}{response, err}
			}()
			select {
			case <-readStarted:
			case <-time.After(time.Second):
				t.Fatal("old remote group read did not start")
			}
			newer := cloneSite(site)
			newer.Metrics.Groups[0].Multiplier = float64Pointer(3)
			newer.Metrics.Group = newer.Metrics.Groups[0]
			at := int64(5000)
			newer.LastSyncedAt = &at
			newer.Metrics.Balance = metric(float64Pointer(2))
			actual, _, saved, accepted, err := repo.SaveExistingSiteWithGroupGuard(t.Context(), *site, newer, *site)
			if err != nil || !saved || !accepted {
				t.Fatal("new result could not save")
			}
			if err = cache.Set(t.Context(), &actual); err != nil {
				t.Fatal(err)
			}
			close(release)
			result := <-done
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.response.GroupRatesWarning == nil || !strings.Contains(result.response.GroupRatesWarning.Detail, "旧分组结果未写入") {
				t.Fatal("late group result lacked actual-result warning")
			}
			cached, err := cache.Get(t.Context(), site.ID)
			if err != nil || cached == nil || cached.Metrics.Groups[0].Multiplier == nil || *cached.Metrics.Groups[0].Multiplier != 3 || cached.LastSyncedAt == nil || *cached.LastSyncedAt != 5000 {
				t.Fatal("late result overwritten newer Redis group publication")
			}
			persisted, err := repo.ListSites(t.Context())
			if err != nil || len(persisted) != 1 || *persisted[0].Metrics.Groups[0].Multiplier != 3 || persisted[0].Metrics.TodayConsume.Value == nil || *persisted[0].Metrics.TodayConsume.Value != 7 {
				t.Fatal("late result changed PG groups or lost independent cost")
			}
			if writer.calls.Load() != 0 || loginCallbacks.Load() != 0 {
				t.Fatal("late groups triggered multiplier writer or login group callback")
			}
			select {
			case <-callbacks:
				t.Fatal("late groups triggered pricing/notification callback")
			case <-time.After(15 * time.Millisecond):
			}
		})
	}
}
func TestGroupGuardAdditionalWarningOutcomesAndNilAudit(t *testing.T) {
	site := groupGuardSite(t)
	audit := &GroupSnapshotAudit{AllowUpdate: true, AllowCleanup: true}
	writer := &additionalSnapshotWriter{}
	svc := NewService(nil, nil, writer, newSyncTestCache(site))
	t.Cleanup(svc.Close)
	for _, tc := range []struct {
		name     string
		audit    *GroupSnapshotAudit
		version  string
		failure  error
		contains string
		called   bool
	}{
		{"no-audit", nil, "v1", nil, "没有取得可核验", false},
		{"no-version", audit, "", nil, "数据库版本", false},
		{"bad-input", &GroupSnapshotAudit{Issue: "读取可用分组失败：固定倍率缺失"}, "v1", nil, "旧倍率和历史仍保留", false},
		{"partial-success", &GroupSnapshotAudit{AllowUpdate: true}, "v1", nil, "不清理未返回组", true},
		{"explicit-rollback", audit, "v1", errors.New("数据库连接中断"), "旧倍率记录保留", true},
		{"commit-unknown", audit, "v1", &additionalUncertainSnapshotError{}, "不能确认哪些记录", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := writer.calls.Load()
			writer.failure = tc.failure
			warning := svc.saveSnapshot(t.Context(), site, tc.audit, tc.version)
			if warning == nil || !strings.Contains(warning.Detail, tc.contains) {
				t.Fatalf("warning did not explain actual outcome: %#v", warning)
			}
			if (writer.calls.Load() > before) != tc.called {
				t.Fatal("writer eligibility bypassed")
			}
			if tc.name == "commit-unknown" && strings.Contains(warning.Detail, "旧倍率记录保留") {
				t.Fatal("unknown commit falsely reported rollback")
			}
			result := syncSiteResult(site.ID, Response{Status: StatusConnected, GroupRatesWarning: warning}, nil)
			if result.Status != "success" || result.GroupRatesWarning != warning {
				t.Fatal("warning altered independent successful site status or was lost")
			}
		})
	}
	raw, _ := json.Marshal(site.Metrics)
	if strings.Contains(string(raw), "GroupSnapshotAudit") {
		t.Fatal("audit entered DTO")
	}
}

func TestGroupGuardAdditionalWarningSurvivesCreateReloginSingleBatchAndSSEPostgres(t *testing.T) {
	for _, entry := range []string{"create", "relogin", "single", "batch", "progress", "sse"} {
		t.Run(entry, func(t *testing.T) {
			pool := singaporeCostPGPool(t)
			writer := &additionalSnapshotWriter{failure: &additionalUncertainSnapshotError{}}
			platform := groupGuardFixturePlatform(t, nil)
			cache := newSyncTestCache()
			svc := NewService(platform, NewRepository(pool), writer, cache)
			svc.now = func() time.Time { return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC) }
			svc.backgroundTasksDisabled = true
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			created, err := svc.Create(t.Context(), "user", CreateRequest{Name: "验收-倍率说明", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
			if err != nil {
				t.Fatal(err)
			}
			assertWarning := func(response Response) {
				t.Helper()
				if response.Status != StatusConnected || response.GroupRatesWarning == nil || !strings.Contains(response.GroupRatesWarning.Detail, "不能确认哪些记录") || response.Metrics.TodayConsume.Value == nil || *response.Metrics.TodayConsume.Value != 7 {
					t.Fatalf("warning/status/independent cost was lost for %s", entry)
				}
			}
			switch entry {
			case "create":
				assertWarning(created)
			case "relogin":
				response, err := svc.Update(t.Context(), "user", created.ID, UpdateRequest{Name: created.Name, SiteURL: created.BaseURL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
				if err != nil {
					t.Fatal(err)
				}
				assertWarning(response)
			case "single":
				response, err := svc.Sync(t.Context(), "user", created.ID)
				if err != nil {
					t.Fatal(err)
				}
				assertWarning(response)
			case "batch":
				responses, err := svc.SyncAll(t.Context(), "user")
				if err != nil || len(responses) != 1 {
					t.Fatalf("batch result %d %v", len(responses), err)
				}
				assertWarning(responses[0])
			case "progress":
				results := svc.SyncSites(t.Context(), "user", "workspace", []string{created.ID}, true)
				if len(results) != 1 || results[0].Status != "success" || results[0].GroupRatesWarning == nil {
					t.Fatal("successful health sync result dropped warning")
				}
			case "sse":
				var terminal *Response
				err := svc.SyncAllStream(t.Context(), "user", func(event SyncEvent) {
					if event.Event == SyncEventDone {
						terminal = event.Site
					}
				})
				if err != nil || terminal == nil {
					t.Fatalf("SSE terminal missing: %v", err)
				}
				assertWarning(*terminal)
			}
			if writer.calls.Load() == 0 || writer.version == "" || !writer.cleanup {
				t.Fatal("source save version/audit were lost before writer")
			}
			var raw string
			if err = pool.QueryRow(t.Context(), `SELECT metrics::text FROM upstream_sites WHERE id=$1`, created.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "SnapshotAudit") || strings.Contains(raw, "groupRatesWarning") {
				t.Fatal("one-request qualification/warning was persisted")
			}
		})
	}
}
