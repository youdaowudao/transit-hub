package connection_health

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	errQuestionAnswerActive      = errors.New("question answer batch already active")
	errQuestionAnswerUnavailable = errors.New("question answer question unavailable")
)

type uncertainQuestionAnswerCreateError struct {
	cause error
}

func (e *uncertainQuestionAnswerCreateError) Error() string {
	return e.cause.Error()
}

func (e *uncertainQuestionAnswerCreateError) Unwrap() error {
	return e.cause
}

func (*uncertainQuestionAnswerCreateError) questionAnswerCreateResultUncertain() {}

func isQuestionAnswerCreateResultUncertain(err error) bool {
	var uncertain interface {
		questionAnswerCreateResultUncertain()
	}
	return errors.As(err, &uncertain)
}

type questionAnswerRepository interface {
	ListTestQuestions(ctx context.Context, userID string) ([]TestQuestion, error)
	CreateTestQuestion(ctx context.Context, userID string, name string, body string, keywords []string) (TestQuestion, error)
	UpdateTestQuestion(ctx context.Context, userID string, questionID string, name string, body string, keywords *[]string) (*TestQuestion, error)
	SetTestQuestionEnabled(ctx context.Context, userID string, questionID string, enabled bool) (*TestQuestion, error)
	SetDefaultTestQuestion(ctx context.Context, userID string, questionID string) (*TestQuestion, error)
	DeleteTestQuestion(ctx context.Context, userID string, questionID string) (bool, error)
	CreateQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int) ([]QuestionAnswerRecord, error)
	MarkQuestionAnswerRunning(ctx context.Context, userID string, batchID string, recordID string) (bool, error)
	CompleteQuestionAnswer(ctx context.Context, userID string, batchID string, recordID string, status QuestionAnswerStatus, answerBody string, errorType string) (bool, error)
	StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error)
	FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error)
	FailAbandonedQuestionAnswers(ctx context.Context, errorType string) (int64, error)
	ListQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) ([]QuestionAnswerRecord, error)
	LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) ([]QuestionAnswerRecord, error)
	ListQuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int) (QuestionAnswerHistory, error)
	SetQuestionAnswerJudgment(ctx context.Context, userID string, targetID string, recordID string, judgment QuestionAnswerJudgment) (*QuestionAnswerRecord, error)
}

func (r *Repository) ListTestQuestions(ctx context.Context, userID string) ([]TestQuestion, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, body, keywords, enabled, is_default, created_at, updated_at
		FROM connection_health_test_questions
		WHERE user_id = $1
		ORDER BY is_default DESC, created_at, id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	questions := make([]TestQuestion, 0)
	for rows.Next() {
		var question TestQuestion
		if err := rows.Scan(&question.ID, &question.Name, &question.Body, &question.Keywords, &question.Enabled, &question.IsDefault, &question.CreatedAt, &question.UpdatedAt); err != nil {
			return nil, err
		}
		questions = append(questions, question)
	}
	return questions, rows.Err()
}

func (r *Repository) CreateTestQuestion(ctx context.Context, userID string, name string, body string, keywords []string) (TestQuestion, error) {
	id, err := newID()
	if err != nil {
		return TestQuestion{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return TestQuestion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "question-default|"+userID); err != nil {
		return TestQuestion{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_test_questions WHERE user_id = $1`, userID).Scan(&count); err != nil {
		return TestQuestion{}, err
	}
	question := TestQuestion{ID: id, Name: name, Body: body, Enabled: true, IsDefault: count == 0}
	if err := tx.QueryRow(ctx, `
		INSERT INTO connection_health_test_questions (id, user_id, name, body, keywords, enabled, is_default)
		VALUES ($1, $2, $3, $4, $5, true, $6)
		RETURNING keywords, created_at, updated_at
	`, question.ID, userID, question.Name, question.Body, keywords, question.IsDefault).Scan(&question.Keywords, &question.CreatedAt, &question.UpdatedAt); err != nil {
		return TestQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TestQuestion{}, err
	}
	return question, nil
}

func (r *Repository) UpdateTestQuestion(ctx context.Context, userID string, questionID string, name string, body string, keywords *[]string) (*TestQuestion, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE connection_health_test_questions
		SET name = $3, body = $4, keywords = COALESCE($5::text[], keywords), updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, name, body, keywords, enabled, is_default, created_at, updated_at
	`, questionID, userID, name, body, keywords)
	return scanTestQuestion(row)
}

func (r *Repository) SetTestQuestionEnabled(ctx context.Context, userID string, questionID string, enabled bool) (*TestQuestion, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE connection_health_test_questions
		SET enabled = $3, is_default = CASE WHEN $3 THEN is_default ELSE false END, updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, name, body, keywords, enabled, is_default, created_at, updated_at
	`, questionID, userID, enabled)
	return scanTestQuestion(row)
}

func (r *Repository) SetDefaultTestQuestion(ctx context.Context, userID string, questionID string) (*TestQuestion, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "question-default|"+userID); err != nil {
		return nil, err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM connection_health_test_questions WHERE id = $1 AND user_id = $2`, questionID, userID).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !enabled {
		return nil, errQuestionAnswerUnavailable
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_health_test_questions SET is_default = false, updated_at = now() WHERE user_id = $1 AND is_default`, userID); err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		UPDATE connection_health_test_questions
		SET is_default = true, updated_at = now()
		WHERE id = $1 AND user_id = $2 AND enabled
		RETURNING id, name, body, keywords, enabled, is_default, created_at, updated_at
	`, questionID, userID)
	question, err := scanTestQuestion(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return question, nil
}

func (r *Repository) DeleteTestQuestion(ctx context.Context, userID string, questionID string) (bool, error) {
	result, err := r.db.Exec(ctx, `DELETE FROM connection_health_test_questions WHERE id = $1 AND user_id = $2`, questionID, userID)
	return result.RowsAffected() > 0, err
}

func scanTestQuestion(row rowScanner) (*TestQuestion, error) {
	var question TestQuestion
	if err := row.Scan(&question.ID, &question.Name, &question.Body, &question.Keywords, &question.Enabled, &question.IsDefault, &question.CreatedAt, &question.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &question, nil
}

func (r *Repository) CreateQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int) ([]QuestionAnswerRecord, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockKey := fmt.Sprintf("question-answer|%s|%s", userID, targetID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return nil, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND status IN ('pending', 'running')
		)
	`, userID, targetID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, errQuestionAnswerActive
	}

	rows, err := tx.Query(ctx, `
		SELECT id, name, body, keywords, enabled, is_default, created_at, updated_at
		FROM connection_health_test_questions
		WHERE user_id = $1 AND enabled AND id = ANY($2)
	`, userID, questionIDs)
	if err != nil {
		return nil, err
	}
	questionsByID := make(map[string]TestQuestion, len(questionIDs))
	for rows.Next() {
		var question TestQuestion
		if err := rows.Scan(&question.ID, &question.Name, &question.Body, &question.Keywords, &question.Enabled, &question.IsDefault, &question.CreatedAt, &question.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		questionsByID[question.ID] = question
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(questionsByID) != len(questionIDs) {
		return nil, errQuestionAnswerUnavailable
	}

	records := make([]QuestionAnswerRecord, 0, len(models)*len(questionIDs)*repeatCount)
	for _, model := range models {
		for _, questionID := range questionIDs {
			question := questionsByID[questionID]
			for sample := 0; sample < repeatCount; sample++ {
				recordID, err := newID()
				if err != nil {
					return nil, err
				}
				record := QuestionAnswerRecord{
					ID: recordID, TargetID: targetID, BatchID: batchID, ModelName: model,
					QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body,
					QuestionKeywordSnapshot: append([]string{}, question.Keywords...),
					ReasoningEffort:         questionAnswerReasoningEffortPointer(reasoningEffort),
					Status:                  QuestionAnswerPending,
				}
				if err := tx.QueryRow(ctx, `
					INSERT INTO connection_health_question_answer_records (
						id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
						question_keyword_snapshot, reasoning_effort, status
					) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending')
					RETURNING created_at, updated_at
				`, record.ID, userID, record.TargetID, record.BatchID, record.ModelName, record.QuestionID, record.QuestionName, record.QuestionBody, record.QuestionKeywordSnapshot, reasoningEffort).Scan(&record.CreatedAt, &record.UpdatedAt); err != nil {
					return nil, err
				}
				records = append(records, record)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return nil, err
		}
		return nil, &uncertainQuestionAnswerCreateError{cause: err}
	}
	return records, nil
}

func (r *Repository) MarkQuestionAnswerRunning(ctx context.Context, userID string, batchID string, recordID string) (bool, error) {
	result, err := r.db.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET status = 'running', started_at = now(), updated_at = now()
		WHERE id = $1 AND user_id = $2 AND batch_id = $3 AND status = 'pending'
	`, recordID, userID, batchID)
	return result.RowsAffected() > 0, err
}

func (r *Repository) CompleteQuestionAnswer(ctx context.Context, userID string, batchID string, recordID string, status QuestionAnswerStatus, answerBody string, errorType string) (bool, error) {
	result, err := r.db.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET status = $4,
			answer_body = $5,
			error_type = $6,
			answer_judgment = CASE WHEN $4 = 'succeeded' THEN 'unreviewed' ELSE NULL END,
			manual_error = false,
			completed_at = now(),
			updated_at = now()
		WHERE id = $1 AND user_id = $2 AND batch_id = $3 AND status = 'running'
	`, recordID, userID, batchID, status, answerBody, errorType)
	return result.RowsAffected() > 0, err
}

func (r *Repository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	return r.stopQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType, false)
}

func (r *Repository) FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	return r.stopQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType, true)
}

func (r *Repository) stopQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string, includeRunning bool) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := fmt.Sprintf("question-answer|%s|%s", userID, targetID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return false, err
	}
	var found bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM connection_health_question_answer_records
			WHERE user_id = $1 AND target_id = $2 AND batch_id = $3
		)
	`, userID, targetID, batchID).Scan(&found); err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	statusPredicate := "status = 'pending'"
	if includeRunning {
		statusPredicate = "status IN ('pending', 'running')"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET status = $4, answer_body = '', error_type = $5, answer_judgment = NULL, manual_error = false, completed_at = now(), updated_at = now()
		WHERE user_id = $1 AND target_id = $2 AND batch_id = $3 AND `+statusPredicate,
		userID, targetID, batchID, status, errorType,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) FailAbandonedQuestionAnswers(ctx context.Context, errorType string) (int64, error) {
	result, err := r.db.Exec(ctx, `
		UPDATE connection_health_question_answer_records
		SET status = 'failed', answer_body = '', error_type = $1, answer_judgment = NULL, manual_error = false, completed_at = now(), updated_at = now()
		WHERE status IN ('pending', 'running')
	`, errorType)
	return result.RowsAffected(), err
}

func (r *Repository) ListQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) ([]QuestionAnswerRecord, error) {
	rows, err := r.db.Query(ctx, questionAnswerRecordSelect+`
		WHERE user_id = $1 AND target_id = $2 AND batch_id = $3
		ORDER BY
			CASE WHEN started_at IS NULL THEN 1 ELSE 0 END,
			started_at,
			completed_at NULLS LAST,
			id
	`, userID, targetID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionAnswerRecords(rows)
}

func (r *Repository) LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) ([]QuestionAnswerRecord, error) {
	var batchID string
	if err := r.db.QueryRow(ctx, `
		SELECT batch_id FROM connection_health_question_answer_records
		WHERE user_id = $1 AND target_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, userID, targetID).Scan(&batchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return []QuestionAnswerRecord{}, nil
		}
		return nil, err
	}
	return r.ListQuestionAnswerBatch(ctx, userID, targetID, batchID)
}

func (r *Repository) ListQuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int) (QuestionAnswerHistory, error) {
	if page < 1 {
		page = 1
	}
	var totalItems int
	if err := r.db.QueryRow(ctx, `
		SELECT count(*) FROM connection_health_question_answer_records WHERE user_id = $1 AND target_id = $2
	`, userID, targetID).Scan(&totalItems); err != nil {
		return QuestionAnswerHistory{}, err
	}
	stats, todayStats, err := r.questionAnswerStats(ctx, userID, targetID)
	if err != nil {
		return QuestionAnswerHistory{}, err
	}
	rows, err := r.db.Query(ctx, questionAnswerRecordSelect+`
		WHERE user_id = $1 AND target_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3 OFFSET $4
	`, userID, targetID, QuestionAnswerPageSize, (page-1)*QuestionAnswerPageSize)
	if err != nil {
		return QuestionAnswerHistory{}, err
	}
	defer rows.Close()
	records, err := scanQuestionAnswerRecords(rows)
	if err != nil {
		return QuestionAnswerHistory{}, err
	}
	totalPages := 0
	if totalItems > 0 {
		totalPages = (totalItems + QuestionAnswerPageSize - 1) / QuestionAnswerPageSize
	}
	return QuestionAnswerHistory{
		Records: records, Page: page, PageSize: QuestionAnswerPageSize,
		TotalItems: totalItems, TotalPages: totalPages, Stats: stats, TodayStats: todayStats,
	}, nil
}

func (r *Repository) ListQuestionAnswerTodaySummaries(ctx context.Context, userID string, targetIDs []string) (map[string]QuestionAnswerTodaySummary, error) {
	summaries := make(map[string]QuestionAnswerTodaySummary)
	if len(targetIDs) == 0 {
		return summaries, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT target_id,
			count(*),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'correct')
		FROM connection_health_question_answer_records
		WHERE user_id = $1
			AND target_id = ANY($2::text[])
			AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date
		GROUP BY target_id
	`, userID, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var targetID string
		var summary QuestionAnswerTodaySummary
		if err := rows.Scan(&targetID, &summary.Submitted, &summary.Correct); err != nil {
			return nil, err
		}
		summaries[targetID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

func (r *Repository) questionAnswerStats(ctx context.Context, userID string, targetID string) (QuestionAnswerStats, QuestionAnswerStats, error) {
	stats := QuestionAnswerStats{ByModel: []QuestionAnswerModelStats{}}
	todayStats := QuestionAnswerStats{ByModel: []QuestionAnswerModelStats{}}
	rows, err := r.db.Query(ctx, `
		SELECT model_name,
			count(*),
			count(*) FILTER (WHERE status IN ('pending', 'running')),
			count(*) FILTER (WHERE status = 'succeeded'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'cancelled'),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'unreviewed'),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'correct'),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'incorrect'),
			count(*) FILTER (WHERE (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status IN ('pending', 'running') AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'succeeded' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'failed' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'cancelled' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'unreviewed' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'correct' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'incorrect' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date)
		FROM connection_health_question_answer_records
		WHERE user_id = $1 AND target_id = $2
		GROUP BY model_name
		ORDER BY model_name
	`, userID, targetID)
	if err != nil {
		return stats, todayStats, err
	}
	defer rows.Close()

	addModel := func(total *QuestionAnswerStats, model QuestionAnswerModelStats) {
		total.ByModel = append(total.ByModel, model)
		total.Requests.Submitted += model.Requests.Submitted
		total.Requests.InProgress += model.Requests.InProgress
		total.Requests.Succeeded += model.Requests.Succeeded
		total.Requests.Failed += model.Requests.Failed
		total.Requests.Cancelled += model.Requests.Cancelled
		total.Reviews.Unreviewed += model.Reviews.Unreviewed
		total.Reviews.Correct += model.Reviews.Correct
		total.Reviews.Incorrect += model.Reviews.Incorrect
	}
	for rows.Next() {
		var model QuestionAnswerModelStats
		var today QuestionAnswerModelStats
		if err := rows.Scan(
			&model.ModelName,
			&model.Requests.Submitted, &model.Requests.InProgress, &model.Requests.Succeeded, &model.Requests.Failed, &model.Requests.Cancelled,
			&model.Reviews.Unreviewed, &model.Reviews.Correct, &model.Reviews.Incorrect,
			&today.Requests.Submitted, &today.Requests.InProgress, &today.Requests.Succeeded, &today.Requests.Failed, &today.Requests.Cancelled,
			&today.Reviews.Unreviewed, &today.Reviews.Correct, &today.Reviews.Incorrect,
		); err != nil {
			return stats, todayStats, err
		}
		today.ModelName = model.ModelName
		addModel(&stats, model)
		if today.Requests.Submitted > 0 {
			addModel(&todayStats, today)
		}
	}
	if err := rows.Err(); err != nil {
		return stats, todayStats, err
	}
	return stats, todayStats, nil
}

func (r *Repository) SetQuestionAnswerJudgment(ctx context.Context, userID string, targetID string, recordID string, judgment QuestionAnswerJudgment) (*QuestionAnswerRecord, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE connection_health_question_answer_records
		SET answer_judgment = $4,
			manual_error = ($4 = 'incorrect'),
			updated_at = CASE
				WHEN answer_judgment IS DISTINCT FROM $4 OR manual_error IS DISTINCT FROM ($4 = 'incorrect') THEN now()
				ELSE updated_at
			END
		WHERE id = $1 AND user_id = $2 AND target_id = $3 AND status = 'succeeded'
		RETURNING id, target_id, batch_id, model_name, question_id, question_name, question_body, question_keyword_snapshot,
			reasoning_effort, answer_body, status, error_type, answer_judgment, created_at, started_at, completed_at, updated_at
	`, recordID, userID, targetID, judgment)
	return scanQuestionAnswerRecord(row)
}

const questionAnswerRecordSelect = `
	SELECT id, target_id, batch_id, model_name, question_id, question_name, question_body, question_keyword_snapshot,
		reasoning_effort, answer_body, status, error_type, answer_judgment, created_at, started_at, completed_at, updated_at
	FROM connection_health_question_answer_records
`

func scanQuestionAnswerRecords(rows pgx.Rows) ([]QuestionAnswerRecord, error) {
	records := make([]QuestionAnswerRecord, 0)
	for rows.Next() {
		record, err := scanQuestionAnswerRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func scanQuestionAnswerRecord(row rowScanner) (*QuestionAnswerRecord, error) {
	var record QuestionAnswerRecord
	var reasoningEffort *string
	if err := row.Scan(
		&record.ID, &record.TargetID, &record.BatchID, &record.ModelName, &record.QuestionID,
		&record.QuestionName, &record.QuestionBody, &record.QuestionKeywordSnapshot, &reasoningEffort, &record.AnswerBody, &record.Status,
		&record.ErrorType, &record.AnswerJudgment, &record.CreatedAt, &record.StartedAt,
		&record.CompletedAt, &record.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if reasoningEffort != nil {
		normalized, err := normalizeQuestionAnswerReasoningEffort(*reasoningEffort)
		if err != nil || strings.TrimSpace(*reasoningEffort) == "" {
			return nil, fmt.Errorf("invalid question answer reasoning effort snapshot")
		}
		record.ReasoningEffort = questionAnswerReasoningEffortPointer(normalized)
	}
	record.ManualError = record.AnswerJudgment != nil && *record.AnswerJudgment == QuestionAnswerIncorrect
	return &record, nil
}

func questionAnswerReasoningEffortPointer(value QuestionAnswerReasoningEffort) *QuestionAnswerReasoningEffort {
	return &value
}

func cloneQuestionAnswerRecords(records []QuestionAnswerRecord) []QuestionAnswerRecord {
	cloned := append([]QuestionAnswerRecord(nil), records...)
	for i := range cloned {
		if cloned[i].QuestionKeywordSnapshot != nil {
			cloned[i].QuestionKeywordSnapshot = append([]string{}, cloned[i].QuestionKeywordSnapshot...)
		}
	}
	return cloned
}
