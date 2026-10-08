package connection_health

import (
	"encoding/json"
	"testing"
	"time"
)

func c2StoragePreparedExecution(t *testing.T, repo *Repository, now time.Time) (QuestionAnswerScheduleExecution, QuestionAnswerScheduleExecutionTarget) {
	t.Helper()
	ctx := t.Context()
	config := QuestionAnswerScheduleConfig{Name: "C2 frozen", TargetMode: "accounts", SelectedGroupIDs: []string{}, SelectedAccountTargetIDs: []string{"sub2api:c2-storage-workspace:a"}, Models: []string{"model"}, QuestionIDs: []string{"frozen-question"}, ReasoningEffort: "medium", RepeatCount: 2, PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
	plan, err := repo.CreateQuestionAnswerSchedule(ctx, "c2-storage-user", "c2-storage-workspace", config, false, QuestionAnswerScheduleSelectionSnapshot{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, plan.UserID, plan.AdminAccountID, plan.ID, "292ef3c4-9aaa-471c-afbb-e996e25d1def", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	execution, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, plan.UserID, plan.AdminAccountID, func() time.Time { return now })
	if err != nil || execution == nil {
		t.Fatalf("claim=%+v err=%v", execution, err)
	}
	if execution.ID != accepted.ID {
		t.Fatal("wrong FIFO claim")
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	target := QuestionAnswerScheduleExecutionTarget{ID: id, ExecutionID: execution.ID, TargetID: config.SelectedAccountTargetIDs[0], AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: "a", AccountName: "A", Platform: "sub2api"}, MatchedGroupsSnapshot: []QuestionAnswerScheduleGroupRef{}, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: plan.AdminAccountID, Memberships: []TestConfigurationSource{}, InventoryComplete: true}, Protocol: TestProtocolResponses}, RequestedModels: config.Models, AvailableModels: config.Models, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, PlannedRequestCount: 2, Status: "pending"}
	resolved := QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "frozen-question", Name: "Original", Body: "Original body", Keywords: []string{"original"}}}, ResolvedAt: now, MissingTargetIDs: []string{}}
	reason, err := repo.FreezeQuestionAnswerScheduleExecution(ctx, plan.UserID, plan.AdminAccountID, execution.ID, resolved, []QuestionAnswerScheduleExecutionTarget{target}, func() time.Time { return now })
	if err != nil || reason != "" {
		t.Fatalf("freeze reason=%s err=%v", reason, err)
	}
	return *execution, target
}

func TestC2QuestionAnswerStorageScheduledBatchFrozenAtomicIdentityAndCancel(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e, target := c2StoragePreparedExecution(t, repo, now)
	// No current question row exists; the accepted execution is the request source.
	records, created, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
	if err != nil || !created || len(records) != 2 {
		t.Fatalf("create count=%d new=%v err=%v", len(records), created, err)
	}
	for _, record := range records {
		if record.QuestionBody != "Original body" || record.QuestionName != "Original" || record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolResponses || record.RepeatIndex == nil {
			t.Fatalf("frozen record=%+v", record)
		}
	}
	again, newlyCreated, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
	if err != nil || newlyCreated || len(again) != 2 {
		t.Fatalf("same-batch must precede its own active check: count=%d new=%v err=%v", len(again), newlyCreated, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_records WHERE batch_id=$1`, target.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate batch count=%d err=%v", count, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET question_body='tampered' WHERE id=$1`, records[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID); err == nil {
		t.Fatal("frozen configuration mismatch accepted")
	}
}

func TestC2QuestionAnswerStorageRuntimeDefaultCASAndScheduleCascade(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('c2-storage-workspace','c2-storage-user')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := repo.GetQuestionAnswerRuntimeSettings(ctx)
	if err != nil || settings.QuestionAnswerConcurrency != 15 || settings.Version != 0 {
		t.Fatalf("defaults=%+v err=%v", settings, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_runtime_settings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("GET inserted singleton")
	}
	settings, err = repo.SaveQuestionAnswerRuntimeSettings(ctx, 6, 0)
	if err != nil || settings.Version != 1 {
		t.Fatalf("first CAS=%+v err=%v", settings, err)
	}
	if _, err = repo.SaveQuestionAnswerRuntimeSettings(ctx, 15, 0); err == nil {
		t.Fatal("stale CAS accepted")
	}
	now := time.Now().UTC()
	e, _ := c2StoragePreparedExecution(t, repo, now)
	if err = repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM admin_accounts WHERE id=$1`, e.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"connection_health_question_answer_schedules", "connection_health_question_answer_schedule_executions", "connection_health_question_answer_schedule_execution_targets"} {
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade table=%s count=%d err=%v", table, count, err)
		}
	}
	settings, err = repo.GetQuestionAnswerRuntimeSettings(ctx)
	if err != nil || settings.QuestionAnswerConcurrency != 6 || settings.Version != 1 {
		t.Fatal("workspace cascade removed global settings")
	}
}

func TestC2QuestionAnswerStoragePreparationBudgetRejectKeepsSnapshotUnwritten(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e, target := c2StoragePreparedExecution(t, repo, now)
	// Start a second execution in another plan with a zero waiting limit.
	var config QuestionAnswerScheduleSnapshot
	if err := pool.QueryRow(ctx, `SELECT config_snapshot FROM connection_health_question_answer_schedule_executions WHERE id=$1`, e.ID).Scan(&config); err != nil {
		t.Fatal(err)
	}
	config.Resolved = nil
	config.Requested.Limits.MaxQueuedScheduledRequests = 0
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM connection_health_question_answer_schedule_execution_targets WHERE execution_id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET config_snapshot=$2,reserved_request_count=0,planned_target_count=0 WHERE id=$1`, e.ID, data); err != nil {
		t.Fatal(err)
	}
	reason, err := repo.FreezeQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "frozen-question", Name: "Q", Body: "B"}}, ResolvedAt: now}, []QuestionAnswerScheduleExecutionTarget{target}, func() time.Time { return now })
	if err != nil || reason != "queue_full" {
		t.Fatalf("zero queue reject reason=%s err=%v", reason, err)
	}
	actual, err := repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Status != "active" || actual.ConfigSnapshot.Resolved != nil || actual.ReservedRequestCount != 0 || len(actual.Targets) != 0 {
		t.Fatalf("rejection prematurely committed resources/terminal: %+v", actual)
	}
}

func TestC2QuestionAnswerStorageRevalidationPreservesUnchangedVersionAndExternalBlock(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	config := QuestionAnswerScheduleConfig{Name: "C2 revalidate", TargetMode: "accounts", SelectedGroupIDs: []string{}, SelectedAccountTargetIDs: []string{"sub2api:c2-storage-workspace:a"}, Models: []string{"model"}, QuestionIDs: []string{"q"}, ReasoningEffort: "medium", RepeatCount: 2, PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
	plan, err := repo.CreateQuestionAnswerSchedule(ctx, "c2-storage-user", "c2-storage-workspace", config, true, QuestionAnswerScheduleSelectionSnapshot{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Run("healthy_revalidate_is_idempotent", func(t *testing.T) {
		fresh, err := repo.SetQuestionAnswerScheduleState(ctx, plan.UserID, plan.AdminAccountID, plan.ID, "revalidate", "", plan.Version, func() time.Time { return now.Add(31 * time.Minute) })
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Version != plan.Version || !equalScheduleTime(fresh.NextRunAt, plan.NextRunAt) {
			t.Fatalf("unchanged revalidate mutated version/cursor: old=%+v new=%+v", plan, fresh)
		}
	})
	t.Run("local_limits_cannot_mask_external_block", func(t *testing.T) {
		if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason='all_accounts_missing',next_run_at=NULL WHERE id=$1`, plan.ID); err != nil {
			t.Fatal(err)
		}
		limits := defaultQuestionAnswerScheduleLimits()
		limits.MaxScheduleRequestsPerExecution = 1
		lower, err := repo.SaveQuestionAnswerScheduleLimits(ctx, plan.UserID, plan.AdminAccountID, limits, 0, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		limits.MaxScheduleRequestsPerExecution = 500
		if _, err = repo.SaveQuestionAnswerScheduleLimits(ctx, plan.UserID, plan.AdminAccountID, limits, lower.Version, func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetQuestionAnswerSchedule(ctx, plan.UserID, plan.AdminAccountID, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.BlockedReason != "all_accounts_missing" || current.NextRunAt != nil || !current.Enabled {
			t.Fatalf("limits indirectly cleared inventory block: %+v", current)
		}
	})
}

func TestC2QuestionAnswerStorageQueueTransitionsKeepDailyReservation(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e, target := c2StoragePreparedExecution(t, repo, now)
	assertBudget := func(wantQueue int) {
		t.Helper()
		queued, err := scheduleQueueUsed(ctx, pool, e.UserID, e.AdminAccountID)
		if err != nil || queued != wantQueue {
			t.Fatalf("queue=%d want=%d err=%v", queued, wantQueue, err)
		}
		var daily int
		if err = pool.QueryRow(ctx, `SELECT COALESCE(sum(reserved_request_count),0) FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND (scheduled_for AT TIME ZONE 'Asia/Singapore')::date=($3::timestamptz AT TIME ZONE 'Asia/Singapore')::date`, e.UserID, e.AdminAccountID, e.ScheduledFor).Scan(&daily); err != nil || daily != 2 {
			t.Fatalf("daily=%d must retain 2 err=%v", daily, err)
		}
	}
	assertBudget(2)
	records, created, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
	if err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	assertBudget(2) // Conversion to records does not double count the target.
	if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='running' WHERE id=$1`, records[0].ID); err != nil {
		t.Fatal(err)
	}
	assertBudget(1) // Running consumes a shared slot, not a waiting reservation.
	current, err := repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := repo.DecideQuestionAnswerScheduleTermination(ctx, e.UserID, e.AdminAccountID, e.ID, "user_cancel", &current.Version, func() time.Time { return now })
	if err != nil || !decision.Accepted || decision.Execution.Status != "active" {
		t.Fatalf("cancel=%+v err=%v", decision, err)
	}
	assertBudget(1) // Persisting the cause alone cannot remove pending C1 work.
	if found, err := repo.FinalizeQuestionAnswerBatch(ctx, e.UserID, target.TargetID, target.ID, QuestionAnswerCancelled, "user_cancel"); err != nil || !found {
		t.Fatalf("exact finalizer found=%v err=%v", found, err)
	}
	assertBudget(0)
	finished, err := repo.FinishQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, true, "", func() time.Time { return now.Add(24 * time.Hour) })
	if err != nil || finished.Status != "cancelled" {
		t.Fatalf("cross-day finish=%+v err=%v", finished, err)
	}
	assertBudget(0) // Terminal/cross-midnight cancellation never refunds dailyUsed.
}

func TestC2QuestionAnswerStorageBatchRollbackAndCancelCannotRecreate(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e, target := c2StoragePreparedExecution(t, repo, now)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION c2_reject_batch_state() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='batch_created' THEN RAISE EXCEPTION 'C2 batch state failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER c2_reject_batch_state BEFORE UPDATE ON connection_health_question_answer_schedule_execution_targets FOR EACH ROW EXECUTE FUNCTION c2_reject_batch_state()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID); err == nil {
		t.Fatal("target update failure must fail the whole creation")
	}
	var records int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_records WHERE batch_id=$1`, target.ID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("rollback left %d records: %v", records, err)
	}
	current, err := repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil || len(current.Targets) != 1 || current.Targets[0].Status != "pending" || current.ReservedRequestCount != 2 {
		t.Fatalf("rollback corrupted frozen facts: %+v err=%v", current, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER c2_reject_batch_state ON connection_health_question_answer_schedule_execution_targets`); err != nil {
		t.Fatal(err)
	}
	decision, err := repo.DecideQuestionAnswerScheduleTermination(ctx, e.UserID, e.AdminAccountID, e.ID, "user_cancel", &current.Version, func() time.Time { return now })
	if err != nil || !decision.Accepted {
		t.Fatalf("cancel=%+v err=%v", decision, err)
	}
	if _, created, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID); err == nil || created {
		t.Fatal("cancelled no-record target recreated a batch")
	}
	queued, err := scheduleQueueUsed(ctx, pool, e.UserID, e.AdminAccountID)
	if err != nil || queued != 0 {
		t.Fatalf("unbuilt cancellation did not release queue: %d %v", queued, err)
	}
	finished, err := repo.FinishQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, true, "", func() time.Time { return now })
	if err != nil || finished.Status != "cancelled" || finished.ReservedRequestCount != 2 {
		t.Fatalf("cancelled finish=%+v err=%v", finished, err)
	}
}

func TestC2QuestionAnswerStorageFreshLimitsGuardScheduleWrites(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	config := QuestionAnswerScheduleConfig{Name: "C2 limits race", TargetMode: "accounts", SelectedGroupIDs: []string{}, SelectedAccountTargetIDs: []string{"sub2api:c2-storage-workspace:a"}, Models: []string{"model"}, QuestionIDs: []string{"q"}, ReasoningEffort: "medium", RepeatCount: 2, PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
	plan, err := repo.CreateQuestionAnswerSchedule(ctx, "c2-storage-user", "c2-storage-workspace", config, false, QuestionAnswerScheduleSelectionSnapshot{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	limits := defaultQuestionAnswerScheduleLimits()
	limits.MaxScheduleRequestsPerExecution = 3
	if _, err = repo.SaveQuestionAnswerScheduleLimits(ctx, plan.UserID, plan.AdminAccountID, limits, 0, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetQuestionAnswerSchedule(ctx, plan.UserID, plan.AdminAccountID, plan.ID)
	if err != nil || current.Version != plan.Version {
		t.Fatalf("lower limits should leave valid existing plan unchanged: %+v %v", current, err)
	}
	// HTTP preview may have succeeded under earlier limits. The final SQL
	// transaction must validate its configuration against the current limits.
	larger := config
	larger.RepeatCount = 4
	t.Run("put", func(t *testing.T) {
		if _, err := repo.UpdateQuestionAnswerSchedule(ctx, plan.UserID, plan.AdminAccountID, plan.ID, larger, QuestionAnswerScheduleSelectionSnapshot{}, plan.Version, func() time.Time { return now }); err == nil {
			t.Fatal("PUT accepted stale-limit matrix")
		}
	})
	t.Run("create_paused", func(t *testing.T) {
		if _, err := repo.CreateQuestionAnswerSchedule(ctx, plan.UserID, plan.AdminAccountID, larger, false, QuestionAnswerScheduleSelectionSnapshot{}, func() time.Time { return now }); err == nil {
			t.Fatal("paused create accepted stale-limit matrix")
		}
	})
}
