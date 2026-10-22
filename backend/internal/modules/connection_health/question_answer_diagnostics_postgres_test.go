package connection_health

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestQuestionAnswerDiagnosticsPostgresMigrationRoundTripAndJudgment(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_question_answer_records DROP COLUMN request_diagnostics;
 INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status) VALUES
 ('diagnostic-old','user','target','old','model','q','Q','Body','failed')`); err != nil {
		t.Fatal(err)
	}
	path := "../../database/migrations/000043_connection_health_question_answer_diagnostics.sql"
	applyQuestionAnswerMigrationForTest(t, ctx, pool, path)
	applyQuestionAnswerMigrationForTest(t, ctx, pool, path)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := repo.ListQuestionAnswerBatch(ctx, "user", "target", "old")
	if err != nil || len(old) != 1 || old[0].Diagnostics != nil || old[0].DiagnosticsUnavailable {
		t.Fatal("migration changed legacy records")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET request_diagnostics='[]' WHERE id='diagnostic-old'`); err == nil {
		t.Fatal("JSONB object check missing")
	}
	for _, status := range []QuestionAnswerStatus{QuestionAnswerSucceeded, QuestionAnswerFailed, QuestionAnswerCancelled} {
		id := "diagnostic-" + string(status)
		if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,started_at) VALUES ($1,'user','target',$1,'model','q','Q','Body','running',now())`, id); err != nil {
			t.Fatal(err)
		}
		completion := questionAnswerTestCompletion(status, "", "")
		if status == QuestionAnswerSucceeded {
			completion = questionAnswerTestCompletion(status, "complete answer", "")
		}
		diagnostics := newQuestionAnswerProgress(time.Now()).snapshot(id).Diagnostics
		completion.Diagnostics = diagnostics
		if ok, err := repo.CompleteQuestionAnswer(ctx, "user", id, id, completion); err != nil || !ok {
			t.Fatalf("complete %s: %v", status, err)
		}
		records, err := repo.ListQuestionAnswerBatch(ctx, "user", "target", id)
		if err != nil || len(records) != 1 || records[0].Diagnostics == nil || records[0].Diagnostics.SelectedAt == nil {
			t.Fatalf("roundtrip %s: %v", status, err)
		}
		if status == QuestionAnswerSucceeded {
			judged, err := repo.SetQuestionAnswerJudgment(ctx, "user", "target", id, QuestionAnswerCorrect, records[0].UpdatedAt)
			if err != nil || judged == nil || judged.Diagnostics == nil || judged.AnswerBody != "complete answer" {
				t.Fatal("manual judgment lost answer or diagnostic")
			}
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET request_diagnostics='{"version":99}' WHERE id='diagnostic-succeeded'`); err != nil {
		t.Fatal(err)
	}
	records, err := repo.ListQuestionAnswerBatch(ctx, "user", "target", "diagnostic-succeeded")
	if err != nil || len(records) != 1 || records[0].AnswerBody != "complete answer" || records[0].Diagnostics != nil || !records[0].DiagnosticsUnavailable {
		t.Fatal("unknown diagnostic version damaged saved answer")
	}
	summaryRows, err := pool.Query(ctx, questionAnswerRecordSummarySelect()+` WHERE user_id='user' AND target_id='target'`)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := scanQuestionAnswerRecords(summaryRows)
	summaryRows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range summaries {
		if record.AnswerBody != "" || record.Diagnostics != nil || record.DiagnosticsUnavailable {
			t.Fatal("summary fetched excluded content")
		}
	}
	if strings.Contains(questionAnswerRecordSummarySelect(), "SELECT *") {
		t.Fatal("unbounded summary projection")
	}
}
