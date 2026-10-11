package connection_health

import (
	"fmt"
	"testing"
	"time"
)

func TestModelControlAutoRequestHealthAndManualPresentation(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprintf("currentProbe=%v", current), func(t *testing.T) {
			f := c32AutoFixture(t, true)
			if current {
				f.monitor([]string{"1"}, "A")
				if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET auto_remote_action_enabled=false`); err != nil {
					t.Fatal(err)
				}
			}
			st := defaultTargetState(c32REDUser, c32REDWorkspace, AdminProbeTarget{TargetID: c32REDTarget("1")}, "A")
			st.State = StateSuspended
			if err := f.service.repo.UpsertState(t.Context(), st); err != nil {
				t.Fatal(err)
			}
			writes := f.writeCount()
			before := c32AutoObject(t, f, "1", "A")
			c32DirectScan(t, f)
			after := c32AutoObject(t, f, "1", "A")
			if current {
				if f.writeCount() != writes || after.LastAttempt == nil || after.LastAttempt.ReasonKey != modelControlError("RequestHealthSuspended") || !after.UpdatedAt.Equal(before.UpdatedAt) || c32State(t, f) != "retry" {
					t.Fatal("automatic restore bypassed current request-health pause", after)
				}
			} else if f.writeCount() != writes+1 || len(after.ClosedEntries) != 0 {
				t.Fatal("obsolete request health blocked restore", after)
			}
		})
	}
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("background retains manual-change reason/changed=%v", changed), func(t *testing.T) {
			f := c32AutoFixture(t, true)
			value := "A"
			if changed {
				value = "OTHER"
			}
			f.mapping("1", map[string]string{"a": value, "b": "B"})
			writes := f.writeCount()
			c32DirectScan(t, f)
			item, _, err := f.service.getModelControlItem(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget("1"), "A")
			if err != nil || item.Control.LastAttempt == nil || item.Control.LastAttempt.ReasonKey != modelControlError("AutoManualChange") || item.Auto == nil || item.Auto.State != "waiting" || f.writeCount() != writes {
				t.Fatal("background verify lost manual-change presentation", item, err)
			}
		})
	}
}

func TestModelControlAutoLastModelNeverClosesAccount(t *testing.T) {
	f := c32AutoFixture(t, false)
	f.mapping("1", map[string]string{"a": "A"})
	f.verify("1")
	f.freshRound("1", "A", 1, 2)
	c32DirectScan(t, f)
	item, _, err := f.service.getModelControlItem(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget("1"), "A")
	if err != nil || item.Auto == nil || item.Auto.State != "manual" || item.Auto.ReasonKey != modelControlError("AutoLastModel") || f.writeCount() != 0 || f.scheduleWrites != 0 || f.inactiveCount() != 0 {
		t.Fatal("automatic last model affected entire account", item, err)
	}
}

func TestModelControlAutoPendingAfterDueSelection(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			f := c32AutoFixture(t, true)
			r := f.service.modelControls.(*Repository)
			c32SetAuto(t, r, false, false, true)
			hook := &c32AutoHookRepository{Repository: r}
			f.service.modelControls = hook
			hook.beforeDue = func() {
				if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$1 WHERE target_id=$2`, modelControlPending{ID: "after-due", Operation: "close", StartedAt: time.Now().UTC()}, c32REDTarget("1")); err != nil {
					t.Fatal(err)
				}
				if missing {
					f.mu.Lock()
					delete(f.accounts, "1")
					f.mu.Unlock()
				}
			}
			before := f.detailReads
			c32DirectScan(t, f)
			o := c32AutoObject(t, f, "1", "A")
			if f.detailReads != before || o.Pending == nil || o.Pending.ID != "after-due" || o.AutoVerifyAt == nil {
				t.Fatal("due selection race confirmed pending", o)
			}
		})
	}
}

func TestModelControlAutoOldestAttemptFirst(t *testing.T) {
	f := newC32REDFixture(t)
	f.switches(true, false, false)
	r := f.service.modelControls.(*Repository)
	for id := 10; id < 21; id++ {
		name := fmt.Sprint(id)
		f.mu.Lock()
		f.accounts[name] = c32REDAccount(id, map[string]string{"a": "A", "b": "B"})
		f.mu.Unlock()
		f.manage(name, "A")
		f.freshRound(name, "A", 1, 2)
		o := c32AutoObject(t, f, name, "A")
		if err := r.SaveModelControlAutoState(t.Context(), c32REDUser, c32REDWorkspace, o.ID, &modelControlAutoAttempt{Operation: "close", AttemptAt: time.Now().Add(-time.Duration(id) * time.Minute).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	c32DirectScan(t, f)
	if f.writeCount() != 10 || len(f.closed("10", "A")) != 0 || len(f.closed("20", "A")) != 1 {
		t.Fatal("attempt ordering used target order")
	}
	c32DirectScan(t, f)
	if f.writeCount() != 11 || len(f.closed("10", "A")) != 1 {
		t.Fatal("remaining newest attempt did not rotate")
	}
}
