package connection_health

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type cleanupPathsSnapshotRepository struct{ *fakeRepository }

func (r cleanupPathsSnapshotRepository) ListWorkspaceHealthSettings(ctx context.Context) ([]WorkspaceHealthSettings, error) {
	settings, err := r.GetWorkspaceHealthSettings(ctx, "user1", "ws1")
	return []WorkspaceHealthSettings{settings}, err
}

func cleanupPathsFixture(t *testing.T, monitored bool) (*Service, *fakeRepository, TargetActionState, *fakePlatformActioner) {
	t.Helper()
	repo := newFakeRepository()
	stored := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
	repo.targetActionStates["user1|ws1|"+stored.TargetID] = stored
	if monitored {
		policy := monitoringScopeTestPolicy("cleanup")
		policy.AutoDegradeEnabled, policy.AutoRemoteActionEnabled = true, true
		repo.policies = []Policy{policy}
		repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}
	}
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Status: "active", Models: "gpt-4o", Priority: intPointer(20), Schedulable: boolPointer(true)}}}}
	s := &Service{repo: repo, platformGroups: reader, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, accounts: fakeAdminAccountResolver{id: "ws1"}, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("cleanup fixture shutdown: %v", err)
		}
	})
	return s, repo, stored, platform
}

func cleanupPathsRestore(s *Service, repo *fakeRepository, stored TargetActionState, ctx context.Context) {
	s.restoreUnmanagedTargetActions(ctx, repo.policies, repo.assignments, repo.groupAssignments, repo.groupExclusions, []TargetActionState{stored}, adminInventoryCache{})
}

func TestAbandonEventFailureKeepsCheckpoint(t *testing.T) {
	s, repo, stored, platform := cleanupPathsFixture(t, false)
	repo.insertEventErr = errors.New("fixture event storage unavailable")
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || !checkpoint.Conflict || len(repo.events) != 0 {
		t.Fatal("event failure must retain the conflict checkpoint without an audit")
	}
	repo.insertEventErr = nil
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint != nil || len(repo.events) != 1 || len(platform.sub2APICalls) != 0 {
		t.Fatalf("retry must atomically abandon and audit without a main-site write: checkpoint=%v events=%d", checkpoint, len(repo.events))
	}
}

func TestAbandonWaitsForStableGeneration(t *testing.T) {
	s, repo, stored, _ := cleanupPathsFixture(t, false)
	ctx, err := s.captureWorkspaceRules(context.Background(), "user1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	repo.testConfigurationMu.Lock()
	repo.bumpFakeConfigGeneration("user1", "ws1")
	repo.testConfigurationMu.Unlock()
	cleanupPathsRestore(s, repo, stored, ctx)
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || len(repo.events) != 0 {
		t.Fatal("changed configuration must retain the old unmanaged decision")
	}
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint != nil || len(repo.events) != 1 {
		t.Fatal("stable next tick must abandon the still-unmanaged conflict")
	}
}

func TestAbandonWaitsForPriorityPending(t *testing.T) {
	for _, kind := range []string{"value", "dispatch"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, stored, _ := cleanupPathsFixture(t, false)
			priority := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: stored.TargetID, OriginalPriority: 20, LastAppliedPriority: 20}
			if kind == "value" {
				priority.PendingPriority = intPointer(30)
			} else {
				priority.PendingDispatchID, priority.PendingOwnerID, priority.PendingDispatchPhase = "fixture", "fixture", DispatchSending
			}
			repo.priorityStates["user1|ws1|"+stored.TargetID] = priority
			cleanupPathsRestore(s, repo, stored, context.Background())
			if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || len(repo.events) != 0 {
				t.Fatal("pending Priority must keep the target conflict checkpoint")
			}
			delete(repo.priorityStates, "user1|ws1|"+stored.TargetID)
			cleanupPathsRestore(s, repo, stored, context.Background())
			if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint != nil || len(repo.events) != 1 {
				t.Fatal("next tick without pending Priority must abandon and audit")
			}
		})
	}
}

func TestManagedConflictCheckpointKept(t *testing.T) {
	s, repo, stored, platform := cleanupPathsFixture(t, true)
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || !checkpoint.Conflict || len(repo.events) != 0 || len(platform.sub2APICalls) != 0 {
		t.Fatal("managed manual conflict must retain its checkpoint and upstream value")
	}
}

func TestInvisibleUnconflictedCheckpointKept(t *testing.T) {
	s, repo, stored, platform := cleanupPathsFixture(t, false)
	stored.Conflict = false
	repo.targetActionStates["user1|ws1|"+stored.TargetID] = stored
	s.platformGroups = fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}}
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || checkpoint.Conflict || len(repo.events) != 0 || len(platform.sub2APICalls) != 0 {
		t.Fatal("invisible account without a stored manual conflict must stay untouched")
	}
}

func TestNewAPIConflictCheckpointNotAbandoned(t *testing.T) {
	s, repo, stored, platform := cleanupPathsFixture(t, false)
	delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
	stored.TargetID = "newapi:ws1:a"
	repo.targetActionStates["user1|ws1|"+stored.TargetID] = stored
	s.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	cleanupPathsRestore(s, repo, stored, context.Background())
	if checkpoint, _ := repo.GetTargetActionState(context.Background(), stored.UserID, stored.AdminAccountID, stored.TargetID); checkpoint == nil || !checkpoint.Conflict || len(repo.events) != 0 || len(platform.calls) != 0 {
		t.Fatal("Sub2API abandonment must preserve NewAPI's existing conflict behavior")
	}
}

func TestAbandonedCheckpointAuditVisibleWithoutAssignments(t *testing.T) {
	s, repo, stored, _ := cleanupPathsFixture(t, false)
	cleanupPathsRestore(s, repo, stored, context.Background())
	events, err := s.Events(context.Background(), "user1", stored.TargetID, 100)
	if err != nil || len(events) != 1 || events[0].Result != "policy_unmanaged_abandoned" {
		t.Fatalf("abandonment must remain visible after assignments disappear: events=%+v err=%v", events, err)
	}
}

func cleanupPathsState(repo *fakeRepository, workspace, account, model string) string {
	id := "sub2api:" + workspace + ":" + account
	if repo.states[id] == nil {
		repo.states[id] = map[string]ConnectionHealthState{}
	}
	now := time.Now().UTC()
	repo.states[id][model] = ConnectionHealthState{ConnectionID: id, UserID: "user1", AdminAccountID: workspace, ModelName: model, State: StateHealthy, LastProbeAt: &now}
	return id
}

func TestUnmonitoredTargetStatesDeleted(t *testing.T) {
	for _, kind := range []string{"excluded", "manual-priority", "left-groups"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, stored, _ := cleanupPathsFixture(t, true)
			delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
			id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
			cleanupPathsState(repo, "ws1", "a", "stale-model")
			reader := s.platformGroups.(fakePlatformGroupReader)
			switch kind {
			case "excluded":
				repo.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: id}}
			case "manual-priority":
				reader.accountsByGrp["g1"][0].Priority = intPointer(5)
			case "left-groups":
				reader.accountsByGrp["g1"] = nil
			}
			s.platformGroups = reader
			s.runSchedulerTick(context.Background())
			if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 0 {
				t.Fatalf("%s account retained %d health rows", kind, len(states))
			}
		})
	}
}

func TestStatesKeptWhenNotProvablyUnmonitored(t *testing.T) {
	for _, kind := range []string{"incomplete", "monitored", "target-checkpoint", "priority-checkpoint", "changed-generation"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, stored, _ := cleanupPathsFixture(t, true)
			delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
			id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
			reader := s.platformGroups.(fakePlatformGroupReader)
			if kind != "monitored" {
				reader.accountsByGrp["g1"] = nil
			}
			switch kind {
			case "incomplete":
				reader.errByGrp = map[string]error{"g1": errors.New("fixture partial inventory")}
			case "target-checkpoint":
				stored.Conflict = false
				repo.targetActionStates["user1|ws1|"+id] = stored
			case "priority-checkpoint":
				repo.priorityStates["user1|ws1|"+id] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 20, LastAppliedPriority: 20}
			case "changed-generation":
				s.repo = cleanupPathsSnapshotRepository{repo}
				base := reader
				s.platformGroups = &inventoryPathsReader{fakePlatformGroupReader: base, fetch: func(context.Context, upstream.Session, int) ([]upstream.AdminGroupInfo, error) {
					repo.testConfigurationMu.Lock()
					repo.bumpFakeConfigGeneration("user1", "ws1")
					repo.testConfigurationMu.Unlock()
					return base.groups, nil
				}, list: func(_ context.Context, _ upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
					return base.accountsByGrp[group.ID], nil
				}}
			}
			if kind != "changed-generation" {
				s.platformGroups = reader
			}
			s.runSchedulerTick(context.Background())
			waitSchedulerPathsIdle(t, s)
			if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 1 {
				t.Fatalf("%s account lost health rows without sufficient evidence", kind)
			}
		})
	}
}

func TestInScopeModelLeftoverKept(t *testing.T) {
	s, repo, stored, _ := cleanupPathsFixture(t, true)
	delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
	id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
	cleanupPathsState(repo, "ws1", "a", "stale-model")
	s.runSchedulerTick(context.Background())
	if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 2 {
		t.Fatal("still-monitored account must retain every model row")
	}
}

func TestCleanupScopedToWorkspace(t *testing.T) {
	s, repo, stored, _ := cleanupPathsFixture(t, true)
	delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
	first := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
	second := cleanupPathsState(repo, "ws2", "a", "gpt-4o")
	other := monitoringScopeTestPolicy("other-workspace")
	other.AdminAccountID = "ws2"
	repo.policies = append(repo.policies, other)
	repo.groupAssignments = append(repo.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws2", AdminGroupID: "g1", PolicyID: other.ID})
	repo.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: first}}
	s.runSchedulerTick(context.Background())
	if a, _ := repo.ListStatesByConnection(context.Background(), first); len(a) != 0 {
		t.Fatal("excluded workspace retained old state")
	}
	if b, _ := repo.ListStatesByConnection(context.Background(), second); len(b) != 1 {
		t.Fatal("cleanup deleted the same account in another monitored workspace")
	}
}

func TestCleanupWaitsUntilNextTickAfterAbandon(t *testing.T) {
	for _, kind := range []string{"abandoned", "restored"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, stored, _ := cleanupPathsFixture(t, false)
			if kind == "restored" {
				stored.Conflict, stored.LastAppliedStatus = false, "active"
				repo.targetActionStates["user1|ws1|"+stored.TargetID] = stored
			}
			id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
			s.runSchedulerTick(context.Background())
			if checkpoint, _ := repo.GetTargetActionState(context.Background(), "user1", "ws1", id); checkpoint != nil {
				t.Fatal("first tick did not remove the abandoned or already-restored checkpoint")
			}
			if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 1 {
				t.Fatal("same tick must keep health until its initial checkpoint has disappeared")
			}
			s.runSchedulerTick(context.Background())
			if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 0 {
				t.Fatal("next stable tick without a checkpoint must clear old health")
			}
		})
	}
}

func TestCleanupSkipsInFlightAndLeasedTargets(t *testing.T) {
	t.Run("account-lease", func(t *testing.T) {
		s, repo, stored, _ := cleanupPathsFixture(t, false)
		delete(repo.targetActionStates, "user1|ws1|"+stored.TargetID)
		id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
		_, release, acquired, err := s.acquireActionTargetLease(context.Background(), id, false)
		if err != nil || !acquired {
			t.Fatalf("hold account lease: %v", err)
		}
		defer release()
		s.runSchedulerTick(context.Background())
		if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 1 {
			t.Fatal("cleanup deleted an account while its lease was held")
		}
		release()
		s.runSchedulerTick(context.Background())
		if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 0 {
			t.Fatal("cleanup did not retry after the account lease was released")
		}
	})
	t.Run("probe-commit", func(t *testing.T) {
		s, repo := schedulerPathsService(t, "gpt-4o")
		id := cleanupPathsState(repo, "ws1", "a", "gpt-4o")
		state := repo.states[id]["gpt-4o"]
		state.LastProbeAt = timePointer(time.Now().Add(-time.Hour))
		repo.states[id]["gpt-4o"] = state
		base := s.platformGroups.(fakePlatformGroupReader)
		var gone atomic.Bool
		s.platformGroups = &inventoryPathsReader{fakePlatformGroupReader: base, list: func(_ context.Context, _ upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
			if gone.Load() {
				return nil, nil
			}
			return base.accountsByGrp[group.ID], nil
		}}
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			close(started)
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-release:
				return schedulerPathsResponse(request, http.StatusOK), nil
			}
		})
		s.runSchedulerTick(context.Background())
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("scheduled probe did not start")
		}
		gone.Store(true)
		s.runSchedulerTick(context.Background())
		if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 1 {
			t.Fatal("moved account was deleted before its in-flight probe committed")
		}
		unblock()
		waitSchedulerPathsIdle(t, s)
		if got, _ := repo.GetState(context.Background(), id, "gpt-4o"); got == nil || got.LastProbeAt == nil || !got.LastProbeAt.After(*state.LastProbeAt) {
			t.Fatal("protected in-flight probe did not commit its result")
		}
		s.runSchedulerTick(context.Background())
		if states, _ := repo.ListStatesByConnection(context.Background(), id); len(states) != 0 {
			t.Fatal("next tick did not delete the moved account after its final commit")
		}
	})
}
