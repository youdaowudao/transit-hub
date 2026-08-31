package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const questionAnswerPostgresTimeout = 15 * time.Second

func TestQuestionAnswerRepositoryRepeatExpansionPersistsIndependentOrderedRecords(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	q1, err := repository.CreateTestQuestion(ctx, "repeat-user", "Question 1", "Body 1", []string{"one"})
	if err != nil {
		t.Fatalf("create q1: %v", err)
	}
	q2, err := repository.CreateTestQuestion(ctx, "repeat-user", "Question 2", "Body 2", []string{"two", "二"})
	if err != nil {
		t.Fatalf("create q2: %v", err)
	}
	q3, err := repository.CreateTestQuestion(ctx, "repeat-user", "Question 3", "Body 3", []string{})
	if err != nil {
		t.Fatalf("create q3: %v", err)
	}
	questions := []TestQuestion{q1, q2, q3}
	models := []string{"model-a", "model-b"}
	records, err := repository.CreateQuestionAnswerBatch(
		ctx, "repeat-user", "repeat-target", "repeat-batch",
		models, []string{q1.ID, q2.ID, q3.ID}, QuestionAnswerReasoningEffortHigh, 4,
	)
	if err != nil {
		t.Fatalf("create repeat batch: %v", err)
	}
	if len(records) != 24 {
		t.Fatalf("created records=%d want=24", len(records))
	}
	wantOrder := []string{
		"model-a/" + q1.ID, "model-a/" + q1.ID, "model-a/" + q1.ID, "model-a/" + q1.ID,
		"model-a/" + q2.ID, "model-a/" + q2.ID, "model-a/" + q2.ID, "model-a/" + q2.ID,
		"model-a/" + q3.ID, "model-a/" + q3.ID, "model-a/" + q3.ID, "model-a/" + q3.ID,
		"model-b/" + q1.ID, "model-b/" + q1.ID, "model-b/" + q1.ID, "model-b/" + q1.ID,
		"model-b/" + q2.ID, "model-b/" + q2.ID, "model-b/" + q2.ID, "model-b/" + q2.ID,
		"model-b/" + q3.ID, "model-b/" + q3.ID, "model-b/" + q3.ID, "model-b/" + q3.ID,
	}
	ids := make(map[string]struct{}, len(records))
	for i, record := range records {
		if got := record.ModelName + "/" + record.QuestionID; got != wantOrder[i] {
			t.Fatalf("create order[%d]=%s want=%s", i, got, wantOrder[i])
		}
		if _, duplicate := ids[record.ID]; duplicate {
			t.Fatalf("duplicate record id %q", record.ID)
		}
		ids[record.ID] = struct{}{}
	}
	var storedCount, distinctIDCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT id)
		FROM connection_health_question_answer_records
		WHERE user_id = 'repeat-user' AND target_id = 'repeat-target' AND batch_id = 'repeat-batch'
	`).Scan(&storedCount, &distinctIDCount); err != nil {
		t.Fatalf("count stored repeat records: %v", err)
	}
	if storedCount != 24 || distinctIDCount != 24 {
		t.Fatalf("stored=%d distinct_ids=%d want=24/24", storedCount, distinctIDCount)
	}
	persisted, err := repository.ListQuestionAnswerBatch(ctx, "repeat-user", "repeat-target", "repeat-batch")
	if err != nil || len(persisted) != 24 {
		t.Fatalf("list repeat batch records=%d err=%v", len(persisted), err)
	}
	questionByID := make(map[string]TestQuestion, len(questions))
	combinationCounts := make(map[string]int, 6)
	for _, question := range questions {
		questionByID[question.ID] = question
	}
	for _, record := range persisted {
		question := questionByID[record.QuestionID]
		if record.QuestionName != question.Name || record.QuestionBody != question.Body || !reflect.DeepEqual(record.QuestionKeywordSnapshot, question.Keywords) {
			t.Fatalf("stored snapshot=%+v want question=%+v", record, question)
		}
		if record.ReasoningEffort == nil || *record.ReasoningEffort != QuestionAnswerReasoningEffortHigh {
			t.Fatalf("stored reasoning effort=%v", record.ReasoningEffort)
		}
		combinationCounts[record.ModelName+"/"+record.QuestionID]++
	}
	for combination, count := range combinationCounts {
		if count != 4 {
			t.Fatalf("combination %s count=%d want=4", combination, count)
		}
	}
	if len(combinationCounts) != 6 {
		t.Fatalf("combination count=%d want=6", len(combinationCounts))
	}

	repeatOne, err := repository.CreateQuestionAnswerBatch(
		ctx, "repeat-user", "repeat-one-target", "repeat-one-batch",
		models, []string{q1.ID, q2.ID, q3.ID}, QuestionAnswerReasoningEffortMedium, 1,
	)
	if err != nil || len(repeatOne) != 6 {
		t.Fatalf("repeat one records=%d err=%v want=6", len(repeatOne), err)
	}
}

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
		"../../database/migrations/000027_connection_health_question_answer_judgment.sql",
		"../../database/migrations/000028_connection_health_question_keywords.sql",
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

	q1, err := repository.CreateTestQuestion(ctx, "user-1", "Question 1", "original body", []string{})
	if err != nil || !q1.IsDefault || !q1.Enabled {
		t.Fatalf("first question = %+v err=%v", q1, err)
	}
	q2, err := repository.CreateTestQuestion(ctx, "user-1", "Question 2", "second body", []string{})
	if err != nil || q2.IsDefault {
		t.Fatalf("second question = %+v err=%v", q2, err)
	}
	if _, err := repository.CreateTestQuestion(ctx, "user-2", "Other user", "private body", []string{}); err != nil {
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
	records, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", batchID, []string{"model-a"}, []string{q1.ID}, QuestionAnswerReasoningEffortHigh, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("create snapshot batch records=%+v err=%v", records, err)
	}
	if records[0].ReasoningEffort == nil || *records[0].ReasoningEffort != QuestionAnswerReasoningEffortHigh {
		t.Fatalf("reasoning effort snapshot=%v", records[0].ReasoningEffort)
	}
	if _, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-duplicate", []string{"model-b"}, []string{q2.ID}, QuestionAnswerReasoningEffortHigh, 1); !errors.Is(err, errQuestionAnswerActive) {
		t.Fatalf("duplicate active batch error=%v", err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "user-1", batchID, records[0].ID); err != nil || !running {
		t.Fatalf("mark running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "user-1", batchID, records[0].ID, QuestionAnswerSucceeded, "saved answer", ""); err != nil || !completed {
		t.Fatalf("complete succeeded=%v err=%v", completed, err)
	}
	if _, err := repository.UpdateTestQuestion(ctx, "user-1", q1.ID, "Question 1 changed", "changed body", nil); err != nil {
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
	bulk, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-bulk", models, []string{q2.ID}, QuestionAnswerReasoningEffortMedium, 1)
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

	cancelled, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-cancelled", []string{"model-cancelled"}, []string{q2.ID}, QuestionAnswerReasoningEffortLow, 1)
	if err != nil || len(cancelled) != 1 {
		t.Fatalf("create cancelled batch=%+v err=%v", cancelled, err)
	}
	if found, err := repository.StopPendingQuestionAnswerBatch(ctx, "user-1", "target-1", "batch-cancelled", QuestionAnswerCancelled, ""); err != nil || !found {
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

	abandoned, err := repository.CreateQuestionAnswerBatch(ctx, "user-1", "target-restart", "batch-restart", []string{"model-a"}, []string{q2.ID}, QuestionAnswerReasoningEffortXHigh, 1)
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

func TestQuestionAnswerKeywordSnapshotPersistsWithNilAndEmptyDistinction(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_test_questions (id, user_id, name, body)
		VALUES ('legacy-keyword-question', 'keyword-user', 'Legacy question', 'Legacy body')
	`); err != nil {
		t.Fatalf("insert legacy question: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			reasoning_effort, answer_body, status, answer_judgment
		) VALUES (
			'legacy-keyword-record', 'keyword-user', 'legacy-keyword-target', 'legacy-keyword-batch',
			'model-legacy', 'legacy-keyword-question', 'Legacy question', 'Legacy body',
			'medium', 'legacy answer', 'succeeded', 'unreviewed'
		)
	`); err != nil {
		t.Fatalf("insert legacy record: %v", err)
	}

	questions, err := repository.ListTestQuestions(ctx, "keyword-user")
	if err != nil || len(questions) != 1 || questions[0].Keywords == nil || len(questions[0].Keywords) != 0 {
		t.Fatalf("legacy question keywords=%+v err=%v, want non-nil empty", questions, err)
	}
	legacyRecords, err := repository.ListQuestionAnswerBatch(ctx, "keyword-user", "legacy-keyword-target", "legacy-keyword-batch")
	if err != nil || len(legacyRecords) != 1 || legacyRecords[0].QuestionKeywordSnapshot != nil {
		t.Fatalf("legacy snapshot=%+v err=%v, want nil", legacyRecords, err)
	}

	emptyQuestion, err := repository.CreateTestQuestion(ctx, "keyword-user", "Empty keywords", "Empty body", []string{})
	if err != nil || emptyQuestion.Keywords == nil || len(emptyQuestion.Keywords) != 0 {
		t.Fatalf("empty question=%+v err=%v", emptyQuestion, err)
	}
	emptyRecords, err := repository.CreateQuestionAnswerBatch(
		ctx, "keyword-user", "empty-keyword-target", "empty-keyword-batch",
		[]string{"model-empty"}, []string{emptyQuestion.ID}, QuestionAnswerReasoningEffortMedium, 1,
	)
	if err != nil || len(emptyRecords) != 1 || emptyRecords[0].QuestionKeywordSnapshot == nil || len(emptyRecords[0].QuestionKeywordSnapshot) != 0 {
		t.Fatalf("empty snapshot=%+v err=%v, want non-nil empty", emptyRecords, err)
	}

	wantSnapshot := []string{"Error", "错误码"}
	configuredQuestion, err := repository.CreateTestQuestion(ctx, "keyword-user", "Configured keywords", "Configured body", wantSnapshot)
	if err != nil || !reflect.DeepEqual(configuredQuestion.Keywords, wantSnapshot) {
		t.Fatalf("configured question=%+v err=%v", configuredQuestion, err)
	}
	configuredRecords, err := repository.CreateQuestionAnswerBatch(
		ctx, "keyword-user", "configured-keyword-target", "configured-keyword-batch",
		[]string{"model-a", "model-b"}, []string{configuredQuestion.ID}, QuestionAnswerReasoningEffortHigh, 1,
	)
	if err != nil || len(configuredRecords) != 2 {
		t.Fatalf("configured records=%+v err=%v", configuredRecords, err)
	}
	for _, record := range configuredRecords {
		if !reflect.DeepEqual(record.QuestionKeywordSnapshot, wantSnapshot) {
			t.Fatalf("record %s snapshot=%#v, want %#v", record.ID, record.QuestionKeywordSnapshot, wantSnapshot)
		}
	}
	configuredRecords[0].QuestionKeywordSnapshot[0] = "mutated caller copy"
	if !reflect.DeepEqual(configuredRecords[1].QuestionKeywordSnapshot, wantSnapshot) {
		t.Fatalf("record snapshots share a mutable slice: %#v", configuredRecords[1].QuestionKeywordSnapshot)
	}

	newKeywords := []string{"new"}
	updated, err := repository.UpdateTestQuestion(
		ctx, "keyword-user", configuredQuestion.ID, "Configured keywords changed", "Configured body changed", &newKeywords,
	)
	if err != nil || updated == nil || !reflect.DeepEqual(updated.Keywords, newKeywords) {
		t.Fatalf("updated question=%+v err=%v", updated, err)
	}
	persistedRecords, err := repository.ListQuestionAnswerBatch(ctx, "keyword-user", "configured-keyword-target", "configured-keyword-batch")
	if err != nil || len(persistedRecords) != 2 {
		t.Fatalf("read configured batch=%+v err=%v", persistedRecords, err)
	}
	for _, record := range persistedRecords {
		if !reflect.DeepEqual(record.QuestionKeywordSnapshot, wantSnapshot) {
			t.Fatalf("snapshot changed with question: %#v, want %#v", record.QuestionKeywordSnapshot, wantSnapshot)
		}
	}

	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second run: %v", err)
	}
	questions, err = repository.ListTestQuestions(ctx, "keyword-user")
	if err != nil {
		t.Fatalf("list after second EnsureSchema: %v", err)
	}
	var configuredAfterEnsure *TestQuestion
	for i := range questions {
		if questions[i].ID == configuredQuestion.ID {
			configuredAfterEnsure = &questions[i]
			break
		}
	}
	if configuredAfterEnsure == nil || !reflect.DeepEqual(configuredAfterEnsure.Keywords, newKeywords) {
		t.Fatalf("EnsureSchema changed current keywords: %+v", configuredAfterEnsure)
	}
	persistedRecords, err = repository.ListQuestionAnswerBatch(ctx, "keyword-user", "configured-keyword-target", "configured-keyword-batch")
	if err != nil || len(persistedRecords) != 2 || !reflect.DeepEqual(persistedRecords[0].QuestionKeywordSnapshot, wantSnapshot) {
		t.Fatalf("EnsureSchema changed historical snapshot=%+v err=%v", persistedRecords, err)
	}

	foreignUpdate := []string{"foreign"}
	foreignQuestion, err := repository.UpdateTestQuestion(
		ctx, "other-keyword-user", configuredQuestion.ID, "Foreign", "Foreign body", &foreignUpdate,
	)
	if err != nil || foreignQuestion != nil {
		t.Fatalf("cross-user update=%+v err=%v, want nil", foreignQuestion, err)
	}
	foreignRecords, err := repository.ListQuestionAnswerBatch(ctx, "other-keyword-user", "configured-keyword-target", "configured-keyword-batch")
	if err != nil || len(foreignRecords) != 0 {
		t.Fatalf("cross-user records=%+v err=%v", foreignRecords, err)
	}
}

func TestQuestionAnswerKeywordMigrationBeforeEnsureSchemaIsIdempotent(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	for _, migrationPath := range []string{
		"../../database/migrations/000025_connection_health_question_answers.sql",
		"../../database/migrations/000026_connection_health_question_answer_reasoning_effort.sql",
		"../../database/migrations/000027_connection_health_question_answer_judgment.sql",
		"../../database/migrations/000028_connection_health_question_keywords.sql",
	} {
		applyQuestionAnswerMigrationForTest(t, ctx, pool, migrationPath)
	}
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema after migrations: %v", err)
	}
	keywords := []string{"Error", "错误码"}
	question, err := repository.CreateTestQuestion(ctx, "migration-order-user", "Migration order", "Migration body", keywords)
	if err != nil || !reflect.DeepEqual(question.Keywords, keywords) {
		t.Fatalf("question after migrations=%+v err=%v", question, err)
	}
	records, err := repository.CreateQuestionAnswerBatch(
		ctx, "migration-order-user", "migration-order-target", "migration-order-batch",
		[]string{"model"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 1,
	)
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0].QuestionKeywordSnapshot, keywords) {
		t.Fatalf("records after migrations=%+v err=%v", records, err)
	}
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("second EnsureSchema after migrations: %v", err)
	}
	records, err = repository.ListQuestionAnswerBatch(ctx, "migration-order-user", "migration-order-target", "migration-order-batch")
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0].QuestionKeywordSnapshot, keywords) {
		t.Fatalf("idempotent snapshot=%+v err=%v", records, err)
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
	question, err := repository.CreateTestQuestion(ctx, "race-user", "Race question", "race body", []string{})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}

	for i := 0; i < 10; i++ {
		targetID := fmt.Sprintf("race-target-%d", i)
		batchID := fmt.Sprintf("race-batch-%d", i)
		records, err := repository.CreateQuestionAnswerBatch(ctx, "race-user", targetID, batchID, []string{"model-a"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 1)
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
			stopResult, stopErr = repository.FinalizeQuestionAnswerBatch(ctx, "race-user", targetID, batchID, QuestionAnswerCancelled, "")
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

	preserveRecords, err := repository.CreateQuestionAnswerBatch(ctx, "race-user", "preserve-target", "preserve-batch", []string{"model-a", "model-b"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 1)
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
	if found, err := repository.FinalizeQuestionAnswerBatch(ctx, "race-user", "preserve-target", "preserve-batch", QuestionAnswerCancelled, ""); err != nil || !found {
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

func TestQuestionAnswerRepositoryStopFinalizeStateTransitions(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "stop-finalize-user", "Stop/finalize", "Body", []string{})
	if err != nil {
		t.Fatalf("create stop/finalize question: %v", err)
	}
	records, err := repository.CreateQuestionAnswerBatch(
		ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch",
		[]string{"model-a"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 5,
	)
	if err != nil || len(records) != 5 {
		t.Fatalf("create stop/finalize records=%d err=%v", len(records), err)
	}
	for _, record := range records {
		if running, err := repository.MarkQuestionAnswerRunning(ctx, "stop-finalize-user", record.BatchID, record.ID); err != nil || !running {
			t.Fatalf("mark record %s running=%v err=%v", record.ID, running, err)
		}
	}
	if found, err := repository.StopPendingQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch", QuestionAnswerCancelled, ""); err != nil || !found {
		t.Fatalf("stop pending on all-running batch found=%v err=%v", found, err)
	}
	allRunning, err := repository.ListQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch")
	if err != nil || len(allRunning) != 5 {
		t.Fatalf("list all-running batch records=%d err=%v", len(allRunning), err)
	}
	for _, record := range allRunning {
		if record.Status != QuestionAnswerRunning {
			t.Fatalf("StopPending changed running record: %+v", record)
		}
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "stop-finalize-user", "stop-finalize-batch", records[0].ID, QuestionAnswerSucceeded, "kept", ""); err != nil || !completed {
		t.Fatalf("complete terminal record before finalizer=%v err=%v", completed, err)
	}
	if found, err := repository.FinalizeQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch", QuestionAnswerCancelled, ""); err != nil || !found {
		t.Fatalf("finalize batch found=%v err=%v", found, err)
	}
	finalized, err := repository.ListQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch")
	if err != nil || len(finalized) != 5 {
		t.Fatalf("list finalized batch records=%d err=%v", len(finalized), err)
	}
	for _, record := range finalized {
		if record.ID == records[0].ID {
			if record.Status != QuestionAnswerSucceeded || record.AnswerBody != "kept" {
				t.Fatalf("finalizer overwrote existing terminal record: %+v", record)
			}
		} else if record.Status != QuestionAnswerCancelled || record.AnswerBody != "" {
			t.Fatalf("finalizer did not close residual running record: %+v", record)
		}
	}
	for _, operation := range []struct {
		name string
		call func() (bool, error)
	}{
		{name: "stop pending terminal", call: func() (bool, error) {
			return repository.StopPendingQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch", QuestionAnswerCancelled, "")
		}},
		{name: "finalize terminal", call: func() (bool, error) {
			return repository.FinalizeQuestionAnswerBatch(ctx, "stop-finalize-user", "stop-finalize-target", "stop-finalize-batch", QuestionAnswerCancelled, "")
		}},
	} {
		found, err := operation.call()
		if err != nil || !found {
			t.Fatalf("%s found=%v err=%v want existing batch", operation.name, found, err)
		}
	}
	if found, err := repository.StopPendingQuestionAnswerBatch(ctx, "stop-finalize-user", "missing-target", "missing-batch", QuestionAnswerCancelled, ""); err != nil || found {
		t.Fatalf("missing StopPending found=%v err=%v", found, err)
	}
	if found, err := repository.FinalizeQuestionAnswerBatch(ctx, "stop-finalize-user", "missing-target", "missing-batch", QuestionAnswerCancelled, ""); err != nil || found {
		t.Fatalf("missing Finalize found=%v err=%v", found, err)
	}

	pending, err := repository.CreateQuestionAnswerBatch(
		ctx, "stop-finalize-user", "pending-stop-target", "pending-stop-batch",
		[]string{"model-a"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 2,
	)
	if err != nil || len(pending) != 2 {
		t.Fatalf("create pending stop batch records=%d err=%v", len(pending), err)
	}
	if found, err := repository.StopPendingQuestionAnswerBatch(ctx, "stop-finalize-user", "pending-stop-target", "pending-stop-batch", QuestionAnswerFailed, QuestionAnswerErrorStorage); err != nil || !found {
		t.Fatalf("stop pending batch found=%v err=%v", found, err)
	}
	pendingStopped, err := repository.ListQuestionAnswerBatch(ctx, "stop-finalize-user", "pending-stop-target", "pending-stop-batch")
	if err != nil || len(pendingStopped) != 2 {
		t.Fatalf("list stopped pending batch records=%d err=%v", len(pendingStopped), err)
	}
	for _, record := range pendingStopped {
		if record.Status != QuestionAnswerFailed || record.ErrorType != QuestionAnswerErrorStorage {
			t.Fatalf("pending stop record=%+v", record)
		}
	}
}

func TestQuestionAnswerRepositoryFinalizeWaitsForInFlightCreateTransaction(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "create-race-user", "Create race", "Create race body", []string{})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}

	const targetID = "create-race-target"
	const batchID = "create-race-batch"
	createTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin create transaction: %v", err)
	}
	defer func() { _ = createTx.Rollback(context.Background()) }()
	lockKey := fmt.Sprintf("question-answer|%s|%s", "create-race-user", targetID)
	if _, err := createTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		t.Fatalf("lock create transaction: %v", err)
	}
	if _, err := createTx.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			question_keyword_snapshot, reasoning_effort, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending')
	`, "create-race-record", "create-race-user", targetID, batchID, "model-a", question.ID, question.Name, question.Body, []string{}, QuestionAnswerReasoningEffortMedium); err != nil {
		t.Fatalf("insert uncommitted batch: %v", err)
	}

	type finalizeResult struct {
		found bool
		err   error
	}
	result := make(chan finalizeResult, 1)
	go func() {
		found, finalizeErr := repository.FinalizeQuestionAnswerBatch(
			ctx, "create-race-user", targetID, batchID, QuestionAnswerFailed, QuestionAnswerErrorServiceShutdown,
		)
		result <- finalizeResult{found: found, err: finalizeErr}
	}()
	select {
	case early := <-result:
		t.Fatalf("finalizer returned before create transaction settled: found=%v err=%v", early.found, early.err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := createTx.Commit(ctx); err != nil {
		t.Fatalf("commit create transaction: %v", err)
	}
	select {
	case finalized := <-result:
		if finalized.err != nil || !finalized.found {
			t.Fatalf("finalize committed batch: found=%v err=%v", finalized.found, finalized.err)
		}
	case <-time.After(time.Second):
		t.Fatal("finalizer did not continue after create transaction committed")
	}
	records, err := repository.ListQuestionAnswerBatch(ctx, "create-race-user", targetID, batchID)
	if err != nil || len(records) != 1 {
		t.Fatalf("list finalized create-race batch: records=%+v err=%v", records, err)
	}
	if records[0].Status != QuestionAnswerFailed || records[0].ErrorType != QuestionAnswerErrorServiceShutdown {
		t.Fatalf("finalized create-race record=%+v", records[0])
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
	question, err := repository.CreateTestQuestion(ctx, "judgment-race-user", "Judgment race", "Race body", []string{})
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
		1,
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
	question, err := repository.CreateTestQuestion(ctx, "stats-user", "Stats question", "Stats body", []string{})
	if err != nil {
		t.Fatalf("create stats question: %v", err)
	}
	success, err := repository.CreateQuestionAnswerBatch(ctx, "stats-user", "stats-target", "stats-success", []string{"success-model"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 1)
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

	failed, err := repository.CreateQuestionAnswerBatch(ctx, "stats-user", "stats-target", "stats-failed", []string{"failed-model"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium, 1)
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

func TestQuestionAnswerRepositoryModelStatsLifetimeTodayAndEmptyArrays(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			status, answer_judgment, answer_body, completed_at
		) VALUES
			('stats-a-correct',    'model-stats-user', 'model-stats-target', 'stats-a', 'model-a',      'q1', 'Q1', 'Body', 'succeeded', 'correct',    'answer', now()),
			('stats-a-unreviewed', 'model-stats-user', 'model-stats-target', 'stats-a', 'model-a',      'q2', 'Q2', 'Body', 'succeeded', 'unreviewed', 'answer', now()),
			('stats-a-failed',     'model-stats-user', 'model-stats-target', 'stats-a', 'model-a',      'q3', 'Q3', 'Body', 'failed',    NULL,         '',       now()),
			('stats-b-incorrect',  'model-stats-user', 'model-stats-target', 'stats-b', 'model-b',      'q1', 'Q1', 'Body', 'succeeded', 'incorrect',  'answer', now()),
			('stats-b-cancelled',  'model-stats-user', 'model-stats-target', 'stats-b', 'model-b',      'q2', 'Q2', 'Body', 'cancelled', NULL,         '',       now()),
			('stats-failed-1',     'model-stats-user', 'model-stats-target', 'stats-f', 'model-failed', 'q1', 'Q1', 'Body', 'failed',    NULL,         '',       now()),
			('stats-failed-2',     'model-stats-user', 'model-stats-target', 'stats-f', 'model-failed', 'q2', 'Q2', 'Body', 'failed',    NULL,         '',       now())
	`); err != nil {
		t.Fatalf("insert model stats fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET created_at = (((now() AT TIME ZONE 'Asia/Shanghai')::date - interval '1 second') AT TIME ZONE 'Asia/Shanghai')
		WHERE id = 'stats-a-correct'
	`); err != nil {
		t.Fatalf("move one model-a record before Shanghai today: %v", err)
	}

	history, err := repository.ListQuestionAnswerHistory(ctx, "model-stats-user", "model-stats-target", 1)
	if err != nil {
		t.Fatalf("list model stats history: %v", err)
	}
	wantLifetime := QuestionAnswerStats{
		Requests: QuestionAnswerRequestStats{Submitted: 7, Succeeded: 3, Failed: 3, Cancelled: 1},
		Reviews:  QuestionAnswerReviewStats{Unreviewed: 1, Correct: 1, Incorrect: 1},
		ByModel: []QuestionAnswerModelStats{
			{ModelName: "model-a", Requests: QuestionAnswerRequestStats{Submitted: 3, Succeeded: 2, Failed: 1}, Reviews: QuestionAnswerReviewStats{Unreviewed: 1, Correct: 1}},
			{ModelName: "model-b", Requests: QuestionAnswerRequestStats{Submitted: 2, Succeeded: 1, Cancelled: 1}, Reviews: QuestionAnswerReviewStats{Incorrect: 1}},
			{ModelName: "model-failed", Requests: QuestionAnswerRequestStats{Submitted: 2, Failed: 2}},
		},
	}
	wantToday := QuestionAnswerStats{
		Requests: QuestionAnswerRequestStats{Submitted: 6, Succeeded: 2, Failed: 3, Cancelled: 1},
		Reviews:  QuestionAnswerReviewStats{Unreviewed: 1, Incorrect: 1},
		ByModel: []QuestionAnswerModelStats{
			{ModelName: "model-a", Requests: QuestionAnswerRequestStats{Submitted: 2, Succeeded: 1, Failed: 1}, Reviews: QuestionAnswerReviewStats{Unreviewed: 1}},
			{ModelName: "model-b", Requests: QuestionAnswerRequestStats{Submitted: 2, Succeeded: 1, Cancelled: 1}, Reviews: QuestionAnswerReviewStats{Incorrect: 1}},
			{ModelName: "model-failed", Requests: QuestionAnswerRequestStats{Submitted: 2, Failed: 2}},
		},
	}
	if !reflect.DeepEqual(history.Stats, wantLifetime) {
		t.Fatalf("lifetime stats=%+v want=%+v", history.Stats, wantLifetime)
	}
	if !reflect.DeepEqual(history.TodayStats, wantToday) {
		t.Fatalf("today stats=%+v want=%+v", history.TodayStats, wantToday)
	}
	assertQuestionAnswerStatsReconcile(t, history.Stats)
	assertQuestionAnswerStatsReconcile(t, history.TodayStats)
	assertQuestionAnswerModelSumEqualsTotal(t, history.Stats)
	assertQuestionAnswerModelSumEqualsTotal(t, history.TodayStats)

	pageTwo, err := repository.ListQuestionAnswerHistory(ctx, "model-stats-user", "model-stats-target", 2)
	if err != nil || len(pageTwo.Records) != 0 || !reflect.DeepEqual(pageTwo.Stats, history.Stats) || !reflect.DeepEqual(pageTwo.TodayStats, history.TodayStats) {
		t.Fatalf("page two stats changed with pagination: records=%d history=%+v err=%v", len(pageTwo.Records), pageTwo, err)
	}
	empty, err := repository.ListQuestionAnswerHistory(ctx, "model-stats-user", "empty-model-stats-target", 1)
	if err != nil {
		t.Fatalf("list empty model stats history: %v", err)
	}
	if empty.Stats.ByModel == nil || empty.TodayStats.ByModel == nil || len(empty.Stats.ByModel) != 0 || len(empty.TodayStats.ByModel) != 0 {
		t.Fatalf("empty byModel arrays lifetime=%#v today=%#v", empty.Stats.ByModel, empty.TodayStats.ByModel)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty history: %v", err)
	}
	if strings.Count(string(encoded), `"byModel":[]`) != 2 {
		t.Fatalf("empty history JSON=%s want two byModel arrays", encoded)
	}
}

func assertQuestionAnswerStatsReconcile(t *testing.T, stats QuestionAnswerStats) {
	t.Helper()
	if stats.Requests.Submitted != stats.Requests.InProgress+stats.Requests.Succeeded+stats.Requests.Failed+stats.Requests.Cancelled {
		t.Fatalf("request stats do not reconcile: %+v", stats.Requests)
	}
	if stats.Requests.Succeeded != stats.Reviews.Unreviewed+stats.Reviews.Correct+stats.Reviews.Incorrect {
		t.Fatalf("review stats do not reconcile: requests=%+v reviews=%+v", stats.Requests, stats.Reviews)
	}
	for _, model := range stats.ByModel {
		if model.Requests.Submitted != model.Requests.InProgress+model.Requests.Succeeded+model.Requests.Failed+model.Requests.Cancelled {
			t.Fatalf("model %s request stats do not reconcile: %+v", model.ModelName, model.Requests)
		}
		if model.Requests.Succeeded != model.Reviews.Unreviewed+model.Reviews.Correct+model.Reviews.Incorrect {
			t.Fatalf("model %s review stats do not reconcile: requests=%+v reviews=%+v", model.ModelName, model.Requests, model.Reviews)
		}
	}
}

func assertQuestionAnswerModelSumEqualsTotal(t *testing.T, stats QuestionAnswerStats) {
	t.Helper()
	var requests QuestionAnswerRequestStats
	var reviews QuestionAnswerReviewStats
	for _, model := range stats.ByModel {
		requests.Submitted += model.Requests.Submitted
		requests.InProgress += model.Requests.InProgress
		requests.Succeeded += model.Requests.Succeeded
		requests.Failed += model.Requests.Failed
		requests.Cancelled += model.Requests.Cancelled
		reviews.Unreviewed += model.Reviews.Unreviewed
		reviews.Correct += model.Reviews.Correct
		reviews.Incorrect += model.Reviews.Incorrect
	}
	if !reflect.DeepEqual(requests, stats.Requests) || !reflect.DeepEqual(reviews, stats.Reviews) {
		t.Fatalf("model sums requests=%+v reviews=%+v total requests=%+v reviews=%+v", requests, reviews, stats.Requests, stats.Reviews)
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

func TestQuestionAnswerTask2BrowserFixturePostgresContract(t *testing.T) {
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
	const userID, targetID = "task2-fixture-user", "sub2api:task2-fixture-target"
	const activeSentinelID, terminalSentinelID = "task2-non-fixture-active", "task2-non-fixture-terminal"
	if _, err := connection.Exec(ctx, `
		SELECT set_config('task2.user_id', $1, false), set_config('task2.target_id', $2, false)
	`, userID, targetID); err != nil {
		t.Fatalf("set fixture scope: %v", err)
	}
	batchIDs := []string{"task2-active-20260830", "task2-bulk-20260830", "task2-older-20260830"}
	defer func() {
		_, _ = connection.Exec(context.Background(), `
			DELETE FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2
			  AND (batch_id = ANY($3) OR id = ANY($4))
		`, userID, targetID, batchIDs, []string{activeSentinelID, terminalSentinelID})
	}()
	countRows := func() int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `
			SELECT count(*) FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND batch_id = ANY($3)
		`, userID, targetID, batchIDs).Scan(&count); err != nil {
			t.Fatalf("count fixture: %v", err)
		}
		return count
	}
	countBatch := func(records []QuestionAnswerRecord, batchID string) int {
		count := 0
		for _, record := range records {
			if record.BatchID == batchID {
				count++
			}
		}
		return count
	}
	countSentinel := func(id string) int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `
			SELECT count(*) FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND id = $3
		`, userID, targetID, id).Scan(&count); err != nil {
			t.Fatalf("count sentinel %s: %v", id, err)
		}
		return count
	}

	if _, err := connection.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body, status
		) VALUES ($1, $2, $3, 'task2-non-fixture-active-batch', 'model', 'q', 'Q', 'Body', 'pending')
	`, activeSentinelID, userID, targetID); err != nil {
		t.Fatalf("insert non-fixture active sentinel: %v", err)
	}
	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "non-fixture active batch exists") {
		t.Fatalf("active conflict prepare error=%v", err)
	}
	if got := countRows(); got != 0 {
		t.Fatalf("active conflict left %d fixture rows", got)
	}
	if got := countSentinel(activeSentinelID); got != 1 {
		t.Fatalf("active conflict changed sentinel count=%d, want 1", got)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = $1`, activeSentinelID); err != nil {
		t.Fatalf("delete non-fixture active sentinel: %v", err)
	}
	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got := countRows(); got != 29 {
		t.Fatalf("prepared rows=%d, want 29", got)
	}
	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "fixture id already exists") {
		t.Fatalf("duplicate prepare error=%v", err)
	}
	latest, err := repository.LatestQuestionAnswerBatch(ctx, userID, targetID)
	if err != nil || len(latest) != 2 || countBatch(latest, "task2-active-20260830") != 2 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	bulk, err := repository.ListQuestionAnswerBatch(ctx, userID, targetID, "task2-bulk-20260830")
	if err != nil || len(bulk) != 25 {
		t.Fatalf("bulk records=%d err=%v", len(bulk), err)
	}
	page1, err := repository.ListQuestionAnswerHistory(ctx, userID, targetID, 1)
	if err != nil || len(page1.Records) != 20 || page1.TotalItems != 29 || page1.TotalPages != 2 ||
		countBatch(page1.Records, "task2-active-20260830") != 2 ||
		countBatch(page1.Records, "task2-bulk-20260830") != 18 {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := repository.ListQuestionAnswerHistory(ctx, userID, targetID, 2)
	if err != nil || len(page2.Records) != 9 ||
		countBatch(page2.Records, "task2-bulk-20260830") != 7 ||
		countBatch(page2.Records, "task2-older-20260830") != 2 {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	if _, err := connection.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			status, created_at, completed_at, updated_at
		) VALUES ($1, $2, $3, 'task2-non-fixture-terminal-batch', 'model', 'q', 'Q', 'Body',
			'cancelled', now() - interval '1 day', now() - interval '1 day', now() - interval '1 day')
	`, terminalSentinelID, userID, targetID); err != nil {
		t.Fatalf("insert non-fixture terminal sentinel: %v", err)
	}
	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "cleanup"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if got := countRows(); got != 0 {
		t.Fatalf("rows after cleanup=%d", got)
	}
	if got := countSentinel(terminalSentinelID); got != 1 {
		t.Fatalf("cleanup changed non-fixture terminal sentinel count=%d, want 1", got)
	}

	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare short cleanup: %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = 'task2-bulk-20260830-25'`); err != nil {
		t.Fatalf("remove one fixture row: %v", err)
	}
	if err := runQuestionAnswerTask2FixtureAction(ctx, connection, "cleanup"); err == nil || !strings.Contains(err.Error(), "expected exactly twenty-nine deleted rows") {
		t.Fatalf("short cleanup error=%v", err)
	}
	if got := countRows(); got != 28 {
		t.Fatalf("failed cleanup rows=%d, want 28", got)
	}
}

func TestQuestionAnswerKeywordHighlightBrowserFixturePostgresContract(t *testing.T) {
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

	const userID, targetID = "task3-fixture-user", "sub2api:task3-fixture-target"
	const activeSentinelID = "task3-non-fixture-active"
	const terminalSentinelID = "task3-non-fixture-terminal"
	batchIDs := []string{
		"task3-latest-20260831",
		"task3-old-snapshot-20260831",
		"task3-highlight-limit-20260831",
		"task3-highlight-overlap-20260831",
	}
	if _, err := connection.Exec(ctx, `
		SELECT set_config('task3.user_id', $1, false), set_config('task3.target_id', $2, false)
	`, userID, targetID); err != nil {
		t.Fatalf("set fixture scope: %v", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `
			DELETE FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2
			  AND (batch_id = ANY($3) OR id = ANY($4))
		`, userID, targetID, batchIDs, []string{activeSentinelID, terminalSentinelID})
	}()

	countFixture := func() int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `
			SELECT count(*) FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND batch_id = ANY($3)
		`, userID, targetID, batchIDs).Scan(&count); err != nil {
			t.Fatalf("count task3 fixture: %v", err)
		}
		return count
	}
	countSentinel := func(id string) int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `
			SELECT count(*) FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND id = $3
		`, userID, targetID, id).Scan(&count); err != nil {
			t.Fatalf("count sentinel %s: %v", id, err)
		}
		return count
	}
	findRecord := func(records []QuestionAnswerRecord, id string) QuestionAnswerRecord {
		t.Helper()
		for _, record := range records {
			if record.ID == id {
				return record
			}
		}
		t.Fatalf("record %s not found in %+v", id, records)
		return QuestionAnswerRecord{}
	}

	if _, err := connection.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body, status
		) VALUES ($1, $2, $3, 'task3-non-fixture-active-batch', 'model', 'q', 'Q', 'Body', 'pending')
	`, activeSentinelID, userID, targetID); err != nil {
		t.Fatalf("insert active sentinel: %v", err)
	}
	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "non-fixture active batch exists") {
		t.Fatalf("active conflict prepare error=%v", err)
	}
	if got := countFixture(); got != 0 {
		t.Fatalf("active conflict left %d task3 fixture rows", got)
	}
	if got := countSentinel(activeSentinelID); got != 1 {
		t.Fatalf("active conflict changed sentinel count=%d, want 1", got)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = $1`, activeSentinelID); err != nil {
		t.Fatalf("delete active sentinel: %v", err)
	}

	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare task3 fixture: %v", err)
	}
	if got := countFixture(); got != 10 {
		t.Fatalf("prepared task3 rows=%d, want 10", got)
	}
	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "prepare"); err == nil || !strings.Contains(err.Error(), "fixture id already exists") {
		t.Fatalf("duplicate task3 prepare error=%v", err)
	}

	latest, err := repository.LatestQuestionAnswerBatch(ctx, userID, targetID)
	if err != nil || len(latest) != 7 {
		t.Fatalf("latest task3 records=%d err=%v", len(latest), err)
	}
	unreviewed := findRecord(latest, "task3-latest-unreviewed")
	if unreviewed.AnswerJudgment == nil || *unreviewed.AnswerJudgment != QuestionAnswerUnreviewed ||
		!reflect.DeepEqual(unreviewed.QuestionKeywordSnapshot, []string{"错误码", "Error"}) {
		t.Fatalf("latest unreviewed=%+v", unreviewed)
	}
	correct := findRecord(latest, "task3-latest-correct")
	incorrect := findRecord(latest, "task3-latest-incorrect")
	if correct.AnswerJudgment == nil || *correct.AnswerJudgment != QuestionAnswerCorrect ||
		incorrect.AnswerJudgment == nil || *incorrect.AnswerJudgment != QuestionAnswerIncorrect {
		t.Fatalf("judged records correct=%+v incorrect=%+v", correct, incorrect)
	}
	nullSnapshot := findRecord(latest, "task3-latest-null")
	emptySnapshot := findRecord(latest, "task3-latest-empty")
	if nullSnapshot.QuestionKeywordSnapshot != nil {
		t.Fatalf("null snapshot=%#v, want nil", nullSnapshot.QuestionKeywordSnapshot)
	}
	if emptySnapshot.QuestionKeywordSnapshot == nil || len(emptySnapshot.QuestionKeywordSnapshot) != 0 {
		t.Fatalf("empty snapshot=%#v, want non-nil empty", emptySnapshot.QuestionKeywordSnapshot)
	}
	htmlRecord := findRecord(latest, "task3-latest-html")
	if !strings.Contains(htmlRecord.AnswerBody, "<script>alert(1)</script>") ||
		!reflect.DeepEqual(htmlRecord.QuestionKeywordSnapshot, []string{"<script>", "[done]"}) {
		t.Fatalf("HTML literal record=%+v", htmlRecord)
	}
	maximum := findRecord(latest, "task3-latest-max-keywords")
	if len(maximum.QuestionKeywordSnapshot) != 20 {
		t.Fatalf("maximum keyword count=%d, want 20", len(maximum.QuestionKeywordSnapshot))
	}
	keywordBytes := 0
	seenKeywords := make(map[string]struct{}, len(maximum.QuestionKeywordSnapshot))
	for _, keyword := range maximum.QuestionKeywordSnapshot {
		keywordBytes += len([]byte(keyword))
		if utf8.RuneCountInString(keyword) > 64 {
			t.Fatalf("maximum keyword rune count=%d, want <=64", utf8.RuneCountInString(keyword))
		}
		folded := strings.ToLower(keyword)
		if _, exists := seenKeywords[folded]; exists {
			t.Fatalf("maximum keyword duplicated: %q", keyword)
		}
		seenKeywords[folded] = struct{}{}
	}
	if keywordBytes != 2048 {
		t.Fatalf("maximum keyword bytes=%d, want 2048", keywordBytes)
	}

	oldBatch, err := repository.ListQuestionAnswerBatch(ctx, userID, targetID, "task3-old-snapshot-20260831")
	if err != nil || len(oldBatch) != 1 ||
		!reflect.DeepEqual(oldBatch[0].QuestionKeywordSnapshot, []string{"旧关键字"}) ||
		!strings.Contains(oldBatch[0].AnswerBody, "当前配置关键字") {
		t.Fatalf("old snapshot batch=%+v err=%v", oldBatch, err)
	}
	highlightLimit, err := repository.ListQuestionAnswerBatch(ctx, userID, targetID, "task3-highlight-limit-20260831")
	if err != nil || len(highlightLimit) != 1 ||
		highlightLimit[0].AnswerBody != "Error one. Error two. Error three. Error four." ||
		!reflect.DeepEqual(highlightLimit[0].QuestionKeywordSnapshot, []string{"Error"}) {
		t.Fatalf("highlight-limit batch=%+v err=%v", highlightLimit, err)
	}
	highlightOverlap, err := repository.ListQuestionAnswerBatch(ctx, userID, targetID, "task3-highlight-overlap-20260831")
	if err != nil || len(highlightOverlap) != 1 ||
		highlightOverlap[0].AnswerBody != "错误码 one；错误码 two；错误码 three；错误码 four。" ||
		!reflect.DeepEqual(highlightOverlap[0].QuestionKeywordSnapshot, []string{"错误", "错误码"}) {
		t.Fatalf("highlight-overlap batch=%+v err=%v", highlightOverlap, err)
	}

	if _, err := connection.Exec(ctx, `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			status, created_at, completed_at, updated_at
		) VALUES ($1, $2, $3, 'task3-non-fixture-terminal-batch', 'model', 'q', 'Q', 'Body',
			'cancelled', now() - interval '1 day', now() - interval '1 day', now() - interval '1 day')
	`, terminalSentinelID, userID, targetID); err != nil {
		t.Fatalf("insert terminal sentinel: %v", err)
	}
	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "cleanup"); err != nil {
		t.Fatalf("cleanup task3 fixture: %v", err)
	}
	if got := countFixture(); got != 0 {
		t.Fatalf("task3 rows after cleanup=%d", got)
	}
	if got := countSentinel(terminalSentinelID); got != 1 {
		t.Fatalf("cleanup changed terminal sentinel count=%d, want 1", got)
	}

	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "prepare"); err != nil {
		t.Fatalf("prepare short cleanup task3 fixture: %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM connection_health_question_answer_records WHERE id = 'task3-highlight-overlap'`); err != nil {
		t.Fatalf("remove one task3 fixture row: %v", err)
	}
	if err := runQuestionAnswerTask3FixtureAction(ctx, connection, "cleanup"); err == nil || !strings.Contains(err.Error(), "expected exactly ten deleted rows") {
		t.Fatalf("short task3 cleanup error=%v", err)
	}
	if got := countFixture(); got != 9 {
		t.Fatalf("failed task3 cleanup rows=%d, want 9", got)
	}
}

func runQuestionAnswerTask3FixtureAction(ctx context.Context, connection *pgxpool.Conn, action string) error {
	fixtureSQL, err := os.ReadFile("testdata/task3_question_answer_keyword_highlight_browser_fixture.sql")
	if err != nil {
		return fmt.Errorf("read task3 fixture SQL: %w", err)
	}
	source := string(fixtureSQL)
	startMarker := "\\if :" + action + "\n"
	start := strings.Index(source, startMarker)
	if start < 0 {
		return fmt.Errorf("task3 fixture action %s not found", action)
	}
	start += len(startMarker)
	endRelative := strings.Index(source[start:], "\n\\endif")
	if endRelative < 0 {
		return fmt.Errorf("task3 fixture action %s end not found", action)
	}
	_, err = connection.Exec(ctx, source[start:start+endRelative])
	if err != nil {
		_, _ = connection.Exec(ctx, "ROLLBACK")
	}
	return err
}

func runQuestionAnswerTask2FixtureAction(ctx context.Context, connection *pgxpool.Conn, action string) error {
	fixtureSQL, err := os.ReadFile("testdata/task2_question_answer_batch_review_browser_fixture.sql")
	if err != nil {
		return fmt.Errorf("read task2 fixture SQL: %w", err)
	}
	source := string(fixtureSQL)
	startMarker := "\\if :" + action + "\n"
	start := strings.Index(source, startMarker)
	if start < 0 {
		return fmt.Errorf("task2 fixture action %s not found", action)
	}
	start += len(startMarker)
	endRelative := strings.Index(source[start:], "\n\\endif")
	if endRelative < 0 {
		return fmt.Errorf("task2 fixture action %s end not found", action)
	}
	_, err = connection.Exec(ctx, source[start:start+endRelative])
	if err != nil {
		_, _ = connection.Exec(ctx, "ROLLBACK")
	}
	return err
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
