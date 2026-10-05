package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type reviewSessionCache struct {
	*keySnapshotTestCache
	failRotation        bool
	waitRotationTimeout bool
}

func (c *reviewSessionCache) Set(ctx context.Context, site *Site) error {
	if c.failRotation && site.Session != nil && site.Session.AccessToken == "fixture-rotated-access" {
		if c.waitRotationTimeout {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("fixture cache persistence failure")
	}
	return c.keySnapshotTestCache.Set(ctx, site)
}

type reviewSessionRepository struct {
	mu             sync.Mutex
	fail           bool
	attempts       int
	saved          []Site
	expiredContext bool
}

func (r *reviewSessionRepository) ListSites(context.Context) ([]Site, error) { return nil, nil }
func (r *reviewSessionRepository) ListSitesForUser(context.Context, string) ([]Site, error) {
	return nil, nil
}
func (r *reviewSessionRepository) DeleteSite(context.Context, string, string) error { return nil }
func (r *reviewSessionRepository) SaveSite(ctx context.Context, site Site) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if err := ctx.Err(); err != nil {
		r.expiredContext = true
		return err
	}
	if r.fail {
		return errors.New("fixture durable persistence failure")
	}
	r.saved = append(r.saved, site)
	return nil
}
func (r *reviewSessionRepository) last() (*Site, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.saved) == 0 {
		return nil, r.attempts
	}
	copy := r.saved[len(r.saved)-1]
	return &copy, r.attempts
}

type reviewSessionFixture struct {
	site       *Site
	cache      *reviewSessionCache
	repo       *reviewSessionRepository
	svc        *Service
	statsReads atomic.Int32
	onRefresh  func()
	onStats    func()
}

func newReviewSessionFixture(t *testing.T) *reviewSessionFixture {
	t.Helper()
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	expires := now.Add(50 * time.Second).UnixMilli()
	amount := 3.0
	f := &reviewSessionFixture{repo: &reviewSessionRepository{}}
	f.site = newTestSite("site", "user", "workspace", 1, &Session{AuthMode: AuthModeToken, Platform: PlatformSub2API, AccessToken: "fixture-old-access", RefreshToken: "fixture-old-refresh", TokenType: "Bearer", ExpiresAt: &expires})
	f.site.Metrics = Metrics{TodayConsume: metric(&amount), TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &now, TodayConsumeStatus: "ok"}
	f.cache = &reviewSessionCache{keySnapshotTestCache: newKeySnapshotTestCache(f.site)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			if f.onRefresh != nil {
				f.onRefresh()
			}
			writeJSON(w, map[string]any{"data": map[string]any{"access_token": "fixture-rotated-access", "refresh_token": "fixture-rotated-refresh", "token_type": "Bearer"}})
		case "/api/v1/usage/dashboard/stats":
			f.statsReads.Add(1)
			if f.onStats != nil {
				f.onStats()
			}
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": map[string]any{"items": []map[string]any{{"id": 1, "name": "验收-Key"}}, "total": 1}})
		case "/api/v1/usage/dashboard/api-keys-usage":
			writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 3}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.site.BaseURL = server.URL
	f.site.Session.BaseURL = server.URL
	_ = f.cache.Set(t.Context(), f.site)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	f.svc = NewService(platform, f.repo, nil, f.cache)
	f.svc.now = func() time.Time { return now }
	t.Cleanup(f.svc.Close)
	return f
}

func TestHomeCostReviewCollectorPersistenceFailureStaysIncomplete(t *testing.T) {
	for _, mode := range []string{"cache", "repository"} {
		t.Run(mode, func(t *testing.T) {
			f := newReviewSessionFixture(t)
			f.cache.failRotation = mode == "cache"
			f.repo.fail = mode == "repository"
			f.svc.collectKeyUsageSnapshot(*f.site)
			current, err := f.cache.Get(t.Context(), f.site.ID)
			if err != nil || current == nil {
				t.Fatal("site cache was lost")
			}
			if mode == "repository" && (current.Session.AccessToken != "fixture-rotated-access" || current.Session.RefreshToken != "fixture-rotated-refresh") {
				t.Error("durable failure rolled cache back to invalid old credentials")
			}
			if mode == "cache" && current.Session.AccessToken != "fixture-old-access" {
				t.Error("failed cache write changed prior credentials")
			}
			snapshot, err := f.cache.GetKeyUsageSnapshot(t.Context(), f.site.ID)
			if err != nil || snapshot == nil || snapshot.Complete || snapshot.FailureReason != ErrorRequest {
				t.Error("session persistence failure became a complete collection")
			}
			if f.statsReads.Load() != 0 {
				t.Error("collection continued after session persistence failure")
			}
			saved, attempts := f.repo.last()
			if attempts != 1 || (mode == "repository" && saved != nil) || (mode == "cache" && (saved == nil || saved.Session.AccessToken != "fixture-rotated-access" || saved.Session.RefreshToken != "fixture-rotated-refresh")) {
				t.Error("unexpected durable session write outcome")
			}
		})
	}
}

func TestHomeCostReviewCollectorAttemptsRepositoryAfterCacheFailure(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cache_error"
		if timeout {
			name = "cache_context_exhausted"
		}
		t.Run(name, func(t *testing.T) {
			f := newReviewSessionFixture(t)
			f.cache.failRotation, f.cache.waitRotationTimeout = true, timeout
			f.svc.collectKeyUsageSnapshot(*f.site)
			saved, attempts := f.repo.last()
			if attempts != 1 || saved == nil || saved.Session.AccessToken != "fixture-rotated-access" || saved.Session.RefreshToken != "fixture-rotated-refresh" {
				t.Error("cache failure prevented durable preservation of the rotated session")
			}
			if f.repo.expiredContext {
				t.Error("durable preservation reused the exhausted cache context")
			}
			current, err := f.cache.Get(t.Context(), f.site.ID)
			if err != nil || current == nil || current.Session.AccessToken != "fixture-old-access" {
				t.Error("failed cache write changed original local value")
			}
			snapshot, err := f.cache.GetKeyUsageSnapshot(t.Context(), f.site.ID)
			if err != nil || snapshot == nil || snapshot.Complete || snapshot.FailureReason != ErrorRequest {
				t.Error("partial session preservation made the collection complete")
			}
			if f.statsReads.Load() != 0 {
				t.Error("collection continued after cache persistence failed")
			}
		})
	}
}

func TestHomeCostReviewCollectorRejectsChangedLifecycleOrSession(t *testing.T) {
	for _, mode := range []string{"new_credentials", "later_expiry", "deleted", "disabled", "workspace_changed", "user_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newReviewSessionFixture(t)
			f.onRefresh = func() {
				if mode == "deleted" {
					if err := f.svc.CleanupDeletedWorkspaceSites(t.Context(), "user", []string{f.site.ID}); err != nil {
						t.Error("fixture deletion failed")
					}
					return
				}
				current, _ := f.cache.Get(t.Context(), f.site.ID)
				next := *current.Session
				switch mode {
				case "new_credentials":
					next.AccessToken, next.RefreshToken = "fixture-later-access", "fixture-later-refresh"
					current.Session = &next
				case "later_expiry":
					expires := *next.ExpiresAt + int64(time.Hour/time.Millisecond)
					next.ExpiresAt = &expires
					current.Session = &next
				case "disabled":
					current.Enabled = boolPointer(false)
				case "workspace_changed":
					current.AdminAccountID = "other-workspace"
				case "user_changed":
					current.UserID = "other-user"
				}
				if err := f.cache.Set(t.Context(), current); err != nil {
					t.Error("fixture local update failed")
				}
			}
			f.svc.collectKeyUsageSnapshot(*f.site)
			current, _ := f.cache.Get(t.Context(), f.site.ID)
			if mode == "deleted" {
				if current != nil {
					t.Error("deleted site resurrected")
				}
			} else if current == nil || current.Session.AccessToken == "fixture-rotated-access" {
				t.Error("stale collector replaced current local state")
			}
			if f.statsReads.Load() != 0 {
				t.Error("stale collector continued after local state changed")
			}
			if snapshot, _ := f.cache.GetKeyUsageSnapshot(t.Context(), f.site.ID); snapshot != nil {
				t.Error("stale collector published a cost snapshot")
			}
			if saved, attempts := f.repo.last(); saved != nil || attempts != 0 {
				t.Error("stale collector wrote durable state")
			}
		})
	}
}

func TestHomeCostReviewCollectorPersistsCurrentMetadataOnly(t *testing.T) {
	f := newReviewSessionFixture(t)
	f.onRefresh = func() {
		current, _ := f.cache.Get(t.Context(), f.site.ID)
		current.Name, current.Remark, current.RechargeRate = "验收-更新后", "验收-保留编辑", 2
		amount := 7.0
		current.Metrics.TodayConsume = metric(&amount)
		if err := f.cache.Set(t.Context(), current); err != nil {
			t.Error("fixture metadata edit failed")
		}
	}
	f.svc.collectKeyUsageSnapshot(*f.site)
	saved, attempts := f.repo.last()
	if saved == nil || attempts != 1 || saved.Name != "验收-更新后" || saved.Remark != "验收-保留编辑" || saved.RechargeRate != 2 || saved.Metrics.TodayConsume.Value == nil || *saved.Metrics.TodayConsume.Value != 7 {
		t.Error("session persistence lost current metadata or metrics")
	}
}

func TestHomeCostReviewCollectorDoesNotPublishAfterLaterSession(t *testing.T) {
	f := newReviewSessionFixture(t)
	f.onStats = func() {
		current, _ := f.cache.Get(t.Context(), f.site.ID)
		next := *current.Session
		next.AccessToken, next.RefreshToken = "fixture-later-access", "fixture-later-refresh"
		current.Session = &next
		if err := f.cache.Set(t.Context(), current); err != nil {
			t.Error("fixture later cache session failed")
		}
		if err := f.repo.SaveSite(t.Context(), *current); err != nil {
			t.Error("fixture later durable session failed")
		}
	}
	f.svc.collectKeyUsageSnapshot(*f.site)
	current, _ := f.cache.Get(t.Context(), f.site.ID)
	saved, _ := f.repo.last()
	if current == nil || saved == nil || current.Session.AccessToken != "fixture-later-access" || saved.Session.AccessToken != "fixture-later-access" {
		t.Error("older collection replaced a later session")
	}
	if snapshot, _ := f.cache.GetKeyUsageSnapshot(t.Context(), f.site.ID); snapshot != nil {
		t.Error("older collection published after the session changed")
	}
}
