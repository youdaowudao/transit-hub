package upstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Verify the actual local editing entries preserve the saved authentication
// refusal, rather than relying on a frontend fixture returning a paused Site.
func TestSiteReadRetainedMetadataCannotResumeRejectedLogin(t *testing.T) {
	f := newSiteReadREDFixture(t, 401, "")
	key := ErrorRefreshTokenRejected
	f.site.Status, f.site.ErrorKey = StatusError, &key
	_ = f.cache.Set(t.Context(), f.site)
	f.repo.durable = cloneSite(f.site)
	scheduled := 0
	f.svc.backgroundTasksDisabled = false
	f.svc.refreshConfigs[refreshWorkspaceKey{userID: "user", adminAccountID: "workspace"}] = RefreshConfig{Enabled: true, Interval: time.Hour}
	f.svc.afterFunc = func(time.Duration, func()) *time.Timer { scheduled++; return nil }
	website := "https://www.fixture.invalid/console"
	_, err := f.svc.Update(t.Context(), "user", "site", UpdateRequest{Name: "保留原登录", SiteURL: f.site.BaseURL, WebsiteURL: &website, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: f.site.Account, RechargeRate: 1, Remark: "只更新资料"})
	if err != nil {
		t.Fatal(err)
	}
	operations := []func() (Response, error){
		func() (Response, error) { return f.svc.UpdatePinned(t.Context(), "user", "site", true) },
		func() (Response, error) { return f.svc.UpdateEnabled(t.Context(), "user", "site", false) },
		func() (Response, error) { return f.svc.UpdateEnabled(t.Context(), "user", "site", true) },
		func() (Response, error) { return f.svc.UpdateSettings(t.Context(), "user", "site", SiteSettings{}) },
	}
	for _, operation := range operations {
		if _, err := operation(); err != nil {
			t.Fatal(err)
		}
		cached, _ := f.cache.Get(t.Context(), "site")
		durable := staleDurable(f.repo)
		if SiteReadAuthError(cached) == nil || SiteReadAuthError(&durable) == nil || cached.Session.RefreshToken != "fixture-refresh" {
			t.Fatal("只改资料、置顶、设置或启用状态不能清除已保存的拒绝登录")
		}
	}
	if scheduled != 0 || f.refreshes.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("编辑被拒绝的登录资料不能重新调度或访问上游")
	}
}

type siteReadMissingCache struct{ SiteCache }

func (siteReadMissingCache) Get(context.Context, string) (*Site, error) {
	return nil, errors.New("fixture runtime storage unavailable")
}

func TestSiteReadRetainedRuntimeReadFailureHasDetailAndNoUpstreamCalls(t *testing.T) {
	f := newSiteReadREDFixture(t, 200, "")
	f.svc.cache = siteReadMissingCache{SiteCache: f.cache}
	result, err := f.svc.KeyUsageForDate(t.Context(), "user", "workspace", "2031-02-03")
	if err != nil || len(result.Sites) != 1 || result.CompletedSites != 0 || result.Sites[0].Complete || !strings.Contains(result.Sites[0].Error, "读取本站已保存的登录资料失败") || !strings.Contains(result.Sites[0].Error, "fixture runtime storage unavailable") {
		t.Fatal("读取当前登录资料失败必须报告具体保存步骤，不能当完整或零成本结果")
	}
	if f.refreshes.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("未能核对当前登录资料时不能发送上游请求")
	}
}

type siteReadConflictRepo struct{ *singaporeFailureRepository }

func (*siteReadConflictRepo) SaveExistingSiteIfUnchanged(context.Context, Site, Site) (bool, error) {
	return false, nil
}

func TestSiteReadRetainedConditionalSaveConflictStopsStatistics(t *testing.T) {
	f := newSiteReadREDFixture(t, 200, "")
	f.svc.repository = &siteReadConflictRepo{singaporeFailureRepository: f.repo}
	result, err := f.svc.KeyUsageForDate(t.Context(), "user", "workspace", "2031-02-03")
	if err != nil || len(result.Sites) != 1 || result.CompletedSites != 0 || result.Sites[0].Complete || !strings.Contains(result.Sites[0].Error, "没有覆盖资料") {
		t.Fatal("条件保存冲突须保留具体结果，不能继续发布费用")
	}
	if f.refreshes.Load() != 1 || f.reads.Load() != 0 {
		t.Fatal("成功续期但条件保存未通过时，本次不能继续费用读取")
	}
}
