package connection_health

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

func TestModelControlPreparationBudgetNeverSendsAndClearsPrepared(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if stage == "prepared" {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("preparation context lost its deadline")
			}
			timer := time.NewTimer(time.Until(deadline) - 9*time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("budget fixture exceeded preparation deadline")
			}
		}
		return nil
	})
	p := f.preview("1", "A", "close")
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(result["error"].(string), "PreparationTimeout") || f.writeCount() != 0 {
		t.Fatalf("budget exhausted code=%d result=%v writes=%d", code, result, f.writeCount())
	}
	if control := f.item("1", "A")["control"].(map[string]any); control["pending"] != nil || control["unconfirmedClose"] != nil {
		t.Fatal("never-sent prepared record was not cleared")
	}
}
func TestModelControlAddReadFailurePreservesManagedObject(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.failDetail = true
	item := f.manage("1", "A")
	if item["verifyErrorKey"] == nil || item["control"].(map[string]any)["observation"].(map[string]any)["state"] != "unverified" {
		t.Fatal("failed initial verify discarded object or claimed verified")
	}
	f.failDetail = false
	f.verify("1")
	if f.item("1", "A")["control"].(map[string]any)["observation"].(map[string]any)["state"] != "serving" {
		t.Fatal("manual retry did not verify object")
	}
}
func TestModelControlRestoreWhileAccountPausedPreservesSwitchAndHints(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.close("1", "A")
	f.change("1", func(a map[string]any) { a["schedulable"] = false })
	p := f.preview("1", "A", "restore")
	code, result := f.execute("1", "A", "restore", p)
	if code != 200 || result["outcome"] != "restored" || f.scheduleWrites != 0 || !strings.Contains(strings.TrimSpace(strings.Join(anyStrings(result["hintKeys"].([]any)), ",")), "AccountUnschedulable") {
		t.Fatalf("paused-account restore=%v", result)
	}
	if f.item("1", "A")["control"].(map[string]any)["observation"].(map[string]any)["accountSchedulable"] != false {
		t.Fatal("restore reopened account scheduling")
	}
}
func anyStrings(values []any) []string {
	r := []string{}
	for _, v := range values {
		if s, ok := v.(string); ok {
			r = append(r, s)
		}
	}
	return r
}
func TestModelControlVerifyBusyLeaseAndCompleteDeduplicatedTargets(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.round("1", "B", 1, 2, 0)
	f.manage("1", "A")
	f.manage("1", "B")
	result := f.expect("GET", "model-control/verify-targets", nil, 200)
	ids := result["targetIds"].([]any)
	if len(ids) != 1 {
		t.Fatal("same account duplicated in verify list")
	}
	_, release, acquired, err := f.service.acquireActionTargetLease(t.Context(), c3REDTarget("1"), false)
	if err != nil || !acquired {
		t.Fatal(err)
	}
	defer release()
	verify := f.verify("1")
	errors := verify["errors"].([]any)
	if len(errors) != 1 || !strings.Contains(errors[0].(map[string]any)["reasonKey"].(string), "Processing") {
		t.Fatal("busy account verification did not report processing")
	}
	if f.writeCount() != 0 {
		t.Fatal("verification wrote main site")
	}
}

func TestModelControlMissingAccountRequiresBothProofsInEveryWriteEntry(t *testing.T) {
	for _, operation := range []string{"preview", "close_account"} {
		for _, incomplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/incomplete=%t", operation, incomplete), func(t *testing.T) {
				f := newC3REDFixture(t)
				f.mapping("1", map[string]string{"a": "A"})
				f.round("1", "A", 1, 2, 0)
				f.manage("1", "A")
				p := f.preview("1", "A", "close_account")
				f.mu.Lock()
				delete(f.accounts, "1")
				f.failInventory = incomplete
				f.mu.Unlock()
				var code int
				var result map[string]any
				if operation == "preview" {
					code, result = f.request("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "operation": "close_account", "basis": p["item"].(map[string]any)["basis"]})
				} else {
					code, result = f.execute("1", "A", "close_account", p)
				}
				state := f.item("1", "A")["control"].(map[string]any)["observation"].(map[string]any)["state"]
				if incomplete {
					if code != 400 || !strings.Contains(fmt.Sprint(result), "AccountReadFailed") || state == "account_missing" || f.eventCount("account_missing_resolved") != 0 {
						t.Fatalf("single proof was treated as deletion: code=%d result=%v state=%v", code, result, state)
					}
				} else if code != 200 || !strings.Contains(fmt.Sprint(result), "AccountMissing") || state != "account_missing" || f.eventCount("account_missing_resolved") != 1 {
					t.Fatalf("confirmed deletion not persisted: code=%d result=%v state=%v", code, result, state)
				}
				if f.writeCount() != 0 || f.scheduleWrites != 0 {
					t.Fatal("missing-account path sent a remote write")
				}
			})
		}
	}
}

func TestModelControlUnavailableTargetSkipsOnlySourceFloor(t *testing.T) {
	for _, restriction := range []string{"schedulable", "status"} {
		t.Run(restriction, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.mapping("2", map[string]string{"b": "B"})
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			f.change("1", func(a map[string]any) {
				if restriction == "schedulable" {
					a["schedulable"] = false
				} else {
					a["status"] = "disabled"
				}
			})
			p := f.preview("1", "A", "close")
			if p["blockReasonKey"] != "" || len(p["groups"].([]any)) == 0 || p["groups"].([]any)[0].(map[string]any)["count"] != float64(0) {
				t.Fatalf("unavailable target incorrectly required replacement: %v", p)
			}
			code, result := f.execute("1", "A", "close", p)
			if code != 200 || result["outcome"] != "closed" || f.writeCount() != 1 || f.scheduleWrites != 0 {
				t.Fatalf("unavailable target close failed: %d %v", code, result)
			}
		})
	}
}
