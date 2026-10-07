package connection_health

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type taskBStorageFaultRepository struct {
	*fakeRepository
	fault string
}

type taskBStartedWriteLeaseRepository struct {
	*fakeRepository
	claim              RemoteActionClaim
	mu                 sync.Mutex
	acquired, released []string
}

func (r *taskBStartedWriteLeaseRepository) ClaimRemoteAction(ctx context.Context, c RemoteActionClaim) (bool, error) {
	r.claim = c
	return r.fakeRepository.ClaimRemoteAction(ctx, c)
}
func (r *taskBStartedWriteLeaseRepository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	h, ok, err := r.fakeRepository.AcquireActionLease(ctx, key, wait)
	if err == nil && ok {
		r.mu.Lock()
		r.acquired = append(r.acquired, key)
		r.mu.Unlock()
		originalRelease := h.release
		h.release = func() { r.mu.Lock(); r.released = append(r.released, key); r.mu.Unlock(); originalRelease() }
	}
	return h, ok, err
}

type taskBStartedWritePlatform struct {
	*taskBAccountSettingsPlatform
	started chan struct{}
	apply   bool
}

func (f *taskBStartedWritePlatform) UpdateAdminTargetPriorityContext(ctx context.Context, _ upstream.Session, _ string, value int) error {
	f.mu.Lock()
	f.priorityWrites = append(f.priorityWrites, value)
	if f.apply {
		f.priority = value
	}
	f.mu.Unlock()
	close(f.started)
	<-ctx.Done()
	return ctx.Err() // An HTTP cancellation after send began cannot prove no effect.
}

func TestPriorityTaskBPageStartedWriteLeaseLossRemainsPendingAndReleasesReverse(t *testing.T) {
	for _, kind := range []string{"workspace", "account", "write"} {
		for _, applied := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-unknown-not-applied", true: "-unknown-applied"}[applied], func(t *testing.T) {
				s, base, platform := taskBAccountAPIFixture(50, 1)
				r := &taskBStartedWriteLeaseRepository{fakeRepository: base}
				s.repo = r
				p := &taskBStartedWritePlatform{taskBAccountSettingsPlatform: platform, started: make(chan struct{}), apply: applied}
				s.priorityActions = p
				responses := make(chan *httptest.ResponseRecorder, 1)
				done := make(chan struct{})
				go func() {
					defer close(done)
					responses <- taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`)
				}()
				t.Cleanup(func() {
					base.actionMu.Lock()
					owners := []string{}
					for _, h := range base.actionLeases {
						owners = append(owners, h.OwnerID)
					}
					base.actionMu.Unlock()
					for _, owner := range owners {
						base.expireActionLeaseForTest(owner, true)
					}
					select {
					case <-done:
					case <-time.After(6 * time.Second):
						t.Error("fixture worker failed to join")
					}
				})
				select {
				case <-p.started:
				case response := <-responses:
					taskBRequireAccountResult(t, response, "pending")
					t.Fatal("write never started")
				case <-time.After(2 * time.Second):
					t.Fatal("write-start barrier not reached")
				}
				owner := ""
				if field := reflect.ValueOf(r.claim).FieldByName("WorkspaceOwnerID"); field.IsValid() && field.Kind() == reflect.String {
					owner = field.String()
				}
				if kind == "account" {
					owner = r.claim.OwnerID
				}
				if kind == "write" {
					owner = r.claim.MutationOwnerID
				}
				if owner == "" {
					t.Fatal("claim lacks actual lease owner")
				}
				base.expireActionLeaseForTest(owner, true)
				var response *httptest.ResponseRecorder
				select {
				case response = <-responses:
				case <-time.After(6 * time.Second):
					t.Fatal("lost lease did not cancel started request")
				}
				<-done
				taskBRequireAccountResult(t, response, "pending")
				state, exists := base.priorityStates["user1|ws1|sub2api:ws1:a"]
				if !exists || state.PendingPriority == nil || state.LastAppliedPriority != 0 || state.OriginalPriority != 50 || state.PendingDispatchPhase != DispatchUncertain {
					t.Fatalf("started request loss invented no-effect/success: %+v", state)
				}
				fresh, err := s.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
				if err != nil {
					t.Fatal(err)
				}
				observed, _ := findActionInventoryTarget("sub2api:ws1:a", *fresh)
				pair, err := s.reconcileActionObservation(t.Context(), targetObservation("user1", "ws1", observed, fresh))
				if err != nil || pair.Priority == nil || pair.Priority.PendingPriority == nil {
					t.Fatalf("uncertain matching readback released claim: %+v %v", pair, err)
				}
				if next := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`); next.Code != 409 {
					t.Fatalf("uncertain started write allowed replay: %d/%s", next.Code, next.Body.String())
				}
				if len(platform.priorityWrites) != 1 || len(base.states) != 0 || platform.credentials != 0 {
					t.Fatal("unknown started write repeated or triggered probe")
				}
				r.mu.Lock()
				acquired := append([]string(nil), r.acquired[:3]...)
				released := append([]string(nil), r.released[:3]...)
				r.mu.Unlock()
				if !strings.HasPrefix(acquired[0], "connection-health:priority-sync:") || acquired[1] != "connection-health:target:sub2api:ws1:a" {
					t.Fatalf("lease acquisition order changed: %v", acquired)
				}
				want := []string{acquired[2], acquired[1], acquired[0]}
				if !reflect.DeepEqual(released, want) {
					t.Fatalf("lease release order=%v want=%v", released, want)
				}
				base.actionMu.Lock()
				remaining := len(base.actionLeases)
				base.actionMu.Unlock()
				if remaining != 0 {
					t.Fatalf("leaked leases=%d", remaining)
				}
			})
		}
	}
}

func (r *taskBStorageFaultRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	ok, err := r.fakeRepository.ClaimRemoteAction(ctx, claim)
	if err == nil && ok && r.fault == "claim-unknown" {
		return false, errors.New("fixture claim commit outcome unknown")
	}
	return ok, err
}
func (r *taskBStorageFaultRepository) RecordRemoteActionReceipt(ctx context.Context, claim RemoteActionClaim, phase RemoteDispatchPhase) error {
	if r.fault == "receipt-before-store" {
		return errors.New("fixture receipt persistence failed")
	}
	err := r.fakeRepository.RecordRemoteActionReceipt(ctx, claim, phase)
	if err == nil && r.fault == "receipt-after-store" {
		return errors.New("fixture receipt commit outcome unknown")
	}
	return err
}

func TestPriorityTaskBPageStorageUnknownNeverInventsConfirmation(t *testing.T) {
	for _, fault := range []string{"claim-unknown", "receipt-before-store", "receipt-after-store"} {
		t.Run(fault, func(t *testing.T) {
			s, base, platform := taskBAccountAPIFixture(50, 1)
			s.repo = &taskBStorageFaultRepository{fakeRepository: base, fault: fault}
			taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), "pending")
			state, exists := base.priorityStates["user1|ws1|sub2api:ws1:a"]
			if !exists || state.PendingPriority == nil || *state.PendingPriority != 5 || state.LastAppliedPriority != 0 || state.OriginalPriority != 50 || !strings.HasPrefix(state.PendingDispatchID, "priority-release:") {
				t.Fatalf("storage uncertainty lost pending/original evidence: %+v", state)
			}
			wantWrites := 1
			if fault == "claim-unknown" {
				wantWrites = 0
			}
			if len(platform.priorityWrites) != wantWrites {
				t.Fatalf("unexpected writes: %v", platform.priorityWrites)
			}
			if len(base.states) != 0 || platform.credentials != 0 {
				t.Fatal("page storage uncertainty triggered probe/health write")
			}
			if fault == "claim-unknown" {
				return
			} // A persisted prepared claim remains pending at the response boundary.
			fresh, err := s.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
			if err != nil {
				t.Fatal(err)
			}
			observed, _ := findActionInventoryTarget("sub2api:ws1:a", *fresh)
			pair, err := s.reconcileActionObservation(t.Context(), targetObservation("user1", "ws1", observed, fresh))
			if err != nil {
				t.Fatal(err)
			}
			if fault == "receipt-after-store" {
				if pair.Priority != nil {
					t.Fatalf("durable success plus later full matching readback failed to release: %+v", pair.Priority)
				}
			} else {
				if pair.Priority == nil || pair.Priority.PendingPriority == nil || pair.Priority.LastAppliedPriority != 0 {
					t.Fatalf("matching value invented successful receipt: %+v", pair.Priority)
				}
				rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`)
				if rec.Code != 409 {
					t.Fatalf("pending unknown receipt permitted retry: %d/%s", rec.Code, rec.Body.String())
				}
			}
			if len(platform.priorityWrites) != 1 {
				t.Fatalf("uncertain receipt replayed remote mutation: %v", platform.priorityWrites)
			}
		})
	}
}
