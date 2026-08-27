package connection_health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const questionAnswerPostgresTimeout = 15 * time.Second

func TestQuestionAnswerRepositoryPostgresContract(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema first run: %v", err)
	}
	for _, migrationPath := range []string{
		"../../database/migrations/000025_connection_health_question_answers.sql",
		"../../database/migrations/000026_connection_health_question_answer_reasoning_effort.sql",
	} {
		migrationSQL, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("read migration %s: %v", migrationPath, err)
		}
		if _, err := pool.Exec(ctx, string(migrationSQL)); err != nil {
			t.Fatalf("migration after EnsureSchema %s: %v", migrationPath, err)
		}
	}
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second run: %v", err)
	}

	q1, err := repository.CreateTestQuestion(ctx, "user-1", "Question 1", "original body")
	if err != nil || !q1.IsDefault || !q1.Enabled {
		t.Fatalf("first question = %+v err=%v", q1, err)
	}
	q2, err := repository.CreateTestQuestion(ctx, "user-1", "Question 2", "second body")
	if err != nil || q2.IsDefault {
		t.Fatalf("second question = %+v err=%v", q2, err)
	}
	if _, err := repository.CreateTestQuestion(ctx, "user-2", "Other user", "private body"); err != nil {
		t.Fatalf("create other user question: %v", err)
	}
	user1Questions, err := repository.ListTestQuestions(ctx, "user-1")
	if err != nil || len(user1Questions) != 2 {
		t.Fatalf("user isolation questions=%+v err=%v", user1Questions, err)
	}
	defaultQuestion, err := repository.SetDefaultTestQuestion(ctx, "user-1", q2.ID)
	if err != nil || defaultQuestion == nil || !defaultQuestion.IsDefault {
		t.Fatalf("set default = %+v err=%v", defaultQuestion, err)
	}
	disabledQuestion, err := repository.SetTestQuestionEnabled(ctx, "user-1", q2.ID, false)
	if err != nil || disabledQuestion == nil || disabledQuestion.IsDefault || disabledQuestion.Enabled {
		t.Fatalf("disable default = %+v err=%v", disabledQuestion, err)
	}
	if _, err := repository.SetTestQuestionEnabled(ctx, "user-1", q2.ID, true); err != nil {
		t.Fatalf("re-enable q2: %v", err)
	}
	defaultQuestion, err = repository.SetDefaultTestQuestion(ctx, "user-1", q1.ID)
	if err != nil || defaultQuestion == nil || !defaultQuestion.IsDefault {
		t.Fatalf("restore q1 default = %+v err=%v", defaultQuestion, err)
	}

	batchID := "batch-snapshot"
	records, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", batchID, []string{"model-a"}, []string{q1.ID}, QuestionAnswerReasoningEffortHigh)
	if err != nil || len(records) != 1 {
		t.Fatalf("create snapshot batch records=%+v err=%v", records, err)
	}
	if records[0].ReasoningEffort == nil || *records[0].ReasoningEffort != QuestionAnswerReasoningEffortHigh {
		t.Fatalf("reasoning effort snapshot=%v", records[0].ReasoningEffort)
	}
	if _, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-duplicate", []string{"model-b"}, []string{q2.ID}, QuestionAnswerReasoningEffortHigh); !errors.Is(err, errQuestionAnswerActive) {
		t.Fatalf("duplicate active batch error=%v", err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "user-1", batchID, records[0].ID); err != nil || !running {
		t.Fatalf("mark running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "user-1", batchID, records[0].ID, QuestionAnswerSucceeded, "saved answer", ""); err != nil || !completed {
		t.Fatalf("complete succeeded=%v err=%v", completed, err)
	}
	if _, err := repository.UpdateTestQuestion(ctx, "user-1", q1.ID, "Question 1 changed", "changed body"); err != nil {
		t.Fatalf("edit question: %v", err)
	}
	if deleted, err := repository.DeleteTestQuestion(ctx, "user-1", q1.ID); err != nil || !deleted {
		t.Fatalf("delete question=%v err=%v", deleted, err)
	}
	remainingQuestions, err := repository.ListTestQuestions(ctx, "user-1")
	if err != nil || len(remainingQuestions) != 1 || remainingQuestions[0].IsDefault {
		t.Fatalf("delete default should not auto-select replacement: questions=%+v err=%v", remainingQuestions, err)
	}
	snapshotRecords, err := repository.ListQuestionAnswerBatch(ctx, "user-1", "target-1", batchID)
	if err != nil || len(snapshotRecords) != 1 || snapshotRecords[0].QuestionName != "Question 1" || snapshotRecords[0].QuestionBody != "original body" || snapshotRecords[0].AnswerBody != "saved answer" {
		t.Fatalf("history snapshot changed: records=%+v err=%v", snapshotRecords, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET reasoning_effort = NULL WHERE id = $1`, records[0].ID); err != nil {
		t.Fatalf("clear legacy reasoning effort snapshot: %v", err)
	}
	legacyRecords, err := repository.ListQuestionAnswerBatch(ctx, "user-1", "target-1", batchID)
	if err != nil || len(legacyRecords) != 1 || legacyRecords[0].ReasoningEffort != nil {
		t.Fatalf("legacy null reasoning effort=%+v err=%v", legacyRecords, err)
	}
	marked, err := repository.SetQuestionAnswerManualError(ctx, "user-1", "target-1", records[0].ID, true)
	if err != nil || marked == nil || !marked.ManualError {
		t.Fatalf("manual mark=%+v err=%v", marked, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET completed_at = (
			((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '1 second')
			AT TIME ZONE 'Asia/Shanghai'
		), updated_at = now()
		WHERE id = $1
	`, records[0].ID); err != nil {
		t.Fatalf("move snapshot record before Shanghai today: %v", err)
	}

	models := make([]string, 25)
	for i := range models {
		models[i] = fmt.Sprintf("model-%02d", i)
	}
	bulk, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-bulk", models, []string{q2.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(bulk) != 25 {
		t.Fatalf("create bulk records=%d err=%v", len(bulk), err)
	}
	for i, record := range bulk {
		if running, err := repository.MarkQuestionAnswerRunning(ctx, "user-1", record.BatchID, record.ID); err != nil || !running {
			t.Fatalf("bulk mark running %d=%v err=%v", i, running, err)
		}
		status, answer, errorType := QuestionAnswerSucceeded, "answer", ""
		if i%2 == 1 {
			status, answer, errorType = QuestionAnswerFailed, "", QuestionAnswerErrorNetwork
		}
		if completed, err := repository.CompleteQuestionAnswer(ctx, "user-1", record.BatchID, record.ID, status, answer, errorType); err != nil || !completed {
			t.Fatalf("bulk complete %d=%v err=%v", i, completed, err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE connection_health_question_answer_records
			SET
				started_at = (
					date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai')
					+ interval '12 hours' + ($2 * interval '1 second')
				) AT TIME ZONE 'Asia/Shanghai',
				completed_at = (
					date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai')
					+ interval '12 hours 500 milliseconds' + ($2 * interval '1 second')
				) AT TIME ZONE 'Asia/Shanghai',
				updated_at = now()
			WHERE id = $1
		`, record.ID, i); err != nil {
			t.Fatalf("set deterministic bulk timestamps %d: %v", i, err)
		}
	}
	orderedBulk, err := repository.ListQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-bulk")
	if err != nil || len(orderedBulk) != len(bulk) {
		t.Fatalf("ordered bulk records=%d err=%v", len(orderedBulk), err)
	}
	for i := range bulk {
		if orderedBulk[i].ID != bulk[i].ID {
			t.Fatalf("ordered bulk record %d id=%s, want %s", i, orderedBulk[i].ID, bulk[i].ID)
		}
	}
	failedMark, err := repository.SetQuestionAnswerManualError(ctx, "user-1", "target-1", bulk[1].ID, false)
	if err != nil || failedMark != nil {
		t.Fatalf("failed record manual mark=%+v err=%v", failedMark, err)
	}

	cancelled, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-cancelled", []string{"model-cancelled"}, []string{q2.ID}, QuestionAnswerReasoningEffortLow)
	if err != nil || len(cancelled) != 1 {
		t.Fatalf("create cancelled batch=%+v err=%v", cancelled, err)
	}
	if found, err := repository.StopQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-cancelled", QuestionAnswerCancelled, ""); err != nil || !found {
		t.Fatalf("cancel batch found=%v err=%v", found, err)
	}

	page1, err := repository.ListQuestionAnswerHistory(ctx, "user-1", "target-1", 1)
	if err != nil || len(page1.Records) != 20 || page1.TotalItems != 27 || page1.TotalPages != 2 {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := repository.ListQuestionAnswerHistory(ctx, "user-1", "target-1", 2)
	if err != nil || len(page2.Records) != 7 {
		t.Fatalf("page2 records=%d err=%v", len(page2.Records), err)
	}
	if page1.Stats.Total != 26 || page1.Stats.Normal != 13 || page1.Stats.Errors != 13 || page1.Stats.Normal+page1.Stats.Errors != page1.Stats.Total {
		t.Fatalf("stats=%+v", page1.Stats)
	}
	if page1.TodayStats.Total != 25 || page1.TodayStats.Normal != 13 || page1.TodayStats.Errors != 12 || page1.TodayStats.Normal+page1.TodayStats.Errors != page1.TodayStats.Total {
		t.Fatalf("today stats=%+v", page1.TodayStats)
	}
	if page1.Records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("latest record status=%s, want cancelled", page1.Records[0].Status)
	}
	if foreign, err := repository.ListQuestionAnswerHistory(ctx, "user-2", "target-1", 1); err != nil || foreign.TotalItems != 0 {
		t.Fatalf("history user isolation=%+v err=%v", foreign, err)
	}

	abandoned, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-restart", "batch-restart", []string{"model-a"}, []string{q2.ID}, QuestionAnswerReasoningEffortXHigh)
	if err != nil || len(abandoned) != 1 {
		t.Fatalf("create abandoned batch=%+v err=%v", abandoned, err)
	}
	failedCount, err := repository.FailAbandonedQuestionAnswers(ctx, QuestionAnswerErrorServiceRestarted)
	if err != nil || failedCount != 1 {
		t.Fatalf("fail abandoned count=%d err=%v", failedCount, err)
	}
	restarted, err := repository.ListQuestionAnswerBatch(ctx, "user-1", "target-restart", "batch-restart")
	if err != nil || len(restarted) != 1 || restarted[0].Status != QuestionAnswerFailed || restarted[0].ErrorType != QuestionAnswerErrorServiceRestarted {
		t.Fatalf("restarted records=%+v err=%v", restarted, err)
	}
}

func TestQuestionAnswerRepositoryPostgresConcurrentCompletionAndStopHaveOneTerminalResult(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "race-user", "Race question", "race body")
	if err != nil {
		t.Fatalf("create question: %v", err)
	}

	for i := 0; i < 10; i++ {
		targetID := fmt.Sprintf("race-target-%d", i)
		batchID := fmt.Sprintf("race-batch-%d", i)
		records, err := repository.CreateQuestionAnswerBatch(ctx, "race-user", targetID, batchID, []string{"model-a"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
		if err != nil || len(records) != 1 {
			t.Fatalf("create race batch %d: records=%+v err=%v", i, records, err)
		}
		if running, err := repository.MarkQuestionAnswerRunning(ctx, "race-user", batchID, records[0].ID); err != nil || !running {
			t.Fatalf("mark race record running %d: running=%v err=%v", i, running, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var completeResult, stopResult bool
		var completeErr, stopErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			completeResult, completeErr = repository.CompleteQuestionAnswer(ctx, "race-user", batchID, records[0].ID, QuestionAnswerSucceeded, "race answer", "")
		}()
		go func() {
			defer wg.Done()
			<-start
			stopResult, stopErr = repository.StopQuestionAnswerBatch(ctx, "race-user", targetID, batchID, QuestionAnswerCancelled, "")
		}()
		close(start)
		wg.Wait()
		if completeErr != nil || stopErr != nil || !stopResult {
			t.Fatalf("race %d results: complete=%v err=%v stop=%v err=%v", i, completeResult, completeErr, stopResult, stopErr)
		}
		stored, err := repository.ListQuestionAnswerBatch(ctx, "race-user", targetID, batchID)
		if err != nil || len(stored) != 1 {
			t.Fatalf("list race batch %d: records=%+v err=%v", i, stored, err)
		}
		switch stored[0].Status {
		case QuestionAnswerSucceeded:
			if !completeResult || stored[0].AnswerBody != "race answer" {
				t.Fatalf("race %d succeeded without the winning completion: result=%v record=%+v", i, completeResult, stored[0])
			}
		case QuestionAnswerCancelled:
			if completeResult || stored[0].AnswerBody != "" {
				t.Fatalf("race %d cancellation was overwritten by completion: result=%v record=%+v", i, completeResult, stored[0])
			}
		default:
			t.Fatalf("race %d left non-terminal status: %+v", i, stored[0])
		}
	}

	preserveRecords, err := repository.CreateQuestionAnswerBatch(ctx, "race-user", "preserve-target", "preserve-batch", []string{"model-a", "model-b"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(preserveRecords) != 2 {
		t.Fatalf("create preserve batch: records=%+v err=%v", preserveRecords, err)
	}
	for _, record := range preserveRecords {
		if running, err := repository.MarkQuestionAnswerRunning(ctx, "race-user", "preserve-batch", record.ID); err != nil || !running {
			t.Fatalf("mark preserve record running: record=%s running=%v err=%v", record.ID, running, err)
		}
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "race-user", "preserve-batch", preserveRecords[0].ID, QuestionAnswerSucceeded, "kept answer", ""); err != nil || !completed {
		t.Fatalf("complete preserved record: completed=%v err=%v", completed, err)
	}
	if found, err := repository.StopQuestionAnswerBatch(ctx, "race-user", "preserve-target", "preserve-batch", QuestionAnswerCancelled, ""); err != nil || !found {
		t.Fatalf("stop preserve batch: found=%v err=%v", found, err)
	}
	if late, err := repository.CompleteQuestionAnswer(ctx, "race-user", "preserve-batch", preserveRecords[1].ID, QuestionAnswerSucceeded, "late answer", ""); err != nil || late {
		t.Fatalf("late completion after stop: completed=%v err=%v", late, err)
	}
	stored, err := repository.ListQuestionAnswerBatch(ctx, "race-user", "preserve-target", "preserve-batch")
	if err != nil || len(stored) != 2 {
		t.Fatalf("list preserve batch: records=%+v err=%v", stored, err)
	}
	statuses := map[QuestionAnswerStatus]QuestionAnswerRecord{}
	for _, record := range stored {
		statuses[record.Status] = record
	}
	if statuses[QuestionAnswerSucceeded].AnswerBody != "kept answer" || statuses[QuestionAnswerCancelled].AnswerBody != "" {
		t.Fatalf("preserve batch terminal records=%+v", stored)
	}
}

func openQuestionAnswerPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("question_answer_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse PostgreSQL config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("connect test schema: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		adminPool.Close()
		t.Fatalf("ping test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
		defer dropCancel()
		if _, err := adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}
