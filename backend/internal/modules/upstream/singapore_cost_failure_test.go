package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type singaporeFailureRepository struct {
	mu      sync.Mutex
	durable Site
	failure string
	checks  int
}

func (r *singaporeFailureRepository) ListSites(context.Context) ([]Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []Site{cloneSite(&r.durable)}, nil
}
func (r *singaporeFailureRepository) ListSitesForUser(ctx context.Context, _ string) ([]Site, error) {
	return r.ListSites(ctx)
}
func (r *singaporeFailureRepository) SaveSite(_ context.Context, candidate Site) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.durable = cloneSite(&candidate)
	return nil
}
func (r *singaporeFailureRepository) DeleteSite(context.Context, string, string) error { return nil }
func (r *singaporeFailureRepository) SaveExistingSiteIfUnchanged(_ context.Context, expected, candidate Site) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks++
	if !unchangedSiteConfiguration(r.durable, expected) {
		return false, nil
	}
	if r.failure == "known" {
		return false, errors.New("fixture storage temporarily unavailable")
	}
	r.durable = mergeExistingSiteChanges(r.durable, expected, candidate)
	if r.failure == "commit" {
		return false, &siteCommitError{err: errors.New("fixture connection lost during commit")}
	}
	return true, nil
}

type singaporeFailureCache struct {
	*syncTestCache
	rejectNew bool
}

func (c *singaporeFailureCache) Set(ctx context.Context, site *Site) error {
	if c.rejectNew && site.Session != nil && site.Session.AccessToken == "fixture-new" {
		return errors.New("fixture cache temporarily unavailable")
	}
	return c.syncTestCache.Set(ctx, site)
}
func singaporeFailureService(t *testing.T, mode string) (*Service, *singaporeFailureRepository, *singaporeFailureCache, *Site) {
	t.Helper()
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	platform := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
		case "/api/v1/usage/dashboard/stats":
			return 200, map[string]any{"data": map[string]any{"today_actual_cost": 1}}
		case "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 9}}
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			return 200, map[string]any{"data": []any{}}
		}
		return 404, map[string]any{}
	})
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
	site.Account = "同一账号"
	site.RequestedPlatform = PlatformSub2API
	site.Metrics = Metrics{TodayConsume: metric(float64Pointer(3)), TodayConsumeDate: businesstime.DateAt(now), TodayConsumeTimezone: businesstime.Timezone, TodayConsumeAt: &now}
	repo := &singaporeFailureRepository{durable: cloneSite(site), failure: mode}
	cache := &singaporeFailureCache{syncTestCache: newSyncTestCache(site), rejectNew: mode == "cache"}
	svc := NewService(platform, repo, nil, cache)
	svc.now = func() time.Time { return now }
	svc.SetBackgroundTasksDisabled(true)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(svc.Close)
	return svc, repo, cache, site
}
func TestSingaporeCostServicePartialSaveReportsActualStoreResults(t *testing.T) {
	for _, mode := range []string{"known", "commit", "cache"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, cache, original := singaporeFailureService(t, mode)
			notices := 0
			svc.SetSiteLoginSuccessCallback(func(string, string, string) { notices++ })
			_, err := svc.Update(t.Context(), "user", original.ID, UpdateRequest{Name: "新名称", SiteURL: original.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: original.Account, AccessToken: "fixture-new", RechargeRate: 1})
			var detail *RequestError
			if !errors.As(err, &detail) || detail.Stage != "save" || detail.StatusCode != 503 || !strings.Contains(detail.UpstreamMessage, "不会修改主站") {
				t.Fatal("partial save lost its actionable safe result")
			}
			current, _ := cache.Get(t.Context(), original.ID)
			if current.Metrics.TodayConsume.Value == nil || *current.Metrics.TodayConsume.Value != 3 || current.Name != original.Name || notices != 0 {
				t.Fatal("failure published fresh costs, edited config, or success notification")
			}
			switch mode {
			case "known":
				if !strings.Contains(detail.UpstreamMessage, "本站资料未保存") || repo.durable.Session.AccessToken != "fixture-old" || current.Session.AccessToken != "fixture-new" {
					t.Fatal("known PG failure must preserve only rotated cache credentials and report uncommitted PG")
				}
			case "commit":
				if !strings.Contains(detail.UpstreamMessage, "数据库保存结果尚未确认") || current.Session.AccessToken != "fixture-new" {
					t.Fatal("commit uncertainty was reported as known rollback or lost the rotated token")
				}
			case "cache":
				if repo.durable.Session.AccessToken != "fixture-new" || repo.durable.Metrics.TodayConsume.Value == nil || *repo.durable.Metrics.TodayConsume.Value != 9 || current.Session.AccessToken != "fixture-old" || !strings.Contains(detail.UpstreamMessage, "刷新网页无法恢复") {
					t.Fatal("PG success/cache failure rolled back PG or claimed runtime publication")
				}
			}
			recorder := httptest.NewRecorder()
			writeUpstreamFormError(recorder, err)
			var payload struct {
				Failure struct{ Stage, ErrorDetail string }
			}
			if json.Unmarshal(recorder.Body.Bytes(), &payload) != nil || recorder.Code != 503 || payload.Failure.Stage != "save" || payload.Failure.ErrorDetail != detail.UpstreamMessage {
				t.Fatal("HTTP save result lost the actual store detail")
			}
		})
	}
}
func TestSingaporeCostServiceSSEAndAccountRefreshKeepSaveFailureDetail(t *testing.T) {
	svc, _, cache, original := singaporeFailureService(t, "known")
	var events []SyncEvent
	if err := svc.SyncAllStream(t.Context(), "user", func(event SyncEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event == SyncEventError {
			found = true
			if event.ErrorDetail == "" || !strings.Contains(event.ErrorDetail, "本站资料未保存") || event.Site == nil || event.Site.Metrics.TodayConsume.Value == nil || *event.Site.Metrics.TodayConsume.Value != 3 {
				t.Fatal("SSE lost task detail or contaminated retained costs")
			}
		}
	}
	if !found {
		t.Fatal("SSE did not report save failure")
	}
	// Internal refresh uses the same safe failure and does not wait for a collector.
	result, err := svc.SyncAndCollectKeyUsage(t.Context(), "user", "workspace", svc.keyUsageDate(), svc.keyUsageNow().Add(-time.Second))
	if err != nil || len(result.Sites) != 1 || result.Sites[0].Complete || !strings.Contains(result.Sites[0].Error, "本站资料未保存") {
		t.Fatal("account refresh dropped the actual save failure")
	}
	current, _ := cache.Get(t.Context(), original.ID)
	if current.Metrics.TodayConsume.Value == nil || *current.Metrics.TodayConsume.Value != 3 {
		t.Fatal("save failure changed retained fee")
	}
}
func TestSingaporeCostCloneDetachesExpectedPointers(t *testing.T) {
	svc, _, _, original := singaporeFailureService(t, "")
	_ = svc
	original.ErrorKey = func() *string { x := "old"; return &x }()
	original.Settings.BalanceThreshold = float64Pointer(7)
	expected := cloneSite(original)
	candidate := cloneSite(original)
	*candidate.Metrics.TodayConsume.Value = 9
	*candidate.Settings.BalanceThreshold = 10
	*candidate.ErrorKey = "new"
	candidate.Session.AccessToken = "other"
	if !reflect.DeepEqual(expected, cloneSite(original)) {
		t.Fatal("candidate edits mutated the frozen expected input")
	}
}
