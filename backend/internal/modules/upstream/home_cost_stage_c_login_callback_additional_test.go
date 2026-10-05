package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type homeCostCredentialRepository struct {
	saveErr   error
	saved     Site
	committed bool
}

func (r *homeCostCredentialRepository) ListSites(context.Context) ([]Site, error) { return nil, nil }
func (r *homeCostCredentialRepository) ListSitesForUser(context.Context, string) ([]Site, error) {
	return nil, nil
}
func (r *homeCostCredentialRepository) DeleteSite(context.Context, string, string) error { return nil }
func (r *homeCostCredentialRepository) SaveSite(_ context.Context, site Site) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = site
	r.committed = true
	return nil
}

func TestHomeCostStageCCredentialNotificationWaitsForPersistence(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" {
			writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000}})
			return
		}
		if r.URL.Path == "/api/log/self/stat" {
			writeJSON(w, map[string]any{"data": map[string]any{"quota": 500000}})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{}})
	}))
	defer server.Close()
	pl := NewPlatformService(NewHTTPClient(server.Client()))
	pl.now = func() time.Time { return now }
	site := newTestSite("site", "user", "workspace", 1, &Session{AuthMode: AuthModeUserKey, Platform: PlatformNewAPI, BaseURL: server.URL, UserID: "1", AccessToken: "fixture-old"})
	site.Platform = PlatformNewAPI
	cache := newSyncTestCache(site)
	repo := &homeCostCredentialRepository{saveErr: errors.New("fixture persistence failed")}
	svc := NewService(pl, repo, nil, cache)
	svc.now = func() time.Time { return now }
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	defer svc.Close()
	notices := 0
	if setter, ok := any(svc).(interface {
		SetSiteLoginSuccessCallback(func(string, string, string))
	}); ok {
		setter.SetSiteLoginSuccessCallback(func(user, workspace, siteID string) {
			if !repo.committed || repo.saved.Session == nil || repo.saved.Session.AccessToken != "fixture-new" {
				t.Error("notification preceded successful persistence")
			}
			if user != repo.saved.UserID || workspace != repo.saved.AdminAccountID || siteID != repo.saved.ID {
				t.Error("notification used a different persisted scope")
			}
			// Callback runs outside the service lock and may inspect local state.
			if svc.mu.TryLock() {
				svc.mu.Unlock()
			} else {
				t.Error("notification held the upstream service lock")
			}
			notices++
		})
	}
	dto := UpdateRequest{Name: "验收-持久化", SiteURL: server.URL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-new", RechargeRate: 1}
	if _, err := svc.Update(t.Context(), "user", site.ID, dto); err == nil {
		t.Fatal("fixture persistence should fail")
	}
	if notices != 0 {
		t.Error("failed persistence cleared manual block")
	}
	rolledBack, err := cache.Get(t.Context(), site.ID)
	if err != nil || rolledBack == nil || rolledBack.Session.AccessToken != "fixture-old" {
		t.Fatal("failed update lost prior credentials")
	}
	repo.saveErr = nil
	if _, err := svc.Update(t.Context(), "user", site.ID, dto); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Errorf("successfully persisted credential notifications=%d want=1", notices)
	}
}
