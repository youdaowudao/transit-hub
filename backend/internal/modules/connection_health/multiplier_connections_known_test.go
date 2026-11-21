package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type unavailableMultiplierSite struct{}

func (unavailableMultiplierSite) GetSite(context.Context, string) (*upstream.Site, error) {
	return nil, errors.New("fixture site read failed")
}

func TestMultiplierLookupKeepsUnboundAccountIndependentOfOtherSiteFailure(t *testing.T) {
	reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{
		connections: []my_sites.RealConnection{snapshotConnection("bound", "site-1", "key-1")},
	}}
	service := &Service{mySites: reader, sites: unavailableMultiplierSite{}}
	platform := string(upstream.PlatformSub2API)
	known := service.multiplierLookupForWorkspace(t.Context(), "user1", "ws1", platform, false, true)
	if resolution := resolutionForAdminAccount(known, "bound"); resolution.status != MultiplierResolutionUnavailable || !isPriorityMultiplierBlocker(resolution.status) {
		t.Fatalf("account with bad site was not blocked: %+v", resolution)
	}
	if resolution := resolutionForAdminAccount(known, "unbound"); resolution.status != MultiplierResolutionUnassociated || isPriorityMultiplierBlocker(resolution.status) {
		t.Errorf("unbound account inherited an unrelated site error: %+v", resolution)
	}
	if adminMultiplierLookupCacheable(known) {
		t.Error("workspace lookup with failing site must still be non-cacheable")
	}

	service.mySites = &failingPipelineConnectionsReader{snapshotMetadataReader: reader, err: errors.New("fixture connection read failed")}
	unknown := service.multiplierLookupForWorkspace(t.Context(), "user1", "ws1", platform, false, true)
	if resolution := resolutionForAdminAccount(unknown, "unbound"); resolution.status != MultiplierResolutionUnavailable || resolution.reason != MultiplierReasonSiteUnavailable || !isPriorityMultiplierBlocker(resolution.status) {
		t.Fatalf("failed connection read should still block the unverified account: %+v", resolution)
	}
}
