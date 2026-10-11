package connection_health

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

// Hooks stop at existing repository boundaries and still execute the real PG
// transaction, locks, guards, observation writes and commit implementation.
type c32AutoHookRepository struct {
	*Repository
	beforeMutate                          func(string)
	afterMutate                           func(string)
	beforeList                            func()
	beforeSettings                        func()
	beforeDue                             func()
	failSettings, failClosed, failTargets bool
}

func (r *c32AutoHookRepository) mutateModelControlOwnership(ctx context.Context, user, ws, target string, mutation bool, stage string, fn func([]*modelControlTarget, time.Time) ([]ModelControlEvent, error), guards ...modelControlTxGuard) error {
	if r.beforeMutate != nil {
		r.beforeMutate(stage)
	}
	err := r.Repository.mutateModelControlOwnership(ctx, user, ws, target, mutation, stage, fn, guards...)
	if err == nil && r.afterMutate != nil {
		r.afterMutate(stage)
	}
	return err
}
func (r *c32AutoHookRepository) ListModelControlTargets(ctx context.Context, user, ws string) ([]modelControlTarget, error) {
	if r.beforeList != nil {
		r.beforeList()
	}
	if r.failTargets {
		return nil, errors.New("fixture storage unavailable")
	}
	return r.Repository.ListModelControlTargets(ctx, user, ws)
}
func (r *c32AutoHookRepository) GetModelControlAutoSettings(ctx context.Context, user, ws string) (ModelControlAutoSettings, error) {
	if r.beforeSettings != nil {
		r.beforeSettings()
	}
	if r.failSettings {
		return ModelControlAutoSettings{}, errors.New("fixture settings unavailable")
	}
	return r.Repository.GetModelControlAutoSettings(ctx, user, ws)
}
func (r *c32AutoHookRepository) ListModelControlFullyClosed(ctx context.Context, user, ws string) (map[string]map[string]struct{}, error) {
	if r.failClosed {
		return nil, errors.New("fixture closed set unavailable")
	}
	return r.Repository.ListModelControlFullyClosed(ctx, user, ws)
}
func (r *c32AutoHookRepository) ListModelControlVerifyDue(ctx context.Context, user, ws string, interval int, now time.Time, limit int) ([]string, error) {
	ids, err := r.Repository.ListModelControlVerifyDue(ctx, user, ws, interval, now, limit)
	if r.beforeDue != nil {
		r.beforeDue()
	}
	return ids, err
}
func c32AutoObject(t *testing.T, f *c32REDFixture, id, model string) modelControlTarget {
	t.Helper()
	objects, err := f.service.modelControls.ListModelControlTargets(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o.TargetID == c32REDTarget(id) && o.ModelName == model {
			return o
		}
	}
	t.Fatal("object missing")
	return modelControlTarget{}
}
func c32Candidate(t *testing.T, f *c32REDFixture, id, model, op string) modelControlAutoCandidate {
	t.Helper()
	item, _, err := f.service.getModelControlItem(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget(id), model)
	if err != nil {
		t.Fatal(err)
	}
	return modelControlAutoCandidate{TargetID: item.TargetID, ModelName: model, Operation: op, Basis: item.Basis, EvidenceAt: modelControlItemEvidenceAt(item)}
}
func c32Attempt(t *testing.T, f *c32REDFixture, c modelControlAutoCandidate) {
	t.Helper()
	o := c32AutoObject(t, f, "1", "A")
	if err := f.service.modelControls.(modelControlAutoRepository).SaveModelControlAutoState(t.Context(), c32REDUser, c32REDWorkspace, o.ID, &modelControlAutoAttempt{Operation: c.Operation, Basis: autoBasis(c.Basis, c.EvidenceAt), AttemptAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
func c32Run(t *testing.T, f *c32REDFixture, c modelControlAutoCandidate) string {
	t.Helper()
	return f.service.executeModelControlAuto(t.Context(), c32REDUser, c32REDWorkspace, f.session, c)
}
func c32DirectScan(t *testing.T, f *c32REDFixture) {
	t.Helper()
	f.service.runModelControlAutoScan(t.Context(), c32REDUser, c32REDWorkspace)
}
func c32AutoFixture(t *testing.T, restore bool) *c32REDFixture {
	t.Helper()
	f := newC32REDFixture(t)
	f.manage("1", "A")
	if restore {
		f.freshRound("1", "A", 1, 2)
		f.close("1", "A")
	}
	f.switches(!restore, restore, false)
	if restore {
		f.freshRound("1", "A", 3, 0)
	} else {
		f.freshRound("1", "A", 1, 2)
	}
	return f
}
func c32SetAuto(t *testing.T, r *Repository, close, restore, health bool) ModelControlAutoSettings {
	t.Helper()
	a, err := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	a.AutoCloseEnabled, a.AutoRestoreEnabled, a.HealthCoordinationEnabled = close, restore, health
	a, err = r.SaveModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace, a, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func c32State(t *testing.T, f *c32REDFixture) string {
	t.Helper()
	item, _, err := f.service.getModelControlItem(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget("1"), "A")
	if err != nil {
		t.Fatal(err)
	}
	if item.Auto == nil {
		return "off"
	}
	return item.Auto.State
}

func TestModelControlAutoSettingsStorageAndHTTP(t *testing.T) {
	f := newC32REDFixture(t)
	r := f.service.modelControls.(*Repository)
	a, err := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil || !reflect.DeepEqual(a, defaultModelControlAutoSettings()) {
		t.Fatalf("defaults %+v/%v", a, err)
	}
	for _, input := range []map[string]any{{"unknown": true}, {"autoCloseEnabled": true, "autoRestoreEnabled": false, "healthCoordinationEnabled": false, "verifyIntervalMinutes": 4, "retryIntervalMinutes": 5, "expectedVersion": 0}, {"autoCloseEnabled": false, "autoRestoreEnabled": false, "healthCoordinationEnabled": false, "verifyIntervalMinutes": 61, "retryIntervalMinutes": 5, "expectedVersion": 0}, {"autoCloseEnabled": false, "autoRestoreEnabled": false, "healthCoordinationEnabled": false, "verifyIntervalMinutes": 10, "retryIntervalMinutes": 0, "expectedVersion": 0}, {"autoCloseEnabled": false, "autoRestoreEnabled": false, "healthCoordinationEnabled": false, "verifyIntervalMinutes": 10, "retryIntervalMinutes": 61, "expectedVersion": 0}, {"autoCloseEnabled": false, "autoRestoreEnabled": false, "healthCoordinationEnabled": false, "verifyIntervalMinutes": 10, "retryIntervalMinutes": 5, "expectedVersion": -1}} {
		if code, _ := f.request("PUT", "model-control/auto-settings", input); code != 400 {
			t.Fatal("invalid settings accepted", code, input)
		}
	}
	f.manage("1", "A")
	f.freshRound("1", "A", 1, 2)
	preview := f.preview("1", "A", "close")
	a = c32SetAuto(t, r, true, true, true)
	if a.AutoCloseEnabledAt == nil || a.AutoRestoreEnabledAt == nil || a.Version != 1 {
		t.Fatal(a)
	}
	firstClose, firstRestore := *a.AutoCloseEnabledAt, *a.AutoRestoreEnabledAt
	same := c32SetAuto(t, r, true, true, false)
	if !same.AutoCloseEnabledAt.Equal(firstClose) || !same.AutoRestoreEnabledAt.Equal(firstRestore) {
		t.Fatal("unchanged settings moved enable time")
	}
	_, err = r.SaveModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace, a, a.Version)
	var conflict *ModelControlConflictError
	if !errors.As(err, &conflict) || conflict.Key != modelControlError("VersionConflict") || conflict.Current.(ModelControlAutoSettings).Version != same.Version {
		t.Fatal("conflict not current", err)
	}
	if code, result := f.execute("1", "A", "close", preview); code != 200 || result["outcome"] != "closed" {
		t.Fatal("auto save invalidated manual preview", code, result)
	}
	off := c32SetAuto(t, r, false, false, false)
	if off.AutoCloseEnabledAt != nil || off.AutoRestoreEnabledAt != nil {
		t.Fatal("disable time not cleared")
	}
	enabled := c32SetAuto(t, r, true, false, false)
	if !enabled.AutoCloseEnabledAt.After(firstClose) {
		t.Fatal("reenable time not advanced")
	}
	workspaces, err := r.ListModelControlAutoWorkspaces(t.Context())
	if err != nil || len(workspaces) != 1 {
		t.Fatal(workspaces, err)
	}
	c32SetAuto(t, r, false, false, false)
	workspaces, err = r.ListModelControlAutoWorkspaces(t.Context())
	if err != nil || len(workspaces) != 0 {
		t.Fatal("disabled listed", workspaces, err)
	}
	if f.eventCount("auto_settings_saved") != 5 {
		t.Fatal("settings events missing")
	}
}
func TestModelControlAutoAuxiliaryWritesPreserveOwnership(t *testing.T) {
	f := newC32REDFixture(t)
	f.manage("1", "A")
	r := f.service.modelControls.(*Repository)
	before := c32AutoObject(t, f, "1", "A")
	f.switches(true, false, false)
	a, _ := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
	for n := 0; n < 3; n++ {
		at := time.Now().UTC()
		if err := r.SaveModelControlAutoState(t.Context(), c32REDUser, c32REDWorkspace, before.ID, &modelControlAutoAttempt{Operation: "close", AttemptAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkModelControlAutoVerify(t.Context(), c32REDUser, c32REDWorkspace, before.TargetID, &at); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkModelControlAutoScan(t.Context(), c32REDUser, c32REDWorkspace, &at, &at, ""); err != nil {
			t.Fatal(err)
		}
	}
	after := c32AutoObject(t, f, "1", "A")
	saved, _ := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
	if !before.UpdatedAt.Equal(after.UpdatedAt) || before.Version != after.Version || a.Version != saved.Version {
		t.Fatal("auxiliary write changed ownership/version")
	}
	f.verify("1")
	observed := c32AutoObject(t, f, "1", "A")
	if !observed.UpdatedAt.Equal(before.UpdatedAt) || observed.Version != before.Version || !observed.AutoVerifyAt.Equal(*after.AutoVerifyAt) {
		t.Fatal("manual verification changed timestamps without ownership change")
	}
	f.freshRound("1", "A", 1, 2)
	stages := map[string]modelControlTarget{}
	hook := &c32AutoHookRepository{Repository: r}
	hook.afterMutate = func(stage string) { stages[stage] = c32AutoObject(t, f, "1", "A") }
	f.service.modelControls = hook
	f.close("1", "A")
	previous := observed
	for _, stage := range []string{"prepared", "sending", "finalize"} {
		next, ok := stages[stage]
		if !ok || !next.UpdatedAt.After(previous.UpdatedAt) || next.Version <= previous.Version {
			t.Fatalf("%s did not advance ownership: before=%+v after=%+v", stage, previous, next)
		}
		previous = next
	}
	closed := c32AutoObject(t, f, "1", "A")
	if !closed.UpdatedAt.After(before.UpdatedAt) || closed.Version < before.Version+3 {
		t.Fatal("prepare/send/finalize did not each change ownership")
	}
	f.verify("1")
	unchanged := c32AutoObject(t, f, "1", "A")
	if !unchanged.UpdatedAt.Equal(closed.UpdatedAt) || unchanged.Version != closed.Version {
		t.Fatal("unchanged upstream shifted ownership")
	}
	f.mapping("1", map[string]string{"a": "A", "b": "B"})
	f.verify("1")
	changed := c32AutoObject(t, f, "1", "A")
	if !changed.UpdatedAt.After(closed.UpdatedAt) || changed.Version <= closed.Version {
		t.Fatal("upstream change not recorded")
	}
}
func TestModelControlAutoRoundUsesRejudgmentTime(t *testing.T) {
	f := newC32REDFixture(t)
	f.manage("1", "A")
	at := time.Now().Add(-time.Minute).UTC()
	batch := f.roundAt("1", "A", 1, 2, 0, at, at)
	f.switches(true, false, false)
	if c32State(t, f) != "waiting" {
		t.Fatal("old round should wait")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET updated_at=clock_timestamp() WHERE batch_id=$1`, batch); err != nil {
		t.Fatal(err)
	}
	if c32State(t, f) != "eligible" {
		t.Fatal("rejudgment did not update evidence")
	}
}
func TestModelControlAutoLockedChecksAndFinalTransaction(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, change := range []string{"before switch", "before rule", "before ownership", "before pending", "second switch", "second rule", "second ownership", "second pending", "final off-on", "final interval"} {
			t.Run(fmt.Sprintf("restore=%v/%s", restore, change), func(t *testing.T) {
				f := c32AutoFixture(t, restore)
				r := f.service.modelControls.(*Repository)
				op := "close"
				if restore {
					op = "restore"
				}
				candidate := c32Candidate(t, f, "1", "A", op)
				writes := f.writeCount()
				hook := &c32AutoHookRepository{Repository: r}
				f.service.modelControls = hook
				apply := func() {
					switch change {
					case "before switch", "second switch":
						c32SetAuto(t, r, false, false, false)
					case "before rule", "second rule":
						settings, _ := r.GetModelControlSettings(t.Context(), c32REDUser, c32REDWorkspace)
						if _, err := r.SaveModelControlSettings(t.Context(), c32REDUser, c32REDWorkspace, settings, settings.Version); err != nil {
							t.Fatal(err)
						}
					case "before ownership", "second ownership":
						if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET version=version+1,updated_at=clock_timestamp() WHERE id=$1`, c32AutoObject(t, f, "1", "A").ID); err != nil {
							t.Fatal(err)
						}
					case "before pending", "second pending":
						f.manage("1", "B")
						if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$1 WHERE model_name='B'`, modelControlPending{ID: "fixture-pending", Operation: "close", StartedAt: time.Now().UTC()}); err != nil {
							t.Fatal(err)
						}
					case "final off-on":
						c32SetAuto(t, r, false, false, false)
						c32SetAuto(t, r, !restore, restore, false)
					case "final interval":
						settings, _ := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
						settings.RetryIntervalMinutes++
						if _, err := r.SaveModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace, settings, settings.Version); err != nil {
							t.Fatal(err)
						}
					}
				}
				if change[:6] == "before" {
					apply()
				} else if change[:6] == "second" {
					f.beforeDetail = func() { f.beforeDetail = nil; apply() }
				} else {
					hook.beforeMutate = func(stage string) {
						if stage == "prepared" {
							hook.beforeMutate = nil
							apply()
						}
					}
				}
				reason := c32Run(t, f, candidate)
				if f.writeCount() != writes {
					t.Fatal("changed input wrote main site", reason)
				}
				if change[:5] == "final" && reason != modelControlError("AutoDisabled") {
					t.Fatal("final version did not reject", reason)
				}
				if o := c32AutoObject(t, f, "1", "A"); o.Pending != nil {
					t.Fatal("abandoned attempt persisted pending")
				}
			})
		}
	}
}

func TestModelControlAutoRestoreManualChangesWaitForFreshRound(t *testing.T) {
	for _, mode := range []string{"partial restored", "partial changed", "all restored", "all changed"} {
		t.Run(mode, func(t *testing.T) {
			f := c32AutoFixture(t, true)
			r := f.service.modelControls.(*Repository)
			candidate := c32Candidate(t, f, "1", "A", "restore")
			before := c32AutoObject(t, f, "1", "A")
			writes := f.writeCount()
			mapping := map[string]string{"b": "B"}
			switch mode {
			case "partial restored":
				mapping["a"] = "A"
			case "partial changed":
				mapping["a"] = "OTHER"
			case "all restored":
				mapping["a"] = "A"
				mapping["a-alias"] = "A"
			case "all changed":
				mapping["a"] = "OTHER"
				mapping["a-alias"] = "OTHER"
			}
			f.mapping("1", mapping)
			at := time.Now().UTC()
			if err := r.MarkModelControlAutoVerify(t.Context(), c32REDUser, c32REDWorkspace, candidate.TargetID, &at); err != nil {
				t.Fatal(err)
			}
			c32Attempt(t, f, candidate)
			reason := c32Run(t, f, candidate)
			if f.writeCount() != writes {
				t.Fatal("manual changes caused a remote restore")
			}
			if mode == "partial restored" || mode == "partial changed" {
				stopped := c32AutoObject(t, f, "1", "A")
				if reason != modelControlError("AutoManualChange") || !reflect.DeepEqual(stopped.ClosedEntries, before.ClosedEntries) || stopped.AutoVerifyAt != nil || !stopped.UpdatedAt.Equal(before.UpdatedAt) {
					t.Fatalf("manual-change stop %+v reason %s", stopped, reason)
				}
				if err := r.SaveModelControlAutoState(t.Context(), c32REDUser, c32REDWorkspace, stopped.ID, &modelControlAutoAttempt{Operation: "restore", Basis: autoBasis(candidate.Basis, candidate.EvidenceAt), AttemptAt: time.Now().UTC(), ReasonKey: reason}); err != nil {
					t.Fatal(err)
				}
				if c32State(t, f) != "retry" {
					t.Fatal("unrecorded manual change should retry")
				}
				c32DirectScan(t, f)
				verified := c32AutoObject(t, f, "1", "A")
				if len(verified.ClosedEntries) != 1 || verified.ClosedEntries["a-alias"] != "A" || !verified.UpdatedAt.After(before.UpdatedAt) || c32State(t, f) != "waiting" || f.writeCount() != writes {
					t.Fatal("immediate verify did not wait", verified)
				}
				if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET auto_state=jsonb_set(auto_state,'{attemptAt}',to_jsonb(clock_timestamp()-interval '6 minutes')) WHERE id=$1`, verified.ID); err != nil {
					t.Fatal(err)
				}
				c32DirectScan(t, f)
				if f.writeCount() != writes {
					t.Fatal("same round restored after retry interval")
				}
				f.freshRound("1", "A", 3, 0)
				c32DirectScan(t, f)
				if f.writeCount() != writes+1 || len(f.closed("1", "A")) != 0 {
					t.Fatal("new round did not restore remaining entry")
				}
				f.mu.Lock()
				actual := f.accounts["1"]["credentials"].(map[string]any)["model_mapping"]
				f.mu.Unlock()
				if !reflect.DeepEqual(actual, c32REDClone(map[string]string{"a": mapping["a"], "a-alias": "A", "b": "B"})) {
					t.Fatal("restore overwrote manually changed value", actual)
				}
			} else {
				if reason != "" || len(f.closed("1", "A")) != 0 {
					t.Fatal("all manual entries should clear locally", reason)
				}
			}
		})
	}
	t.Run("manual partial restore unchanged", func(t *testing.T) {
		f := c32AutoFixture(t, true)
		f.mapping("1", map[string]string{"a": "OTHER", "b": "B"})
		preview := f.preview("1", "A", "restore")
		before := f.writeCount()
		code, result := f.execute("1", "A", "restore", preview)
		if code != 200 || result["outcome"] != "restored" || f.writeCount() != before+1 {
			t.Fatal("manual restore changed", code, result)
		}
	})
}
func TestModelControlAutoSameRoundSingleWriteAndUnknown(t *testing.T) {
	for _, mode := range []string{"success", "rejected", "partial restore", "unknown", "timeout", "changed during send"} {
		t.Run(mode, func(t *testing.T) {
			restore := mode == "partial restore"
			f := c32AutoFixture(t, restore)
			r := f.service.modelControls.(*Repository)
			op := "close"
			if restore {
				op = "restore"
			}
			candidate := c32Candidate(t, f, "1", "A", op)
			initial := f.writeCount()
			if mode == "rejected" {
				f.bulkCode = 400
			}
			if mode == "unknown" {
				f.afterWrite = func() { f.failDetail = true }
			}
			if mode == "partial restore" {
				f.afterWrite = func() { f.mapping("1", map[string]string{"a": "A", "b": "B"}) }
			}
			if mode == "changed during send" {
				f.afterWrite = func() { f.mapping("1", map[string]string{"a": "OTHER", "b": "B"}) }
			}
			if mode == "timeout" {
				hook := &c32AutoHookRepository{Repository: r}
				f.service.modelControls = hook
				// Use the core's existing preparation budget contract; a successful saved
				// claim with insufficient remaining budget must be cleared without a send.
				result, err := f.service.runModelControl(withModelControlAutoActor(t.Context()), &modelControlRun{user: c32REDUser, ws: c32REDWorkspace, account: "1", target: candidate.TargetID, model: "A", op: op, actor: "", session: f.session, submitted: candidate.Basis, auto: &candidate, prepare: func(ctx context.Context, user, ws, target string) (context.Context, func(), error) {
					leased, release, err := f.service.prepareAutomaticModelControlAction(ctx, user, ws, target)
					if err != nil {
						return ctx, nil, err
					}
					short, cancel := context.WithTimeout(leased, actionPermitMinimumBudget/2)
					return short, func() { cancel(); release() }, nil
				}})
				if err == nil || modelControlAutoReason(err) != modelControlError("PreparationTimeout") {
					t.Fatalf("timeout result=%+v err=%v", result, err)
				}
			} else {
				reason := c32Run(t, f, candidate)
				if reason != "" {
					t.Fatal(reason)
				}
			}
			expected := initial + 1
			if mode == "timeout" {
				expected = initial
			}
			if f.writeCount() != expected {
				t.Fatal("first attempt write count", f.writeCount(), expected)
			}
			if mode == "unknown" {
				f.failDetail = false
				if c32State(t, f) != "busy" {
					t.Fatal("unknown must freeze account")
				}
			} else {
				expectedState := "waiting"
				// SPEC 7.2 evaluates an already closed model before the timestamp test.
				if mode == "success" || mode == "changed during send" {
					expectedState = "done"
				}
				if c32State(t, f) != expectedState {
					t.Fatalf("state=%s want=%s", c32State(t, f), expectedState)
				}
			}
			if !c32AutoObject(t, f, "1", "A").UpdatedAt.After(*candidate.EvidenceAt) {
				t.Fatal("saved ownership did not invalidate old evidence")
			}
			c32Run(t, f, candidate)
			older := candidate
			olderAt := candidate.EvidenceAt.Add(-time.Second)
			older.EvidenceAt = &olderAt
			c32Run(t, f, older)
			c32DirectScan(t, f)
			if f.writeCount() != expected {
				t.Fatal("same round repeated main-site write")
			}
			if mode == "rejected" {
				f.bulkCode = 200
				f.freshRound("1", "A", 1, 2)
				c32DirectScan(t, f)
				if f.writeCount() != expected+1 {
					t.Fatal("fresh round not retried")
				}
			}
		})
	}
}
func TestModelControlAutoAccountBusyAndRepeatedBlocks(t *testing.T) {
	t.Run("account lease", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		candidate := c32Candidate(t, f, "1", "A", "close")
		_, release, ok, err := f.service.acquireActionTargetLease(t.Context(), candidate.TargetID, false)
		if err != nil || !ok {
			t.Fatal(err)
		}
		defer release()
		before := f.eventCount("close_blocked")
		if reason := c32Run(t, f, candidate); reason != modelControlError("Busy") || f.writeCount() != 0 || f.eventCount("close_blocked") != before {
			t.Fatal("busy account sent or audited", reason)
		}
	})
	t.Run("read failure no event", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		candidate := c32Candidate(t, f, "1", "A", "close")
		f.failDetail = true
		if reason := c32Run(t, f, candidate); reason != modelControlError("AccountReadFailed") || f.writeCount() != 0 || f.eventCount("close_blocked") != 0 {
			t.Fatal("read failure wrote/audited", reason)
		}
	})
	t.Run("group source block retry", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		f.mu.Lock()
		delete(f.accounts, "2")
		delete(f.accounts, "3")
		f.mu.Unlock()
		before := c32AutoObject(t, f, "1", "A")
		c32DirectScan(t, f)
		if f.writeCount() != 0 || f.eventCount("close_blocked") != 1 || c32State(t, f) != "retry" {
			t.Fatal("first block")
		}
		after := c32AutoObject(t, f, "1", "A")
		if !after.UpdatedAt.Equal(before.UpdatedAt) || after.Version != before.Version {
			t.Fatal("blocked plan moved ownership")
		}
		c32DirectScan(t, f)
		if f.eventCount("close_blocked") != 1 {
			t.Fatal("duplicate block event")
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET auto_state=jsonb_set(auto_state,'{attemptAt}',to_jsonb(clock_timestamp()-interval '6 minutes'))`); err != nil {
			t.Fatal(err)
		}
		c32DirectScan(t, f)
		if f.eventCount("close_blocked") != 1 || f.writeCount() != 0 {
			t.Fatal("retry duplicated event")
		}
		var actor string
		if err := f.pool.QueryRow(t.Context(), `SELECT actor_user_id FROM connection_health_model_control_events WHERE event_type='close_blocked'`).Scan(&actor); err != nil || actor != "" {
			t.Fatal("automatic blocked actor", actor, err)
		}
	})
	t.Run("disable after send keeps readback", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		r := f.service.modelControls.(*Repository)
		f.afterWrite = func() { c32SetAuto(t, r, false, false, false) }
		reason := c32Run(t, f, c32Candidate(t, f, "1", "A", "close"))
		if reason != "" || f.writeCount() != 1 || len(f.closed("1", "A")) != 2 {
			t.Fatal("disabling canceled in-flight completion", reason)
		}
	})
}

func TestModelControlAutoBackgroundVerificationGuards(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending appears/missing=%v", missing), func(t *testing.T) {
			f := c32AutoFixture(t, true)
			r := f.service.modelControls.(*Repository)
			c32SetAuto(t, r, false, false, true)
			hook := &c32AutoHookRepository{Repository: r}
			f.service.modelControls = hook
			reads := 0
			hook.beforeList = func() {
				reads++
				if reads == 2 {
					if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$1 WHERE target_id=$2`, modelControlPending{ID: "late-pending", Operation: "close", StartedAt: time.Now().UTC()}, c32REDTarget("1")); err != nil {
						t.Fatal(err)
					}
					if missing {
						f.mu.Lock()
						delete(f.accounts, "1")
						f.mu.Unlock()
					}
				}
			}
			before := f.detailReads
			result, err := f.service.verifyModelControlTargets(withModelControlAutoActor(t.Context()), c32REDUser, c32REDWorkspace, []string{c32REDTarget("1")}, modelControlVerifyOptions{Session: &f.session, Background: true})
			if err != nil || len(result.Errors) > 0 || f.detailReads != before {
				t.Fatal("background read account with pending", err, result.Errors)
			}
			hook.beforeList = nil
			o := c32AutoObject(t, f, "1", "A")
			if o.Pending == nil || o.Pending.ID != "late-pending" || o.AutoVerifyAt == nil {
				t.Fatal("background cleared pending or failed rotation", o)
			}
		})
	}
	t.Run("pending excluded from due", func(t *testing.T) {
		f := c32AutoFixture(t, true)
		r := f.service.modelControls.(*Repository)
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$1`, modelControlPending{ID: "pending", StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		ids, err := r.ListModelControlVerifyDue(t.Context(), c32REDUser, c32REDWorkspace, 10, time.Now().UTC(), 20)
		if err != nil || len(ids) > 0 {
			t.Fatal(ids, err)
		}
	})
	t.Run("closed before inventory", func(t *testing.T) {
		f := c32AutoFixture(t, true)
		r := f.service.modelControls.(*Repository)
		c32SetAuto(t, r, false, false, false)
		f.failInventory = true
		before := f.detailReads
		result, err := f.service.verifyModelControlTargets(t.Context(), c32REDUser, c32REDWorkspace, []string{c32REDTarget("1")}, modelControlVerifyOptions{Session: &f.session, Background: true})
		if err != nil || len(result.Errors) > 0 || f.detailReads != before {
			t.Fatal("disabled verify read upstream", err)
		}
	})
	t.Run("disabled during loop", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.closedSources()
		r := f.service.modelControls.(*Repository)
		c32SetAuto(t, r, false, false, true)
		before := f.detailReads
		f.beforeDetail = func() { f.beforeDetail = nil; c32SetAuto(t, r, false, false, false) }
		_, err := f.service.verifyModelControlTargets(withModelControlAutoActor(t.Context()), c32REDUser, c32REDWorkspace, []string{c32REDTarget("1"), c32REDTarget("2")}, modelControlVerifyOptions{Session: &f.session, Background: true})
		if err != nil || f.detailReads != before+1 {
			t.Fatal("continued after disable", err, f.detailReads-before)
		}
	})
	t.Run("failures rotate; manual can confirm", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		f.afterWrite = func() { f.failDetail = true }
		c32Run(t, f, c32Candidate(t, f, "1", "A", "close"))
		f.failDetail = false
		f.afterWrite = nil
		pending := c32AutoObject(t, f, "1", "A")
		if pending.Pending == nil {
			t.Fatal("unknown setup")
		}
		f.agePending("1", "A", 31)
		f.verify("1")
		resolved := c32AutoObject(t, f, "1", "A")
		if resolved.Pending != nil || resolved.AutoVerifyAt != nil {
			t.Fatal("manual verification stopped confirming", resolved)
		}
		f.failDetail = true
		_, err := f.service.verifyModelControlTargets(withModelControlAutoActor(t.Context()), c32REDUser, c32REDWorkspace, []string{c32REDTarget("1")}, modelControlVerifyOptions{Session: &f.session, Background: true})
		if err != nil {
			t.Fatal(err)
		}
		if c32AutoObject(t, f, "1", "A").AutoVerifyAt == nil {
			t.Fatal("failed account did not rotate")
		}
	})
	t.Run("health-only interval and twenty cap", func(t *testing.T) {
		f := newC32REDFixture(t)
		r := f.service.modelControls.(*Repository)
		c32SetAuto(t, r, false, false, true)
		for id := 10; id < 32; id++ {
			name := fmt.Sprint(id)
			f.mu.Lock()
			f.accounts[name] = c32REDAccount(id, map[string]string{"b": "B"})
			f.mu.Unlock()
			o, err := r.InsertModelControlTarget(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget(name), "A")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET closed_entries='{"a":"A"}',observed_state='closed' WHERE id=$1`, o.ID); err != nil {
				t.Fatal(err)
			}
		}
		before := f.detailReads
		c32DirectScan(t, f)
		if f.detailReads != before+20 {
			t.Fatal("verify cap", f.detailReads-before)
		}
		c32DirectScan(t, f)
		if f.detailReads != before+22 {
			t.Fatal("verification did not rotate", f.detailReads-before)
		}
		c32DirectScan(t, f)
		if f.detailReads != before+22 {
			t.Fatal("verified too early")
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET auto_verify_at=clock_timestamp()-interval '11 minutes'`); err != nil {
			t.Fatal(err)
		}
		c32DirectScan(t, f)
		if f.detailReads != before+42 {
			t.Fatal("due interval ignored")
		}
		if f.writeCount() != 0 {
			t.Fatal("health-only wrote whitelist")
		}
	})
}
func TestModelControlAutoScanLimitsFlightsAndShutdown(t *testing.T) {
	t.Run("ten candidates rotate", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.switches(true, false, false)
		for id := 10; id < 31; id++ {
			name := fmt.Sprint(id)
			f.mu.Lock()
			f.accounts[name] = c32REDAccount(id, map[string]string{"a": "A", "b": "B"})
			f.mu.Unlock()
			f.manage(name, "A")
			f.freshRound(name, "A", 1, 2)
		}
		c32DirectScan(t, f)
		if f.writeCount() != 10 {
			t.Fatal("first scan cap", f.writeCount())
		}
		c32DirectScan(t, f)
		if f.writeCount() != 20 {
			t.Fatal("second scan rotation", f.writeCount())
		}
		c32DirectScan(t, f)
		if f.writeCount() != 21 {
			t.Fatal("third scan rotation", f.writeCount())
		}
	})
	t.Run("single flight", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		entered := make(chan struct{})
		finish := make(chan struct{})
		f.beforeDetail = func() { f.beforeDetail = nil; close(entered); <-finish }
		f.service.startModelControlAutoScans(t.Context())
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("scan not started")
		}
		f.service.startModelControlAutoScans(t.Context())
		close(finish)
		f.service.modelControlScanWG.Wait()
		if f.writeCount() != 1 {
			t.Fatal("overlapping scan duplicate", f.writeCount())
		}
		if _, busy := f.service.modelControlScanFlights.Load(c32REDUser + "|" + c32REDWorkspace); busy {
			t.Fatal("flight not released")
		}
	})
	t.Run("session unavailable finishes", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		f.service.mySites = c32UnavailableSessionReader{}
		c32DirectScan(t, f)
		settings, err := f.service.modelControls.(modelControlAutoRepository).GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
		if err != nil || settings.LastScanFinishedAt == nil || settings.LastScanErrorKey == "" || f.writeCount() != 0 {
			t.Fatal(settings, err)
		}
	})
	t.Run("shutdown stops new objects", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		f.manage("2", "A")
		f.freshRound("2", "A", 1, 2)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.afterWrite = cancel
		c32DirectScanContext := func() { f.service.runModelControlAutoScan(ctx, c32REDUser, c32REDWorkspace) }
		c32DirectScanContext()
		if f.writeCount() != 1 || len(f.closed("1", "A")) != 2 {
			t.Fatal("shutdown canceled completion or started another", f.writeCount())
		}
		f.service.closeActionAdmission()
		f.service.startModelControlAutoScans(t.Context())
		f.service.modelControlScanWG.Wait()
		if f.writeCount() != 1 {
			t.Fatal("closed admission scan started")
		}
	})
	t.Run("read-only never starts", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		f.service.backgroundTasksDisabled = true
		f.service.startModelControlAutoScans(t.Context())
		f.service.modelControlScanWG.Wait()
		a, _ := f.service.modelControls.(modelControlAutoRepository).GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
		if f.writeCount() != 0 || a.LastScanStartedAt != nil {
			t.Fatal("read-only started scan")
		}
	})
}

type c32UnavailableSessionReader struct{ fakeMySitesReader }

func (c32UnavailableSessionReader) RequireSession(context.Context, string, string) (upstream.Session, error) {
	return upstream.Session{}, errors.New("fixture session unavailable")
}

type c32ScopeSessionReader struct {
	fakeMySitesReader
	workspaces []string
}

func (r *c32ScopeSessionReader) RequireSession(ctx context.Context, user, ws string) (upstream.Session, error) {
	r.workspaces = append(r.workspaces, ws)
	return r.fakeMySitesReader.RequireSession(ctx, user, ws)
}
func TestModelControlAutoNonCurrentWorkspaceAndUnsupported(t *testing.T) {
	t.Run("non current workspace", func(t *testing.T) {
		f := newC32REDFixture(t)
		r := f.service.modelControls.(*Repository)
		ws := "ws-other"
		target := "sub2api:" + ws + ":1"
		if _, err := r.InsertModelControlTarget(t.Context(), c32REDUser, ws, target, "A"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SaveModelControlAutoSettings(t.Context(), c32REDUser, ws, ModelControlAutoSettings{AutoCloseEnabled: true, VerifyIntervalMinutes: 10, RetryIntervalMinutes: 5}, 0); err != nil {
			t.Fatal(err)
		}
		f.freshRound("1", "A", 1, 2)
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET target_id=$1`, target); err != nil {
			t.Fatal(err)
		}
		reader := &c32ScopeSessionReader{fakeMySitesReader: fakeMySitesReader{session: f.session}}
		f.service.mySites = reader
		f.service.runModelControlAutoScan(t.Context(), c32REDUser, ws)
		if f.writeCount() != 1 || len(reader.workspaces) == 0 {
			t.Fatal("other workspace not handled")
		}
		for _, scope := range reader.workspaces {
			if scope != ws {
				t.Fatal("borrowed current workspace session", scope)
			}
		}
		current, _ := r.GetModelControlAutoSettings(t.Context(), c32REDUser, c32REDWorkspace)
		if current.LastScanStartedAt != nil {
			t.Fatal("other scan changed current settings")
		}
	})
	t.Run("optional repo absent", func(t *testing.T) {
		f := newC32REDFixture(t)
		r := f.service.modelControls
		f.service.modelControls = struct{ modelControlRepository }{r}
		code, body := f.request("GET", "model-control/auto-settings", nil)
		if code != 400 || body["message"] != modelControlError("Unsupported") {
			t.Fatal(code, body)
		}
		settings, err := f.service.modelControlAuto(t.Context(), c32REDUser, c32REDWorkspace)
		if err != nil || settings.enabled() {
			t.Fatal(settings, err)
		}
		f.service.startModelControlAutoScans(t.Context())
		f.service.modelControlScanWG.Wait()
		if f.writeCount() > 0 {
			t.Fatal("optional absent scanned")
		}
	})
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("late automatic event/expired=%v", expired), func(t *testing.T) {
			f := newC32REDFixture(t)
			f.manage("1", "A")
			r := f.service.modelControls.(*Repository)
			c32SetAuto(t, r, false, false, true)
			sentAt := time.Now().Add(-time.Hour).UTC()
			if expired {
				sentAt = sentAt.Add(-24 * time.Hour)
			}
			unconfirmed := modelControlUnconfirmed{Entries: map[string]string{"a": "A"}, Basis: modelControlBasis{Decision: "close_recommended"}, SentAt: sentAt}
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET unconfirmed_close=$1`, unconfirmed); err != nil {
				t.Fatal(err)
			}
			f.mapping("1", map[string]string{"b": "B"})
			before := c32AutoObject(t, f, "1", "A")
			c32DirectScan(t, f)
			after := c32AutoObject(t, f, "1", "A")
			if after.UnconfirmedClose != nil || !after.UpdatedAt.After(before.UpdatedAt) {
				t.Fatal("late/expiry did not change ownership")
			}
			events, _, err := r.ListModelControlEvents(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget("1"), "A", 1)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range events {
				if e.EventType == "close_late_applied" {
					found = true
					if e.ActorUserID != "" {
						t.Fatal("background late actor", e)
					}
				}
			}
			if found == expired {
				t.Fatal("late event expiration behavior changed", found, expired)
			}
		})
	}
}
func TestModelControlAutoOldCandidateDuringAndAfterOtherScan(t *testing.T) {
	f := c32AutoFixture(t, false)
	candidate := c32Candidate(t, f, "1", "A", "close")
	entered := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan string, 1)
	f.beforeDetail = func() { f.beforeDetail = nil; close(entered); <-finish }
	go func() { done <- c32Run(t, f, candidate) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first did not start")
	}
	if reason := c32Run(t, f, candidate); reason != modelControlError("Busy") {
		t.Fatal("parallel candidate not busy", reason)
	}
	close(finish)
	if reason := <-done; reason != "" {
		t.Fatal(reason)
	}
	if reason := c32Run(t, f, candidate); reason != "" || f.writeCount() != 1 {
		t.Fatal("second old candidate wrote", reason, f.writeCount())
	}
}
func TestModelControlAutoManualRestoreWaitsForNewEvidence(t *testing.T) {
	f := c32AutoFixture(t, true)
	r := f.service.modelControls.(*Repository)
	c32SetAuto(t, r, true, true, false)
	f.freshRound("1", "A", 1, 2)
	preview := f.preview("1", "A", "restore")
	code, result := f.execute("1", "A", "restore", preview)
	if code != 200 || result["outcome"] != "restored" {
		t.Fatal(code, result)
	}
	before := f.writeCount()
	if c32State(t, f) != "waiting" {
		t.Fatal("manual restore did not wait")
	}
	c32DirectScan(t, f)
	if f.writeCount() != before {
		t.Fatal("old bad round reversed manual restore")
	}
	f.freshRound("1", "A", 1, 2)
	c32DirectScan(t, f)
	if f.writeCount() != before+1 {
		t.Fatal("new bad round did not close")
	}
}
