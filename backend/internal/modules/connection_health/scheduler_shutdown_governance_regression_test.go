package connection_health

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestShutdownSchedulerAdmissionAndCancellationOrder(t *testing.T) {
	repo := newFakeRepository()
	policy := schedulableProbePolicy("shutdown-fixture", true, 60)
	policy.AutoRemoteActionEnabled = false
	repo.policies = []Policy{policy}
	repo.assignments = []PolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", PolicyID: policy.ID}}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Status: "active", Models: "gpt-4o", Schedulable: boolPointer(true)}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://shutdown.invalid", Key: "fixture"}}}
	s := &Service{repo: &governanceCanceledRepository{repo}, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, platformGroups: reader, dispatcher: noopRemoteActionRunner{}, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var startOnce, cancelOnce sync.Once
	s.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: c1RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-r.Context().Done()
		cancelOnce.Do(func() { close(canceled) })
		return nil, r.Context().Err()
	})}}
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	// Explicitly exercise only startup's immediate scan; no real clock tick or
	// external service is awaited, and the runtime is always cancelled below.
	s.StartScheduler(parent)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup did not dispatch fake probe")
	}
	complete, err := s.registerActionDispatch()
	if err != nil {
		t.Fatal(err)
	}
	var completeOnce sync.Once
	release := func() { completeOnce.Do(complete) }
	defer release()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(shutdownCtx) }()
	select {
	case err := <-done:
		cancelParent()
		t.Fatalf("shutdown returned before admitted write drained: %v", err)
	case <-canceled:
		cancelParent()
		t.Fatal("probe canceled before admitted main-site write drained")
	case <-time.After(30 * time.Millisecond):
	}
	s.actionDispatchMu.Lock()
	closed := s.actionDispatchClosed
	s.actionDispatchMu.Unlock()
	if !closed {
		cancelParent()
		release()
		<-done
		t.Fatal("shutdown did not close main-site write admission")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			cancelParent()
			t.Fatal(err)
		}
	case <-shutdownCtx.Done():
		cancelParent()
		t.Fatal("shutdown never settled")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		cancelParent()
		<-canceled
		t.Fatal("shutdown left scheduler probe running after writes drained")
	}
	before := calls.Load()
	s.runSchedulerTick(parent)
	if calls.Load() != before {
		t.Fatal("closed scheduler admitted another probe")
	}
}
