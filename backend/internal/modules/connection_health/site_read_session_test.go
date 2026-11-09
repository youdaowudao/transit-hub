package connection_health

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// my_sites' guarded reads return this exact local error without an HTTP call.
// Its entry/late-response behavior is covered in that package; this case checks
// the real multiplier and Priority consumers of the same error contract.
type siteReadPausedMetadataReader struct {
	fakeMySitesReader
	site *upstream.Site
}

func (r *siteReadPausedMetadataReader) GetUpstreamKeyForWorkspace(context.Context, string, string, string, string) (upstream.Sub2APIKeyItem, error) {
	return upstream.Sub2APIKeyItem{}, upstream.SiteReadAuthError(r.site)
}

func (r *siteReadPausedMetadataReader) ListUpstreamKeysForWorkspace(context.Context, string, string, string) ([]upstream.Sub2APIKeyItem, error) {
	return nil, upstream.SiteReadAuthError(r.site)
}

func TestSiteReadPausedKeyKeepsMultiplierUnavailableAndPriorityUntouched(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	site := snapshotSite("site-1")
	key := upstream.ErrorRefreshTokenRejected
	site.Status, site.ErrorKey = upstream.StatusError, &key
	site.Session = &upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"}
	reader := &siteReadPausedMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}}, site: site}
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	svc := &Service{mySites: reader, sites: &failureFixRecoverableSiteRead{site: site}, multiplierNow: func() time.Time { return now }, repo: repo, priorityActions: actions}
	homeCostStopMultiplierService(t, svc)
	var resolution upstreamMultiplierResolution
	for _, force := range []bool{false, true, false} {
		lookup := svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
		resolution = lookup.byAccount["account-1"]
		if resolution.status != MultiplierResolutionUnavailable || resolution.reason != key || !site.IsEnabled() {
			t.Fatal("authentication pause became a disabled/resolved multiplier or changed Enabled")
		}
	}
	policy := probePolicy()
	policy.AutoDegradeEnabled = true
	policy.PriorityMode, policy.StrategyMode = PriorityModeMultiplier, StrategyModeHealthProbe
	target := "sub2api:ws1:100"
	pending := 20
	stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: target, OriginalPriority: 13, LastAppliedPriority: 13, PendingPriority: &pending, EffectiveMultiplier: .05}
	repo.priorityStates["user1|ws1|"+target] = stored
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", PendingSignature: "generation-1", LastDecision: "running"}
	inventory := map[string]*priorityTargetInventory{target: {target: AdminProbeTarget{TargetID: target, AccountID: "100", Models: []string{"gpt-4o"}}, policies: []Policy{policy}, upstreamMultiplier: resolution, currentPriority: 13, priorityPresent: true}}
	states := []ConnectionHealthState{{ConnectionID: target, ModelName: "gpt-4o", State: StateHealthy}}
	svc.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, states, []PrioritySyncState{stored}, "generation-1")
	current := repo.priorityStates["user1|ws1|"+target]
	if len(actions.calls) != 0 || current.LastAppliedPriority != 13 || current.EffectiveMultiplier != .05 || current.PendingPriority == nil || *current.PendingPriority != pending {
		t.Fatal("unavailable authentication metadata wrote Priority or replaced its checkpoint")
	}
}
