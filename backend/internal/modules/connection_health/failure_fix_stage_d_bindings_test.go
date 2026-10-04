package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestFailureFixStageDMultipleBindingsPreserveActiveConflictAndDisabledRules(t *testing.T) {
	for _, sample := range []struct {
		name        string
		siteIDs     []string
		want        string
		activeCalls int
	}{
		{"missing_and_active", []string{"missing", "active-one"}, MultiplierResolutionResolved, 1},
		{"missing_and_disabled", []string{"missing", "disabled"}, MultiplierResolutionMissing, 0},
		{"disabled_only", []string{"disabled"}, MultiplierResolutionDisabled, 0},
		{"missing_and_multiple_active", []string{"missing", "active-one", "active-two"}, MultiplierResolutionConflict, 2},
		{"disabled_and_active", []string{"disabled", "active-one"}, MultiplierResolutionResolved, 1},
		{"multiple_active_only", []string{"active-one", "active-two"}, MultiplierResolutionConflict, 2},
	} {
		t.Run(sample.name, func(t *testing.T) {
			reader := &snapshotMetadataReader{directItems: map[string]upstream.Sub2APIKeyItem{}}
			for _, siteID := range sample.siteIDs {
				reader.fakeMySitesReader.connections = append(reader.fakeMySitesReader.connections, snapshotConnection("account-one", siteID, "key-one"))
				reader.directItems[siteID+"|key-one"] = upstream.Sub2APIKeyItem{ID: "key-one", GroupID: "group-1", GroupName: "vip"}
			}
			service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"active-one": snapshotSite("active-one"), "active-two": snapshotSite("active-two"), "disabled": disabledSnapshotSite("disabled")}}}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if service.Shutdown(ctx) != nil {
					t.Error("refresh cleanup failed")
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			result := service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
			if result.unavailable || result.byAccount["account-one"].status != sample.want {
				t.Errorf("account fallback status=%s, unavailable=%t", result.byAccount["account-one"].status, result.unavailable)
			}
			direct, lists := reader.callCounts("missing")
			if direct != 0 || lists != 0 {
				t.Error("missing binding requested upstream metadata")
			}
			direct, lists = reader.callCounts("disabled")
			if direct != 0 || lists != 0 {
				t.Error("disabled binding requested upstream metadata")
			}
			total := 0
			for _, siteID := range []string{"active-one", "active-two"} {
				direct, _ = reader.callCounts(siteID)
				total += direct
			}
			if total != sample.activeCalls {
				t.Error("valid bindings were skipped or queried more than once")
			}
		})
	}
}

type failureFixSharedCacheError struct {
	mu          sync.Mutex
	failedCalls int
}

func (f *failureFixSharedCacheError) GetSite(_ context.Context, siteID string) (*upstream.Site, error) {
	if siteID != "read-failed" {
		return snapshotSite(siteID), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failedCalls++
	if f.failedCalls == 1 {
		return snapshotSite(siteID), errors.New("fixture cache read failed")
	}
	return snapshotSite(siteID), nil
}

func TestFailureFixStageDSharedInitialReadErrorRemainsIsolatedInEitherOrder(t *testing.T) {
	for _, sharedAccount := range []bool{false, true} {
		for _, reversed := range []bool{false, true} {
			cache := &failureFixSharedCacheError{}
			reader := &snapshotMetadataReader{directItems: map[string]upstream.Sub2APIKeyItem{"normal|key-one": {ID: "key-one", GroupID: "group-1", GroupName: "vip"}}}
			first := snapshotConnection("failed-one", "read-failed", "key-one")
			second := snapshotConnection("failed-two", "read-failed", "key-two")
			normal := snapshotConnection("healthy", "normal", "key-one")
			if sharedAccount {
				normal.AdminAccountID = "failed-one"
			}
			reader.fakeMySitesReader.connections = append(reader.fakeMySitesReader.connections, first, second, normal)
			if reversed {
				for i, j := 0, len(reader.fakeMySitesReader.connections)-1; i < j; i, j = i+1, j-1 {
					reader.fakeMySitesReader.connections[i], reader.fakeMySitesReader.connections[j] = reader.fakeMySitesReader.connections[j], reader.fakeMySitesReader.connections[i]
				}
			}
			service := &Service{mySites: reader, sites: cache}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if service.Shutdown(ctx) != nil {
					t.Error("refresh cleanup failed")
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			result := service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
			if !result.unavailable || result.byAccount["failed-one"].status != MultiplierResolutionUnavailable || result.byAccount["failed-two"].status != MultiplierResolutionUnavailable {
				t.Error("initial cache read error was overwritten by a later valid binding")
			}
			if !sharedAccount && result.byAccount["healthy"].status != MultiplierResolutionResolved {
				t.Error("failed cache binding blocked an unrelated valid account")
			}
			if direct, lists := reader.callCounts("read-failed"); direct != 0 || lists != 0 {
				t.Error("failed cache binding queried upstream metadata")
			}
			if direct, _ := reader.callCounts("normal"); direct != 1 {
				t.Error("valid binding did not retain its normal metadata read")
			}
			cache.mu.Lock()
			failedCalls := cache.failedCalls
			cache.mu.Unlock()
			if failedCalls != 1 {
				t.Error("same failed site was re-read in the same lookup round")
			}
		}
	}
}
