package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestModelControlCommitProofRequiresCompleteAccountAndIndependentOwnership(t *testing.T) {
	a := modelControlTarget{ID: "a", TargetID: "account", Version: 2, ClosedEntries: map[string]string{"a": "A"}}
	b := modelControlTarget{ID: "b", TargetID: "account", Version: 3, ClosedEntries: map[string]string{}}
	proof := []modelControlCommitTarget{}
	for _, object := range []modelControlTarget{a, b} {
		encoded, err := json.Marshal(modelControlOwnershipSnapshot(object))
		if err != nil {
			t.Fatal(err)
		}
		proof = append(proof, modelControlCommitTarget{ID: object.ID, Version: object.Version, Ownership: string(encoded)})
	}
	if !modelControlCommitMatches(proof, []modelControlTarget{b, a}, "account") {
		t.Fatal("same account state depended on result order")
	}
	a.Observation.State = "serving"
	a.LastAttempt = &modelControlAttempt{ReasonKey: "new observation"}
	if !modelControlCommitMatches(proof, []modelControlTarget{a, b}, "account") {
		t.Fatal("independent observation invalidated ownership proof")
	}
	a.ClosedEntries["a"] = "changed"
	if modelControlCommitMatches(proof, []modelControlTarget{a, b}, "account") {
		t.Fatal("serialized proof shared a mutable ownership map")
	}
	a.ClosedEntries["a"] = "A"
	for _, fresh := range [][]modelControlTarget{{a}, {a, b, {ID: "extra", TargetID: "account"}}, {{ID: a.ID, TargetID: a.TargetID, Version: 1, ClosedEntries: a.ClosedEntries}, b}} {
		if modelControlCommitMatches(proof, fresh, "account") {
			t.Fatal("missing, additional, or stale account object proved a commit")
		}
	}
	proof[0] = modelControlCommitTarget{ID: a.ID, Deleted: true}
	if !modelControlCommitMatches(proof, []modelControlTarget{b}, "account") || modelControlCommitMatches(proof, []modelControlTarget{a, b}, "account") {
		t.Fatal("deletion proof did not require the deleted object to be absent")
	}
}

func TestModelControlVerifyCommitProofCoversEveryManagedModel(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			f.close("1", "A")
			f.mapping("1", map[string]string{"b": "B", "c": "C"})
			f.round("1", "B", 1, 2, 0)
			f.manage("1", "B")
			f.close("1", "B")
			f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B", "c": "C"})
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
				if committed {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				} else if err := tx.Rollback(ctx); err != nil {
					return err
				}
				return errors.New("fixture account-wide verify confirmation loss")
			})
			result := f.verify("1")
			failures := result["errors"].([]any)
			if committed {
				if len(failures) != 0 || len(f.closed("1", "A"))+len(f.closed("1", "B")) != 0 || f.eventCount("manual_takeover") != 2 {
					t.Fatal("committed whole-account attribution was not proved")
				}
			} else if len(failures) != 1 || failures[0].(map[string]any)["reasonKey"] != modelControlError("Storage") || len(f.closed("1", "A")) != 2 || len(f.closed("1", "B")) != 1 || f.eventCount("manual_takeover") != 0 {
				t.Fatal("rolled back whole-account attribution was reported as committed")
			}
			if f.writeCount() != 2 {
				t.Fatal("commit confirmation repeated a remote write")
			}
		})
	}
}

func TestModelControlSendingAndMissingRecoveryHaveIndependentStorageBudget(t *testing.T) {
	for _, stage := range []string{"sending", "missing"} {
		t.Run(stage, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			if stage == "missing" {
				f.mu.Lock()
				delete(f.accounts, "1")
				f.mu.Unlock()
			}
			storage := &c3ReviewBlockedStorage{modelControlRepository: f.service.modelControls, armed: &atomic.Bool{}}
			f.service.modelControls = storage
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, current string) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if current == stage || stage == "missing" && current == "finalize" {
					storage.armed.Store(true)
					return errors.New("fixture blocked confirmation storage")
				}
				return nil
			})
			started := time.Now()
			code, result := f.execute("1", "A", "close", p)
			if code != 400 || !strings.Contains(fmt.Sprint(result), "StorageUnknown") || !storage.bounded.Load() || time.Since(started) > 5750*time.Millisecond || f.writeCount() != 0 {
				t.Fatalf("storage recovery was unbounded or promised state: code=%d result=%v", code, result)
			}
			control := f.item("1", "A")["control"].(map[string]any)
			if stage == "sending" {
				if control["pending"] == nil || control["pending"].(map[string]any)["phase"] != "sending" {
					t.Fatal("bounded recovery changed the committed sending state")
				}
			} else if control["observation"].(map[string]any)["state"] != "account_missing" {
				t.Fatal("bounded recovery changed the committed missing state")
			}
			_, release, acquired, err := f.service.acquireActionTargetLease(t.Context(), c3REDTarget("1"), false)
			if err != nil || !acquired {
				t.Fatal("storage timeout leaked the account lease")
			}
			release()
		})
	}
}
