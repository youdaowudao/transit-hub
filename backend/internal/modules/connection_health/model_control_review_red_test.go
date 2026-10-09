package connection_health

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Review regressions use the same real storage and HTTP business boundary as
// the original RED suite. Their expectations are owned by the main agent.
func TestC3ModelControlReviewREDAccountPendingPreventsOtherModelRemoval(t *testing.T) {
	for _, abandon := range []bool{false, true} {
		t.Run(fmt.Sprint(abandon), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.round("1", "B", 3, 0, 0)
			f.manage("1", "A")
			f.manage("1", "B")
			f.bulkCode = 502
			f.close("1", "A")
			b := f.item("1", "B")
			if b["control"].(map[string]any)["accountPending"] == nil {
				t.Fatal("fixture did not retain A's account-wide pending")
			}
			code, result := f.request("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "B", "expectedVersion": b["version"], "abandonClosed": abandon})
			if code != 409 {
				t.Fatalf("another model's pending allowed removal: code=%d result=%v", code, result)
			}
			if !reflect.DeepEqual(f.item("1", "B"), b) || f.item("1", "A")["control"].(map[string]any)["pending"] == nil || f.eventCount("managed_removed")+f.eventCount("managed_abandoned") != 0 || f.writeCount() != 1 {
				t.Fatal("rejected removal changed objects, pending, archived events, or remote writes")
			}
		})
	}
}

type c3ReviewOnceReadFailure struct {
	modelControlRepository
	armed *atomic.Bool
}

func (r c3ReviewOnceReadFailure) ListModelControlTargets(ctx context.Context, user, workspace string) ([]modelControlTarget, error) {
	if r.armed.CompareAndSwap(true, false) {
		return nil, errors.New("fixture one failed commit confirmation read")
	}
	return r.modelControlRepository.ListModelControlTargets(ctx, user, workspace)
}

func TestC3ModelControlReviewREDNoPendingVerifyRequiresActualCommitProof(t *testing.T) {
	for _, change := range []string{"human_restore", "late_close"} {
		for _, commit := range []string{"committed", "rolled_back", "read_failed"} {
			t.Run(change+"/"+commit, func(t *testing.T) {
				f := newC3REDFixture(t)
				f.round("1", "A", 1, 2, 0)
				f.manage("1", "A")
				if change == "human_restore" {
					f.close("1", "A")
					f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B"})
				} else {
					f.bulkCode = 502
					f.close("1", "A")
					f.agePending("1", "A", 31)
					f.verify("1")
					f.mapping("1", map[string]string{"b": "B"})
				}
				if f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
					t.Fatal("fixture unexpectedly has a pending marker")
				}
				armed := &atomic.Bool{}
				f.service.modelControls = c3ReviewOnceReadFailure{f.service.modelControls, armed}
				injected := false
				f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
					if stage != "finalize" || injected {
						return tx.Commit(ctx)
					}
					injected = true
					if commit == "rolled_back" {
						if err := tx.Rollback(ctx); err != nil {
							return err
						}
					} else if err := tx.Commit(ctx); err != nil {
						return err
					}
					armed.Store(commit == "read_failed")
					return errors.New("fixture verify acknowledgement loss")
				})
				result := f.verify("1")
				if !injected {
					t.Fatal("verify transaction was not intercepted")
				}
				failures := result["errors"].([]any)
				if commit == "committed" {
					if len(failures) != 0 {
						t.Fatalf("actual commit not recognized: %v", failures)
					}
				} else {
					want := modelControlError("Storage")
					if commit == "read_failed" {
						want = modelControlError("StorageUnknown")
					}
					if len(failures) != 1 || failures[0].(map[string]any)["reasonKey"] != want {
						t.Fatalf("unproven verify reported success: %v, want %s", failures, want)
					}
				}
				wantClosed, wantEvents := 0, 1
				event := "manual_takeover"
				if change == "late_close" {
					wantClosed, event = 2, "close_late_applied"
				}
				if commit == "rolled_back" {
					wantClosed, wantEvents = 2-wantClosed, 0
				}
				if len(f.closed("1", "A")) != wantClosed || f.eventCount(event) != wantEvents || f.writeCount() != 1 {
					t.Fatal("verify response and actual ownership/event commit diverged, or verify wrote upstream")
				}
			})
		}
	}
}

type c3ReviewBlockedStorage struct {
	modelControlRepository
	armed   *atomic.Bool
	event   bool
	bounded atomic.Bool
}

func (r *c3ReviewBlockedStorage) wait(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 5*time.Second && ctx.Err() == nil {
		r.bounded.Store(true)
	}
	// Simulate an occupied pool. A deadline releases it; an unbounded read
	// receives a late successful answer after the permitted recovery budget.
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *c3ReviewBlockedStorage) ListModelControlTargets(ctx context.Context, user, workspace string) ([]modelControlTarget, error) {
	if !r.event && r.armed.CompareAndSwap(true, false) {
		if err := r.wait(ctx); err != nil {
			return nil, err
		}
	}
	return r.modelControlRepository.ListModelControlTargets(ctx, user, workspace)
}

func (r *c3ReviewBlockedStorage) InsertModelControlEvent(ctx context.Context, event ModelControlEvent) error {
	if r.event && event.EventType == "close_unknown" && r.armed.CompareAndSwap(true, false) {
		if err := r.wait(ctx); err != nil {
			return err
		}
	}
	return r.modelControlRepository.InsertModelControlEvent(ctx, event)
}

func TestC3ModelControlReviewREDBoundedStorageRecoveryAndFinalResponse(t *testing.T) {
	for _, path := range []string{"prepared", "finalize", "no_remote_restore", "remove", "verify", "final_response", "unknown_event"} {
		t.Run(path, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			op := "close"
			if path == "no_remote_restore" {
				f.close("1", "A")
				f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B"})
				op = "restore"
			}
			p := f.preview("1", "A", op)
			storage := &c3ReviewBlockedStorage{modelControlRepository: f.service.modelControls, armed: &atomic.Bool{}, event: path == "unknown_event"}
			f.service.modelControls = storage
			stage := path
			if path == "no_remote_restore" || path == "verify" || path == "final_response" {
				stage = "finalize"
			}
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, current string) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if current == stage {
					storage.armed.Store(true)
					if path != "final_response" {
						return errors.New("fixture acknowledgement loss with blocked storage")
					}
				}
				return nil
			})
			if path == "unknown_event" {
				f.bulkCode = 502
				storage.armed.Store(true)
			}
			started := time.Now()
			var code int
			var result map[string]any
			switch path {
			case "remove":
				code, result = f.request("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": f.item("1", "A")["version"]})
			case "verify":
				code, result = f.request("POST", "model-control/verify", map[string]any{"targetIds": []string{c3REDTarget("1")}})
			default:
				code, result = f.execute("1", "A", op, p)
			}
			if !storage.bounded.Load() || time.Since(started) > 5750*time.Millisecond {
				t.Errorf("post-commit/event storage was not bounded to independent 5-second recovery (elapsed %s)", time.Since(started))
			}
			if path == "unknown_event" {
				if code != 200 || result["outcome"] != "unknown" || f.writeCount() != 1 {
					t.Fatalf("event timeout changed remote uncertainty: %d %v", code, result)
				}
			} else if path == "verify" {
				failures, _ := result["errors"].([]any)
				if code != 200 || len(failures) != 1 || failures[0].(map[string]any)["reasonKey"] != modelControlError("StorageUnknown") {
					t.Fatalf("verify timeout promised storage state: %d %v", code, result)
				}
			} else if code != 400 || !strings.Contains(fmt.Sprint(result), "StorageUnknown") {
				t.Fatalf("recovery timeout promised success or storage state: %d %v", code, result)
			}
			if path == "prepared" && f.writeCount() != 0 {
				t.Fatal("confirmation timeout allowed sending")
			}
		})
	}
}
