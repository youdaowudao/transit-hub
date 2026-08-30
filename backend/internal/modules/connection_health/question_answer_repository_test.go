package connection_health

import (
	"context"
	"encoding/json"
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
	marked, err := repository.SetQuestionAnswerJudgment(ctx, "user-1", "target-1", records[0].ID, QuestionAnswerIncorrect)
	if err != nil || marked == nil || marked.AnswerJudgment == nil || *marked.AnswerJudgment != QuestionAnswerIncorrect || !marked.ManualError {
		t.Fatalf("incorrect judgment=%+v err=%v", marked, err)
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
	failedMark, err := repository.SetQuestionAnswerJudgment(ctx, "user-1", "target-1", bulk[1].ID, QuestionAnswerCorrect)
	if err != nil || failedMark != nil {
		t.Fatalf("failed record judgment=%+v err=%v", failedMark, err)
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
	if page1.Stats.Requests.Submitted != 27 || page1.Stats.Requests.InProgress != 0 || page1.Stats.Requests.Succeeded != 14 || page1.Stats.Requests.Failed != 12 || page1.Stats.Requests.Cancelled != 1 {
		t.Fatalf("stats=%+v", page1.Stats)
	}
	if page1.Stats.Reviews.Unreviewed != 13 || page1.Stats.Reviews.Correct != 0 || page1.Stats.Reviews.Incorrect != 1 || page1.Stats.Reviews.Unreviewed+page1.Stats.Reviews.Correct+page1.Stats.Reviews.Incorrect != page1.Stats.Requests.Succeeded {
		t.Fatalf("review stats=%+v", page1.Stats)
	}
	if page1.TodayStats.Requests.Submitted != 27 || page1.TodayStats.Requests.InProgress != 0 || page1.TodayStats.Requests.Succeeded != 14 || page1.TodayStats.Requests.Failed != 12 || page1.TodayStats.Requests.Cancelled != 1 {
		t.Fatalf("today stats=%+v", page1.TodayStats)
	}
	if page1.TodayStats.Reviews.Unreviewed != 13 || page1.TodayStats.Reviews.Correct != 0 || page1.TodayStats.Reviews.Incorrect != 1 || page1.TodayStats.Reviews.Unreviewed+page1.TodayStats.Reviews.Correct+page1.TodayStats.Reviews.Incorrect != page1.TodayStats.Requests.Succeeded {
		t.Fatalf("today review stats=%+v", page1.TodayStats)
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

func TestQuestionAnswerRepositoryPostgresConcurrentOppositeJudgmentsNeverReturnMissingRecord(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "judgment-race-user", "Judgment race", "Race body")
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	records, err := repository.CreateQuestionAnswerBatch(
		ctx,
		"judgment-race-user",
		"judgment-race-target",
		"judgment-race-batch",
		[]string{"model-a"},
		[]string{question.ID},
		QuestionAnswerReasoningEffortMedium,
	)
	if err != nil || len(records) != 1 {
		t.Fatalf("create race record: records=%+v err=%v", records, err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "judgment-race-user", records[0].BatchID, records[0].ID); err != nil || !running {
		t.Fatalf("mark race record running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "judgment-race-user", records[0].BatchID, records[0].ID, QuestionAnswerSucceeded, "answer", ""); err != nil || !completed {
		t.Fatalf("complete race record=%v err=%v", completed, err)
	}

	type judgmentResult struct {
		judgment QuestionAnswerJudgment
		record   *QuestionAnswerRecord
		err      error
	}
	const (
		rounds         = 40
		writersPerSide = 8
	)
	for round := 0; round < rounds; round++ {
		if _, err := pool.Exec(ctx, `
			UPDATE connection_health_question_answer_records
			SET answer_judgment = 'correct', manual_error = false, updated_at = now()
			WHERE id = $1
		`, records[0].ID); err != nil {
			t.Fatalf("reset judgment before round %d: %v", round, err)
		}

		start := make(chan struct{})
		results := make(chan judgmentResult, writersPerSide*2)
		var wg sync.WaitGroup
		for writer := 0; writer < writersPerSide*2; writer++ {
			judgment := QuestionAnswerCorrect
			if writer%2 == 1 {
				judgment = QuestionAnswerIncorrect
			}
			wg.Add(1)
			go func(desired QuestionAnswerJudgment) {
				defer wg.Done()
				<-start
				record, err := repository.SetQuestionAnswerJudgment(
					ctx,
					"judgment-race-user",
					"judgment-race-target",
					records[0].ID,
					desired,
				)
				results <- judgmentResult{judgment: desired, record: record, err: err}
			}(judgment)
		}
		close(start)
		wg.Wait()
		close(results)

		for result := range results {
			if result.err != nil {
				t.Fatalf("round %d judgment %s returned error: %v", round, result.judgment, result.err)
			}
			if result.record == nil {
				t.Fatalf("round %d judgment %s returned a missing record for a succeeded request", round, result.judgment)
			}
		}
	}
}

func TestQuestionAnswerJudgmentMigrationBackfillsAndConstrains(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000025_connection_health_question_answers.sql")
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000026_connection_health_question_answer_reasoning_effort.sql")

	rows := []struct {
		id          string
		status      string
		manualError bool
	}{
		{id: "old-correct-unknown", status: "succeeded", manualError: false},
		{id: "old-incorrect", status: "succeeded", manualError: true},
		{id: "old-pending", status: "pending", manualError: false},
		{id: "old-failed", status: "failed", manualError: true},
		{id: "old-cancelled", status: "cancelled", manualError: false},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO connection_health_question_answer_records (
				id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
				status, manual_error
			) VALUES ($1, 'migration-user', 'migration-target', 'migration-batch', 'model', 'question', 'Question', 'Body', $2, $3)
		`, row.id, row.status, row.manualError); err != nil {
			t.Fatalf("insert legacy row %s: %v", row.id, err)
		}
	}

	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000027_connection_health_question_answer_judgment.sql")

	want := map[string]*string{
		"old-correct-unknown": stringPointer("unreviewed"),
		"old-incorrect":       stringPointer("incorrect"),
		"old-pending":         nil,
		"old-failed":          nil,
		"old-cancelled":       nil,
	}
	queryRows, err := pool.Query(ctx, `
		SELECT id, answer_judgment
		FROM connection_health_question_answer_records
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("read migrated judgments: %v", err)
	}
	defer queryRows.Close()
	seen := 0
	for queryRows.Next() {
		var id string
		var judgment *string
		if err := queryRows.Scan(&id, &judgment); err != nil {
			t.Fatalf("scan migrated judgment: %v", err)
		}
		if !equalOptionalString(judgment, want[id]) {
			t.Fatalf("judgment for %s = %v, want %v", id, judgment, want[id])
		}
		seen++
	}
	if err := queryRows.Err(); err != nil {
		t.Fatalf("iterate migrated judgments: %v", err)
	}
	if seen != len(want) {
		t.Fatalf("migrated rows = %d, want %d", seen, len(want))
	}

	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET answer_judgment = 'correct' WHERE id = 'old-pending'`); err == nil {
		t.Fatal("pending request accepted an answer judgment")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET answer_judgment = NULL WHERE id = 'old-correct-unknown'`); err == nil {
		t.Fatal("succeeded request accepted a NULL answer judgment")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET answer_judgment = 'unknown' WHERE id = 'old-incorrect'`); err == nil {
		t.Fatal("succeeded request accepted an unknown answer judgment")
	}
}

func TestQuestionAnswerEnsureSchemaBackfillsOnceAndPreservesCorrect(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000025_connection_health_question_answers.sql")
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000026_connection_health_question_answer_reasoning_effort.sql")
	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body, status, manual_error
		) VALUES
			('ensure-success', 'ensure-user', 'ensure-target', 'ensure-batch', 'model', 'q', 'Q', 'Body', 'succeeded', false),
			('ensure-failed', 'ensure-user', 'ensure-target', 'ensure-batch', 'model', 'q', 'Q', 'Body', 'failed', true)
	`); err != nil {
		t.Fatalf("insert legacy EnsureSchema rows: %v", err)
	}

	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema first run: %v", err)
	}
	var succeededJudgment, failedJudgment *string
	if err := pool.QueryRow(ctx, `SELECT answer_judgment FROM connection_health_question_answer_records WHERE id = 'ensure-success'`).Scan(&succeededJudgment); err != nil {
		t.Fatalf("read succeeded judgment after EnsureSchema: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT answer_judgment FROM connection_health_question_answer_records WHERE id = 'ensure-failed'`).Scan(&failedJudgment); err != nil {
		t.Fatalf("read failed judgment after EnsureSchema: %v", err)
	}
	if succeededJudgment == nil || *succeededJudgment != "unreviewed" || failedJudgment != nil {
		t.Fatalf("EnsureSchema backfill = succeeded:%v failed:%v", succeededJudgment, failedJudgment)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET answer_judgment = 'correct', manual_error = false
		WHERE id = 'ensure-success'
	`); err != nil {
		t.Fatalf("save authoritative correct judgment: %v", err)
	}
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second run: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT answer_judgment FROM connection_health_question_answer_records WHERE id = 'ensure-success'`).Scan(&succeededJudgment); err != nil {
		t.Fatalf("read succeeded judgment after second EnsureSchema: %v", err)
	}
	if succeededJudgment == nil || *succeededJudgment != "correct" {
		t.Fatalf("second EnsureSchema overwrote correct judgment: %v", succeededJudgment)
	}
}

func TestQuestionAnswerRepositoryCompletesWithJudgmentAndReconciledStats(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "stats-user", "Stats question", "Stats body")
	if err != nil {
		t.Fatalf("create stats question: %v", err)
	}
	success, err := repository.CreateQuestionAnswerBatch(ctx, "stats-user", "stats-target", "stats-success", []string{"success-model"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(success) != 1 {
		t.Fatalf("create success record: records=%+v err=%v", success, err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "stats-user", "stats-success", success[0].ID); err != nil || !running {
		t.Fatalf("mark success running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "stats-user", "stats-success", success[0].ID, QuestionAnswerSucceeded, "answer", ""); err != nil || !completed {
		t.Fatalf("complete success=%v err=%v", completed, err)
	}
	var successJudgment *string
	var successManualError bool
	if err := pool.QueryRow(ctx, `SELECT answer_judgment, manual_error FROM connection_health_question_answer_records WHERE id = $1`, success[0].ID).Scan(&successJudgment, &successManualError); err != nil {
		t.Fatalf("read completed success judgment: %v", err)
	}
	if successJudgment == nil || *successJudgment != "unreviewed" || successManualError {
		t.Fatalf("completed success judgment=%v manualError=%v", successJudgment, successManualError)
	}

	failed, err := repository.CreateQuestionAnswerBatch(ctx, "stats-user", "stats-target", "stats-failed", []string{"failed-model"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(failed) != 1 {
		t.Fatalf("create failed record: records=%+v err=%v", failed, err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "stats-user", "stats-failed", failed[0].ID); err != nil || !running {
		t.Fatalf("mark failed running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "stats-user", "stats-failed", failed[0].ID, QuestionAnswerFailed, "", QuestionAnswerErrorNetwork); err != nil || !completed {
		t.Fatalf("complete failed=%v err=%v", completed, err)
	}
	var failedJudgment *string
	if err := pool.QueryRow(ctx, `SELECT answer_judgment FROM connection_health_question_answer_records WHERE id = $1`, failed[0].ID).Scan(&failedJudgment); err != nil {
		t.Fatalf("read completed failed judgment: %v", err)
	}
	if failedJudgment != nil {
		t.Fatalf("failed request received answer judgment %v", failedJudgment)
	}

	history, err := repository.ListQuestionAnswerHistory(ctx, "stats-user", "stats-target", 1)
	if err != nil {
		t.Fatalf("list reconciled history: %v", err)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal reconciled history: %v", err)
	}
	var payload struct {
		Stats struct {
			Requests struct {
				Submitted  int `json:"submitted"`
				InProgress int `json:"inProgress"`
				Succeeded  int `json:"succeeded"`
				Failed     int `json:"failed"`
				Cancelled  int `json:"cancelled"`
			} `json:"requests"`
			Reviews struct {
				Unreviewed int `json:"unreviewed"`
				Correct    int `json:"correct"`
				Incorrect  int `json:"incorrect"`
			} `json:"reviews"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode reconciled history: %v", err)
	}
	requests, reviews := payload.Stats.Requests, payload.Stats.Reviews
	if requests.Submitted != 2 || requests.InProgress != 0 || requests.Succeeded != 1 || requests.Failed != 1 || requests.Cancelled != 0 {
		t.Fatalf("history request stats = %+v", requests)
	}
	if reviews.Unreviewed != 1 || reviews.Correct != 0 || reviews.Incorrect != 0 {
		t.Fatalf("history review stats = %+v", reviews)
	}
}

func TestQuestionAnswerBrowserFixtureSQLConflictsAndRollsBack(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire fixture connection: %v", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `
		SELECT set_config('task1.user_id', $1, false), set_config('task1.target_id', $2, false)
	`, "fixture-user", "sub2api:fixture-target"); err != nil {
		t.Fatalf("set fixture scope: %v", err)
	}

	fixtureCount := func() int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `
			SELECT count(*)
			FROM connection_health_question_answer_records
			WHERE user_id = 'fixture-user'
			  AND target_id = 'sub2api:fixture-target'
			  AND batch_id = 'task1-review-20260830-batch'
		`).Scan(&count); err != nil {
			t.Fatalf("count fixture records: %v", err)
		}
		return count
	}

	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	if got := fixtureCount(); got != 8 {
		t.Fatalf("prepared fixture rows = %d, want 8", got)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "fixture id already exists") {
		t.Fatalf("duplicate prepare error = %v", err)
	}
	if got := fixtureCount(); got != 8 {
		t.Fatalf("duplicate prepare changed fixture rows to %d", got)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "advance"); err != nil {
		t.Fatalf("advance fixture: %v", err)
	}
	var pending, running, failed, cancelled int
	if err := connection.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'cancelled')
		FROM connection_health_question_answer_records
		WHERE batch_id = 'task1-review-20260830-batch'
	`).Scan(&pending, &running, &failed, &cancelled); err != nil {
		t.Fatalf("read advanced fixture: %v", err)
	}
	if pending != 0 || running != 0 || failed != 2 || cancelled != 2 {
		t.Fatalf("advanced states pending=%d running=%d failed=%d cancelled=%d", pending, running, failed, cancelled)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "cleanup"); err != nil {
		t.Fatalf("cleanup advanced fixture: %v", err)
	}

	if _, err := connection.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body, status
		) VALUES (
			'non-fixture-active', 'fixture-user', 'sub2api:fixture-target', 'other-batch', 'model', 'q', 'Q', 'Body', 'pending'
		)
	`); err != nil {
		t.Fatalf("insert non-fixture active row: %v", err)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "non-fixture active batch exists") {
		t.Fatalf("active conflict prepare error = %v", err)
	}
	if got := fixtureCount(); got != 0 {
		t.Fatalf("active conflict left %d fixture rows", got)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = 'non-fixture-active'`); err != nil {
		t.Fatalf("delete non-fixture active row: %v", err)
	}

	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare cleanup rollback fixture: %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = 'task1-review-20260830-cancelled'`); err != nil {
		t.Fatalf("remove one fixture row: %v", err)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "cleanup"); err == nil || !strings.Contains(err.Error(), "expected exactly eight deleted rows") {
		t.Fatalf("short cleanup error = %v", err)
	}
	if got := fixtureCount(); got != 7 {
		t.Fatalf("failed cleanup did not roll back: rows=%d, want 7", got)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE batch_id = 'task1-review-20260830-batch'`); err != nil {
		t.Fatalf("reset short fixture: %v", err)
	}

	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare advance conflict fixture: %v", err)
	}
	if _, err := connection.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET status = 'cancelled', completed_at = now(), updated_at = now()
		WHERE id = 'task1-review-20260830-pending'
	`); err != nil {
		t.Fatalf("change pending fixture state: %v", err)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "advance"); err == nil || !strings.Contains(err.Error(), "expected exactly two old states") {
		t.Fatalf("advance state conflict error = %v", err)
	}
	var runningStatus string
	if err := connection.QueryRow(ctx, `SELECT status FROM connection_health_question_answer_records WHERE id = 'task1-review-20260830-running'`).Scan(&runningStatus); err != nil {
		t.Fatalf("read running fixture after failed advance: %v", err)
	}
	if runningStatus != "running" {
		t.Fatalf("failed advance partially changed running row to %s", runningStatus)
	}
	if err := runQuestionAnswerBrowserFixtureAction(ctx, connection, "cleanup"); err != nil {
		t.Fatalf("final fixture cleanup: %v", err)
	}
	if got := fixtureCount(); got != 0 {
		t.Fatalf("final fixture rows = %d", got)
	}
}

func runQuestionAnswerBrowserFixtureAction(ctx context.Context, connection *pgxpool.Conn, action string) error {
	fixtureSQL, err := os.ReadFile("testdata/task1_question_answer_browser_fixture.sql")
	if err != nil {
		return fmt.Errorf("read fixture SQL: %w", err)
	}
	source := string(fixtureSQL)
	startMarker := "\\if :" + action + "\n"
	start := strings.Index(source, startMarker)
	if start < 0 {
		return fmt.Errorf("fixture action %s not found", action)
	}
	start += len(startMarker)
	endRelative := strings.Index(source[start:], "\n\\endif")
	if endRelative < 0 {
		return fmt.Errorf("fixture action %s end not found", action)
	}
	_, err = connection.Exec(ctx, source[start:start+endRelative])
	if err != nil {
		_, _ = connection.Exec(ctx, "ROLLBACK")
	}
	return err
}

func applyQuestionAnswerMigrationForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, migrationPath string) {
	t.Helper()
	migrationSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration %s: %v", migrationPath, err)
	}
	if _, err := pool.Exec(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("apply migration %s: %v", migrationPath, err)
	}
}

func stringPointer(value string) *string { return &value }

func equalOptionalString(got *string, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
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
