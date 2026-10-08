package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL is opt-in through the existing isolated-schema helper. These
// contracts exercise persisted outcomes, not SQL implementation strings.
func c2ScheduleRepository(t *testing.T) (*Repository, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return repo, pool, ctx
}

func c2ScheduleFixture(t *testing.T, repo *Repository, ctx context.Context, workspace string, now time.Time, enabled bool) *QuestionAnswerSchedule {
	t.Helper()
	config := QuestionAnswerScheduleConfig{Name: "C2 FAKE contract", TargetMode: "accounts", SelectedGroupIDs: []string{}, SelectedAccountTargetIDs: []string{"sub2api:w:1"}, Models: []string{"m"}, QuestionIDs: []string{"q"}, ReasoningEffort: "high", RepeatCount: 1, PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
	selection := QuestionAnswerScheduleSelectionSnapshot{TargetPreview: []QuestionAnswerSchedulePreviewTarget{}, PreviewedAt: now, EstimatedTargetCount: 1, EstimatedRequestsPerTarget: 1, EstimatedRequestsPerExecution: 1}
	s, err := repo.CreateQuestionAnswerSchedule(ctx, "u", workspace, config, enabled, selection, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestC2QuestionAnswerDueTransactionGoldenSlotsAndReentry(t *testing.T) {
	cases := []struct {
		name, now string
		grace     int
		missed    int
		through   string
		retained  bool
	}{
		{"inclusive_older_also_in_grace", "2026-10-08T08:30:00", 30, 1, "2026-10-08T08:00:00", true},
		{"one_second_before_next", "2026-10-08T08:29:59", 30, 0, "", true},
		{"one_second_after_next", "2026-10-08T08:30:01", 30, 1, "2026-10-08T08:00:00", true},
		{"zero_grace_exact", "2026-10-08T08:00:00", 0, 0, "", true},
		{"zero_grace_late", "2026-10-08T08:00:01", 0, 1, "2026-10-08T08:00:00", false},
		{"all_slots_missed", "2026-10-08T08:36:00", 5, 2, "2026-10-08T08:30:00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			base := c2FixedSingapore(t, "2026-10-08T07:59:00")
			now := c2FixedSingapore(t, tc.now)
			limits := defaultQuestionAnswerScheduleLimits()
			limits.ScheduleLateGraceMinutes = tc.grace
			if _, err := repo.SaveQuestionAnswerScheduleLimits(ctx, "u", "w", limits, 0, func() time.Time { return base }); err != nil {
				t.Fatal(err)
			}
			plan := c2ScheduleFixture(t, repo, ctx, "w", base, true)
			clockCalls := 0
			items, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", plan.ID, func() time.Time { clockCalls++; return now })
			if err != nil {
				t.Fatal(err)
			}
			wantRows := 0
			if tc.missed > 0 {
				wantRows++
			}
			if tc.retained {
				wantRows++
			}
			if len(items) != wantRows || clockCalls != 1 {
				t.Fatalf("rows=%+v clock calls=%d", items, clockCalls)
			}
			for _, e := range items {
				if e.ConfigSnapshot.Requested.Limits.ScheduleLateGraceMinutes != tc.grace {
					t.Fatal("accepted limits differ from locked grace")
				}
				if e.Status == "skipped" {
					if e.StatusReason != "misfire_no_catchup" || e.MissedCount != tc.missed || e.MissedFrom == nil || e.MissedThrough == nil || !e.ScheduledFor.Equal(*e.MissedFrom) || !e.MissedFrom.Equal(c2FixedSingapore(t, "2026-10-08T08:00:00")) || !e.MissedThrough.Equal(c2FixedSingapore(t, tc.through)) {
						t.Fatalf("wrong aggregate %+v", e)
					}
				} else if e.Status != "pending" || !tc.retained || e.MissedCount != 0 {
					t.Fatalf("wrong retained row %+v", e)
				}
			}
			after, err := repo.GetQuestionAnswerSchedule(ctx, "u", "w", plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != plan.Version || after.NextRunAt == nil || !after.NextRunAt.After(now) || !after.Enabled {
				t.Fatalf("cursor changed config version or choice: %+v", after)
			}
			repeated, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", plan.ID, func() time.Time { return now })
			if err != nil || len(repeated) != 0 {
				t.Fatalf("reentry=%+v error=%v", repeated, err)
			}
			var rows int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedule_executions`).Scan(&rows); err != nil || rows != wantRows {
				t.Fatalf("history=%d error=%v", rows, err)
			}
		})
	}
}

func TestC2QuestionAnswerDueTransactionRollbackAndOverlap(t *testing.T) {
	repo, pool, ctx := c2ScheduleRepository(t)
	base := c2FixedSingapore(t, "2026-10-08T07:59:00")
	now := c2FixedSingapore(t, "2026-10-08T08:30:00")
	limits := defaultQuestionAnswerScheduleLimits()
	limits.ScheduleLateGraceMinutes = 30
	if _, err := repo.SaveQuestionAnswerScheduleLimits(ctx, "u", "w", limits, 0, func() time.Time { return base }); err != nil {
		t.Fatal(err)
	}
	plan := c2ScheduleFixture(t, repo, ctx, "w", base, true)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION c2_reject_cursor() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.next_run_at IS DISTINCT FROM OLD.next_run_at THEN RAISE EXCEPTION 'FAKE C2 transaction rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER c2_cursor_failure BEFORE UPDATE ON connection_health_question_answer_schedules FOR EACH ROW EXECUTE FUNCTION c2_reject_cursor()`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", plan.ID, func() time.Time { return now }); err == nil {
		t.Fatal("injected cursor failure succeeded")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedule_executions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial history count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER c2_cursor_failure ON connection_health_question_answer_schedules`); err != nil {
		t.Fatal(err)
	}
	accepted, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "same-request", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	items, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", plan.ID, func() time.Time { return now })
	if err != nil || len(items) != 2 {
		t.Fatalf("rows=%+v err=%v", items, err)
	}
	for _, e := range items {
		if e.Status != "skipped" {
			t.Fatalf("second active admitted: %+v", e)
		}
		if e.MissedCount == 0 && e.StatusReason != "previous_execution_active" {
			t.Fatalf("retained overlap=%+v", e)
		}
	}
	paused, err := repo.SetQuestionAnswerScheduleState(ctx, "u", "w", plan.ID, "disable", "", plan.Version, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.SetQuestionAnswerScheduleState(ctx, "u", "w", plan.ID, "delete", "", paused.Version, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil {
		t.Fatal("not soft deleted")
	}
	again, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "same-request", func() time.Time { return now.Add(time.Minute) })
	if err != nil || again.ID != accepted.ID {
		t.Fatalf("idempotent deleted retry=%+v err=%v", again, err)
	}
	if _, err = repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "different-request", func() time.Time { return now }); err == nil {
		t.Fatal("deleted plan accepted new request")
	}
	claimed, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now })
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("accepted execution stopped by plan delete: %+v err=%v", claimed, err)
	}
}

func TestC2QuestionAnswerConcurrentRunNowAndDueSingleOwner(t *testing.T) {
	repo, pool, ctx := c2ScheduleRepository(t)
	base := c2FixedSingapore(t, "2026-10-08T07:59:00")
	now := c2FixedSingapore(t, "2026-10-08T08:00:00")
	plan := c2ScheduleFixture(t, repo, ctx, "w", base, true)
	start := make(chan struct{})
	results := make(chan error, 5)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, string(rune('a'+n)), func() time.Time { return now })
			results <- err
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", plan.ID, func() time.Time { return now })
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		var conflict *QuestionAnswerScheduleConflictError
		if err != nil && (!errors.As(err, &conflict) || conflict.Key != ErrorQuestionAnswerScheduleActive || conflict.ActiveExecutionID == "") {
			t.Fatalf("unexpected concurrent result %v", err)
		}
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedule_executions WHERE status IN ('pending','active')`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("nonterminal=%d err=%v", active, err)
	}
	if other, err := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "other", "missing"); err != nil || other != nil {
		t.Fatalf("foreign lookup=%+v err=%v", other, err)
	}
}

func TestC2QuestionAnswerClaimFIFOExpiryAndActiveCapacity(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := c2FixedSingapore(t, "2026-10-08T08:00:00")
	limits := defaultQuestionAnswerScheduleLimits()
	limits.MaxActiveScheduleExecutions = 1
	limits.ScheduleExecutionTimeoutMinutes = 30
	if _, err := repo.SaveQuestionAnswerScheduleLimits(ctx, "u", "w", limits, 0, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	p1 := c2ScheduleFixture(t, repo, ctx, "w", now, false)
	p2 := c2ScheduleFixture(t, repo, ctx, "w", now, false)
	e1, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", p1.ID, "r1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	e2, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", p2.ID, "r2", func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now.Add(time.Minute) })
	if err != nil || first == nil || first.ID != e1.ID {
		t.Fatalf("FIFO %+v err=%v", first, err)
	}
	second, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now.Add(2 * time.Minute) })
	if err != nil || second != nil {
		t.Fatalf("active cap leaked %+v err=%v", second, err)
	}
	if _, err = repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now.Add(31 * time.Minute) }); err != nil {
		t.Fatal(err)
	}
	expired, err := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e2.ID)
	if err != nil || expired.Status != "skipped" || expired.StatusReason != "execution_timeout_before_start" || expired.TerminationCause != "execution_timeout" || expired.RequestRecordCount != 0 || expired.ConfigSnapshot.Resolved != nil {
		t.Fatalf("expired %+v err=%v", expired, err)
	}
}

func c2MissingOutcome(e *QuestionAnswerScheduleExecution, now time.Time) QuestionAnswerSchedulePreparationOutcome {
	id := e.ConfigSnapshot.Requested.Schedule.SelectedAccountTargetIDs[0]
	return QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "all_accounts_missing", BlockedReason: "all_accounts_missing", Resolved: &QuestionAnswerScheduleResolved{Questions: []TestQuestion{}, ResolvedAt: now, MissingTargetIDs: []string{id}}, Targets: []QuestionAnswerScheduleExecutionTarget{{ID: "missing-" + e.ID, ExecutionID: e.ID, TargetID: id, AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: "1", AccountName: "FAKE deleted"}, MatchedGroupsSnapshot: []QuestionAnswerScheduleGroupRef{}, RequestedModels: []string{"m"}, AvailableModels: []string{}, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, Status: "failed", StatusReason: "account_not_found", CreatedAt: now, UpdatedAt: now}}}
}

func TestC2QuestionAnswerAllMissingAtomicEvidenceVersionAndTermination(t *testing.T) {
	for _, scenario := range []string{"missing", "new_version", "cancel_first", "timeout_first", "commit_failure"} {
		t.Run(scenario, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			now := c2FixedSingapore(t, "2026-10-08T08:00:00")
			plan := c2ScheduleFixture(t, repo, ctx, "w", now, true)
			e, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "r", func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			e, err = repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now })
			if err != nil || e == nil {
				t.Fatalf("claim %v", err)
			}
			if scenario == "new_version" {
				if _, err = repo.UpdateQuestionAnswerSchedule(ctx, "u", "w", plan.ID, plan.QuestionAnswerScheduleConfig, plan.TargetSelectionSnapshot, plan.Version, func() time.Time { return now }); err != nil {
					t.Fatal(err)
				}
			}
			cause := ""
			if scenario == "cancel_first" {
				cause = "user_cancel"
			}
			if scenario == "timeout_first" {
				cause = "execution_timeout"
				now = now.Add(361 * time.Minute)
			}
			if cause != "" {
				v := e.Version
				result, err := repo.DecideQuestionAnswerScheduleTermination(ctx, "u", "w", e.ID, cause, &v, func() time.Time { return now })
				if err != nil || !result.Accepted {
					t.Fatalf("termination %+v err=%v", result, err)
				}
			}
			if scenario == "commit_failure" {
				if _, err = pool.Exec(ctx, `CREATE FUNCTION c2_reject_terminal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status NOT IN ('pending','active') THEN RAISE EXCEPTION 'FAKE C2 commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER c2_terminal_failure BEFORE UPDATE ON connection_health_question_answer_schedule_executions FOR EACH ROW EXECUTE FUNCTION c2_reject_terminal()`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := repo.FinishQuestionAnswerSchedulePreparation(ctx, "u", "w", e.ID, c2MissingOutcome(e, now), func() time.Time { return now })
			if scenario == "commit_failure" {
				if err == nil {
					t.Fatal("injected failure succeeded")
				}
				current, x := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID)
				if x != nil || current.Status != "active" || len(current.Targets) != 0 || current.ConfigSnapshot.Resolved != nil {
					t.Fatalf("half committed %+v err=%v", current, x)
				}
				if _, err = pool.Exec(ctx, `DROP TRIGGER c2_terminal_failure ON connection_health_question_answer_schedule_executions`); err != nil {
					t.Fatal(err)
				}
				result, err = repo.FinishQuestionAnswerSchedulePreparation(ctx, "u", "w", e.ID, c2MissingOutcome(e, now), func() time.Time { return now })
			}
			if err != nil {
				t.Fatal(err)
			}
			if cause != "" {
				// Preparation cannot finalize accepted termination. The coordinator
				// supplies its separate, verified calls/finalizer-exited proof.
				if result.Status != "active" || result.TerminationCause != cause || result.ConfigSnapshot.Resolved != nil {
					t.Fatalf("preparation replaced accepted termination %+v", result)
				}
				result, err = repo.FinishQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID, true, "", func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err = repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cause != "" {
				want := "cancelled"
				if cause == "execution_timeout" {
					want = "skipped"
				}
				if result.Status != want || result.TerminationCause != cause || len(result.Targets) != 0 || result.ConfigSnapshot.Resolved != nil {
					t.Fatalf("missing facts overwrote cause %+v", result)
				}
			} else if result.Status != "skipped" || result.StatusReason != "all_accounts_missing" || result.ReservedRequestCount != 0 || result.RequestRecordCount != 0 || result.ConfigSnapshot.Resolved == nil || len(result.Targets) != 1 || result.Targets[0].Status != "failed" || result.Targets[0].StatusReason != "account_not_found" || result.Targets[0].BatchAvailable {
				t.Fatalf("missing evidence %+v", result)
			}
			current, err := repo.GetQuestionAnswerSchedule(ctx, "u", "w", plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cause == "" && scenario != "new_version" {
				if current.BlockedReason != "all_accounts_missing" || current.NextRunAt != nil || !current.Enabled || len(current.SelectedAccountTargetIDs) != 1 {
					t.Fatalf("missing plan state %+v", current)
				}
			} else if current.BlockedReason != "" {
				t.Fatalf("old execution overwrote plan %+v", current)
			}
			again, err := repo.FinishQuestionAnswerSchedulePreparation(ctx, "u", "w", e.ID, c2MissingOutcome(e, now), func() time.Time { return now })
			if err != nil || again.Status != result.Status || again.Version != result.Version {
				t.Fatalf("terminal retry %+v err=%v", again, err)
			}
		})
	}
}

func TestC2QuestionAnswerFrozenExecutionIgnoresEditedQuestionsAndProtocol(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := time.Now().UTC()
	question, err := repo.CreateTestQuestion(ctx, "u", "Original", "Frozen original body", []string{"FROZEN"})
	if err != nil {
		t.Fatal(err)
	}
	plan := c2ScheduleFixture(t, repo, ctx, "w", now, false)
	config := plan.QuestionAnswerScheduleConfig
	config.QuestionIDs = []string{question.ID}
	config.SelectedAccountTargetIDs = []string{"sub2api:w:1", "sub2api:w:2"}
	plan, err = repo.UpdateQuestionAnswerSchedule(ctx, "u", "w", plan.ID, config, plan.TargetSelectionSnapshot, plan.Version, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "g", &GroupTestConfiguration{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	accepted, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "freeze", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	e, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now })
	if err != nil || e == nil || e.ID != accepted.ID {
		t.Fatalf("claim=%+v err=%v", e, err)
	}
	questions, _, err := repo.ReadQuestionAnswerSchedulePreparationConfiguration(ctx, "u", "w", config.QuestionIDs)
	if err != nil {
		t.Fatal(err)
	}
	resolved := QuestionAnswerScheduleResolved{Questions: questions, ResolvedAt: now, MissingTargetIDs: []string{}}
	targets := []QuestionAnswerScheduleExecutionTarget{}
	for i, id := range config.SelectedAccountTargetIDs {
		batchID, err := newID()
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, QuestionAnswerScheduleExecutionTarget{ID: batchID, ExecutionID: e.ID, TargetID: id, AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: string(rune('1' + i)), AccountName: "FAKE account", Platform: "sub2api"}, MatchedGroupsSnapshot: []QuestionAnswerScheduleGroupRef{{ID: "g", Name: "Frozen group"}}, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "w", InventoryComplete: true, Memberships: []TestConfigurationSource{{AdminGroupID: "g", AdminGroupName: "Frozen group", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}}}, Protocol: TestProtocolResponses}, RequestedModels: []string{"m"}, AvailableModels: []string{"m"}, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, PlannedRequestCount: 1, Status: "pending"})
	}
	if reason, err := repo.FreezeQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID, resolved, targets, func() time.Time { return now }); err != nil || reason != "" {
		t.Fatalf("freeze=%s err=%v", reason, err)
	}
	a, created, err := repo.CreateScheduledQuestionAnswerBatch(ctx, "u", "w", e.ID, targets[0].TargetID, targets[0].ID)
	if err != nil || !created || len(a) != 1 {
		t.Fatalf("batch A=%+v created=%t err=%v", a, created, err)
	}
	changedKeywords := []string{"CHANGED"}
	if _, err = repo.UpdateTestQuestion(ctx, "u", question.ID, "Changed", "Changed body", &changedKeywords); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SetTestQuestionEnabled(ctx, "u", question.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "g", &GroupTestConfiguration{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	// Reopening a repository models the persistence portion of restart. It has
	// no preparation state or cached current question/configuration collection.
	recovered := NewRepository(repo.db)
	b, created, err := recovered.CreateScheduledQuestionAnswerBatch(ctx, "u", "w", e.ID, targets[1].TargetID, targets[1].ID)
	if err != nil || !created || len(b) != 1 {
		t.Fatalf("recovered batch B=%+v created=%t err=%v", b, created, err)
	}
	for _, records := range [][]QuestionAnswerRecord{a, b} {
		for _, record := range records {
			if record.QuestionName != "Original" || record.QuestionBody != "Frozen original body" || len(record.QuestionKeywordSnapshot) != 1 || record.QuestionKeywordSnapshot[0] != "FROZEN" || record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolResponses {
				t.Fatalf("current configuration leaked into execution %+v", record)
			}
		}
	}
	changedResolved := QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: question.ID, Body: "Forbidden second freeze"}}, ResolvedAt: now}
	if reason, err := recovered.FreezeQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID, changedResolved, nil, func() time.Time { return now }); err != nil || reason != "" {
		t.Fatalf("freeze retry=%s err=%v", reason, err)
	}
	current, err := recovered.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID)
	if err != nil || current.ReservedRequestCount != 2 || len(current.Targets) != 2 || current.ConfigSnapshot.Resolved.Questions[0].Body != "Frozen original body" {
		t.Fatalf("refreeze changed accepted facts %+v err=%v", current, err)
	}
}

func TestC2QuestionAnswerTerminalRecordsNeedSuccessfulFinalizerProof(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := time.Now().UTC()
	e, target := c2StoragePreparedExecution(t, repo, now)
	records, _, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if changed, err := repo.MarkQuestionAnswerRunning(ctx, e.UserID, target.ID, record.ID); err != nil || !changed {
			t.Fatalf("running=%t err=%v", changed, err)
		}
		judgment := QuestionAnswerCorrect
		source := QuestionAnswerJudgmentAutomatic
		if changed, err := repo.CompleteQuestionAnswer(ctx, e.UserID, target.ID, record.ID, QuestionAnswerCompletion{Status: QuestionAnswerSucceeded, AnswerBody: "original", AnswerJudgment: &judgment, JudgmentSource: &source}); err != nil || !changed {
			t.Fatalf("complete=%t err=%v", changed, err)
		}
	}
	current, err := repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "active" || current.Stats.Requests.InProgress != 0 || current.Stats.Requests.Succeeded != 2 {
		t.Fatalf("wrong persisted C1 facts %+v", current)
	}
	for _, cause := range []string{"user_cancel", "execution_timeout"} {
		v := current.Version
		result, err := repo.DecideQuestionAnswerScheduleTermination(ctx, e.UserID, e.AdminAccountID, e.ID, cause, &v, func() time.Time { return now.Add(361 * time.Minute) })
		if err != nil || result.Accepted || !result.Conflict || result.Execution.TerminationCause != "" || result.Execution.Status != "active" {
			t.Fatalf("terminal records replaced by %s: %+v err=%v", cause, result, err)
		}
		current, err = repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = repo.FinishQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, false, "c1_finalization_failed", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	current, err = repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil || !current.Active || current.StatusReason != "c1_finalization_failed" || current.TerminationCause != "" || current.CompletedAt != nil {
		t.Fatalf("failed finalizer released execution %+v err=%v", current, err)
	}
	if _, err = repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ScheduleID, "cannot-steal-slot", func() time.Time { return now }); err == nil {
		t.Fatal("same plan restarted before reliable finalization")
	}
	if _, err = repo.FinishQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, true, "", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	current, err = repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
	if err != nil || current.Status != "completed" || current.Active || current.TerminationCause != "" || current.Stats.Reviews.Correct != 2 {
		t.Fatalf("normal finalization %+v err=%v", current, err)
	}
}
