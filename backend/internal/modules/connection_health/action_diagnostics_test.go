package connection_health

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestActionDiagnosticsUnverifiedLegacyVisibleAndIdleHiddenWithoutUpstreamReads(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}}
	repo.priorityStates["u|w|sub2api:w:1"] = PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalPriority: 1, LastAppliedPriority: 2}
	repo.targetActionStates["u|w|sub2api:w:2"] = TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:2", OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}
	snapshot := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, snapshotStartedAt: time.Now()}
	service.rememberActionInventory("u", "w", &snapshot)
	status, err := service.PrioritySyncStatus(context.Background(), "u")
	if err != nil {
		t.Fatal(err)
	}
	invisible := map[string]bool{}
	for _, diagnostic := range status.ActionDiagnostics {
		if diagnostic.Reason == "target_unverified" {
			invisible[diagnostic.TargetID] = true
			if diagnostic.ObservedAt == nil {
				t.Fatal("missing observation provenance")
			}
		}
	}
	if invisible["sub2api:w:1"] || !invisible["sub2api:w:2"] || len(status.ActionDiagnostics) != 1 {
		t.Fatalf("unverified legacy diagnostic and hidden idle checkpoint=%+v", status.ActionDiagnostics)
	}
	if len(repo.priorityStates) != 1 || repo.targetActionStates["u|w|sub2api:w:2"].PendingStatus != "inactive" {
		t.Fatal("diagnostic read changed checkpoint")
	}
}

func TestActionDiagnosticsUnknownAndStaleInventoryCannotClaimTargetDeparted(t *testing.T) {
	svc := &Service{}
	target := "sub2api:w:1"
	pairs := map[string]RemoteActionCheckpoints{target: {Priority: &PrioritySyncState{TargetID: target}}}
	now := time.Now()
	visible := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, snapshotStartedAt: now, groups: []adminInventoryGroup{{accounts: []upstream.AdminGroupAccountInfo{{ID: "1"}}}}}
	svc.rememberActionInventory("u", "w", &visible)
	oldEmpty := adminWorkspaceInventory{session: visible.session, groupsComplete: true, snapshotStartedAt: now.Add(-time.Second)}
	svc.rememberActionInventory("u", "w", &oldEmpty)
	if got := svc.invisibleActionDiagnostics("u", "w", pairs); len(got) != 0 {
		t.Fatalf("older completed refresh replaced newer visibility: %+v", got)
	}
	unknown := adminWorkspaceInventory{session: visible.session, snapshotStartedAt: now.Add(time.Second)}
	svc.rememberActionInventory("u", "w", &unknown)
	if got := svc.invisibleActionDiagnostics("u", "w", pairs); len(got) != 0 {
		t.Fatalf("unknown empty inventory proved departure: %+v", got)
	}
}
