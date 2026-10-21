package connection_health

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type governanceCanceledRepository struct{ *fakeRepository }

func (r *governanceCanceledRepository) CommitTargetProbe(ctx context.Context, in TargetProbeCommit) (TargetProbeCommitResult, error) {
	if err := ctx.Err(); err != nil {
		return TargetProbeCommitResult{}, err
	}
	return r.fakeRepository.CommitTargetProbe(ctx, in)
}

type forbiddenAutomaticSchedulingWriter struct{ t *testing.T }

func (w forbiddenAutomaticSchedulingWriter) SetSub2APIAdminAccountSchedulable(upstream.Session, string, bool) error {
	w.t.Error("automatic workflow wrote the main-site scheduling switch")
	return nil
}

func TestSchedulerSlowJobDoesNotBlockNextTick(t *testing.T) {
	repo := newFakeRepository()
	p := schedulableProbePolicy("daily-fixture", true, 60)
	p.AutoRemoteActionEnabled = false
	p.PriorityMode = PriorityModeMultiplier
	p.StrategyMode = StrategyModeHealthProbe
	repo.policies = []Policy{p}
	repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: p.ID}}
	recent := time.Now().UTC()
	protocol := TestProtocolChatCompletions
	repo.states["sub2api:ws1:b"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:b", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", State: StateHealthy, LastProbeAt: &recent, LastAppliedProbeProtocol: &protocol}}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "fixture"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {
		{ID: "a", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: "gpt-4o"},
		{ID: "b", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: "gpt-4o"},
	}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://a.invalid", Key: "fixture"}, "b": {BaseURL: "https://b.invalid", Key: "fixture"}}}
	s := &Service{repo: &governanceCanceledRepository{repo}, platformGroups: reader, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, dispatcher: noopRemoteActionRunner{}, priorityActions: &fakeTargetPriorityActioner{}, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	startedA, startedB := make(chan struct{}, 4), make(chan struct{}, 4)
	var countA atomic.Int32
	s.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: c1RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "a.invalid" {
			countA.Add(1)
			startedA <- struct{}{}
		} else {
			startedB <- struct{}{}
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tickDone := make(chan struct{})
	go func() { s.runSchedulerTick(ctx); close(tickDone) }()
	select {
	case <-startedA:
	case <-time.After(time.Second):
		cancel()
		<-tickDone
		t.Fatal("first job never reached fake probe")
	}
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		cancel()
		<-tickDone
		t.Fatal("scan waited for the in-flight account")
	}
	old := recent.Add(-2 * time.Minute)
	state := repo.states["sub2api:ws1:b"]["gpt-4o"]
	state.LastProbeAt = &old
	repo.states["sub2api:ws1:b"]["gpt-4o"] = state
	second := make(chan struct{})
	go func() { s.runSchedulerTick(ctx); close(second) }()
	select {
	case <-second:
	case <-time.After(time.Second):
		cancel()
		<-second
		t.Fatal("next tick waited for A instead of dispatching B")
	}
	select {
	case <-startedB:
	case <-time.After(time.Second):
		t.Error("due account B was not dispatched")
	}
	if countA.Load() != 1 {
		t.Errorf("in-flight A dispatched %d times", countA.Load())
	}
	cancel()
	// Every fake request observes cancellation before the service settles.
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestFloorGuardIgnoresOlderInventory(t *testing.T) {
	g := newWorkspaceFloorGuard()
	now := time.Now().UTC()
	inventory := adminWorkspaceInventory{snapshotStartedAt: now, groupsComplete: true, session: upstream.Session{Platform: upstream.PlatformSub2API}, groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "a", Status: "inactive"}}}}}
	g.rememberInventory(inventory)
	g.mu.Lock()
	g.reservedUnavailable["sub2api:ws1:b"] = 1
	g.mu.Unlock()
	older := inventory
	older.snapshotStartedAt = now.Add(-time.Minute)
	older.groups = []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "a", Status: "active"}}}}
	g.rememberInventory(older)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inventory.groups[0].accounts[0].Status != "inactive" {
		t.Error("older inventory replaced the newer main-site state")
	}
	if _, ok := g.reservedUnavailable["sub2api:ws1:b"]; !ok {
		t.Error("older inventory cleared an active reservation")
	}
	if !g.snapshotAt.Equal(now) {
		t.Errorf("snapshotAt=%s want inventory start %s", g.snapshotAt, now)
	}
}

func TestRoundStatsSplitsScanAndJob(t *testing.T) {
	s := &Service{}
	defer schedulerStatsByService.Delete(s)
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	_, r := s.beginSchedulerRound(context.Background())
	r.started = time.Now().Add(-11 * time.Minute)
	s.finishSchedulerRound(r)
	for _, field := range []string{"scans=", "scan_over_30s=", "scan_longest_ms=", "jobs=", "job_over_30s=", "job_over_60s=", "job_longest_ms=", "max_in_flight=", "skipped_in_flight=", "skipped_no_slot=", "skipped_lease_busy=", "skipped_priority_workspace=", "skipped_restore_busy=", "zero_available=", "unknown="} {
		if !strings.Contains(output.String(), field) {
			t.Errorf("missing %s in %s", field, output.String())
		}
	}
}
