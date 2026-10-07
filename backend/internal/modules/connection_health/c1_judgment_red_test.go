package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// These adapters follow the confirmed v3 signatures. Assertions below are the
// fixed C1 business contract, with runtime RED evidence against 53ef6d6.
func c1Complete(t *testing.T, repo *Repository, ctx context.Context, record QuestionAnswerRecord, status QuestionAnswerStatus, answer string) (bool, error) {
	t.Helper()
	completion := QuestionAnswerCompletion{Status: status, AnswerBody: answer}
	if status == QuestionAnswerSucceeded {
		judgment, judgeable := judgeQuestionAnswer(answer, record.QuestionKeywordSnapshot)
		completion.AnswerJudgment = &judgment
		if judgeable {
			source := QuestionAnswerJudgmentSource("automatic")
			completion.JudgmentSource = &source
		}
	}
	return repo.CompleteQuestionAnswer(ctx, "c1-user", record.BatchID, record.ID, completion)
}

func c1Set(t *testing.T, repo *Repository, ctx context.Context, record QuestionAnswerRecord, judgment QuestionAnswerJudgment, expected time.Time) (*QuestionAnswerRecord, error) {
	t.Helper()
	return repo.SetQuestionAnswerJudgment(ctx, "c1-user", record.TargetID, record.ID, judgment, expected)
}

func c1JSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func c1NewRecord(t *testing.T, repo *Repository, ctx context.Context, target, batch string, keywords []string) QuestionAnswerRecord {
	t.Helper()
	q, err := repo.CreateTestQuestion(ctx, "c1-user", "C1 isolated question", "Only answer the keyword", keywords)
	if err != nil {
		t.Fatal(err)
	}
	records, err := repo.CreateQuestionAnswerBatch(ctx, "c1-user", target, batch, []string{"model"}, []string{q.ID}, QuestionAnswerReasoningEffortMedium, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("create records=%d err=%v", len(records), err)
	}
	if ok, err := repo.MarkQuestionAnswerRunning(ctx, "c1-user", batch, records[0].ID); err != nil || !ok {
		t.Fatalf("mark running=%v err=%v", ok, err)
	}
	return records[0]
}

func c1ReadRecord(t *testing.T, repo *Repository, ctx context.Context, record QuestionAnswerRecord) QuestionAnswerRecord {
	t.Helper()
	rows, err := repo.ListQuestionAnswerBatch(ctx, "c1-user", record.TargetID, record.BatchID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read records=%d err=%v", len(rows), err)
	}
	return rows[0]
}

func TestC1QuestionAnswerAtomicAutomaticCompletion(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, answer, judgment string
		keywords               []string
		source                 any
	}{
		{"hit", "prefix ERROR suffix", "correct", []string{"missing", "error"}, "automatic"},
		{"miss", "no match", "incorrect", []string{"keyword"}, "automatic"},
		{"empty", "", "incorrect", []string{"keyword"}, "automatic"},
		{"manual-only", "anything", "unreviewed", []string{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := c1NewRecord(t, repo, ctx, tc.name, "batch-"+tc.name, tc.keywords)
			if ok, err := c1Complete(t, repo, ctx, record, QuestionAnswerSucceeded, tc.answer); err != nil || !ok {
				t.Fatalf("complete=%v err=%v", ok, err)
			}
			stored := c1ReadRecord(t, repo, ctx, record)
			jsonRecord := c1JSON(t, stored)
			if stored.Status != QuestionAnswerSucceeded || stored.AnswerBody != tc.answer || stored.AnswerJudgment == nil || string(*stored.AnswerJudgment) != tc.judgment || jsonRecord["judgmentSource"] != tc.source || stored.ManualError != (tc.judgment == "incorrect") {
				t.Fatalf("atomic result status=%s judgment=%v source=%v mirror=%v; want succeeded/%s/%v/%v", stored.Status, stored.AnswerJudgment, jsonRecord["judgmentSource"], stored.ManualError, tc.judgment, tc.source, tc.judgment == "incorrect")
			}
		})
	}
}

func TestC1QuestionAnswerManualSameReverseIdempotentAndConflict(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	record := c1NewRecord(t, repo, ctx, "manual-target", "manual-batch", []string{"hit"})
	if ok, err := c1Complete(t, repo, ctx, record, QuestionAnswerSucceeded, "hit"); err != nil || !ok {
		t.Fatalf("complete=%v err=%v", ok, err)
	}
	record = c1ReadRecord(t, repo, ctx, record)
	oldVersion := record.UpdatedAt
	same, err := c1Set(t, repo, ctx, record, QuestionAnswerCorrect, oldVersion)
	if err != nil || same == nil {
		t.Fatalf("same-value=%v err=%v", same, err)
	}
	if c1JSON(t, same)["judgmentSource"] != "manual" || same.AnswerJudgment == nil || *same.AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("same-value confirmation must persist manual/correct")
	}
	reverse, err := c1Set(t, repo, ctx, *same, QuestionAnswerIncorrect, same.UpdatedAt)
	if err != nil || reverse == nil || reverse.AnswerJudgment == nil || *reverse.AnswerJudgment != QuestionAnswerIncorrect || !reverse.ManualError {
		t.Fatalf("reverse=%v err=%v", reverse, err)
	}
	idempotent, err := c1Set(t, repo, ctx, *reverse, QuestionAnswerIncorrect, oldVersion)
	if err != nil || idempotent == nil || !idempotent.UpdatedAt.Equal(reverse.UpdatedAt) {
		t.Fatalf("same manual value must be idempotent even with old version")
	}
	_, err = c1Set(t, repo, ctx, *reverse, QuestionAnswerCorrect, oldVersion)
	if err == nil {
		t.Fatal("stale opposite judgment must conflict")
	}
	still := c1ReadRecord(t, repo, ctx, record)
	if still.AnswerJudgment == nil || *still.AnswerJudgment != QuestionAnswerIncorrect {
		t.Fatal("stale request overwrote authoritative judgment")
	}
	back, err := c1Set(t, repo, ctx, still, QuestionAnswerCorrect, still.UpdatedAt)
	if err != nil || back == nil || back.ManualError || *back.AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("reverse back=%v err=%v", back, err)
	}
}

func TestC1QuestionAnswerCompletionCancelFinalizerPreserveWholeTerminal(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, completionFirst := range []bool{true, false} {
		record := c1NewRecord(t, repo, ctx, fmt.Sprintf("race-%v", completionFirst), fmt.Sprintf("batch-%v", completionFirst), []string{"hit"})
		if completionFirst {
			if ok, err := c1Complete(t, repo, ctx, record, QuestionAnswerSucceeded, "hit"); err != nil || !ok {
				t.Fatalf("complete=%v err=%v", ok, err)
			}
		}
		if ok, err := repo.FinalizeQuestionAnswerBatch(ctx, "c1-user", record.TargetID, record.BatchID, QuestionAnswerCancelled, ""); err != nil || !ok {
			t.Fatalf("finalize=%v err=%v", ok, err)
		}
		if !completionFirst {
			if ok, err := c1Complete(t, repo, ctx, record, QuestionAnswerSucceeded, "hit"); err != nil || ok {
				t.Fatalf("late completion=%v err=%v", ok, err)
			}
		}
		stored := c1ReadRecord(t, repo, ctx, record)
		if completionFirst {
			if stored.Status != QuestionAnswerSucceeded || stored.AnswerBody != "hit" || stored.AnswerJudgment == nil || *stored.AnswerJudgment != QuestionAnswerCorrect || c1JSON(t, stored)["judgmentSource"] != "automatic" {
				t.Fatal("finalizer corrupted atomic succeeded result")
			}
		} else if stored.Status != QuestionAnswerCancelled || stored.AnswerBody != "" || stored.AnswerJudgment != nil || c1JSON(t, stored)["judgmentSource"] != nil {
			t.Fatal("late completion revived cancelled result")
		}
	}
}

func TestC1QuestionAnswerFiftyRecordsAreOneCompleteHistoryBatch(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := repo.CreateTestQuestion(ctx, "c1-user", "History", "Body", []string{"hit"})
	if err != nil {
		t.Fatal(err)
	}
	models := []string{"a", "b", "c", "d", "e"}
	records, err := repo.CreateQuestionAnswerBatch(ctx, "c1-user", "history-target", "fifty-batch", models, []string{q.ID}, QuestionAnswerReasoningEffortMedium, 10)
	if err != nil || len(records) != 50 {
		t.Fatalf("create=%d err=%v", len(records), err)
	}
	history, err := repo.ListQuestionAnswerHistory(ctx, "c1-user", "history-target", 1, "today")
	if err != nil {
		t.Fatal(err)
	}
	jsonHistory := c1JSON(t, history)
	batches, ok := jsonHistory["batches"].([]any)
	if !ok || len(batches) != 1 || jsonHistory["totalBatches"] != float64(1) {
		t.Fatalf("50 records must be one batch summary; batches=%v total=%v", jsonHistory["batches"], jsonHistory["totalBatches"])
	}
	batch := batches[0].(map[string]any)
	if batch["batchId"] != "fifty-batch" || batch["repeatCount"] != float64(10) {
		t.Fatalf("batch identity/repetition=%v", batch)
	}
	requests := batch["stats"].(map[string]any)["requests"].(map[string]any)
	if requests["submitted"] != float64(50) {
		t.Fatalf("history truncated batch: submitted=%v", requests["submitted"])
	}
	exact, err := repo.ListQuestionAnswerBatch(ctx, "c1-user", "history-target", "fifty-batch")
	if err != nil || len(exact) != 50 {
		t.Fatalf("exact=%d err=%v", len(exact), err)
	}
}
