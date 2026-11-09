package upstream

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

// 主代理冻结的业务回归；注入 transport，不启动监听、进程或真实上游。
type siteReadREDCache struct {
	*keySnapshotTestCache
	rejectNew  bool
	syncWrites atomic.Int32
}

func (c *siteReadREDCache) Set(ctx context.Context, site *Site) error {
	if site.Status == StatusSyncing {
		c.syncWrites.Add(1)
	}
	if c.rejectNew && site.Session != nil && site.Session.AccessToken == "fixture-new" {
		return errors.New("fixture cache unavailable")
	}
	return c.keySnapshotTestCache.Set(ctx, site)
}

type siteReadREDFixture struct {
	svc        *Service
	site       *Site
	cache      *siteReadREDCache
	repo       *singaporeFailureRepository
	refreshes  atomic.Int32
	reads      atomic.Int32
	onRead     func()
	checkSaved bool
}

func newSiteReadREDFixture(t *testing.T, refreshStatus int, saveFailure string) *siteReadREDFixture {
	t.Helper()
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	expires := now.Add(50 * time.Second).UnixMilli()
	site := newTestSite("site", "user", "workspace", 1, &Session{
		Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid",
		AccessToken: "fixture-old", RefreshToken: "fixture-refresh", ExpiresAt: &expires,
		UserID: "7", UserIDConfirmed: true,
	})
	site.Metrics.TodayConsume = metric(float64Pointer(7))
	site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
	f := &siteReadREDFixture{site: site, cache: &siteReadREDCache{keySnapshotTestCache: newKeySnapshotTestCache(site), rejectNew: saveFailure == "cache"}, repo: &singaporeFailureRepository{durable: cloneSite(site), failure: saveFailure}}
	platform := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			f.refreshes.Add(1)
			if refreshStatus != 200 {
				return refreshStatus, map[string]any{"message": "fixture saved login rejected"}
			}
			return 200, map[string]any{"data": map[string]any{"access_token": "fixture-new", "refresh_token": "fixture-next", "expires_in": 3600}}
		}
		first := f.reads.Add(1) == 1
		if first && f.checkSaved {
			cached, _ := f.cache.Get(t.Context(), site.ID)
			durable := staleDurable(f.repo)
			if cached.Session.AccessToken != "fixture-new" || durable.Session.AccessToken != "fixture-new" {
				t.Error("统计读取前，新登录必须已写入数据库和运行数据")
			}
		}
		if first && f.onRead != nil {
			f.onRead()
		}
		return 503, map[string]any{"message": "fixture statistics unavailable"}
	})
	f.svc = NewService(platform, f.repo, nil, f.cache)
	f.svc.now = func() time.Time { return now }
	f.svc.SetBackgroundTasksDisabled(true)
	f.svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(f.svc.Close)
	return f
}

func (f *siteReadREDFixture) collect(t *testing.T, entry string) {
	t.Helper()
	switch entry {
	case "dated":
		result, err := f.svc.KeyUsageForDate(t.Context(), "user", "workspace", "2031-02-03")
		if err != nil || result.CompletedSites != 0 || len(result.Sites) != 1 || result.Sites[0].Complete {
			t.Fatalf("读取失败必须保留逐站未完成，不能作零成本成功: %v", err)
		}
	case "groups":
		if _, err := f.svc.FetchGroupDailyStats(t.Context(), "user", "site"); err == nil {
			t.Error("统计失败不能返回成功")
		}
	case "sync":
		_, _ = f.svc.Sync(t.Context(), "user", "site")
	case "today":
		f.svc.collectKeyUsageSnapshot(*f.site)
		snapshot, err := f.cache.GetKeyUsageSnapshot(t.Context(), "site")
		if err != nil || snapshot != nil && snapshot.Complete {
			t.Error("失败采集不能发布完整快照")
		}
	}
}

func TestSiteReadREDRotationSavedBeforeFailingStatistics(t *testing.T) {
	for _, entry := range []string{"dated", "groups", "sync", "today"} {
		t.Run(entry, func(t *testing.T) {
			f := newSiteReadREDFixture(t, 200, "")
			f.checkSaved = true
			f.collect(t, entry)
			cached, _ := f.cache.Get(t.Context(), "site")
			durable := staleDurable(f.repo)
			if f.refreshes.Load() != 1 || f.reads.Load() == 0 || cached.Session.RefreshToken != "fixture-next" || durable.Session.RefreshToken != "fixture-next" {
				t.Error("费用失败不得丢失已经轮换的登录信息")
			}
		})
	}
}

func TestSiteReadREDSaveFailureStopsStatistics(t *testing.T) {
	for _, entry := range []string{"dated", "sync"} {
		for _, failure := range []string{"known", "commit", "cache"} {
			t.Run(entry+"/"+failure, func(t *testing.T) {
				f := newSiteReadREDFixture(t, 200, failure)
				f.collect(t, entry)
				if f.reads.Load() != 0 {
					t.Error("登录保存未成功，本次不得发送费用或统计请求")
				}
				cached, _ := f.cache.Get(t.Context(), "site")
				if cached.Status == StatusSyncing {
					t.Error("保存失败后不能残留本次同步中状态")
				}
				if failure == "cache" {
					if staleDurable(f.repo).Session.AccessToken != "fixture-new" || cached.Session.AccessToken != "fixture-old" {
						t.Error("数据库已保存、运行数据未发布的实际结果不能被篡改")
					}
				} else if cached.Session.AccessToken != "fixture-new" {
					t.Error("PG失败仍须复用既有轮换登录运行数据兜底")
				}
			})
		}
	}
}

func TestSiteReadREDRejectedLoginStopsEverySyncAndCostEntry(t *testing.T) {
	f := newSiteReadREDFixture(t, 401, "")
	_, _ = f.svc.Sync(t.Context(), "user", "site")
	cached, _ := f.cache.Get(t.Context(), "site")
	durable := staleDurable(f.repo)
	if cached.Status != StatusError || cached.ErrorKey == nil || *cached.ErrorKey != ErrorRefreshTokenRejected || durable.ErrorKey == nil || *durable.ErrorKey != ErrorRefreshTokenRejected {
		t.Fatal("首次真实续期401必须保存拒绝状态")
	}
	writes := f.cache.syncWrites.Load()
	_, _ = f.svc.Sync(t.Context(), "user", "site")
	batch := f.svc.SyncSites(t.Context(), "user", "workspace", []string{"site"}, true)
	if len(batch) != 1 || batch[0].Status != "auth_failed" || batch[0].ErrorDetail == "" {
		t.Error("批量本地认证阻止须返回auth_failed和具体处理说明")
	}
	_ = f.svc.SyncAllStream(t.Context(), "user", func(SyncEvent) {})
	f.collect(t, "dated")
	f.collect(t, "groups")
	f.collect(t, "today")
	if f.refreshes.Load() != 1 || f.reads.Load() != 0 || f.cache.syncWrites.Load() != writes {
		t.Error("已拒绝登录的同步/费用入口不能再发请求或先写同步中绕过暂停")
	}
	cached, _ = f.cache.Get(t.Context(), "site")
	if !cached.IsEnabled() || cached.Metrics.TodayConsume.Value == nil || *cached.Metrics.TodayConsume.Value != 7 {
		t.Error("认证拒绝不能停用站点或清除原成本")
	}
}

func TestSiteReadREDLateStatisticsCannotEraseRejection(t *testing.T) {
	f := newSiteReadREDFixture(t, 200, "")
	f.onRead = func() {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		current, _ := f.cache.Get(t.Context(), "site")
		current.Status = StatusError
		key := ErrorRefreshTokenRejected
		current.ErrorKey = &key
		_ = f.cache.Set(t.Context(), current)
		f.repo.mu.Lock()
		f.repo.durable = cloneSite(current)
		f.repo.mu.Unlock()
	}
	f.collect(t, "sync")
	cached, _ := f.cache.Get(t.Context(), "site")
	if cached.Status != StatusError || cached.ErrorKey == nil || *cached.ErrorKey != ErrorRefreshTokenRejected {
		t.Error("读取期间出现认证拒绝，旧统计失败不能覆盖拒绝原因")
	}
}

func TestSiteReadREDRecoveryNeverCallsRejectedOldLogin(t *testing.T) {
	for _, wrongAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_account", true: "wrong_account"}[wrongAccount], func(t *testing.T) {
			fixture := newSwitchFixture()
			if wrongAccount {
				fixture.newID = 8
			}
			svc, cache, repo, site := switchSetup(t, fixture, PlatformSub2API, AuthModeToken, true)
			key := ErrorRefreshTokenRejected
			site.Status, site.ErrorKey = StatusError, &key
			cache.add(site)
			repo.durable = cloneSite(site)
			_, err := svc.Update(t.Context(), site.UserID, site.ID, switchDTO(site, AuthModeToken))
			if fixture.oldReads != 0 {
				t.Error("已核实原账号的暂停登录不应再请求旧凭据")
			}
			stored, _ := cache.Get(t.Context(), site.ID)
			if wrongAccount {
				if err == nil || stored.ErrorKey == nil || *stored.ErrorKey != key {
					t.Error("其他账号不能解除原登录暂停")
				}
			} else if err != nil || stored.Status != StatusConnected || stored.ErrorKey != nil {
				t.Error("同账号成功登录并保存后必须恢复")
			}
		})
	}
}

func TestSiteReadREDMigrationCannotReuseRejectedLogin(t *testing.T) {
	fixture := newSwitchFixture()
	svc, cache, repo, site := switchSetup(t, fixture, PlatformSub2API, AuthModeToken, true)
	key := ErrorRefreshTokenRejected
	site.Status, site.ErrorKey = StatusError, &key
	cache.add(site)
	repo.durable = cloneSite(site)
	_, err := svc.Update(t.Context(), site.UserID, site.ID, migrationConfirmedDTO(t, site, AuthModeToken, false))
	if err == nil || len(fixture.paths) != 0 {
		t.Error("迁移无新凭据必须本地拒绝，不得复用已拒绝登录")
	}
}
