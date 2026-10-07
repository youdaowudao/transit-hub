package connection_health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type taskBManualInventory struct {
	mu          sync.Mutex
	priority    int
	credentials atomic.Int32
}

func (*taskBManualInventory) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{{ID: "g1", Name: "Task B isolated fixture"}}, nil
}
func (r *taskBManualInventory) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []upstream.AdminGroupAccountInfo{{ID: "a", Name: "Task B isolated fixture", Models: "gpt-4o", Status: "active", Schedulable: boolPointer(true), Priority: intPointer(r.priority), TempUnschedulableKnown: true}}, nil
}
func (r *taskBManualInventory) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	r.credentials.Add(1)
	return upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fake-fixture-key"}, nil
}

func taskBFormalProbeFixture(priority int) (*Service, *fakeRepository, *taskBManualInventory, *atomic.Int32) {
	r := newFakeRepository()
	p := sub2APIProbePolicy(false)
	r.policies = []Policy{p}
	assignPolicyToTarget(r, p, "sub2api:ws1:a")
	reader := &taskBManualInventory{priority: priority}
	s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, r)
	s.priorityActions = &fakeTargetPriorityActioner{}
	calls := &atomic.Int32{}
	s.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"fixture success"}}]}`)), Request: req}, nil
	})}}
	return s, r, reader, calls
}

func taskBFormalRequest(s *Service, stream bool) *httptest.ResponseRecorder {
	path := "/api/connection-health/targets/sub2api:ws1:a/probe"
	if stream {
		path += "-stream"
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"models":["gpt-4o"]}`))
	req = req.WithContext(authctx.WithUserID(req.Context(), "user1"))
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	mux.ServeHTTP(rec, req)
	return rec
}

func taskBAssertManualProbeRejected(t *testing.T, rec *httptest.ResponseRecorder, stream bool, r *fakeRepository, reader *taskBManualInventory, calls *atomic.Int32) {
	t.Helper()
	const key = "admin.connectionHealth.errors.priorityManualProbeExcluded"
	if stream {
		if !strings.Contains(rec.Body.String(), `"type":"error"`) || !strings.Contains(rec.Body.String(), key) || strings.Contains(rec.Body.String(), `"type":"result"`) {
			t.Errorf("SSE must carry exclusion error and no result: %s", rec.Body.String())
		}
	} else if rec.Code != 400 || !strings.Contains(rec.Body.String(), key) {
		t.Errorf("JSON status/body=%d/%s want400 exclusion", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 || reader.credentials.Load() != 0 || len(r.events) != 0 || len(r.states) != 0 || len(r.budgetClaims) != 0 || len(r.targetActionStates) != 0 {
		t.Errorf("manual excluded probe has side effects: requests=%d credentials=%d events=%d states=%d budget=%d actions=%d", calls.Load(), reader.credentials.Load(), len(r.events), len(r.states), len(r.budgetClaims), len(r.targetActionStates))
	}
}

func TestPriorityTaskBFormalProbeInitialManualZeroSlotsJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s, r, reader, calls := taskBFormalProbeFixture(5)
		var phases []ProbeTargetPhase
		// The service callback proves the initial exclusion happens before an
		// admitted/running phase, rather than merely releasing after probing.
		_, _ = s.ProbeTargetWithProgress(t.Context(), "user1", "sub2api:ws1:a", []string{"gpt-4o"}, func(p ProbeTargetPhase) { phases = append(phases, p) })
		if len(phases) != 0 {
			t.Errorf("initial manual account occupied probe slot: phases=%v", phases)
		}
		rec := taskBFormalRequest(s, stream)
		taskBAssertManualProbeRejected(t, rec, stream, r, reader, calls)
		waitForPriorityAsyncIdle(t)
	}
}

func TestPriorityTaskBFormalProbeQueuedOwnershipChangeJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s, r, reader, calls := taskBFormalProbeFixture(20)
		s.probeLimiter = newProbeConcurrencyLimiter(1, 1)
		release, ok := s.probeLimiter.acquireAutomatic(t.Context(), "user1|ws1")
		if !ok {
			t.Fatal("occupy fixture slot")
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- taskBFormalRequest(s, stream) }()
		waitForProbeManualWaiters(t, s.probeLimiter, "user1|ws1", 1)
		reader.mu.Lock()
		reader.priority = 5
		reader.mu.Unlock()
		release()
		select {
		case rec := <-done:
			taskBAssertManualProbeRejected(t, rec, stream, r, reader, calls)
		case <-time.After(3 * time.Second):
			t.Fatal("queued formal probe did not finish")
		}
		s.probeLimiter.mu.Lock()
		active, waiters := s.probeLimiter.globalActive, s.probeLimiter.globalManualWaiters
		s.probeLimiter.mu.Unlock()
		if active != 0 || waiters != 0 {
			t.Errorf("probe resources leaked active=%d waiters=%d", active, waiters)
		}
		r.actionMu.Lock()
		leases := len(r.actionLeases)
		r.actionMu.Unlock()
		waitForPriorityAsyncIdle(t)
		if leases != 0 {
			t.Errorf("account lease leaked: %d", leases)
		}
	}
}

func TestActionCheckpointTaskBReleaseUncertainOrIncompleteNeverFinishes(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchSending, DispatchUncertain, DispatchConfirmedApplied} {
		for _, mode := range []string{"incomplete", "old-snapshot", "different-value", "matching"} {
			r := newFakeRepository()
			claim := actionClaimFixture(t, r, ActionKindPriority, "priority-release:fixed")
			claim.Priority.PendingPriority = intPointer(1)
			if ok, err := r.ClaimRemoteAction(t.Context(), claim); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if ok, err := r.PermitRemoteAction(t.Context(), claim); !ok || err != nil {
				t.Fatal(ok, err)
			}
			old := time.Now()
			if phase != DispatchSending {
				if err := r.RecordRemoteActionReceipt(t.Context(), claim, phase); err != nil {
					t.Fatal(err)
				}
			}
			r.expireActionLeaseForTest(claim.OwnerID, false)
			obs := RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, Visible: true, InventoryComplete: mode != "incomplete", SnapshotStartedAt: time.Now(), Priority: intPointer(1)}
			if mode == "old-snapshot" {
				obs.SnapshotStartedAt = old.Add(-time.Minute)
			}
			if mode == "different-value" {
				obs.Priority = intPointer(5)
			}
			pair, err := r.ReconcileRemoteAction(context.Background(), obs)
			if err != nil {
				t.Fatal(err)
			}
			shouldEnd := phase == DispatchConfirmedApplied && mode == "matching"
			if shouldEnd {
				if pair.Priority != nil {
					t.Error("confirmed release retained priority record")
				}
			} else if pair.Priority == nil || pair.pendingCount() != 1 || pair.Priority.PendingDispatchID != claim.DispatchID {
				t.Errorf("%s/%s silently finished uncertain/incomplete release: %+v", phase, mode, pair)
			}
		}
	}
}
