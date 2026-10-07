package connection_health

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

// Legacy storage fixtures prepare unreviewed successes explicitly. Production
// dispatcher supplies the automatic result rather than delegating matching to a fake.
func questionAnswerTestCompletion(status QuestionAnswerStatus, answer, errorType string) QuestionAnswerCompletion {
	completion := QuestionAnswerCompletion{Status: status, AnswerBody: answer, ErrorType: errorType}
	if status == QuestionAnswerSucceeded {
		completion.AnswerJudgment = questionAnswerJudgmentPointer(QuestionAnswerUnreviewed)
	}
	return completion
}

func questionAnswerTestUpdatedAt(t *testing.T, repo *Repository, ctx context.Context, userID, targetID, recordID string) time.Time {
	t.Helper()
	var version time.Time
	err := repo.db.QueryRow(ctx, `SELECT updated_at FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=$2 AND id=$3`, userID, targetID, recordID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Now()
	}
	if err != nil {
		t.Fatalf("read test record version: %v", err)
	}
	return version
}
