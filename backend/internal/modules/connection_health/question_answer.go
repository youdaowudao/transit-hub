package connection_health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

const (
	QuestionAnswerPageSize       = 20
	QuestionAnswerRequestTimeout = 10 * time.Minute
	TestQuestionNameLimit        = 100
	TestQuestionBodyLimit        = 4000
)

const (
	ErrorTestQuestionInvalid           = "admin.connectionHealth.errors.testQuestionInvalid"
	ErrorTestQuestionNotFound          = "admin.connectionHealth.errors.testQuestionNotFound"
	ErrorTestQuestionDisabled          = "admin.connectionHealth.errors.testQuestionDisabled"
	ErrorQuestionAnswerSelection       = "admin.connectionHealth.errors.questionAnswerSelection"
	ErrorQuestionAnswerReasoningEffort = "admin.connectionHealth.errors.questionAnswerReasoningEffort"
	ErrorQuestionAnswerActive          = "admin.connectionHealth.errors.questionAnswerActive"
	ErrorQuestionAnswerBatchNotFound   = "admin.connectionHealth.errors.questionAnswerBatchNotFound"
	ErrorQuestionAnswerStorage         = "admin.connectionHealth.errors.questionAnswerStorage"
	ErrorQuestionAnswerMarkForbidden   = "admin.connectionHealth.errors.questionAnswerMarkForbidden"
	ErrorQuestionAnswerServiceStopped  = "admin.connectionHealth.errors.questionAnswerServiceStopped"
)

const (
	QuestionAnswerErrorNetwork          = "network"
	QuestionAnswerErrorRateLimited      = "rate_limited"
	QuestionAnswerErrorAuth             = "auth"
	QuestionAnswerErrorModelNotFound    = "model_not_found"
	QuestionAnswerErrorServer           = "server_error"
	QuestionAnswerErrorInvalidResponse  = "invalid_response"
	QuestionAnswerErrorResponseTooLarge = "response_too_large"
	QuestionAnswerErrorTimeout          = "timeout"
	QuestionAnswerErrorStorage          = "storage_error"
	QuestionAnswerErrorServiceRestarted = "service_restarted"
	QuestionAnswerErrorServiceShutdown  = "service_shutdown"
)

type QuestionAnswerStatus string

type QuestionAnswerReasoningEffort string

const (
	QuestionAnswerReasoningEffortLow    QuestionAnswerReasoningEffort = "low"
	QuestionAnswerReasoningEffortMedium QuestionAnswerReasoningEffort = "medium"
	QuestionAnswerReasoningEffortHigh   QuestionAnswerReasoningEffort = "high"
	QuestionAnswerReasoningEffortXHigh  QuestionAnswerReasoningEffort = "xhigh"
)

const (
	QuestionAnswerPending   QuestionAnswerStatus = "pending"
	QuestionAnswerRunning   QuestionAnswerStatus = "running"
	QuestionAnswerSucceeded QuestionAnswerStatus = "succeeded"
	QuestionAnswerFailed    QuestionAnswerStatus = "failed"
	QuestionAnswerCancelled QuestionAnswerStatus = "cancelled"
)

type TestQuestion struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Enabled   bool      `json:"enabled"`
	IsDefault bool      `json:"isDefault"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TestQuestionInput struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

type QuestionAnswerRecord struct {
	ID              string                         `json:"id"`
	TargetID        string                         `json:"targetId"`
	BatchID         string                         `json:"batchId"`
	ModelName       string                         `json:"modelName"`
	QuestionID      string                         `json:"questionId"`
	QuestionName    string                         `json:"questionName"`
	QuestionBody    string                         `json:"questionBody"`
	ReasoningEffort *QuestionAnswerReasoningEffort `json:"reasoningEffort"`
	AnswerBody      string                         `json:"answerBody"`
	Status          QuestionAnswerStatus           `json:"status"`
	ErrorType       string                         `json:"errorType"`
	ManualError     bool                           `json:"manualError"`
	CreatedAt       time.Time                      `json:"createdAt"`
	StartedAt       *time.Time                     `json:"startedAt"`
	CompletedAt     *time.Time                     `json:"completedAt"`
	UpdatedAt       time.Time                      `json:"updatedAt"`
}

type QuestionAnswerStats struct {
	Total  int `json:"total"`
	Normal int `json:"normal"`
	Errors int `json:"errors"`
}

type QuestionAnswerHistory struct {
	Records    []QuestionAnswerRecord `json:"records"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"pageSize"`
	TotalItems int                    `json:"totalItems"`
	TotalPages int                    `json:"totalPages"`
	Stats      QuestionAnswerStats    `json:"stats"`
	TodayStats QuestionAnswerStats    `json:"todayStats"`
}

type QuestionAnswerBatch struct {
	BatchID         string                         `json:"batchId"`
	Records         []QuestionAnswerRecord         `json:"records"`
	ReasoningEffort *QuestionAnswerReasoningEffort `json:"reasoningEffort"`
	SubmittedCount  int                            `json:"submittedCount"`
	CompletedCount  int                            `json:"completedCount"`
	Active          bool                           `json:"active"`
	CurrentModel    string                         `json:"currentModel"`
	CurrentQuestion string                         `json:"currentQuestion"`
}

type QuestionAnswerStartInput struct {
	Models          []string `json:"models"`
	QuestionIDs     []string `json:"questionIds"`
	ReasoningEffort string   `json:"reasoningEffort"`
}

type activeQuestionAnswerBatch struct {
	userID   string
	targetID string
	batchID  string
	cred     upstream.ProbeCredential
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	reason   string
	done     chan struct{}
	doneOnce sync.Once
}

func (b *activeQuestionAnswerBatch) stop(reason string) {
	b.mu.Lock()
	if b.reason == "" {
		b.reason = reason
	}
	b.mu.Unlock()
	b.cancel()
}

func (b *activeQuestionAnswerBatch) stopped() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

func (b *activeQuestionAnswerBatch) finish() {
	b.doneOnce.Do(func() { close(b.done) })
}

func (s *Service) initializeQuestionAnswerRuntime() {
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	if s.questionAnswerCtx != nil {
		return
	}
	s.questionAnswerCtx, s.questionAnswerStop = context.WithCancel(context.Background())
	s.questionAnswerClosed = false
	s.questionAnswerRuns = make(map[string]*activeQuestionAnswerBatch)
	if s.questionAnswerHTTP == nil {
		s.questionAnswerHTTP = NewQuestionAnswerRunner()
	}
	if s.questionAnswerTTL <= 0 {
		s.questionAnswerTTL = QuestionAnswerRequestTimeout
	}
}

func (s *Service) ListTestQuestions(ctx context.Context, userID string) ([]TestQuestion, error) {
	if s.questionAnswers == nil {
		return nil, requestError(ErrorUnknown)
	}
	questions, err := s.questionAnswers.ListTestQuestions(ctx, userID)
	if questions == nil {
		questions = []TestQuestion{}
	}
	return questions, err
}

func (s *Service) CreateTestQuestion(ctx context.Context, userID string, input TestQuestionInput) (TestQuestion, error) {
	name, body, err := validateTestQuestionInput(input)
	if err != nil {
		return TestQuestion{}, err
	}
	return s.questionAnswers.CreateTestQuestion(ctx, userID, name, body)
}

func (s *Service) UpdateTestQuestion(ctx context.Context, userID string, questionID string, input TestQuestionInput) (TestQuestion, error) {
	name, body, err := validateTestQuestionInput(input)
	if err != nil {
		return TestQuestion{}, err
	}
	question, err := s.questionAnswers.UpdateTestQuestion(ctx, userID, strings.TrimSpace(questionID), name, body)
	if err != nil {
		return TestQuestion{}, err
	}
	if question == nil {
		return TestQuestion{}, requestError(ErrorTestQuestionNotFound)
	}
	return *question, nil
}

func (s *Service) SetTestQuestionEnabled(ctx context.Context, userID string, questionID string, enabled bool) (TestQuestion, error) {
	question, err := s.questionAnswers.SetTestQuestionEnabled(ctx, userID, strings.TrimSpace(questionID), enabled)
	if err != nil {
		return TestQuestion{}, err
	}
	if question == nil {
		return TestQuestion{}, requestError(ErrorTestQuestionNotFound)
	}
	return *question, nil
}

func (s *Service) SetDefaultTestQuestion(ctx context.Context, userID string, questionID string) (TestQuestion, error) {
	question, err := s.questionAnswers.SetDefaultTestQuestion(ctx, userID, strings.TrimSpace(questionID))
	if errors.Is(err, errQuestionAnswerUnavailable) {
		return TestQuestion{}, requestError(ErrorTestQuestionDisabled)
	}
	if err != nil {
		return TestQuestion{}, err
	}
	if question == nil {
		return TestQuestion{}, requestError(ErrorTestQuestionNotFound)
	}
	return *question, nil
}

func (s *Service) DeleteTestQuestion(ctx context.Context, userID string, questionID string) error {
	deleted, err := s.questionAnswers.DeleteTestQuestion(ctx, userID, strings.TrimSpace(questionID))
	if err != nil {
		return err
	}
	if !deleted {
		return requestError(ErrorTestQuestionNotFound)
	}
	return nil
}

func validateTestQuestionInput(input TestQuestionInput) (string, string, error) {
	name := strings.TrimSpace(input.Name)
	body := strings.TrimSpace(input.Body)
	if name == "" || body == "" || utf8.RuneCountInString(name) > TestQuestionNameLimit || utf8.RuneCountInString(body) > TestQuestionBodyLimit {
		return "", "", requestError(ErrorTestQuestionInvalid)
	}
	return name, body, nil
}

func (s *Service) StartQuestionAnswerBatch(_ context.Context, userID string, targetID string, input QuestionAnswerStartInput) (QuestionAnswerBatch, error) {
	reasoningEffort, err := normalizeQuestionAnswerReasoningEffort(input.ReasoningEffort)
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	models := uniqueNonEmpty(input.Models)
	questionIDs := uniqueNonEmpty(input.QuestionIDs)
	if len(models) == 0 || len(questionIDs) == 0 {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerSelection)
	}
	s.initializeQuestionAnswerRuntime()
	s.questionAnswerMu.Lock()
	startCtx := s.questionAnswerCtx
	closed := s.questionAnswerClosed
	s.questionAnswerMu.Unlock()
	if closed {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
	}

	session, _, account, _, err := s.resolveManualTarget(startCtx, userID, targetID)
	if err != nil {
		if startCtx.Err() != nil {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
		}
		return QuestionAnswerBatch{}, err
	}
	cred, err := s.resolveProbeCredential(startCtx, session, account)
	if err != nil {
		if startCtx.Err() != nil {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
		}
		return QuestionAnswerBatch{}, requestError(reasonToErrorKey(upstream.ProbeCredentialReason(err)))
	}
	discovered, err := s.modelDiscovery.ListModels(startCtx, cred.BaseURL, cred.Key)
	if err != nil {
		if startCtx.Err() != nil {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
		}
		return QuestionAnswerBatch{}, err
	}
	allowedModels := make(map[string]struct{}, len(discovered))
	for _, model := range discovered {
		allowedModels[model.ID] = struct{}{}
	}
	for _, model := range models {
		if _, ok := allowedModels[model]; !ok {
			return QuestionAnswerBatch{}, requestError(ErrorModelUnavailable)
		}
	}

	batchID, err := newID()
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	key := questionAnswerRunKey(userID, targetID)
	s.questionAnswerMu.Lock()
	if s.questionAnswerClosed || s.questionAnswerCtx.Err() != nil {
		s.questionAnswerMu.Unlock()
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
	}
	if _, exists := s.questionAnswerRuns[key]; exists {
		s.questionAnswerMu.Unlock()
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerActive)
	}
	batchCtx, cancel := context.WithCancel(s.questionAnswerCtx)
	run := &activeQuestionAnswerBatch{userID: userID, targetID: targetID, batchID: batchID, cred: cred, ctx: batchCtx, cancel: cancel, done: make(chan struct{})}
	s.questionAnswerRuns[key] = run
	s.questionAnswerWG.Add(1)
	s.questionAnswerMu.Unlock()

	records, err := s.questionAnswers.CreateQuestionAnswerBatch(startCtx, userID, targetID, batchID, models, questionIDs, reasoningEffort)
	if err != nil {
		run.stop(QuestionAnswerErrorStorage)
		s.removeQuestionAnswerRun(key, run)
		run.finish()
		s.questionAnswerWG.Done()
		if startCtx.Err() != nil {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
		}
		if errors.Is(err, errQuestionAnswerActive) {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerActive)
		}
		if errors.Is(err, errQuestionAnswerUnavailable) {
			return QuestionAnswerBatch{}, requestError(ErrorTestQuestionDisabled)
		}
		return QuestionAnswerBatch{}, err
	}
	if reason := run.stopped(); reason != "" || startCtx.Err() != nil {
		if reason == "" {
			reason = QuestionAnswerErrorServiceShutdown
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.questionAnswers.StopQuestionAnswerBatch(ctx, userID, targetID, batchID, QuestionAnswerFailed, reason)
		cancel()
		s.removeQuestionAnswerRun(key, run)
		run.finish()
		s.questionAnswerWG.Done()
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
	}

	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		s.failQuestionAnswerRun(run, QuestionAnswerErrorStorage)
		s.removeQuestionAnswerRun(key, run)
		run.finish()
		s.questionAnswerWG.Done()
		return QuestionAnswerBatch{}, err
	}
	go s.runQuestionAnswerBatch(key, run, cloneQuestionAnswerRecords(records))
	return batch, nil
}

func (s *Service) runQuestionAnswerBatch(key string, run *activeQuestionAnswerBatch, records []QuestionAnswerRecord) {
	defer s.questionAnswerWG.Done()
	defer run.finish()
	defer run.cancel()
	defer s.removeQuestionAnswerRun(key, run)

	for _, record := range records {
		if run.stopped() != "" || run.ctx.Err() != nil {
			return
		}
		running, err := s.questionAnswers.MarkQuestionAnswerRunning(run.ctx, run.userID, run.batchID, record.ID)
		if err != nil {
			s.failQuestionAnswerRun(run, QuestionAnswerErrorStorage)
			return
		}
		if !running {
			continue
		}

		itemCtx, cancel := context.WithTimeout(run.ctx, s.questionAnswerTTL)
		answer, errorType := s.questionAnswerHTTP.Ask(itemCtx, run.cred, record.ModelName, record.QuestionBody, questionAnswerReasoningEffortOrDefault(record.ReasoningEffort))
		itemErr := itemCtx.Err()
		cancel()
		if run.stopped() != "" || (run.ctx.Err() != nil && !errors.Is(itemErr, context.DeadlineExceeded)) {
			return
		}
		status := QuestionAnswerSucceeded
		if errors.Is(itemErr, context.DeadlineExceeded) {
			status = QuestionAnswerFailed
			answer = ""
			errorType = QuestionAnswerErrorTimeout
		} else if errorType != "" {
			status = QuestionAnswerFailed
			answer = ""
		}
		if _, err := s.questionAnswers.CompleteQuestionAnswer(run.ctx, run.userID, run.batchID, record.ID, status, answer, errorType); err != nil {
			s.failQuestionAnswerRun(run, QuestionAnswerErrorStorage)
			return
		}
	}
}

func (s *Service) failQuestionAnswerRun(run *activeQuestionAnswerBatch, errorType string) {
	run.stop(errorType)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.questionAnswers.StopQuestionAnswerBatch(ctx, run.userID, run.targetID, run.batchID, QuestionAnswerFailed, errorType)
}

func (s *Service) removeQuestionAnswerRun(key string, run *activeQuestionAnswerBatch) {
	s.questionAnswerMu.Lock()
	if current := s.questionAnswerRuns[key]; current == run {
		delete(s.questionAnswerRuns, key)
	}
	s.questionAnswerMu.Unlock()
}

func (s *Service) LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) (QuestionAnswerBatch, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerBatch{}, err
	}
	records, err := s.questionAnswers.LatestQuestionAnswerBatch(ctx, userID, targetID)
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	return buildQuestionAnswerBatch(records)
}

func (s *Service) GetQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) (QuestionAnswerBatch, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerBatch{}, err
	}
	records, err := s.questionAnswers.ListQuestionAnswerBatch(ctx, userID, targetID, strings.TrimSpace(batchID))
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	if len(records) == 0 {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerBatchNotFound)
	}
	return buildQuestionAnswerBatch(records)
}

func (s *Service) StopQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) (QuestionAnswerBatch, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerBatch{}, err
	}
	key := questionAnswerRunKey(userID, targetID)
	s.questionAnswerMu.Lock()
	run := s.questionAnswerRuns[key]
	s.questionAnswerMu.Unlock()
	if run != nil && run.batchID == batchID {
		run.stop(string(QuestionAnswerCancelled))
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	found, err := s.questionAnswers.StopQuestionAnswerBatch(stopCtx, userID, targetID, batchID, QuestionAnswerCancelled, "")
	cancel()
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	if !found {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerBatchNotFound)
	}
	if run != nil && run.batchID == batchID {
		select {
		case <-run.done:
		case <-ctx.Done():
			return QuestionAnswerBatch{}, ctx.Err()
		}
	}
	return s.GetQuestionAnswerBatch(ctx, userID, targetID, batchID)
}

func (s *Service) QuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int) (QuestionAnswerHistory, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerHistory{}, err
	}
	return s.questionAnswers.ListQuestionAnswerHistory(ctx, userID, targetID, page)
}

func (s *Service) SetQuestionAnswerManualError(ctx context.Context, userID string, targetID string, recordID string, manualError bool) (QuestionAnswerRecord, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerRecord{}, err
	}
	record, err := s.questionAnswers.SetQuestionAnswerManualError(ctx, userID, targetID, strings.TrimSpace(recordID), manualError)
	if err != nil {
		return QuestionAnswerRecord{}, err
	}
	if record == nil {
		return QuestionAnswerRecord{}, requestError(ErrorQuestionAnswerMarkForbidden)
	}
	return *record, nil
}

func (s *Service) validateQuestionAnswerTarget(ctx context.Context, userID string, targetID string) error {
	parsed, ok := parseTargetID(strings.TrimSpace(targetID))
	if !ok {
		return requestError(ErrorProbeTargetNotFound)
	}
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return err
	}
	if parsed.adminAccountID != adminAccountID {
		return requestError(ErrorProbeTargetNotFound)
	}
	return nil
}

func (s *Service) ShutdownQuestionAnswers(ctx context.Context) error {
	s.initializeQuestionAnswerRuntime()
	s.questionAnswerMu.Lock()
	s.questionAnswerClosed = true
	runs := make([]*activeQuestionAnswerBatch, 0, len(s.questionAnswerRuns))
	for _, run := range s.questionAnswerRuns {
		runs = append(runs, run)
	}
	if s.questionAnswerStop != nil {
		s.questionAnswerStop()
	}
	s.questionAnswerMu.Unlock()

	var stopErrors []error
	for _, run := range runs {
		run.stop(QuestionAnswerErrorServiceShutdown)
		if _, err := s.questionAnswers.StopQuestionAnswerBatch(ctx, run.userID, run.targetID, run.batchID, QuestionAnswerFailed, QuestionAnswerErrorServiceShutdown); err != nil {
			stopErrors = append(stopErrors, err)
		}
	}
	done := make(chan struct{})
	go func() {
		s.questionAnswerWG.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		stopErrors = append(stopErrors, ctx.Err())
	case <-done:
	}
	return errors.Join(stopErrors...)
}

func buildQuestionAnswerBatch(records []QuestionAnswerRecord) (QuestionAnswerBatch, error) {
	reasoningEffort, err := aggregateQuestionAnswerReasoningEffort(records)
	if err != nil {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
	}
	batch := QuestionAnswerBatch{Records: cloneQuestionAnswerRecords(records), ReasoningEffort: reasoningEffort, SubmittedCount: len(records)}
	if len(records) == 0 {
		batch.Records = []QuestionAnswerRecord{}
		return batch, nil
	}
	batch.BatchID = records[0].BatchID
	for _, record := range records {
		switch record.Status {
		case QuestionAnswerPending:
			batch.Active = true
			if batch.CurrentModel == "" {
				batch.CurrentModel, batch.CurrentQuestion = record.ModelName, record.QuestionName
			}
		case QuestionAnswerRunning:
			batch.Active = true
			batch.CurrentModel, batch.CurrentQuestion = record.ModelName, record.QuestionName
		case QuestionAnswerSucceeded, QuestionAnswerFailed, QuestionAnswerCancelled:
			batch.CompletedCount++
		}
	}
	return batch, nil
}

func normalizeQuestionAnswerReasoningEffort(value string) (QuestionAnswerReasoningEffort, error) {
	switch strings.TrimSpace(value) {
	case "":
		return QuestionAnswerReasoningEffortMedium, nil
	case string(QuestionAnswerReasoningEffortLow):
		return QuestionAnswerReasoningEffortLow, nil
	case string(QuestionAnswerReasoningEffortMedium):
		return QuestionAnswerReasoningEffortMedium, nil
	case string(QuestionAnswerReasoningEffortHigh):
		return QuestionAnswerReasoningEffortHigh, nil
	case string(QuestionAnswerReasoningEffortXHigh):
		return QuestionAnswerReasoningEffortXHigh, nil
	default:
		return "", requestError(ErrorQuestionAnswerReasoningEffort)
	}
}

func questionAnswerReasoningEffortOrDefault(value *QuestionAnswerReasoningEffort) QuestionAnswerReasoningEffort {
	if value == nil {
		return QuestionAnswerReasoningEffortMedium
	}
	return *value
}

func aggregateQuestionAnswerReasoningEffort(records []QuestionAnswerRecord) (*QuestionAnswerReasoningEffort, error) {
	var selected *QuestionAnswerReasoningEffort
	sawNull := false
	for _, record := range records {
		if record.ReasoningEffort == nil {
			if selected != nil {
				return nil, errors.New("question answer batch has mixed reasoning effort snapshots")
			}
			sawNull = true
			continue
		}
		if sawNull {
			return nil, errors.New("question answer batch has mixed reasoning effort snapshots")
		}
		if selected == nil {
			value := *record.ReasoningEffort
			selected = &value
			continue
		}
		if *selected != *record.ReasoningEffort {
			return nil, errors.New("question answer batch has mixed reasoning effort snapshots")
		}
	}
	return selected, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func questionAnswerRunKey(userID string, targetID string) string {
	return fmt.Sprintf("%s|%s", userID, targetID)
}
