package connection_health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/upstream"
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
	CreateQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error)
	MarkQuestionAnswerRunning(ctx context.Context, userID string, batchID string, recordID string) (bool, error)
	CompleteQuestionAnswer(ctx context.Context, userID string, batchID string, recordID string, completion QuestionAnswerCompletion) (bool, error)
	StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error)
	FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error)
	FailAbandonedQuestionAnswers(ctx context.Context, errorType string) (int64, error)
	ListQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) ([]QuestionAnswerRecord, error)
	LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) ([]QuestionAnswerRecord, error)
	ListQuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int, scope string) (QuestionAnswerHistory, error)
	GetQuestionAnswerTodayStats(ctx context.Context, userID string, targetID string) (QuestionAnswerSummaryStats, error)
	SetQuestionAnswerJudgment(ctx context.Context, userID string, targetID string, recordID string, judgment QuestionAnswerJudgment, expectedUpdatedAt time.Time) (*QuestionAnswerRecord, error)
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

func (r *Repository) CreateQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error) {
	adminAccountID := ""
	platform := ""
	requiresSnapshot := false
	if parsed, ok := parseTargetID(targetID); ok {
		adminAccountID = parsed.adminAccountID
		platform = parsed.platform
		requiresSnapshot = parsed.platform == string(upstream.PlatformSub2API)
	}
	if len(snapshots) > 1 || (requiresSnapshot && len(snapshots) != 1) {
		return nil, requestError(ErrorTestConfigurationUnavailable)
	}
	if len(snapshots) == 1 {
		snapshot := snapshots[0]
		if snapshot.AdminAccountID == "" || snapshot.AdminAccountID != adminAccountID || !snapshot.InventoryComplete {
			return nil, requestError(ErrorTestConfigurationUnavailable)
		}
	}
	tx, err := r.beginWorkspaceTransaction(ctx, userID, adminAccountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockKey := fmt.Sprintf("question-answer|%s|%s", userID, targetID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return nil, err
	}
	protocol := TestProtocolChatCompletions
	if len(snapshots) == 1 {
		configurations, err := listGroupTestConfigurationsTx(ctx, tx, userID, adminAccountID)
		if err != nil {
			return nil, err
		}
		configuration := ResolveGroupTestConfiguration(platform, snapshots[0].Memberships, snapshots[0].InventoryComplete, configurations)
		if !configuration.usable() {
			return nil, requestError(configuration.BlockedReason)
		}
		protocol = configuration.Protocol
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
					ID: recordID, TargetID: targetID, BatchID: batchID, ModelName: model, RequestProtocol: &protocol,
					QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body,
					QuestionKeywordSnapshot: append([]string{}, question.Keywords...),
					ReasoningEffort:         questionAnswerReasoningEffortPointer(reasoningEffort),
					Status:                  QuestionAnswerPending,
					RepeatIndex:             func() *int { value := sample + 1; return &value }(),
				}
				if err := tx.QueryRow(ctx, `
					INSERT INTO connection_health_question_answer_records (
						id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
						question_keyword_snapshot, reasoning_effort, request_protocol, repeat_index, status
					) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending')
					RETURNING created_at, updated_at
				`, record.ID, userID, record.TargetID, record.BatchID, record.ModelName, record.QuestionID, record.QuestionName, record.QuestionBody, record.QuestionKeywordSnapshot, reasoningEffort, protocol, record.RepeatIndex).Scan(&record.CreatedAt, &record.UpdatedAt); err != nil {
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

func (r *Repository) CompleteQuestionAnswer(ctx context.Context, userID string, batchID string, recordID string, completion QuestionAnswerCompletion) (bool, error) {
	if err := validateQuestionAnswerCompletion(completion); err != nil {
		return false, err
	}
	result, err := r.db.Exec(ctx, `
  UPDATE connection_health_question_answer_records
  SET status = $4, answer_body = $5, error_type = $6,
   answer_judgment = $7, answer_judgment_source = $8,
   manual_error = COALESCE($7 = 'incorrect', false),
   upstream_status = $9, upstream_excerpt = $10,
   completed_at = now(), updated_at = now()
  WHERE id = $1 AND user_id = $2 AND batch_id = $3 AND status = 'running'
 `, recordID, userID, batchID, completion.Status, completion.AnswerBody, completion.ErrorType, completion.AnswerJudgment, completion.JudgmentSource, completion.UpstreamStatus, completion.UpstreamExcerpt)
	return result.RowsAffected() > 0, err
}

func validateQuestionAnswerCompletion(completion QuestionAnswerCompletion) error {
	if (completion.Status == QuestionAnswerSucceeded || completion.Status == QuestionAnswerCancelled) && (completion.UpstreamStatus != nil || completion.UpstreamExcerpt != "") {
		return requestError(ErrorQuestionAnswerStorage)
	}
	switch completion.Status {
	case QuestionAnswerSucceeded:
		if completion.ErrorType != "" || completion.AnswerJudgment == nil {
			return requestError(ErrorQuestionAnswerStorage)
		}
		judgment := *completion.AnswerJudgment
		if judgment == QuestionAnswerUnreviewed && completion.JudgmentSource == nil {
			return nil
		}
		if validQuestionAnswerJudgment(judgment) && completion.JudgmentSource != nil && *completion.JudgmentSource == QuestionAnswerJudgmentAutomatic {
			return nil
		}
	case QuestionAnswerFailed, QuestionAnswerCancelled:
		if completion.AnswerBody == "" && completion.AnswerJudgment == nil && completion.JudgmentSource == nil {
			return nil
		}
	}
	return requestError(ErrorQuestionAnswerStorage)
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
		SET status = $4, answer_body = '', error_type = $5, answer_judgment = NULL, answer_judgment_source = NULL, manual_error = false, completed_at = now(), updated_at = now()
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
		SET status = 'failed', answer_body = '', error_type = $1, answer_judgment = NULL, answer_judgment_source = NULL, manual_error = false, completed_at = now(), updated_at = now()
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

func (r *Repository) ListQuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int, scope string) (QuestionAnswerHistory, error) {
	if page < 1 {
		return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerHistoryPage)
	}
	if scope != "today" && scope != "all" {
		return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerHistoryScope)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return QuestionAnswerHistory{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var total int
	const batchScope = `
  FROM connection_health_question_answer_records
  WHERE user_id = $1 AND target_id = $2
  GROUP BY batch_id
  HAVING $3::text = 'all' OR (MIN(created_at) AT TIME ZONE 'Asia/Singapore')::date = (now() AT TIME ZONE 'Asia/Singapore')::date
 `
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT batch_id `+batchScope+`) batches`, userID, targetID, scope).Scan(&total); err != nil {
		return QuestionAnswerHistory{}, err
	}
	todayStats, err := queryQuestionAnswerStats(ctx, tx, userID, targetID)
	if err != nil {
		return QuestionAnswerHistory{}, err
	}
	totalPages := 0
	if total > 0 {
		totalPages = 1 + (total-1)/QuestionAnswerPageSize
	}
	ids := make([]string, 0, QuestionAnswerPageSize)
	// A page beyond the complete batch count is empty, even at MaxInt. Do not
	// multiply an unbounded caller page into a potentially overflowing OFFSET.
	if page <= totalPages {
		rows, err := tx.Query(ctx, `SELECT batch_id,MIN(created_at) AS created_at `+batchScope+` ORDER BY created_at DESC,batch_id DESC LIMIT $4 OFFSET $5`, userID, targetID, scope, QuestionAnswerPageSize, int64(page-1)*QuestionAnswerPageSize)
		if err != nil {
			return QuestionAnswerHistory{}, err
		}
		for rows.Next() {
			var id string
			var created time.Time
			if err := rows.Scan(&id, &created); err != nil {
				rows.Close()
				return QuestionAnswerHistory{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return QuestionAnswerHistory{}, err
		}
	}

	batches := make([]QuestionAnswerBatchSummary, 0, len(ids))
	if len(ids) > 0 {
		// History summaries do not load long answer bodies; exact GET retains those.
		selectSummary := strings.Replace(questionAnswerRecordSelect, "answer_body,", "'' AS answer_body,", 1)
		rows, err := tx.Query(ctx, selectSummary+` WHERE user_id=$1 AND target_id=$2 AND batch_id=ANY($3::text[]) ORDER BY created_at,id`, userID, targetID, ids)
		if err != nil {
			return QuestionAnswerHistory{}, err
		}
		records, err := scanQuestionAnswerRecords(rows)
		rows.Close()
		if err != nil {
			return QuestionAnswerHistory{}, err
		}
		byBatch := make(map[string][]QuestionAnswerRecord)
		for _, record := range records {
			byBatch[record.BatchID] = append(byBatch[record.BatchID], record)
		}
		for _, id := range ids {
			if len(byBatch[id]) == 0 {
				return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerStorage)
			}
			summary, err := buildQuestionAnswerBatchSummary(byBatch[id])
			if err != nil {
				return QuestionAnswerHistory{}, err
			}
			batches = append(batches, summary)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return QuestionAnswerHistory{}, err
	}
	return QuestionAnswerHistory{Batches: batches, Page: page, PageSize: QuestionAnswerPageSize, TotalBatches: total, TotalPages: totalPages, TodayStats: todayStats}, nil
}

func (r *Repository) ListQuestionAnswerTodaySummaries(ctx context.Context, userID string, targetIDs []string) (map[string]QuestionAnswerTodaySummary, error) {
	summaries := make(map[string]QuestionAnswerTodaySummary)
	if len(targetIDs) == 0 {
		return summaries, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT target_id,
			count(*),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment IN ('correct','incorrect')),
			count(*) FILTER (WHERE status = 'succeeded' AND answer_judgment = 'correct')
		FROM connection_health_question_answer_records
		WHERE user_id = $1
			AND target_id = ANY($2::text[])
			AND (created_at AT TIME ZONE 'Asia/Singapore')::date = (now() AT TIME ZONE 'Asia/Singapore')::date
		GROUP BY target_id
	`, userID, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var targetID string
		var summary QuestionAnswerTodaySummary
		if err := rows.Scan(&targetID, &summary.Submitted, &summary.Judged, &summary.Correct); err != nil {
			return nil, err
		}
		summaries[targetID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// The latest terminal batch identity is chosen before judging its answers.
// Unjudged or failed newest batches never fall back to an older percentage.
func (r *Repository) ListLatestTerminalQuestionAnswerSummaries(ctx context.Context, userID string, targetIDs []string) (map[string]QuestionAnswerRecentSummaryItem, error) {
	result := make(map[string]QuestionAnswerRecentSummaryItem, len(targetIDs))
	for _, id := range targetIDs {
		result[id] = QuestionAnswerRecentSummaryItem{TargetID: id}
	}
	if len(targetIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.Query(ctx, `WITH batches AS (
  SELECT target_id,batch_id,MIN(created_at) AS created_at,MAX(completed_at) AS completed_at,
   count(*) AS submitted,count(*) FILTER(WHERE status IN ('pending','running')) AS in_progress,
   count(*) FILTER(WHERE status='succeeded') AS succeeded,count(*) FILTER(WHERE status='failed') AS failed,count(*) FILTER(WHERE status='cancelled') AS cancelled,
   count(*) FILTER(WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN ('correct','incorrect'))) AS unreviewed,
   count(*) FILTER(WHERE status='succeeded' AND answer_judgment='correct') AS correct,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='incorrect') AS incorrect
  FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=ANY($2::text[]) GROUP BY target_id,batch_id
 ),latest AS (
  SELECT DISTINCT ON(target_id) * FROM batches WHERE in_progress=0 ORDER BY target_id,created_at DESC,batch_id DESC
 )
 SELECT requested.target_id,l.batch_id,l.created_at,l.completed_at,
  COALESCE(e.trigger,'manual'),e.config_snapshot->'requested'->'schedule'->>'name',
  COALESCE(l.submitted,0),COALESCE(l.in_progress,0),COALESCE(l.succeeded,0),COALESCE(l.failed,0),COALESCE(l.cancelled,0),COALESCE(l.unreviewed,0),COALESCE(l.correct,0),COALESCE(l.incorrect,0),
  EXISTS(SELECT 1 FROM batches a WHERE a.target_id=requested.target_id AND a.in_progress>0 AND (l.batch_id IS NULL OR (a.created_at,a.batch_id)>(l.created_at,l.batch_id)))
 FROM unnest($2::text[]) AS requested(target_id) LEFT JOIN latest l ON l.target_id=requested.target_id
 LEFT JOIN connection_health_question_answer_schedule_execution_targets t ON t.id=l.batch_id AND t.target_id=l.target_id
 LEFT JOIN connection_health_question_answer_schedule_executions e ON e.id=t.execution_id AND e.user_id=$1`, userID, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var batchID *string
		var created *time.Time
		var recent QuestionAnswerRecentSummary
		var active bool
		if err = rows.Scan(&id, &batchID, &created, &recent.CompletedAt, &recent.Source, &recent.ScheduleName, &recent.Requests.Submitted, &recent.Requests.InProgress, &recent.Requests.Succeeded, &recent.Requests.Failed, &recent.Requests.Cancelled, &recent.Reviews.Unreviewed, &recent.Reviews.Correct, &recent.Reviews.Incorrect, &active); err != nil {
			return nil, err
		}
		item := QuestionAnswerRecentSummaryItem{TargetID: id, ActiveNewerBatch: active}
		if batchID != nil {
			if created == nil {
				return nil, requestError(ErrorQuestionAnswerStorage)
			}
			recent.BatchID = *batchID
			recent.CreatedAt = *created
			recent.Partial = recent.Requests.Succeeded > 0 && (recent.Requests.Failed > 0 || recent.Requests.Cancelled > 0)
			item.RecentQuestionAnswer = &recent
		}
		result[id] = item
	}
	return result, rows.Err()
}

func questionAnswerRecordSummarySelect() string {
	return strings.Replace(questionAnswerRecordSelect, "answer_body,", "'' AS answer_body,", 1)
}

func (r *Repository) ListQuestionAnswerBatchSummaries(ctx context.Context, userID string, batchIDs []string) (map[string]QuestionAnswerBatchSummary, error) {
	result := map[string]QuestionAnswerBatchSummary{}
	if len(batchIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.Query(ctx, questionAnswerRecordSummarySelect()+` WHERE user_id=$1 AND batch_id=ANY($2::text[]) ORDER BY created_at,id`, userID, batchIDs)
	if err != nil {
		return nil, err
	}
	records, err := scanQuestionAnswerRecords(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	byBatch := map[string][]QuestionAnswerRecord{}
	for _, record := range records {
		byBatch[record.BatchID] = append(byBatch[record.BatchID], record)
	}
	for id, batchRecords := range byBatch {
		summary, err := buildQuestionAnswerBatchSummary(batchRecords)
		if err != nil {
			return nil, err
		}
		result[id] = summary
	}
	return result, nil
}

type questionAnswerQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (r *Repository) questionAnswerStats(ctx context.Context, userID string, targetID string) (QuestionAnswerStats, error) {
	return queryQuestionAnswerStats(ctx, r.db, userID, targetID)
}

func queryQuestionAnswerStats(ctx context.Context, db questionAnswerQueryer, userID string, targetID string) (QuestionAnswerStats, error) {
	today := newQuestionAnswerStatsAccumulator()
	rows, err := db.Query(ctx, `
  SELECT model_name, question_id, question_body, question_keyword_snapshot,
   (array_agg(question_name ORDER BY created_at DESC,id DESC))[1], MAX(created_at),
   (array_agg(id ORDER BY created_at DESC,id DESC))[1],
   count(*), count(*) FILTER (WHERE status IN ('pending','running')),
   count(*) FILTER (WHERE status='succeeded'), count(*) FILTER (WHERE status='failed'), count(*) FILTER (WHERE status='cancelled'),
   count(*) FILTER (WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN ('correct','incorrect'))),
   count(*) FILTER (WHERE status='succeeded' AND answer_judgment='correct'), count(*) FILTER (WHERE status='succeeded' AND answer_judgment='incorrect')
  FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=$2 AND (created_at AT TIME ZONE 'Asia/Singapore')::date = (now() AT TIME ZONE 'Asia/Singapore')::date
  GROUP BY model_name, question_id, question_body, question_keyword_snapshot
 `, userID, targetID)
	if err != nil {
		return today.result(), err
	}
	defer rows.Close()
	for rows.Next() {
		var record QuestionAnswerRecord
		var r QuestionAnswerRequestStats
		var v QuestionAnswerReviewStats
		if err := rows.Scan(&record.ModelName, &record.QuestionID, &record.QuestionBody, &record.QuestionKeywordSnapshot, &record.QuestionName, &record.CreatedAt, &record.ID, &r.Submitted, &r.InProgress, &r.Succeeded, &r.Failed, &r.Cancelled, &v.Unreviewed, &v.Correct, &v.Incorrect); err != nil {
			return QuestionAnswerStats{}, err
		}
		today.add(record, r, v)
	}
	if err := rows.Err(); err != nil {
		return QuestionAnswerStats{}, err
	}
	return today.result(), nil
}

func (r *Repository) GetQuestionAnswerTodayStats(ctx context.Context, userID, targetID string) (QuestionAnswerSummaryStats, error) {
	var stats QuestionAnswerSummaryStats
	err := r.db.QueryRow(ctx, `
  SELECT count(*), count(*) FILTER (WHERE status IN ('pending','running')),
   count(*) FILTER (WHERE status='succeeded'), count(*) FILTER (WHERE status='failed'), count(*) FILTER (WHERE status='cancelled'),
   count(*) FILTER (WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN ('correct','incorrect'))),
   count(*) FILTER (WHERE status='succeeded' AND answer_judgment='correct'), count(*) FILTER (WHERE status='succeeded' AND answer_judgment='incorrect')
  FROM connection_health_question_answer_records
  WHERE user_id=$1 AND target_id=$2 AND (created_at AT TIME ZONE 'Asia/Singapore')::date=(now() AT TIME ZONE 'Asia/Singapore')::date
 `, userID, targetID).Scan(&stats.Requests.Submitted, &stats.Requests.InProgress, &stats.Requests.Succeeded, &stats.Requests.Failed, &stats.Requests.Cancelled, &stats.Reviews.Unreviewed, &stats.Reviews.Correct, &stats.Reviews.Incorrect)
	return stats, err
}

func (r *Repository) SetQuestionAnswerJudgment(ctx context.Context, userID string, targetID string, recordID string, judgment QuestionAnswerJudgment, expectedUpdatedAt time.Time) (*QuestionAnswerRecord, error) {
	if !validQuestionAnswerJudgment(judgment) || expectedUpdatedAt.IsZero() {
		return nil, requestError(ErrorRequest)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := scanQuestionAnswerRecord(tx.QueryRow(ctx, questionAnswerRecordSelect+` WHERE id=$1 AND user_id=$2 AND target_id=$3 AND status='succeeded' FOR UPDATE`, recordID, userID, targetID))
	if err != nil || record == nil {
		return record, err
	}
	if record.AnswerJudgment != nil && *record.AnswerJudgment == judgment && record.JudgmentSource != nil && *record.JudgmentSource == QuestionAnswerJudgmentManual {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return record, nil
	}
	if !record.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, requestError(ErrorQuestionAnswerJudgmentConflict)
	}
	record, err = scanQuestionAnswerRecord(tx.QueryRow(ctx, `
  UPDATE connection_health_question_answer_records SET answer_judgment=$4,answer_judgment_source='manual',manual_error=($4='incorrect'),updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
  WHERE id=$1 AND user_id=$2 AND target_id=$3 AND status='succeeded'
  RETURNING id,target_id,batch_id,model_name,question_id,question_name,question_body,question_keyword_snapshot,reasoning_effort,answer_body,status,error_type,answer_judgment,created_at,started_at,completed_at,updated_at,request_protocol,answer_judgment_source,repeat_index,upstream_status,upstream_excerpt
 `, recordID, userID, targetID, judgment))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return record, nil
}

const questionAnswerRecordSelect = `
	SELECT id, target_id, batch_id, model_name, question_id, question_name, question_body, question_keyword_snapshot,
		reasoning_effort, answer_body, status, error_type, answer_judgment, created_at, started_at, completed_at, updated_at, request_protocol, answer_judgment_source, repeat_index, upstream_status, upstream_excerpt
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
		&record.CompletedAt, &record.UpdatedAt, &record.RequestProtocol, &record.JudgmentSource, &record.RepeatIndex,
		&record.UpstreamStatus, &record.UpstreamExcerpt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if record.JudgmentSource != nil && *record.JudgmentSource != QuestionAnswerJudgmentAutomatic && *record.JudgmentSource != QuestionAnswerJudgmentManual {
		return nil, fmt.Errorf("invalid question answer judgment source")
	}
	if record.RepeatIndex != nil && (*record.RepeatIndex < 1 || *record.RepeatIndex > QuestionAnswerRepeatCountLimit) {
		return nil, fmt.Errorf("invalid question answer repeat index")
	}

	if record.RequestProtocol != nil && !validTestProtocol(*record.RequestProtocol) {
		return nil, fmt.Errorf("invalid question answer protocol snapshot")
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
