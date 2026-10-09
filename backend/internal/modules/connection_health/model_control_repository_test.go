package connection_health

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelControlPostgresRuleVersionForeignKeyAndWorkspaceCascade(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE admin_accounts(id text PRIMARY KEY);INSERT INTO admin_accounts VALUES('ws1')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	object, err := repo.InsertModelControlTarget(t.Context(), "user", "ws1", "sub2api:ws1:1", "A")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := repo.ListModelControlRules(t.Context(), "user", "ws1")
	if err != nil || len(rules) != 1 || rules[0].MinAccuracyPercent != 50 || rules[0].MinJudgedAnswers != 3 {
		t.Fatal("default rule not inserted atomically")
	}
	rule := rules[0]
	rule.MinAccuracyPercent = 60
	saved, err := repo.UpsertModelControlRule(t.Context(), rule, rule.Version)
	if err != nil || saved.Version != 2 {
		t.Fatalf("save version=%+v err=%v", saved, err)
	}
	if _, err := repo.UpsertModelControlRule(t.Context(), rule, rule.Version); err == nil {
		t.Fatal("stale version replaced rule")
	}
	if err := repo.DeleteModelControlRule(t.Context(), "user", "ws1", "A", 2); err == nil {
		t.Fatal("managed rule was deleted")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM admin_accounts WHERE id='ws1'`); err != nil {
		t.Fatalf("workspace cascade failed: %v", err)
	}
	for _, table := range []string{"connection_health_model_control_targets", "connection_health_model_control_rules", "connection_health_model_control_events"} {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("workspace leaked %s count=%d err=%v", table, count, err)
		}
	}
	_ = object
}
func TestModelControlPostgresObservationDoesNotInvalidateOwnershipVersion(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	_, initial, err := f.service.getModelControlItem(t.Context(), c3REDUser, c3REDWorkspace, c3REDTarget("1"), "A")
	if err != nil {
		t.Fatal(err)
	}
	incoming := initial
	incoming.LastAttempt = &modelControlAttempt{Operation: "close", Outcome: "blocked", ReasonKey: modelControlError("FloorInsufficient"), Groups: []modelSourceCount{{GroupID: "g", Key: "a"}}}
	at := time.Now().UTC()
	updated, err := f.service.modelControls.observeModelControlTarget(t.Context(), incoming, at, true, "close_blocked")
	if err != nil || updated.Version != initial.Version {
		t.Fatalf("observation affected version: %v", err)
	}
	incoming = initial
	incoming.LastAttempt = nil
	_, err = f.service.modelControls.observeModelControlTarget(t.Context(), incoming, at.Add(time.Minute), true, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.modelControls.observeModelControlTarget(t.Context(), updated, at.Add(time.Second), true, "close_blocked")
	if err != nil {
		t.Fatal(err)
	}
	item := f.item("1", "A")
	if item["control"].(map[string]any)["lastAttempt"] != nil || f.eventCount("close_blocked") != 1 {
		t.Fatal("older observed failure revived after success")
	}
}
func TestModelControlPostgresPreparedAndPermitCommitUncertaintyNeverResends(t *testing.T) {
	for _, stage := range []string{"prepared", "sending"} {
		t.Run(stage, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			injected := false
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, current string) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if current == stage && !injected {
					injected = true
					return errors.New("fixture commit acknowledgement loss")
				}
				return nil
			})
			p := f.preview("1", "A", "close")
			code, result := f.execute("1", "A", "close", p)
			if code != 200 {
				t.Fatalf("result=%v", result)
			}
			if stage == "sending" {
				if f.writeCount() != 0 || result["outcome"] != "unknown" {
					t.Fatal("uncertain permit sent")
				}
				f.agePending("1", "A", 31)
				f.commitSetter().setModelControlCommitter(nil)
				f.verify("1")
				if f.writeCount() != 0 {
					t.Fatal("verify resent uncertain permit")
				}
				attempt := f.item("1", "A")["control"].(map[string]any)["lastAttempt"].(map[string]any)
				if attempt["operation"] != "close" || attempt["outcome"] != "failed" {
					t.Fatal("verify discarded the uncertain permit's operation result")
				}
			} else if f.writeCount() != 1 || result["outcome"] != "closed" {
				t.Fatal("persisted prepared record did not continue exactly once")
			}
		})
	}
}

func TestModelControlPostgresConcurrentRuleDeletionCannotCreateOrphan(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	rule := ModelControlRule{UserID: "user", AdminAccountID: "ws1", ModelName: "A", MinAccuracyPercent: 50, MinJudgedAnswers: 3, IncludeManual: true, IncludeScheduled: true}
	if _, err := repo.UpsertModelControlRule(t.Context(), rule, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION c3_insert_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(9314721);RETURN NEW;END $$;CREATE TRIGGER c3_model_control_insert_barrier BEFORE INSERT ON connection_health_model_control_targets FOR EACH ROW EXECUTE FUNCTION c3_insert_barrier()`); err != nil {
		t.Fatal(err)
	}
	holder, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(t.Context(), `SELECT pg_advisory_xact_lock(9314721)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := repo.InsertModelControlTarget(t.Context(), "user", "ws1", "sub2api:ws1:1", "A")
		done <- err
	}()
	waiting := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE wait_event='advisory' AND query ILIKE 'INSERT INTO connection_health_model_control_targets%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("target insertion did not reach database barrier")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM connection_health_model_control_rules WHERE user_id='user' AND model_name='A'`); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var conflict *ModelControlConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("concurrent rule deletion returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("insertion did not terminate")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_targets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan model-control object remained")
	}
}

func TestModelControlPostgresScheduleCoverageAndImmutableSourceNames(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	for _, id := range []string{"direct", "disabled", "blocked", "deleted", "group"} {
		_, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedules(id,user_id,admin_account_id,name,target_mode,selected_account_target_ids,model_names,question_ids,reasoning_effort,repeat_count,peak_start,peak_end,peak_interval_minutes,off_peak_interval_minutes,enabled,created_by) VALUES($1,$2,'ws1',$1,'accounts',ARRAY[$3],ARRAY['A'],ARRAY['q'],'medium',1,'08:00','18:00',30,60,true,$2)`, id, c3REDUser, c3REDTarget("1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_schedules SET enabled=false WHERE id='disabled';UPDATE connection_health_question_answer_schedules SET blocked_reason='fixture' WHERE id='blocked';UPDATE connection_health_question_answer_schedules SET deleted_at=now() WHERE id='deleted';UPDATE connection_health_question_answer_schedules SET target_mode='groups',selected_group_ids=ARRAY['1'],selected_account_target_ids='{}' WHERE id='group'`); err != nil {
		t.Fatal(err)
	}
	scheduledBatch := f.round("1", "A", 2, 1, 0)
	runNowBatch := f.round("1", "A", 3, 0, 0)
	for n, batch := range []string{scheduledBatch, runNowBatch} {
		trigger := "scheduled"
		requestID := ""
		if n == 1 {
			trigger, requestID = "run_now", "fixture-run-now"
		}
		executionID := fmt.Sprintf("source-%d", n)
		_, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedule_executions(id,user_id,admin_account_id,schedule_id,trigger,request_id,scheduled_for,status,config_snapshot,created_at) VALUES($1,$2,'ws1','group',$3,$4,now()+make_interval(secs=>$5),'completed','{"requested":{"schedule":{"name":"Original plan name"}}}',now()+make_interval(secs=>$5))`, executionID, c3REDUser, trigger, requestID, n)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedule_execution_targets(id,execution_id,target_id,account_snapshot,requested_models,status) VALUES($1,$2,$3,'{}',ARRAY['A'],'batch_created')`, batch, executionID, c3REDTarget("1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_schedules SET name='Renamed current plan' WHERE id='group'`); err != nil {
		t.Fatal(err)
	}
	pair := modelControlPair{c3REDTarget("1"), "A"}
	coverage, err := f.service.modelControls.ListModelControlCoverage(t.Context(), c3REDUser, c3REDWorkspace, []modelControlPair{pair})
	if err != nil || len(coverage[pair]) != 2 {
		t.Fatalf("active direct and latest group plan coverage=%v err=%v", coverage, err)
	}
	rounds, err := f.service.modelControls.ListModelControlRounds(t.Context(), c3REDUser, []modelControlPair{pair})
	if err != nil || len(rounds[pair]) != 3 || rounds[pair][0].Source != "run_now" || rounds[pair][1].Source != "scheduled" || rounds[pair][2].Source != "manual" {
		t.Fatalf("source joins=%v err=%v", rounds, err)
	}
	for _, r := range rounds[pair][:2] {
		if r.ScheduleName != "Original plan name" {
			t.Fatal("current plan rename replaced immutable source name")
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_schedule_execution_targets SET target_id=$1 WHERE execution_id='source-1'`, c3REDTarget("2")); err != nil {
		t.Fatal(err)
	}
	coverage, err = f.service.modelControls.ListModelControlCoverage(t.Context(), c3REDUser, c3REDWorkspace, []modelControlPair{pair})
	if err != nil || len(coverage[pair]) != 1 || coverage[pair][0].ID != "direct" {
		t.Fatal("older group execution incorrectly counted as current coverage")
	}
	coverage, err = f.service.modelControls.ListModelControlCoverage(t.Context(), c3REDUser, "other-workspace", []modelControlPair{pair})
	if err != nil || len(coverage[pair]) != 0 {
		t.Fatal("plan coverage crossed workspace")
	}
}

type modelControlCommitReadFailure struct {
	modelControlRepository
	failed *atomic.Bool
}

func (r modelControlCommitReadFailure) ListModelControlTargets(ctx context.Context, user, workspace string) ([]modelControlTarget, error) {
	if r.failed.Load() {
		return nil, errors.New("fixture commit verification read unavailable")
	}
	return r.modelControlRepository.ListModelControlTargets(ctx, user, workspace)
}

func TestModelControlPostgresCommitUnknownReadFailureNeverPromisesStorageState(t *testing.T) {
	for _, stage := range []string{"prepared", "sending", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			failed := &atomic.Bool{}
			f.service.modelControls = modelControlCommitReadFailure{f.service.modelControls, failed}
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, current string) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if current == stage {
					failed.Store(true)
					return errors.New("fixture commit acknowledgement loss")
				}
				return nil
			})
			code, result := f.execute("1", "A", "close", p)
			if code != 400 || !strings.Contains(fmt.Sprint(result), "StorageUnknown") {
				t.Fatalf("unknown storage was promised: code=%d result=%v", code, result)
			}
			wantWrites := 0
			if stage == "finalize" {
				wantWrites = 1
			}
			if f.writeCount() != wantWrites {
				t.Fatal("commit uncertainty sent an extra remote request")
			}
			failed.Store(false)
			f.commitSetter().setModelControlCommitter(nil)
			control := f.item("1", "A")["control"].(map[string]any)
			if stage == "finalize" {
				if control["pending"] != nil || len(f.closed("1", "A")) != 2 || f.eventCount("close_unknown") != 0 {
					t.Fatal("failed confirmation changed committed final state")
				}
			} else if control["pending"] == nil || control["pending"].(map[string]any)["phase"] != stage {
				t.Fatal("failed confirmation discarded actual prepared or sending state")
			}
		})
	}
}

func TestModelControlPostgresMissingAccountCommitLostUsesActualPersistedState(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.mu.Lock()
	delete(f.accounts, "1")
	f.mu.Unlock()
	f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return errors.New("fixture missing-account commit acknowledgement loss")
	})
	p := f.preview("1", "A", "close")
	if p["blockReasonKey"] != modelControlError("AccountMissing") || f.eventCount("account_missing_resolved") != 1 || f.writeCount() != 0 {
		t.Fatal("committed missing-account state was reported as uncommitted")
	}
}

func TestModelControlPostgresRemovalVersionAndCommitOutcome(t *testing.T) {
	for _, outcome := range []string{"committed", "rolled_back", "read_failed"} {
		t.Run(outcome, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			item := f.manage("1", "A")
			input := map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": item["version"].(float64) + 1}
			code, _ := f.request("DELETE", "model-control/managed", input)
			if code != 409 || f.eventCount("managed_removed") != 0 {
				t.Fatal("stale conditional removal deleted the object")
			}
			input["expectedVersion"] = item["version"]
			failed := &atomic.Bool{}
			f.service.modelControls = modelControlCommitReadFailure{f.service.modelControls, failed}
			f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
				if stage != "remove" {
					return tx.Commit(ctx)
				}
				if outcome == "rolled_back" {
					if err := tx.Rollback(ctx); err != nil {
						return err
					}
				} else if err := tx.Commit(ctx); err != nil {
					return err
				}
				failed.Store(outcome == "read_failed")
				return errors.New("fixture remove commit acknowledgement loss")
			})
			code, result := f.request("DELETE", "model-control/managed", input)
			wantCode := 204
			if outcome == "rolled_back" {
				wantCode = 409
			} else if outcome == "read_failed" {
				wantCode = 400
				if !strings.Contains(fmt.Sprint(result), "StorageUnknown") {
					t.Fatal("unreadable removal state was promised")
				}
			}
			if code != wantCode {
				t.Fatalf("remove outcome=%s code=%d result=%v", outcome, code, result)
			}
			failed.Store(false)
			var objects int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_targets WHERE target_id=$1`, c3REDTarget("1")).Scan(&objects); err != nil {
				t.Fatal(err)
			}
			wantObjects, wantEvents := 0, 1
			if outcome == "rolled_back" {
				wantObjects, wantEvents = 1, 0
			}
			if objects != wantObjects || f.eventCount("managed_removed") != wantEvents || f.writeCount() != 0 {
				t.Fatal("removal and archived event did not follow actual commit state")
			}
		})
	}
}
