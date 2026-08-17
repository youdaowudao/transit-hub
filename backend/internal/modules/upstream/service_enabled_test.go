package upstream

import (
	"context"
	"testing"
	"time"
)

type enabledTestRepository struct {
	sites []Site
	saved []Site
}

func (r *enabledTestRepository) ListSites(ctx context.Context) ([]Site, error) {
	return append([]Site(nil), r.sites...), nil
}

func (r *enabledTestRepository) ListSitesForUser(ctx context.Context, userID string) ([]Site, error) {
	return append([]Site(nil), r.sites...), nil
}

func (r *enabledTestRepository) SaveSite(ctx context.Context, site Site) error {
	r.saved = append(r.saved, site)
	return nil
}

func (r *enabledTestRepository) DeleteSite(ctx context.Context, userID string, id string) error {
	return nil
}

func TestUpdateEnabledStopsAndRestoresSchedulingWithoutImmediateSync(t *testing.T) {
	site := newTestSite("site-1", "user-1", "acc-1", 1, &Session{Platform: PlatformSub2API, AccessToken: "token"})
	repository := &enabledTestRepository{sites: []Site{*site}}
	cache := newFakeSiteCache()
	cache.add(site)
	service := NewService(nil, repository, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "acc-1"}})
	service.SetWorkspaceRefreshConfig("user-1", "acc-1", RefreshConfig{Enabled: true, Interval: time.Hour})
	t.Cleanup(service.Close)

	if service.timers[site.ID] == nil {
		t.Fatal("enabled site was not scheduled")
	}
	disabled, err := service.UpdateEnabled(context.Background(), "user-1", site.ID, false)
	if err != nil {
		t.Fatalf("UpdateEnabled(false) error: %v", err)
	}
	if disabled.IsEnabled() || service.timers[site.ID] != nil {
		t.Fatalf("disabled site still enabled or scheduled: enabled=%v timer=%v", disabled.IsEnabled(), service.timers[site.ID])
	}
	if _, err := service.Sync(context.Background(), "user-1", site.ID); errorKey(err) != ErrorDisabled {
		t.Fatalf("Sync(disabled) error = %v, want %s", err, ErrorDisabled)
	}
	responses, err := service.SyncAll(context.Background(), "user-1")
	if err != nil || len(responses) != 1 || responses[0].IsEnabled() {
		t.Fatalf("SyncAll disabled result = %#v, err=%v", responses, err)
	}
	events := make([]SyncEvent, 0)
	if err := service.SyncAllStream(context.Background(), "user-1", func(event SyncEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("SyncAllStream disabled error: %v", err)
	}
	if len(events) != 1 || events[0].Event != SyncEventComplete {
		t.Fatalf("SyncAllStream disabled events = %#v, want only complete", events)
	}

	restored, err := service.UpdateEnabled(context.Background(), "user-1", site.ID, true)
	if err != nil {
		t.Fatalf("UpdateEnabled(true) error: %v", err)
	}
	if !restored.IsEnabled() || service.timers[site.ID] == nil {
		t.Fatalf("restored site not enabled or scheduled: enabled=%v timer=%v", restored.IsEnabled(), service.timers[site.ID])
	}
	if len(repository.saved) != 2 {
		t.Fatalf("saved lifecycle states = %d, want 2", len(repository.saved))
	}
}

func TestDisabledSiteIsExcludedFromCostQueries(t *testing.T) {
	disabled := false
	site := newTestSite("site-1", "user-1", "acc-1", 1, &Session{Platform: PlatformSub2API, AccessToken: "token"})
	site.Enabled = &disabled
	cache := newFakeSiteCache()
	cache.add(site)
	service := NewService(nil, nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "acc-1"}})

	items, err := service.KeyUsageToday(context.Background(), "user-1")
	if err != nil || len(items) != 0 {
		t.Fatalf("KeyUsageToday disabled result = %#v, err=%v", items, err)
	}
	costs, err := service.FetchSiteCostsForDate(context.Background(), "user-1", "acc-1", "2026-08-08")
	if err != nil || len(costs) != 0 {
		t.Fatalf("FetchSiteCostsForDate disabled result = %#v, err=%v", costs, err)
	}
	balances, err := service.BalanceBreakdown(context.Background(), "user-1")
	if err != nil || len(balances) != 0 {
		t.Fatalf("BalanceBreakdown disabled result = %#v, err=%v", balances, err)
	}
}
