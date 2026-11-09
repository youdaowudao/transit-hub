package upstream

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Frozen by the primary agent after the final review found the independent
// dated-total reader. No refresh, listener or change to the cost strategy.
func siteReadTotalREDFixture(t *testing.T, onRead func(*siteReadREDFixture)) (*siteReadREDFixture, *atomic.Int32) {
	t.Helper()
	f := newSiteReadREDFixture(t, 200, "")
	calls := new(atomic.Int32)
	f.svc.platformService = singaporeCostPlatform(t, time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		calls.Add(1)
		if onRead != nil {
			onRead(f)
		}
		switch r.URL.Path {
		case "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
		case "/api/log/self/stat":
			return 200, map[string]any{"data": map[string]any{"quota": 1500000}}
		default:
			t.Errorf("dated total changed its original query or started refreshing: %s", r.URL.Path)
			return 503, map[string]any{}
		}
	})
	return f, calls
}

func siteReadTotalREDPause(t *testing.T, f *siteReadREDFixture) {
	t.Helper()
	current, _ := f.cache.Get(t.Context(), "site")
	key := ErrorRefreshTokenRejected
	current.Status, current.ErrorKey = StatusError, &key
	_ = f.cache.Set(t.Context(), current)
	f.repo.mu.Lock()
	f.repo.durable = cloneSite(current)
	f.repo.mu.Unlock()
}

func TestSiteReadTotalCostREDPausedSiteMakesNoRequest(t *testing.T) {
	f, calls := siteReadTotalREDFixture(t, nil)
	siteReadTotalREDPause(t, f)
	results, err := f.svc.FetchSiteCostsForDate(t.Context(), "user", "workspace", "2031-02-03")
	if err != nil || len(results) != 1 || results[0].Err == nil || calls.Load() != 0 {
		t.Fatal("已暂停站点的指定日总成本必须保留逐站错误且零上游请求")
	}
	var detail *RequestError
	if !errors.As(results[0].Err, &detail) || detail.Reason != ErrorRefreshTokenRejected || detail.UpstreamMessage == "" || detail.StatusCode != 0 {
		t.Fatal("本地阻止需要具体原因和重新登录指引，不能伪造本次HTTP401")
	}
}

type siteReadTotalStaleList struct {
	SiteCache
	stale Site
}

func (c siteReadTotalStaleList) ListByUser(context.Context, string) ([]*Site, error) {
	copy := cloneSite(&c.stale)
	return []*Site{&copy}, nil
}

func TestSiteReadTotalCostREDChecksCurrentSiteBeforeReading(t *testing.T) {
	f, calls := siteReadTotalREDFixture(t, nil)
	stale := cloneSite(f.site)
	siteReadTotalREDPause(t, f)
	f.svc.cache = siteReadTotalStaleList{SiteCache: f.cache, stale: stale}
	results, err := f.svc.FetchSiteCostsForDate(t.Context(), "user", "workspace", "2031-02-03")
	if err != nil || len(results) != 1 || results[0].Err == nil || calls.Load() != 0 {
		t.Fatal("排队时取得的旧站点列表不能绕过当前已保存的拒绝")
	}
}

func TestSiteReadTotalCostREDLateSuccessCannotPublishChangedSite(t *testing.T) {
	for _, change := range []string{"pause", "login", "delete", "workspace", "disabled"} {
		t.Run(change, func(t *testing.T) {
			f, calls := siteReadTotalREDFixture(t, func(f *siteReadREDFixture) {
				f.svc.mu.Lock()
				defer f.svc.mu.Unlock()
				if change == "pause" {
					siteReadTotalREDPause(t, f)
					return
				}
				current, _ := f.cache.Get(t.Context(), "site")
				switch change {
				case "login":
					session := copyReadSession(*current.Session)
					session.AccessToken = "fixture-new-login"
					current.Session = &session
				case "delete":
					f.svc.deletedSites["site"] = struct{}{}
					_ = f.cache.Delete(t.Context(), "site", "user")
					return
				case "workspace":
					current.AdminAccountID = "other-workspace"
				case "disabled":
					current.Enabled = boolPointer(false)
				}
				_ = f.cache.Set(t.Context(), current)
			})
			results, err := f.svc.FetchSiteCostsForDate(t.Context(), "user", "workspace", "2031-02-03")
			if err != nil || len(results) != 1 || results[0].Err == nil || results[0].RawCost != 0 || calls.Load() != 1 {
				t.Fatal("旧200总成本在暂停、换登录、删除或归属变化后不能当成本成功")
			}
		})
	}
}

func TestSiteReadTotalCostREDKeepsNormalQueriesAndNewAPIBehavior(t *testing.T) {
	for _, platform := range []Platform{PlatformSub2API, PlatformNewAPI} {
		t.Run(string(platform), func(t *testing.T) {
			f, calls := siteReadTotalREDFixture(t, nil)
			f.site.Platform, f.site.Session.Platform = platform, platform
			if platform == PlatformNewAPI {
				f.site.Status = StatusError
				key := ErrorRefreshTokenRejected
				f.site.ErrorKey = &key // Legacy key on another platform is not a Sub2API pause.
			}
			_ = f.cache.Set(t.Context(), f.site)
			f.repo.durable = cloneSite(f.site)
			results, err := f.svc.FetchSiteCostsForDate(t.Context(), "user", "workspace", "2031-02-03")
			want := 7.0
			if platform == PlatformNewAPI {
				want = 3
			}
			if err != nil || len(results) != 1 || results[0].Err != nil || results[0].RawCost != want || calls.Load() != 1 || f.repo.checks != 0 {
				t.Fatal("正常站点和NewAPI必须保留原账号统计查询，不新增续期或保存")
			}
		})
	}
}
