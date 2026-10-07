package connection_health

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestQuestionAnswerC1MigrationSourcesAndHistoryCompatibility(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_question_answer_records DROP COLUMN answer_judgment_source,DROP COLUMN repeat_index`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment) VALUES
 ('old-correct','migration-user','target','old-correct','model','q','Q','Body','succeeded','correct'),
 ('old-incorrect','migration-user','target','old-incorrect','model','q','Q','Body','succeeded','incorrect'),
 ('old-unreviewed','migration-user','target','old-unreviewed','model','q','Q','Body','succeeded','unreviewed'),
 ('old-failed','migration-user','target','old-failed','model','q','Q','Body','failed',NULL),
 ('old-cancelled','migration-user','target','old-cancelled','model','q','Q','Body','cancelled',NULL)`); err != nil {
		t.Fatal(err)
	}
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000032_connection_health_question_answer_automatic.sql")
	for _, id := range []string{"old-correct", "old-incorrect", "old-unreviewed", "old-failed", "old-cancelled"} {
		records, err := repo.ListQuestionAnswerBatch(ctx, "migration-user", "target", id)
		if err != nil || len(records) != 1 {
			t.Fatalf("migration read=%v err=%v", records, err)
		}
		record := records[0]
		judged := id == "old-correct" || id == "old-incorrect"
		if judged && (record.JudgmentSource == nil || *record.JudgmentSource != QuestionAnswerJudgmentManual) {
			t.Fatalf("manual backfill missing for %s", id)
		}
		if !judged && record.JudgmentSource != nil {
			t.Fatalf("nonjudged backfilled for %s", id)
		}
		if record.RepeatIndex != nil {
			t.Fatal("historical repeat sequence fabricated")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET answer_judgment_source=NULL WHERE id='old-correct'`); err != nil {
		t.Fatal(err)
	}
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000032_connection_health_question_answer_automatic.sql")
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := repo.ListQuestionAnswerBatch(ctx, "migration-user", "target", "old-correct")
	if err != nil || records[0].JudgmentSource != nil || records[0].AnswerJudgment == nil || *records[0].AnswerJudgment != QuestionAnswerCorrect {
		t.Fatal("repeated schema changed source-null historical judgment")
	}
	for _, query := range []string{
		`UPDATE connection_health_question_answer_records SET answer_judgment_source='invalid' WHERE id='old-correct'`,
		`UPDATE connection_health_question_answer_records SET repeat_index=0 WHERE id='old-correct'`,
		`UPDATE connection_health_question_answer_records SET repeat_index=11 WHERE id='old-correct'`,
	} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("invalid source/repeat constraint accepted")
		}
	}
}

func TestQuestionAnswerC1HistoryTwentyBatchPagesAndCrossMidnightStats(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var today time.Time
	if err := pool.QueryRow(ctx, `SELECT ((now() AT TIME ZONE 'Asia/Singapore')::date + time '12:00:00') AT TIME ZONE 'Asia/Singapore'`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	for batch := 0; batch < 21; batch++ {
		id := fmt.Sprintf("page-%02d", batch)
		if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at) VALUES($1,'page-user','page-target',$1,'model','q','Q','Body','succeeded','correct',$2,$2)`, id, today); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at) VALUES
 ('cross-old','page-user','page-target','cross-batch','model','q','Q','Body','succeeded','incorrect',$1),
 ('cross-today','page-user','page-target','cross-batch','model','q','Q','Body','pending',NULL,$2)`, today.AddDate(0, 0, -1), today); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListQuestionAnswerHistory(ctx, "page-user", "page-target", 1, "today")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.ListQuestionAnswerHistory(ctx, "page-user", "page-target", 2, "today")
	if err != nil {
		t.Fatal(err)
	}
	extreme, err := repo.ListQuestionAnswerHistory(ctx, "page-user", "page-target", int(^uint(0)>>1), "today")
	if err != nil || len(extreme.Batches) != 0 || extreme.TotalBatches != 21 || extreme.TotalPages != 2 || extreme.TodayStats.Requests != first.TodayStats.Requests || extreme.AllTimeStats.Reviews != first.AllTimeStats.Reviews {
		t.Fatalf("MaxInt empty page=%+v err=%v", extreme, err)
	}

	if first.TotalBatches != 21 || first.TotalPages != 2 || len(first.Batches) != 20 || len(second.Batches) != 1 || first.Batches[0].BatchID != "page-20" || second.Batches[0].BatchID != "page-00" {
		t.Fatalf("complete stable pages=%+v/%+v", first, second)
	}
	if first.TodayStats.Requests.Submitted != 22 || first.TodayStats.Reviews.Correct != 21 || first.TodayStats.Reviews.Incorrect != 0 || first.TodayStats.Requests.InProgress != 1 || first.AllTimeStats.Requests.Submitted != 23 || first.AllTimeStats.Reviews.Incorrect != 1 {
		t.Fatal("record-day stats changed with batch attribution")
	}
	all, err := repo.ListQuestionAnswerHistory(ctx, "page-user", "page-target", 2, "all")
	if err != nil {
		t.Fatal(err)
	}
	if all.TotalBatches != 22 || len(all.Batches) != 2 || all.Batches[1].BatchID != "cross-batch" || !all.Batches[1].Active || all.Batches[1].CompletedAt != nil || all.Batches[1].Stats.Requests.Submitted != 2 {
		t.Fatalf("all/cross batch=%+v", all)
	}
	record, err := repo.SetQuestionAnswerJudgment(ctx, "page-user", "page-target", "cross-old", QuestionAnswerCorrect, questionAnswerTestUpdatedAt(t, repo, ctx, "page-user", "page-target", "cross-old"))
	if err != nil || record == nil {
		t.Fatalf("old record change=%+v err=%v", record, err)
	}
	changed, err := repo.ListQuestionAnswerHistory(ctx, "page-user", "page-target", 1, "today")
	if err != nil {
		t.Fatal(err)
	}
	if changed.TodayStats.Reviews.Correct != 21 || changed.AllTimeStats.Reviews.Correct != 22 || changed.AllTimeStats.Reviews.Incorrect != 0 {
		t.Fatal("old record judgment migrated into today's numerator")
	}
	summary, err := repo.GetQuestionAnswerTodayStats(ctx, "page-user", "page-target")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != changed.TodayStats.Requests || summary.Reviews != changed.TodayStats.Reviews {
		t.Fatal("summary and complete today stats differ")
	}
	accounts, err := repo.ListQuestionAnswerTodaySummaries(ctx, "page-user", []string{"page-target", "page-target", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if accounts["page-target"] != (QuestionAnswerTodaySummary{Submitted: 22, Judged: 21, Correct: 21}) {
		t.Fatalf("account projection=%+v", accounts)
	}
}
