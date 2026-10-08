package connection_health

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/upstream"
)

// These regressions reproduce independent Review findings against
// the frozen implementation. PostgreSQL remains explicitly opt-in and isolated.
func c2ReviewAssertFacts(t *testing.T, e QuestionAnswerScheduleExecution, target string) {
	t.Helper()
	if e.RequestRecordCount != 2 || e.BatchCreatedTargetCount != 1 || len(e.Targets) != 1 || e.Targets[0].TargetID != target || !e.Targets[0].BatchAvailable || e.Targets[0].BatchID != e.Targets[0].ID || e.Stats.Requests.Submitted != 2 || e.Stats.Requests.Succeeded != 1 || e.Stats.Reviews.Correct != 1 || e.Targets[0].Stats.Reviews.Correct != 1 {
		t.Fatalf("authoritative response lost C1 facts: %+v", e)
	}
}

func TestC2QuestionAnswerReviewCancelReturnsAuthoritativeFacts(t *testing.T) {
	for _, branch := range []string{"accepted", "stale_version", "already_cancelled", "terminal", "timeout_already_accepted", "business_done_finalizer_pending"} {
		t.Run(branch, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			now := time.Now().UTC()
			e, target := c2StoragePreparedExecution(t, repo, now)
			records, _, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
			if err != nil || len(records) != 2 {
				t.Fatalf("batch fixture: %d %v", len(records), err)
			}
			if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='succeeded',answer_body='FAKE preserved answer',answer_judgment='correct' WHERE id=$1`, records[0].ID); err != nil {
				t.Fatal(err)
			}
			if branch == "terminal" || branch == "business_done_finalizer_pending" {
				if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='failed',error_type='server_error' WHERE id=$1`, records[1].ID); err != nil {
					t.Fatal(err)
				}
			}
			if branch == "terminal" {
				if _, err = repo.FinishQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID, true, "", func() time.Time { return now }); err != nil {
					t.Fatal(err)
				}
			}
			current, err := repo.GetQuestionAnswerScheduleExecution(ctx, e.UserID, e.AdminAccountID, e.ID)
			if err != nil {
				t.Fatal(err)
			}
			version := current.Version
			if branch == "stale_version" {
				version++
			}
			if branch == "already_cancelled" || branch == "timeout_already_accepted" {
				cause, at := "user_cancel", now
				if branch == "timeout_already_accepted" {
					cause, at = "execution_timeout", e.deadline()
				}
				if _, err = repo.DecideQuestionAnswerScheduleTermination(ctx, e.UserID, e.AdminAccountID, e.ID, cause, &version, func() time.Time { return at }); err != nil {
					t.Fatal(err)
				}
			}
			result, err := repo.DecideQuestionAnswerScheduleTermination(ctx, e.UserID, e.AdminAccountID, e.ID, "user_cancel", &version, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			c2ReviewAssertFacts(t, result.Execution, target.TargetID)
			if branch == "accepted" && (!result.Accepted || result.Conflict || result.Execution.TerminationCause != "user_cancel") {
				t.Fatalf("accepted cancellation: %+v", result)
			}
			if branch == "already_cancelled" && (result.Accepted || result.Conflict || result.Execution.TerminationCause != "user_cancel") {
				t.Fatalf("idempotent replay: %+v", result)
			}
			if branch != "accepted" && branch != "already_cancelled" && !result.Conflict {
				t.Fatalf("expected authoritative conflict: %+v", result)
			}
		})
	}
}

type c2ReviewQueryTraceKey struct{}
type c2ReviewQueryTrace struct{ after func(context.Context, string) }

func (h *c2ReviewQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, c2ReviewQueryTraceKey{}, data.SQL)
}
func (h *c2ReviewQueryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	h.after(ctx, ctx.Value(c2ReviewQueryTraceKey{}).(string))
}

func TestC2QuestionAnswerReviewParentCascadeDuringPlanRead(t *testing.T) {
	for _, endpoint := range []string{"detail", "list"} {
		t.Run(endpoint, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			ctx := t.Context()
			if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('c2-storage-workspace','c2-storage-user')`); err != nil {
				t.Fatal(err)
			}
			repo := NewRepository(pool)
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			e, target := c2StoragePreparedExecution(t, repo, time.Now().UTC())
			records, _, err := repo.CreateScheduledQuestionAnswerBatch(ctx, e.UserID, e.AdminAccountID, e.ID, target.TargetID, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='succeeded',answer_body='FAKE preserved answer',answer_judgment='correct' WHERE id=$1`, records[0].ID); err != nil {
				t.Fatal(err)
			}
			var fired atomic.Bool
			var deletionError error
			config := pool.Config().Copy()
			config.ConnConfig.Tracer = &c2ReviewQueryTrace{after: func(traceCtx context.Context, sql string) {
				if strings.Contains(sql, "FROM connection_health_question_answer_schedule_executions") && strings.Contains(sql, "ORDER BY created_at DESC,id DESC LIMIT 1") && fired.CompareAndSwap(false, true) {
					_, deletionError = pool.Exec(traceCtx, `DELETE FROM admin_accounts WHERE id=$1`, e.AdminAccountID)
				}
			}}
			readerPool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("cannot create isolated query-trace pool")
			}
			t.Cleanup(readerPool.Close)
			readerRepo := NewRepository(readerPool)
			var plan *QuestionAnswerSchedule
			var readError error
			var panicValue any
			func() {
				defer func() { panicValue = recover() }()
				if endpoint == "detail" {
					plan, readError = readerRepo.GetQuestionAnswerSchedule(ctx, e.UserID, e.AdminAccountID, e.ScheduleID)
				} else {
					page, err := readerRepo.ListQuestionAnswerSchedules(ctx, e.UserID, e.AdminAccountID, "active", 1, 20)
					readError = err
					if len(page.Items) == 1 {
						plan = &page.Items[0]
					}
				}
			}()
			if !fired.Load() || deletionError != nil {
				t.Fatalf("cascade window was not exercised: %v %v", fired.Load(), deletionError)
			}
			if panicValue != nil {
				t.Fatalf("late parent deletion panicked: %v", panicValue)
			}
			if readError != nil || plan == nil || plan.LastExecution == nil || plan.LastActualTargetCount != 1 {
				t.Fatalf("inconsistent read snapshot: %+v %v", plan, readError)
			}
			c2ReviewAssertFacts(t, *plan.LastExecution, target.TargetID)
			missing, err := readerRepo.GetQuestionAnswerSchedule(ctx, e.UserID, e.AdminAccountID, e.ScheduleID)
			if err != nil || missing != nil {
				t.Fatalf("following read resurrected deleted plan: %+v %v", missing, err)
			}
		})
	}
}

func c2ReviewBlockWorkspace(t *testing.T, repo *Repository, pool *pgxpool.Pool, ctx context.Context, user, workspace string) (func(), func()) {
	t.Helper()
	holder, err := repo.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	var pid int32
	if err = holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var blocked bool
			err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks waiter JOIN pg_locks owner ON waiter.locktype=owner.locktype AND waiter.database=owner.database AND waiter.classid=owner.classid AND waiter.objid=owner.objid AND waiter.objsubid=owner.objsubid WHERE owner.pid=$1 AND owner.granted AND NOT waiter.granted AND waiter.locktype='advisory')`, pid).Scan(&blocked)
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("worker never reached the actual workspace lock")
	}
	return wait, func() {
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

type c2ReviewPreviewRepository struct{ *Repository }

func (r *c2ReviewPreviewRepository) ReadQuestionAnswerSchedulePreparationConfiguration(context.Context, string, string, []string) ([]TestQuestion, []GroupTestConfig, error) {
	return []TestQuestion{{ID: "q", Enabled: true}}, []GroupTestConfig{}, nil
}
func c2ReviewService(t *testing.T, repo *Repository, clock *atomic.Int64) *Service {
	t.Helper()
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{}, credByAccount: map[string]upstream.ProbeCredential{}}, allAccounts: []upstream.AdminGroupAccountInfo{{ID: "1", Name: "FAKE C2 Review", Status: "inactive"}}}
	s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	s.accounts = fakeAdminAccountResolver{id: "w"}
	s.questionAnswerSchedules = &c2ReviewPreviewRepository{Repository: repo}
	s.questionAnswers = newFakeQuestionAnswerRepository()
	s.questionAnswerScheduleCtx, s.questionAnswerScheduleCancel = context.WithCancel(context.Background())
	s.questionAnswerScheduleHandles = map[string]*questionAnswerScheduleHandle{}
	s.questionAnswerScheduleNow = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.ShutdownQuestionAnswers(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return s
}

func TestC2QuestionAnswerReviewStrictFutureAfterWorkspaceWait(t *testing.T) {
	for _, action := range []string{"create", "update", "enable", "revalidate", "limits_recovery"} {
		t.Run(action, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			before := c2FixedSingapore(t, "2026-10-08T08:29:59")
			after := before.Add(2 * time.Second)
			plan := c2ScheduleFixture(t, repo, ctx, "w", before, true)
			if action == "enable" {
				var err error
				plan, err = repo.SetQuestionAnswerScheduleState(ctx, "u", "w", plan.ID, "disable", "", plan.Version, func() time.Time { return before })
				if err != nil {
					t.Fatal(err)
				}
			}
			if action == "revalidate" || action == "limits_recovery" {
				if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason='target_limit',next_run_at=NULL WHERE id=$1`, plan.ID); err != nil {
					t.Fatal(err)
				}
			}
			var clock atomic.Int64
			clock.Store(before.UnixNano())
			service := c2ReviewService(t, repo, &clock)
			wait, release := c2ReviewBlockWorkspace(t, repo, pool, ctx, "u", "w")
			type result struct {
				plan QuestionAnswerSchedule
				err  error
			}
			finished := make(chan result, 1)
			go func() {
				var p QuestionAnswerSchedule
				var err error
				switch action {
				case "create":
					p, err = service.CreateQuestionAnswerSchedule(ctx, "u", QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: plan.QuestionAnswerScheduleConfig, Enabled: boolPointer(true)})
				case "update":
					p, err = service.UpdateQuestionAnswerSchedule(ctx, "u", plan.ID, QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: plan.QuestionAnswerScheduleConfig, ExpectedVersion: &plan.Version})
				case "enable", "revalidate":
					p, err = service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, action, plan.Version)
				case "limits_recovery":
					_, err = service.SaveQuestionAnswerScheduleLimits(ctx, "u", defaultQuestionAnswerScheduleLimits(), 0)
					if err == nil {
						p, err = service.GetQuestionAnswerSchedule(ctx, "u", plan.ID)
					}
				}
				finished <- result{p, err}
			}()
			wait()
			clock.Store(after.UnixNano())
			release()
			got := <-finished
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.plan.NextRunAt == nil || !got.plan.NextRunAt.After(after) || !got.plan.Enabled {
				t.Fatalf("lock wait persisted an already due grid: %+v; commit clock=%v", got.plan, after)
			}
		})
	}
}

func TestC2QuestionAnswerReviewPreparationDeadlineAfterWorkspaceWait(t *testing.T) {
	for _, outcomeKind := range []string{"freeze", "preparation_failed", "all_accounts_missing"} {
		t.Run(outcomeKind, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			base := time.Now().UTC()
			plan := c2ScheduleFixture(t, repo, ctx, "w", base, false)
			e, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "review-deadline", func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			e, err = repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(e.deadline().Add(-time.Second).UnixNano())
			service := c2ReviewService(t, repo, &clock)
			h := service.ensureQuestionAnswerScheduleHandle(*e)
			h.prepareDone = make(chan struct{})
			close(h.prepareDone)
			resolved := QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "q", Body: "FAKE frozen"}}, ResolvedAt: base, MissingTargetIDs: []string{}}
			target := QuestionAnswerScheduleExecutionTarget{ID: "review-target", ExecutionID: e.ID, TargetID: "sub2api:w:1", AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: "1", Platform: "sub2api"}, MatchedGroupsSnapshot: []QuestionAnswerScheduleGroupRef{}, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "w", InventoryComplete: true, Memberships: []TestConfigurationSource{}}, Protocol: TestProtocolChatCompletions}, RequestedModels: []string{"m"}, AvailableModels: []string{"m"}, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, Status: "pending", PlannedRequestCount: 1}
			h.preparation.outcome = QuestionAnswerSchedulePreparationOutcome{Resolved: &resolved, Targets: []QuestionAnswerScheduleExecutionTarget{target}}
			if outcomeKind == "preparation_failed" {
				h.preparation.outcome = QuestionAnswerSchedulePreparationOutcome{Status: "failed", Reason: "inventory_unavailable"}
			}
			if outcomeKind == "all_accounts_missing" {
				resolved.MissingTargetIDs = []string{target.TargetID}
				target.Status = "failed"
				target.StatusReason = "account_not_found"
				target.PlannedRequestCount = 0
				target.AvailableModels = []string{}
				target.TestConfigurationSnapshot = QuestionAnswerScheduleTargetConfiguration{}
				h.preparation.outcome = QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "all_accounts_missing", BlockedReason: "all_accounts_missing", Resolved: &resolved, Targets: []QuestionAnswerScheduleExecutionTarget{target}}
			}
			wait, release := c2ReviewBlockWorkspace(t, repo, pool, ctx, "u", "w")
			finished := make(chan error, 1)
			go func() { finished <- service.reconcileQuestionAnswerScheduleHandle(ctx, h, *e) }()
			wait()
			clock.Store(e.deadline().Add(time.Second).UnixNano())
			release()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "active" || got.TerminationCause != "execution_timeout" || got.ConfigSnapshot.Resolved != nil || got.ReservedRequestCount != 0 || len(got.Targets) != 0 {
				t.Fatalf("expired preparation committed evidence/budget or premature terminal: %+v", got)
			}
			current, err := repo.GetQuestionAnswerSchedule(ctx, "u", "w", plan.ID)
			if err != nil || current.BlockedReason != "" {
				t.Fatalf("expired preparation blocked plan: %+v %v", current, err)
			}
		})
	}
}

func TestC2QuestionAnswerReviewParentCascadeBeforeSavedMutation(t *testing.T) {
	for _, action := range []string{"update", "enable", "disable", "revalidate", "delete"} {
		t.Run(action, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			ctx := t.Context()
			if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('w','u')`); err != nil {
				t.Fatal(err)
			}
			repo := NewRepository(pool)
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			plan := c2ScheduleFixture(t, repo, ctx, "w", now, false)
			var fired atomic.Bool
			var deleteError error
			config := pool.Config().Copy()
			config.ConnConfig.Tracer = &c2ReviewQueryTrace{after: func(traceCtx context.Context, sql string) {
				if strings.Contains(sql, "SELECT pg_advisory_xact_lock") && fired.CompareAndSwap(false, true) {
					_, deleteError = pool.Exec(traceCtx, `DELETE FROM admin_accounts WHERE id='w'`)
				}
			}}
			worker, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("cannot create isolated mutation query-trace pool")
			}
			t.Cleanup(worker.Close)
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			service := c2ReviewService(t, NewRepository(worker), &clock)
			if action == "update" {
				_, err = service.UpdateQuestionAnswerSchedule(ctx, "u", plan.ID, QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: plan.QuestionAnswerScheduleConfig, ExpectedVersion: &plan.Version})
			} else {
				_, err = service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, action, plan.Version)
			}
			if !fired.Load() || deleteError != nil {
				t.Fatalf("deletion window was not exercised: %v %v", fired.Load(), deleteError)
			}
			if err == nil || err.Error() != ErrorQuestionAnswerScheduleNotFound {
				t.Fatalf("vanished plan mutation claimed success or hid missing response: %v", err)
			}
		})
	}
}

// Advance the clock after actual evidence SQL, rather than depending on the
// implementation's number of clock calls. None of those writes may survive
// the deadline, including version-protected missing-account plan blocking.
func TestC2QuestionAnswerReviewDeadlineCrossesEvidenceWrites(t *testing.T) {
	for _, phase := range []string{"freeze", "preparation_failed", "all_accounts_missing"} {
		t.Run(phase, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			base := time.Now().UTC()
			plan := c2ScheduleFixture(t, repo, ctx, "w", base, false)
			e, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "review-evidence-deadline", func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			e, err = repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return base })
			if err != nil {
				t.Fatal(err)
			}
			before, after := e.deadline().Add(-time.Second), e.deadline().Add(time.Second)
			var clock atomic.Int64
			var crossed atomic.Bool
			clock.Store(before.UnixNano())
			config := pool.Config().Copy()
			config.ConnConfig.Tracer = &c2ReviewQueryTrace{after: func(_ context.Context, sql string) {
				if strings.Contains(sql, "UPDATE connection_health_question_answer_schedule_executions SET config_snapshot=") || strings.Contains(sql, "UPDATE connection_health_question_answer_schedule_executions SET status=$4,status_reason=$5,config_snapshot=") {
					clock.Store(after.UnixNano())
					crossed.Store(true)
				}
			}}
			worker, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("cannot create isolated evidence query-trace pool")
			}
			t.Cleanup(worker.Close)
			writer := NewRepository(worker)
			now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			if phase == "freeze" {
				resolved := QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "q", Body: "FAKE review frozen"}}, ResolvedAt: base, MissingTargetIDs: []string{}}
				target := QuestionAnswerScheduleExecutionTarget{ID: "review-evidence-target", ExecutionID: e.ID, TargetID: "sub2api:w:1", AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: "1", Platform: "sub2api"}, MatchedGroupsSnapshot: []QuestionAnswerScheduleGroupRef{}, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "w", InventoryComplete: true, Memberships: []TestConfigurationSource{}}, Protocol: TestProtocolChatCompletions}, RequestedModels: []string{"m"}, AvailableModels: []string{"m"}, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, PlannedRequestCount: 1, Status: "pending"}
				reason, err := writer.FreezeQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID, resolved, []QuestionAnswerScheduleExecutionTarget{target}, now)
				if err != nil || reason != "execution_timeout" {
					t.Fatalf("expired freeze accepted: %s %v", reason, err)
				}
				_, err = writer.FinishQuestionAnswerSchedulePreparation(ctx, "u", "w", e.ID, QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: reason}, now)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				outcome := QuestionAnswerSchedulePreparationOutcome{Status: "failed", Reason: "inventory_unavailable"}
				if phase == "all_accounts_missing" {
					outcome = c2MissingOutcome(e, base)
				}
				if _, err := writer.FinishQuestionAnswerSchedulePreparation(ctx, "u", "w", e.ID, outcome, now); err != nil {
					t.Fatal(err)
				}
			}
			if !crossed.Load() {
				t.Fatal("clock never crossed deadline after actual evidence SQL")
			}
			got, err := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "active" || got.TerminationCause != "execution_timeout" || got.CompletedAt != nil || got.ReservedRequestCount != 0 || got.PlannedTargetCount != 0 || got.ConfigSnapshot.Resolved != nil || len(got.Targets) != 0 || got.RequestRecordCount != 0 {
				t.Fatalf("deadline retained evidence/budget or released execution early: %+v", got)
			}
			current, err := repo.GetQuestionAnswerSchedule(ctx, "u", "w", plan.ID)
			if err != nil || current.Version != plan.Version || current.BlockedReason != "" || current.Enabled {
				t.Fatalf("expired evidence changed paused plan: %+v %v", current, err)
			}
		})
	}
}

// The actual workspace FK takes a parent row lock inside INSERT. An advisory
// guard alone cannot prevent this wait from crossing the next time grid.
func TestC2QuestionAnswerReviewCreateCursorAfterParentForeignKeyWait(t *testing.T) {
	for _, tc := range []struct {
		name, release  string
		enabled, cross bool
	}{
		{"enabled_commit_crosses", "commit", true, true},
		{"enabled_rollback_crosses", "rollback", true, true},
		{"paused_commit_crosses", "commit", false, true},
		{"paused_rollback_crosses", "rollback", false, true},
		{"enabled_commit_same_grid", "commit", true, false},
		{"enabled_rollback_same_grid", "rollback", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('w','u')`); err != nil {
				t.Fatal(err)
			}
			repo := NewRepository(pool)
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			var fk bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE contype='f' AND conrelid='connection_health_question_answer_schedules'::regclass AND confrelid='admin_accounts'::regclass)`).Scan(&fk); err != nil || !fk {
				t.Fatalf("real workspace FK missing: %v %v", fk, err)
			}
			before := c2FixedSingapore(t, "2026-10-08T08:29:59")
			after := before
			if tc.cross {
				after = before.Add(2 * time.Second)
			}
			var clock atomic.Int64
			clock.Store(before.UnixNano())
			service := c2ReviewService(t, repo, &clock)
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
			var parent string
			var holderPID int32
			if err = holder.QueryRow(ctx, `SELECT id,pg_backend_pid() FROM admin_accounts WHERE id='w' FOR UPDATE`).Scan(&parent, &holderPID); err != nil {
				t.Fatal(err)
			}
			config := QuestionAnswerScheduleConfig{Name: "FAKE parent FK clock", TargetMode: "accounts", SelectedGroupIDs: []string{}, SelectedAccountTargetIDs: []string{"sub2api:w:1"}, Models: []string{"m"}, QuestionIDs: []string{"q"}, ReasoningEffort: "high", RepeatCount: 1, PeakStart: "08:00", PeakEnd: "22:00", PeakIntervalMinutes: 30, OffPeakIntervalMinutes: 180}
			type result struct {
				plan QuestionAnswerSchedule
				err  error
			}
			finished := make(chan result, 1)
			go func() {
				plan, err := service.CreateQuestionAnswerSchedule(ctx, "u", QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: config, Enabled: boolPointer(tc.enabled)})
				finished <- result{plan, err}
			}()
			waitUntil := time.Now().Add(3 * time.Second)
			blocked := false
			for time.Now().Before(waitUntil) {
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !blocked {
				t.Fatal("create did not reach the actual parent row lock")
			}
			clock.Store(after.UnixNano())
			if tc.release == "commit" {
				err = holder.Commit(ctx)
			} else {
				err = holder.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := <-finished
			if got.err != nil {
				t.Fatal(got.err)
			}
			persisted, err := repo.GetQuestionAnswerSchedule(ctx, "u", "w", got.plan.ID)
			if err != nil || persisted == nil || got.plan.Version != 1 || persisted.Version != 1 || got.plan.Enabled != tc.enabled || !equalScheduleTime(got.plan.NextRunAt, persisted.NextRunAt) {
				t.Fatalf("created response differs from saved plan: %+v %+v %v", got.plan, persisted, err)
			}
			if tc.enabled {
				want := c2FixedSingapore(t, "2026-10-08T08:30:00")
				if tc.cross {
					want = c2FixedSingapore(t, "2026-10-08T09:00:00")
				}
				if persisted.NextRunAt == nil || !persisted.NextRunAt.Equal(want) || !persisted.NextRunAt.After(after) {
					t.Fatalf("parent FK wait saved an already due or drifting cursor: %+v; commit clock=%v want=%v", persisted, after, want)
				}
			} else if persisted.NextRunAt != nil {
				t.Fatalf("paused create acquired a cursor: %+v", persisted)
			}
			items, err := repo.ProcessDueQuestionAnswerSchedule(ctx, "u", "w", persisted.ID, func() time.Time { return after })
			if err != nil || len(items) != 0 {
				t.Fatalf("newly saved plan accepted a past slot immediately: %+v %v", items, err)
			}
			var executions, records, budget int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_question_answer_schedule_executions),(SELECT count(*) FROM connection_health_question_answer_records),(SELECT COALESCE(sum(reserved_request_count),0) FROM connection_health_question_answer_schedule_executions)`).Scan(&executions, &records, &budget); err != nil || executions != 0 || records != 0 || budget != 0 {
				t.Fatalf("save or immediate scan emitted work: executions=%d records=%d budget=%d err=%v", executions, records, budget, err)
			}
		})
	}
}

// Exercise every cursor-rebuilding entry after its actual write. The controls
// preserve a healthy revalidation cursor even when it is already due.
func TestC2QuestionAnswerReviewCursorAfterPersistedWrite(t *testing.T) {
	for _, action := range []string{"create", "update", "enable", "revalidate", "limits_recovery", "healthy_revalidate", "healthy_limits"} {
		t.Run(action, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			before := c2FixedSingapore(t, "2026-10-08T08:29:59")
			after := before.Add(2 * time.Second)
			plan := c2ScheduleFixture(t, repo, ctx, "w", before, true)
			if action == "enable" {
				var err error
				plan, err = repo.SetQuestionAnswerScheduleState(ctx, "u", "w", plan.ID, "disable", "", plan.Version, func() time.Time { return before })
				if err != nil {
					t.Fatal(err)
				}
			}
			if action == "revalidate" || action == "limits_recovery" {
				if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason='target_limit',next_run_at=NULL WHERE id=$1`, plan.ID); err != nil {
					t.Fatal(err)
				}
			}
			var clock atomic.Int64
			var crossed atomic.Bool
			clock.Store(before.UnixNano())
			unchanged := strings.HasPrefix(action, "healthy_")
			if unchanged {
				clock.Store(after.UnixNano())
			}
			config := pool.Config().Copy()
			config.ConnConfig.Tracer = &c2ReviewQueryTrace{after: func(_ context.Context, sql string) {
				if (strings.Contains(sql, "INSERT INTO connection_health_question_answer_schedules") || strings.Contains(sql, "UPDATE connection_health_question_answer_schedules SET")) && crossed.CompareAndSwap(false, true) {
					clock.Store(after.UnixNano())
				}
			}}
			worker, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("cannot create isolated cursor query-trace pool")
			}
			t.Cleanup(worker.Close)
			service := c2ReviewService(t, NewRepository(worker), &clock)
			var got QuestionAnswerSchedule
			switch action {
			case "create":
				got, err = service.CreateQuestionAnswerSchedule(ctx, "u", QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: plan.QuestionAnswerScheduleConfig, Enabled: boolPointer(true)})
			case "update":
				got, err = service.UpdateQuestionAnswerSchedule(ctx, "u", plan.ID, QuestionAnswerScheduleInput{QuestionAnswerScheduleConfig: plan.QuestionAnswerScheduleConfig, ExpectedVersion: &plan.Version})
			case "enable", "revalidate", "healthy_revalidate":
				stateAction := strings.TrimPrefix(action, "healthy_")
				got, err = service.SetQuestionAnswerScheduleState(ctx, "u", plan.ID, stateAction, plan.Version)
			case "limits_recovery", "healthy_limits":
				_, err = service.SaveQuestionAnswerScheduleLimits(ctx, "u", defaultQuestionAnswerScheduleLimits(), 0)
				if err == nil {
					got, err = service.GetQuestionAnswerSchedule(ctx, "u", plan.ID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if unchanged {
				if crossed.Load() || got.Version != plan.Version || !equalScheduleTime(got.NextRunAt, plan.NextRunAt) {
					t.Fatalf("healthy no-op abandoned cursor/version: %+v before=%+v wrote=%v", got, plan, crossed.Load())
				}
			} else {
				wantVersion := plan.Version + 1
				if action == "create" {
					wantVersion = 1
				}
				if !crossed.Load() || got.NextRunAt == nil || !got.NextRunAt.Equal(c2FixedSingapore(t, "2026-10-08T09:00:00")) || got.Version != wantVersion || !got.Enabled || got.BlockedReason != "" {
					t.Fatalf("write crossing grid retained past cursor or changed version twice: %+v crossed=%v wantVersion=%d", got, crossed.Load(), wantVersion)
				}
			}
		})
	}
}

func TestC2QuestionAnswerReviewLimitsRecoveryCohortAndCorrectionRollback(t *testing.T) {
	for _, failCorrection := range []bool{false, true} {
		name := "all_rebuilt_cursors_follow_late_cohort_write"
		if failCorrection {
			name = "correction_failure_rolls_back_limits_and_every_plan"
		}
		t.Run(name, func(t *testing.T) {
			repo, pool, ctx := c2ScheduleRepository(t)
			before := c2FixedSingapore(t, "2026-10-08T08:29:59")
			after := before.Add(2 * time.Second)
			later := c2FixedSingapore(t, "2026-10-08T09:00:01")
			plans := []*QuestionAnswerSchedule{}
			for i := 0; i < 3; i++ {
				plans = append(plans, c2ScheduleFixture(t, repo, ctx, "w", before, true))
			}
			if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason='target_limit',next_run_at=NULL,name=CASE WHEN id=$2 THEN 'FAKE cursor rollback' ELSE name END WHERE id=ANY($1::text[])`, []string{plans[0].ID, plans[1].ID}, plans[1].ID); err != nil {
				t.Fatal(err)
			}
			if failCorrection {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_c2_cursor_correction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='FAKE cursor rollback' AND NEW.next_run_at>TIMESTAMPTZ '2026-10-08 08:30:00+08' THEN RAISE EXCEPTION 'FAKE cursor correction failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_c2_cursor_correction BEFORE UPDATE ON connection_health_question_answer_schedules FOR EACH ROW EXECUTE FUNCTION fail_c2_cursor_correction()`); err != nil {
					t.Fatal(err)
				}
			}
			var clock atomic.Int64
			var planWrites, correctionWrites int
			clock.Store(before.UnixNano())
			config := pool.Config().Copy()
			config.ConnConfig.Tracer = &c2ReviewQueryTrace{after: func(_ context.Context, sql string) {
				if strings.Contains(sql, "UPDATE connection_health_question_answer_schedules SET blocked_reason=") {
					planWrites++
					if planWrites == 2 {
						clock.Store(after.UnixNano())
					}
				}
				if strings.Contains(sql, "UPDATE connection_health_question_answer_schedules SET next_run_at=") {
					correctionWrites++
					if correctionWrites == 2 && !failCorrection {
						clock.Store(later.UnixNano())
					}
				}
			}}
			worker, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("cannot create isolated cohort query-trace pool")
			}
			t.Cleanup(worker.Close)
			_, err = NewRepository(worker).SaveQuestionAnswerScheduleLimits(ctx, "u", "w", defaultQuestionAnswerScheduleLimits(), 0, func() time.Time { return time.Unix(0, clock.Load()).UTC() })
			if planWrites != 2 || correctionWrites == 0 || (err != nil) != failCorrection {
				t.Fatalf("cohort/correction window missing: planWrites=%d corrections=%d err=%v", planWrites, correctionWrites, err)
			}
			for i, plan := range plans {
				current, readErr := repo.GetQuestionAnswerSchedule(ctx, "u", "w", plan.ID)
				if readErr != nil || current == nil {
					t.Fatalf("missing cohort plan: %+v %v", current, readErr)
				}
				if i == 2 {
					if current.Version != plan.Version || !equalScheduleTime(current.NextRunAt, plan.NextRunAt) || current.BlockedReason != "" {
						t.Fatalf("healthy cohort member was changed: %+v", current)
					}
				} else if failCorrection {
					if current.Version != plan.Version || current.BlockedReason != "target_limit" || current.NextRunAt != nil {
						t.Fatalf("failed correction partially committed plan: %+v", current)
					}
				} else if current.Version != plan.Version+1 || current.BlockedReason != "" || current.NextRunAt == nil || !current.NextRunAt.Equal(c2FixedSingapore(t, "2026-10-08T09:30:00")) || !current.NextRunAt.After(later) {
					t.Fatalf("late cohort write left expired cursor or extra version: %+v", current)
				}
			}
			limits, err := repo.GetQuestionAnswerScheduleLimits(ctx, "u", "w")
			if err != nil || (failCorrection && limits.Version != 0) || (!failCorrection && limits.Version != 1) || limits.TodayReservedRequests != 0 {
				t.Fatalf("settings/budget not atomic with cohort: %+v %v", limits, err)
			}
			var executions, records int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_question_answer_schedule_executions),(SELECT count(*) FROM connection_health_question_answer_records)`).Scan(&executions, &records); err != nil || executions != 0 || records != 0 {
				t.Fatalf("cohort edit emitted work: executions=%d records=%d err=%v", executions, records, err)
			}
		})
	}
}
