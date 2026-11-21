package connection_health

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type actionConfirmationInventory struct {
	fakePlatformGroupReader
	account upstream.AdminGroupAccountInfo
	loadErr error
	reads   int
}

func (i *actionConfirmationInventory) ListSub2APIAdminAccountsForStatusCheck(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error) {
	i.reads++
	return []upstream.AdminGroupAccountInfo{i.account}, i.loadErr
}

type observedActionConfirmationRepository struct {
	*Repository
	beforeResolve func(context.Context, RemoteActionClaim) error
}

type actionConfirmationPreviewRepository struct {
	*fakeRepository
	ownerRunning bool
	ownerErr     error
}

type concurrentActionConfirmationInventory struct {
	fakePlatformGroupReader
	started, resume chan struct{}
}

func (i concurrentActionConfirmationInventory) ListSub2APIAdminAccountsForStatusCheck(ctx context.Context, _ upstream.Session) ([]upstream.AdminGroupAccountInfo, error) {
	i.started <- struct{}{}
	select {
	case <-i.resume:
		return []upstream.AdminGroupAccountInfo{{ID: "42", Status: "inactive"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r actionConfirmationPreviewRepository) RemoteActionOwnerRunning(context.Context, string) (bool, error) {
	return r.ownerRunning, r.ownerErr
}

func (actionConfirmationPreviewRepository) ResolveUncertainAction(context.Context, RemoteActionClaim, RemoteActionObservation) (RemoteActionCheckpoints, error) {
	return RemoteActionCheckpoints{}, errors.New("unexpected preview mutation")
}

func TestResolveUncertainActionPreviewReasons(t *testing.T) {
	for _, tc := range []struct {
		name, reason, status, kind string
		source, wantSource         string
		phase                      RemoteDispatchPhase
		flags                      string
		wantReads                  int
	}{
		{name: "ready automatic without stored source", status: "inactive", wantSource: StrategyModeHealthProbe, wantReads: 1},
		{name: "ready manual", status: "inactive", source: ActionSourceManual, wantSource: ActionSourceManual, wantReads: 1},
		{name: "no records", reason: "none", flags: "empty"},
		{name: "only unrelated account", reason: "none", flags: "other"},
		{name: "multiple records", reason: "multiple_pending", flags: "multiple"},
		{name: "priority", reason: "unsupported_action", flags: "priority"},
		{name: "schedulable", reason: "unsupported_action", kind: TargetMutationSchedulable},
		{name: "delete", reason: "unsupported_action", kind: TargetMutationDelete},
		{name: "sending", reason: "not_uncertain", phase: DispatchSending},
		{name: "original running", reason: "original_running", flags: "running"},
		{name: "original owner store failure", reason: "read_failed", flags: "ownerErr"},
		{name: "no stored credentials", reason: "session_unavailable", flags: "noSession"},
		{name: "new API without credentials", reason: "unsupported_platform", flags: "newAPI"},
		{name: "main site error", reason: "main_site_unavailable", flags: "siteErr", wantReads: 1},
		{name: "missing account", reason: "account_not_found", flags: "missing", wantReads: 1},
		{name: "explicit error", reason: "main_site_status_unknown", status: "error", wantReads: 1},
		{name: "whitespace", reason: "main_site_status_unknown", status: " ", wantReads: 1},
		{name: "unsupported enum", reason: "main_site_status_unknown", status: "enabled", wantReads: 1},
		{name: "fraction", reason: "main_site_status_unknown", status: "0.5", wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const user, workspace, accountID, targetID = "u", "w", "42", "sub2api:w:42"
			ctx := t.Context()
			repo := actionConfirmationPreviewRepository{fakeRepository: newFakeRepository(), ownerRunning: tc.flags == "running"}
			before := time.Now().Add(-10 * time.Minute)
			target := TargetActionState{UserID: user, AdminAccountID: workspace, TargetID: targetID, PendingStatus: "active", PendingActionKind: TargetMutationStatus,
				PendingDispatchID: "dispatch", PendingOwnerID: "old-owner", PendingDispatchPhase: DispatchUncertain, PendingSource: tc.source, UpdatedAt: before}
			if tc.kind != "" {
				target.PendingActionKind = tc.kind
			}
			if tc.phase != "" {
				target.PendingDispatchPhase = tc.phase
			}
			if tc.flags == "other" {
				target.TargetID = "sub2api:w:43"
			}
			if tc.flags != "empty" && tc.flags != "priority" {
				repo.targetActionStates[target.TargetID] = target
			}
			if tc.flags == "priority" || tc.flags == "multiple" {
				repo.priorityStates[targetID] = PrioritySyncState{UserID: user, AdminAccountID: workspace, TargetID: targetID, PendingPriority: intPtr(1)}
			}
			if tc.flags == "ownerErr" {
				repo.ownerErr = errors.New("synthetic repository error")
			}
			inventory := &actionConfirmationInventory{account: upstream.AdminGroupAccountInfo{ID: accountID, Status: tc.status}}
			if tc.flags == "missing" {
				inventory.account.ID = "43"
			}
			if tc.flags == "siteErr" {
				inventory.loadErr = &upstream.RequestError{StatusCode: http.StatusForbidden, UpstreamMessage: "请求被主站拒绝：synthetic-token"}
			}
			service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: workspace}, mySites: confirmTestSessionReader{fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "synthetic-token"}}}, platformGroups: inventory}
			if tc.flags == "noSession" || tc.flags == "newAPI" {
				service.mySites = fakeMySitesReader{}
			}
			if tc.flags == "newAPI" {
				service.accounts = tierNonSub2APIAccountResolver{fakeAdminAccountResolver{id: workspace}}
			}
			view, err := service.readRemoteActionCurrent(ctx, user, accountID)
			if inventory.reads != tc.wantReads {
				t.Fatalf("preview reads %d, want %d", inventory.reads, tc.wantReads)
			}
			if tc.flags == "ownerErr" || tc.flags == "newAPI" {
				var problem *remoteActionConfirmProblem
				if !errors.As(err, &problem) || problem.BlockedReason != tc.reason || problem.Detail == "" {
					t.Fatalf("unexpected failure: %v", err)
				}
				return
			}
			if err != nil || view.BlockedReason != tc.reason || view.CanConfirm != (tc.reason == "") {
				t.Fatalf("incorrect preview: view=%+v err=%v", view, err)
			}
			if view.CanConfirm && (view.WillMarkAs != "rejected" || view.PendingValue != "active" || view.CurrentValue != "inactive" || view.Source != tc.wantSource ||
				view.ObservedAt == nil || view.RecordedAt == nil || !view.RecordedAt.Equal(before)) {
				t.Fatalf("preview did not show the outcome and timestamps: %+v", view)
			}
			if tc.flags == "siteErr" && view.Detail != "请求被主站拒绝：***（HTTP 403）" {
				t.Fatalf("main site reason was lost or unredacted: %q", view.Detail)
			}
		})
	}
}

func (r observedActionConfirmationRepository) ResolveUncertainAction(ctx context.Context, claim RemoteActionClaim, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	if err := r.beforeResolve(ctx, claim); err != nil {
		return RemoteActionCheckpoints{}, err
	}
	return r.Repository.ResolveUncertainAction(ctx, claim, observation)
}

func TestResolveUncertainActionPostgresManualAndLateReceipt(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	siteRepo := my_sites.NewRepository(pool)
	if err := siteRepo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour).UnixMilli()
	if err := siteRepo.Save(ctx, my_sites.State{UserID: "u", AdminAccountID: "w", BaseURL: "https://fixture.invalid", Session: upstream.Session{
		Platform: upstream.PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "synthetic-token", RefreshToken: "synthetic-refresh", ExpiresAt: &expired,
	}}); err != nil {
		t.Fatal(err)
	}
	var beforeSessionVersion string
	const sessionVersionSQL = `SELECT xmin::text FROM my_site_states WHERE user_id='u' AND admin_account_id='w'`
	if err := pool.QueryRow(ctx, sessionVersionSQL).Scan(&beforeSessionVersion); err != nil {
		t.Fatal(err)
	}
	storedSessionReader := my_sites.NewService(siteRepo, nil, nil)
	for _, tc := range []struct {
		name, accountID, rawStatus string
		hadBaseline, wantConflict  bool
	}{
		{"rejected without baseline", "50", "1", false, false},
		{"applied without baseline", "51", "0", false, false},
		{"rejected with baseline", "52", "active", true, false},
		{"applied with baseline", "53", "disabled", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := RemoteActionScope{"u", "w", "sub2api:w:" + tc.accountID}
			_, err := pool.Exec(ctx, `INSERT INTO connection_health_target_action_states
				(user_id, admin_account_id, target_id, original_status, original_weight, last_applied_status, last_applied_weight, pending_status,
				 pending_dispatch_id, pending_owner_id, pending_dispatch_phase, pending_action_kind, pending_source, pending_had_automatic_baseline, updated_at)
				VALUES ('u', 'w', $1, 'active', 15, 'active', 7, 'inactive', 'dispatch', 'old-owner', 'uncertain', 'status', 'manual', $2, clock_timestamp() - interval '10 minutes')`, scope.TargetID, tc.hadBaseline)
			if err != nil {
				t.Fatal(err)
			}
			inventory := &actionConfirmationInventory{account: upstream.AdminGroupAccountInfo{ID: tc.accountID, Status: tc.rawStatus}}
			service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, mySites: storedSessionReader, platformGroups: inventory}
			preview, err := service.readRemoteActionCurrent(ctx, "u", tc.accountID)
			if err != nil || !preview.CanConfirm || inventory.reads != 1 {
				t.Fatalf("manual preview reads=%d canConfirm=%v err=%v", inventory.reads, preview.CanConfirm, err)
			}
			if err := service.confirmRemoteActionCurrent(ctx, "u", tc.accountID, remoteActionConfirmInput{"dispatch", tc.rawStatus}); err != nil || inventory.reads != 2 {
				t.Fatalf("manual confirmation reads=%d err=%v", inventory.reads, err)
			}
			var afterSessionVersion string
			if err := pool.QueryRow(ctx, sessionVersionSQL).Scan(&afterSessionVersion); err != nil || afterSessionVersion != beforeSessionVersion {
				t.Fatalf("preview or confirm changed the saved session, err=%v", err)
			}
			late := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindTarget, OwnerID: "old-owner", DispatchID: "dispatch"}
			if err := repo.RecordRemoteActionReceipt(ctx, late, DispatchConfirmedApplied); !errors.Is(err, ErrRemoteActionPending) {
				t.Fatalf("late receipt not rejected: %v", err)
			}
			state, err := repo.GetTargetActionState(ctx, "u", "w", scope.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.hadBaseline {
				if state != nil {
					t.Fatalf("manual operation without baseline retained a row: %+v", state)
				}
				return
			}
			if state == nil || targetActionPending(state) || state.Conflict != tc.wantConflict ||
				state.OriginalStatus != "active" || state.LastAppliedStatus != "active" ||
				!equalIntPointers(state.OriginalWeight, intPtr(15)) || !equalIntPointers(state.LastAppliedWeight, intPtr(7)) {
				t.Fatalf("manual resolution changed old baseline rules: %+v", state)
			}
		})
	}
}

func TestResolveUncertainActionPostgresConcurrentConfirm(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_target_action_states
		(user_id, admin_account_id, target_id, original_status, last_applied_status, pending_status,
		 pending_dispatch_id, pending_owner_id, pending_dispatch_phase, pending_action_kind, updated_at)
		VALUES ('u', 'w', 'sub2api:w:42', 'active', 'active', 'inactive', 'dispatch', 'old-owner', 'uncertain', 'status', clock_timestamp() - interval '10 minutes')`)
	if err != nil {
		t.Fatal(err)
	}
	inventory := concurrentActionConfirmationInventory{started: make(chan struct{}, 2), resume: make(chan struct{})}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, mySites: confirmTestSessionReader{fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "synthetic-token"}}}, platformGroups: inventory}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- service.confirmRemoteActionCurrent(ctx, "u", "42", remoteActionConfirmInput{"dispatch", "inactive"})
		}()
	}
	for range 2 {
		select {
		case <-inventory.started:
		case <-ctx.Done():
			t.Fatal("both confirmation requests did not start the main-site read")
		}
	}
	close(inventory.resume)
	successes, changed := 0, 0
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				successes++
				continue
			}
			var problem *remoteActionConfirmProblem
			if !errors.As(err, &problem) || problem.BlockedReason != "changed" || problem.status != http.StatusConflict {
				t.Fatalf("unexpected concurrent result: %v", err)
			}
			changed++
		case <-ctx.Done():
			t.Fatal("confirmation requests did not finish")
		}
	}
	state, err := repo.GetTargetActionState(ctx, "u", "w", "sub2api:w:42")
	if err != nil || state == nil || targetActionPending(state) || state.LastAppliedStatus != "inactive" || successes != 1 || changed != 1 {
		t.Fatalf("concurrent confirmation ended with %d successes, %d changes, state=%+v err=%v", successes, changed, state, err)
	}
}

func TestResolveUncertainActionPostgresCommitRollbackAndLeaseFences(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_confirmation_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic deferred commit failure'; END;
		$$;
		CREATE CONSTRAINT TRIGGER test_reject_confirmation_commit AFTER UPDATE ON connection_health_target_action_states
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.target_id = 'sub2api:w:70')
		EXECUTE FUNCTION reject_confirmation_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, id, reason string }{
		{"commit failure", "70", "result_unknown"},
		{"target lease lost", "71", "changed"},
		{"active original owner appears", "72", "original_running"},
		{"new reservation version", "73", ""},
		{"reconciliation rejected", "74", "reconcile_failed"},
		{"main site value changed", "75", "current_value_changed"},
		{"pending alias appears", "76", "multiple_pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetID := "sub2api:w:" + tc.id
			pending, current := "inactive", "active"
			owner := "original-owner-" + tc.id
			if tc.id == "74" {
				// This malformed stored intention disagrees with the existing
				// reconciliation after it interprets numeric zero as active.
				pending, current = "0", "inactive"
			}
			_, err := pool.Exec(ctx, `INSERT INTO connection_health_target_action_states
				(user_id, admin_account_id, target_id, original_status, last_applied_status, pending_status,
				 pending_dispatch_id, pending_owner_id, pending_dispatch_phase, pending_action_kind, updated_at)
				VALUES ('u', 'w', $1, 'active', 'active', $2, 'dispatch', $3, 'uncertain', 'status', clock_timestamp() - interval '10 minutes')`, targetID, pending, owner)
			if err != nil {
				t.Fatal(err)
			}
			inventory := &actionConfirmationInventory{account: upstream.AdminGroupAccountInfo{ID: tc.id, Status: current}}
			service := &Service{accounts: fakeAdminAccountResolver{id: "w"}, mySites: confirmTestSessionReader{fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "synthetic-token"}}}, platformGroups: inventory}
			guard := service.sub2APIFloorGuardFor("u", "w")
			guard.mu.Lock()
			guard.reserveUnavailableLocked(targetID)
			guard.mu.Unlock()
			originalVersion, _ := guard.reservationVersion(targetID)
			service.repo = observedActionConfirmationRepository{repo, func(ctx context.Context, claim RemoteActionClaim) error {
				switch tc.id {
				case "71":
					_, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1`, claim.LeaseKey)
					return err
				case "72":
					_, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key, owner_id, expires_at) VALUES ('original-send', $1, clock_timestamp()+interval '1 minute')`, owner)
					return err
				case "73":
					guard.mu.Lock()
					guard.reserveUnavailableLocked(targetID)
					guard.mu.Unlock()
				case "76":
					_, err := pool.Exec(ctx, `INSERT INTO connection_health_priority_sync_states
						(user_id, admin_account_id, target_id, pending_priority, pending_dispatch_id, pending_owner_id, pending_dispatch_phase)
						VALUES ('u', 'w', 'sub2api:w: 76 ', 14, 'alias-dispatch', 'old-alias-owner', 'uncertain')`)
					return err
				}
				return nil
			}}
			priority := &priorityTargetInventory{target: AdminProbeTarget{InventoryComplete: true, AccountStatus: current},
				priorityPresent: true, currentPriority: 10, snapshotStartedAt: time.Now()}
			checkPriority := func() error {
				return service.syncSafePriorityTarget(ctx, upstream.Session{}, "u", "w", targetID, priority, nil, 10, nil, nil, "", false, false)
			}
			if tc.id == "73" {
				if err := checkPriority(); !errors.Is(err, ErrRemoteActionPending) {
					t.Fatalf("Priority did not see the pending state before confirmation: %v", err)
				}
			}
			requested := current
			if tc.id == "75" {
				requested = "inactive"
			}
			err = service.confirmRemoteActionCurrent(ctx, "u", tc.id, remoteActionConfirmInput{"dispatch", requested})
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("successful resolution failed: %v", err)
				}
			} else {
				var response *remoteActionConfirmProblem
				if !errors.As(err, &response) || response.BlockedReason != tc.reason {
					t.Fatalf("confirmation returned %v, want %s", err, tc.reason)
				}
				wantStatus := http.StatusConflict
				if tc.reason == "result_unknown" {
					wantStatus = http.StatusInternalServerError
				}
				if response.status != wantStatus {
					t.Fatalf("confirmation returned status %d, want %d", response.status, wantStatus)
				}
			}
			state, err := repo.GetTargetActionState(ctx, "u", "w", targetID)
			if err != nil || state == nil || state.LastAppliedStatus != "active" || state.OriginalStatus != "active" || inventory.reads != 1 {
				t.Fatalf("changed baseline or inventory reads: state=%+v reads=%d err=%v", state, inventory.reads, err)
			}
			version, reserved := guard.reservationVersion(targetID)
			if !reserved {
				t.Fatal("released a protected reservation")
			}
			if tc.id == "76" {
				var pendingDispatch string
				err := pool.QueryRow(ctx, `SELECT pending_dispatch_id FROM connection_health_priority_sync_states WHERE target_id='sub2api:w: 76 '`).Scan(&pendingDispatch)
				if err != nil || pendingDispatch != "alias-dispatch" {
					t.Fatalf("confirmation touched the newly pending alias: %q err=%v", pendingDispatch, err)
				}
			}
			if tc.reason == "" {
				if targetActionPending(state) || version == originalVersion {
					t.Fatalf("resolution did not protect replacement reservation: %+v version=%d", state, version)
				}
				if err := checkPriority(); err != nil {
					t.Fatalf("Priority stayed blocked after a rejected action was ended: %v", err)
				}
			} else if state.PendingDispatchPhase != DispatchUncertain || state.PendingDispatchID != "dispatch" || version != originalVersion || state.UpdatedAt.After(time.Now().Add(-5*time.Minute)) {
				t.Fatalf("failed confirmation changed record or reservation: %+v version=%d", state, version)
			}
		})
	}
}
