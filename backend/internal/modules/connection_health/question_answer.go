package connection_health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

const (
	QuestionAnswerPageSize         = 20
	QuestionAnswerRequestTimeout   = 10 * time.Minute
	QuestionAnswerRepeatCountLimit = 10
	QuestionAnswerBatchRecordLimit = 50
	questionAnswerConcurrency      = 5
	TestQuestionNameLimit          = 100
	TestQuestionBodyLimit          = 4000
	TestQuestionKeywordCountLimit  = 20
	TestQuestionKeywordRuneLimit   = 64
	TestQuestionKeywordBytesLimit  = 2048
)

const (
	ErrorTestQuestionInvalid             = "admin.connectionHealth.errors.testQuestionInvalid"
	ErrorTestQuestionKeywordBlank        = "admin.connectionHealth.errors.testQuestionKeywordBlank"
	ErrorTestQuestionKeywordCount        = "admin.connectionHealth.errors.testQuestionKeywordCount"
	ErrorTestQuestionKeywordLength       = "admin.connectionHealth.errors.testQuestionKeywordLength"
	ErrorTestQuestionKeywordBytes        = "admin.connectionHealth.errors.testQuestionKeywordBytes"
	ErrorTestQuestionNotFound            = "admin.connectionHealth.errors.testQuestionNotFound"
	ErrorTestQuestionDisabled            = "admin.connectionHealth.errors.testQuestionDisabled"
	ErrorQuestionAnswerSelection         = "admin.connectionHealth.errors.questionAnswerSelection"
	ErrorQuestionAnswerReasoningEffort   = "admin.connectionHealth.errors.questionAnswerReasoningEffort"
	ErrorQuestionAnswerRepeatCount       = "admin.connectionHealth.errors.questionAnswerRepeatCount"
	ErrorQuestionAnswerBatchLimit        = "admin.connectionHealth.errors.questionAnswerBatchLimit"
	ErrorQuestionAnswerActive            = "admin.connectionHealth.errors.questionAnswerActive"
	ErrorQuestionAnswerBatchNotFound     = "admin.connectionHealth.errors.questionAnswerBatchNotFound"
	ErrorQuestionAnswerStorage           = "admin.connectionHealth.errors.questionAnswerStorage"
	ErrorQuestionAnswerContractMismatch  = "admin.connectionHealth.errors.questionAnswerContractMismatch"
	ErrorQuestionAnswerJudgmentConflict  = "admin.connectionHealth.errors.questionAnswerJudgmentConflict"
	ErrorQuestionAnswerHistoryScope      = "admin.connectionHealth.errors.questionAnswerHistoryScope"
	ErrorQuestionAnswerHistoryPage       = "admin.connectionHealth.errors.questionAnswerHistoryPage"
	ErrorQuestionAnswerJudgmentForbidden = "admin.connectionHealth.errors.questionAnswerJudgmentForbidden"
	ErrorQuestionAnswerServiceStopped    = "admin.connectionHealth.errors.questionAnswerServiceStopped"
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

type QuestionAnswerJudgment string

type QuestionAnswerJudgmentSource string

const (
	QuestionAnswerJudgmentAutomatic QuestionAnswerJudgmentSource = "automatic"
	QuestionAnswerJudgmentManual    QuestionAnswerJudgmentSource = "manual"
)

type QuestionAnswerReasoningEffort string

const (
	QuestionAnswerReasoningEffortLow    QuestionAnswerReasoningEffort = "low"
	QuestionAnswerReasoningEffortMedium QuestionAnswerReasoningEffort = "medium"
	QuestionAnswerReasoningEffortHigh   QuestionAnswerReasoningEffort = "high"
	QuestionAnswerReasoningEffortXHigh  QuestionAnswerReasoningEffort = "xhigh"
)

const (
	QuestionAnswerUnreviewed QuestionAnswerJudgment = "unreviewed"
	QuestionAnswerCorrect    QuestionAnswerJudgment = "correct"
	QuestionAnswerIncorrect  QuestionAnswerJudgment = "incorrect"
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
	Keywords  []string  `json:"keywords"`
	Enabled   bool      `json:"enabled"`
	IsDefault bool      `json:"isDefault"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TestQuestionInput struct {
	Name     string    `json:"name"`
	Body     string    `json:"body"`
	Keywords *[]string `json:"keywords"`
}

// QuestionAnswerConfigurationSnapshot carries membership evidence prepared outside W.
// Configuration values themselves are always re-read inside the create transaction.
type QuestionAnswerConfigurationSnapshot struct {
	AdminAccountID    string
	Memberships       []TestConfigurationSource
	InventoryComplete bool
}

type QuestionAnswerRecord struct {
	RequestProtocol         *TestProtocol                  `json:"requestProtocol"`
	ID                      string                         `json:"id"`
	TargetID                string                         `json:"targetId"`
	BatchID                 string                         `json:"batchId"`
	ModelName               string                         `json:"modelName"`
	QuestionID              string                         `json:"questionId"`
	QuestionName            string                         `json:"questionName"`
	QuestionBody            string                         `json:"questionBody"`
	QuestionKeywordSnapshot []string                       `json:"questionKeywordSnapshot"`
	ReasoningEffort         *QuestionAnswerReasoningEffort `json:"reasoningEffort"`
	AnswerBody              string                         `json:"answerBody"`
	Status                  QuestionAnswerStatus           `json:"status"`
	ErrorType               string                         `json:"errorType"`
	AnswerJudgment          *QuestionAnswerJudgment        `json:"answerJudgment"`
	JudgmentSource          *QuestionAnswerJudgmentSource  `json:"judgmentSource"`
	RepeatIndex             *int                           `json:"repeatIndex"`
	ManualError             bool                           `json:"manualError"`
	CreatedAt               time.Time                      `json:"createdAt"`
	StartedAt               *time.Time                     `json:"startedAt"`
	CompletedAt             *time.Time                     `json:"completedAt"`
	UpdatedAt               time.Time                      `json:"updatedAt"`
}

type QuestionAnswerRequestStats struct {
	Submitted  int `json:"submitted"`
	InProgress int `json:"inProgress"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
}

type QuestionAnswerReviewStats struct {
	Unreviewed int `json:"unreviewed"`
	Correct    int `json:"correct"`
	Incorrect  int `json:"incorrect"`
}

type QuestionAnswerModelStats struct {
	ModelName string                     `json:"modelName"`
	Requests  QuestionAnswerRequestStats `json:"requests"`
	Reviews   QuestionAnswerReviewStats  `json:"reviews"`
}

type QuestionAnswerQuestionStats struct {
	QuestionSnapshotKey string                     `json:"questionSnapshotKey"`
	QuestionID          string                     `json:"questionId"`
	DisplayQuestionName string                     `json:"displayQuestionName"`
	QuestionBody        string                     `json:"questionBody"`
	NormalizedKeywords  []string                   `json:"normalizedKeywords"`
	Requests            QuestionAnswerRequestStats `json:"requests"`
	Reviews             QuestionAnswerReviewStats  `json:"reviews"`
	ByModel             []QuestionAnswerModelStats `json:"byModel"`
}

type QuestionAnswerStats struct {
	Requests   QuestionAnswerRequestStats    `json:"requests"`
	Reviews    QuestionAnswerReviewStats     `json:"reviews"`
	ByModel    []QuestionAnswerModelStats    `json:"byModel"`
	ByQuestion []QuestionAnswerQuestionStats `json:"byQuestion"`
}

// QuestionAnswerTodaySummary is the local Singapore-day projection for account lists.
type QuestionAnswerTodaySummary struct {
	Submitted int
	Judged    int
	Correct   int
}

type QuestionAnswerSummaryStats struct {
	Requests QuestionAnswerRequestStats `json:"requests"`
	Reviews  QuestionAnswerReviewStats  `json:"reviews"`
}

type QuestionAnswerSummary struct {
	TargetID   string                     `json:"targetId"`
	TodayStats QuestionAnswerSummaryStats `json:"todayStats"`
}

type QuestionAnswerHistory struct {
	Batches      []QuestionAnswerBatchSummary `json:"batches"`
	Page         int                          `json:"page"`
	PageSize     int                          `json:"pageSize"`
	TotalBatches int                          `json:"totalBatches"`
	TotalPages   int                          `json:"totalPages"`
	TodayStats   QuestionAnswerStats          `json:"todayStats"`
	AllTimeStats QuestionAnswerStats          `json:"allTimeStats"`
}

type QuestionAnswerBatchSummary struct {
	BatchID         string                         `json:"batchId"`
	CreatedAt       time.Time                      `json:"createdAt"`
	StartedAt       *time.Time                     `json:"startedAt"`
	CompletedAt     *time.Time                     `json:"completedAt"`
	RequestProtocol *TestProtocol                  `json:"requestProtocol"`
	ReasoningEffort *QuestionAnswerReasoningEffort `json:"reasoningEffort"`
	Models          []string                       `json:"models"`
	Questions       []QuestionAnswerQuestionStats  `json:"questions"`
	RepeatCount     int                            `json:"repeatCount"`
	Active          bool                           `json:"active"`
	Stats           QuestionAnswerStats            `json:"stats"`
}

type QuestionAnswerCompletion struct {
	Status         QuestionAnswerStatus
	AnswerBody     string
	ErrorType      string
	AnswerJudgment *QuestionAnswerJudgment
	JudgmentSource *QuestionAnswerJudgmentSource
}

type QuestionAnswerBatch struct {
	Finalization    *QuestionAnswerFinalization    `json:"finalization,omitempty"`
	BatchID         string                         `json:"batchId"`
	Records         []QuestionAnswerRecord         `json:"records"`
	ReasoningEffort *QuestionAnswerReasoningEffort `json:"reasoningEffort"`
	RepeatCount     int                            `json:"repeatCount"`
	SubmittedCount  int                            `json:"submittedCount"`
	CompletedCount  int                            `json:"completedCount"`
	RunningCount    int                            `json:"runningCount"`
	Active          bool                           `json:"active"`
	CurrentModel    string                         `json:"currentModel"`
	CurrentQuestion string                         `json:"currentQuestion"`
	Stats           QuestionAnswerStats            `json:"stats"`
}

// QuestionAnswerFinalization describes a retained runtime reservation. It is
// independent of record status: StopPending may already have ended every row.
type QuestionAnswerFinalization struct {
	BatchID  string `json:"batchId"`
	State    string `json:"state"`
	Recovery string `json:"recovery"`
}

type QuestionAnswerStartInput struct {
	Models          []string        `json:"models"`
	QuestionIDs     []string        `json:"questionIds"`
	ReasoningEffort string          `json:"reasoningEffort"`
	RepeatCount     json.RawMessage `json:"repeatCount"`
}

type activeQuestionAnswerBatch struct {
	userID             string
	targetID           string
	batchID            string
	cred               upstream.ProbeCredential
	ctx                context.Context
	cancel             context.CancelFunc
	records            []QuestionAnswerRecord
	next               int
	inFlight           int
	heldSlots          int
	finalizing         bool
	stopReason         string
	finalErr           error
	done               chan struct{}
	finalizeSettled    chan struct{}
	finalizeGeneration uint64
	finalizeResults    map[uint64]error
}

type questionAnswerShutdownAttempt struct {
	done chan struct{}
	err  error
}

type questionAnswerShutdownRun struct {
	key             string
	run             *activeQuestionAnswerBatch
	entryGeneration uint64
	retryExisting   bool
}

func (s *Service) initializeQuestionAnswerRuntime() {
	s.questionAnswerMu.Lock()
	if s.questionAnswerCtx != nil {
		s.questionAnswerMu.Unlock()
		return
	}
	s.questionAnswerCtx, s.questionAnswerStop = context.WithCancel(context.Background())
	s.questionAnswerClosed = false
	s.questionAnswerRuns = make(map[string]*activeQuestionAnswerBatch)
	s.questionAnswerOrder = []string{}
	s.questionAnswerLastKey = ""
	s.questionAnswerWake = make(chan struct{}, 1)
	s.questionAnswerDispatcherDone = make(chan struct{})
	s.questionAnswerInFlight = 0
	s.questionAnswerShutdown = nil
	if s.questionAnswerStorageTimeout <= 0 {
		s.questionAnswerStorageTimeout = 5 * time.Second
	}
	if s.questionAnswerHTTP == nil {
		s.questionAnswerHTTP = NewQuestionAnswerRunner()
	}
	if s.questionAnswerTTL <= 0 {
		s.questionAnswerTTL = QuestionAnswerRequestTimeout
	}
	s.questionAnswerMu.Unlock()
	go s.runQuestionAnswerDispatcher()
}

func (s *Service) ListTestQuestions(ctx context.Context, userID string) ([]TestQuestion, error) {
	if s.questionAnswers == nil {
		return nil, requestError(ErrorUnknown)
	}
	questions, err := s.questionAnswers.ListTestQuestions(ctx, userID)
	if questions == nil {
		questions = []TestQuestion{}
	}
	for i := range questions {
		questions[i] = normalizeTestQuestionOutput(questions[i])
	}
	return questions, err
}

func (s *Service) CreateTestQuestion(ctx context.Context, userID string, input TestQuestionInput) (TestQuestion, error) {
	name, body, err := validateTestQuestionInput(input)
	if err != nil {
		return TestQuestion{}, err
	}
	keywords, err := normalizeTestQuestionKeywords(input.Keywords)
	if err != nil {
		return TestQuestion{}, err
	}
	if keywords == nil {
		empty := []string{}
		keywords = &empty
	}
	question, err := s.questionAnswers.CreateTestQuestion(ctx, userID, name, body, *keywords)
	return normalizeTestQuestionOutput(question), err
}

func (s *Service) UpdateTestQuestion(ctx context.Context, userID string, questionID string, input TestQuestionInput) (TestQuestion, error) {
	name, body, err := validateTestQuestionInput(input)
	if err != nil {
		return TestQuestion{}, err
	}
	keywords, err := normalizeTestQuestionKeywords(input.Keywords)
	if err != nil {
		return TestQuestion{}, err
	}
	question, err := s.questionAnswers.UpdateTestQuestion(ctx, userID, strings.TrimSpace(questionID), name, body, keywords)
	if err != nil {
		return TestQuestion{}, err
	}
	if question == nil {
		return TestQuestion{}, requestError(ErrorTestQuestionNotFound)
	}
	return normalizeTestQuestionOutput(*question), nil
}

func (s *Service) SetTestQuestionEnabled(ctx context.Context, userID string, questionID string, enabled bool) (TestQuestion, error) {
	question, err := s.questionAnswers.SetTestQuestionEnabled(ctx, userID, strings.TrimSpace(questionID), enabled)
	if err != nil {
		return TestQuestion{}, err
	}
	if question == nil {
		return TestQuestion{}, requestError(ErrorTestQuestionNotFound)
	}
	return normalizeTestQuestionOutput(*question), nil
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
	return normalizeTestQuestionOutput(*question), nil
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

func normalizeTestQuestionKeywords(input *[]string) (*[]string, error) {
	if input == nil {
		return nil, nil
	}

	normalized := make([]string, 0, len(*input))
	seen := make(map[string]struct{}, len(*input))
	totalBytes := 0
	for _, value := range *input {
		keyword := strings.TrimSpace(value)
		if keyword == "" {
			return nil, requestError(ErrorTestQuestionKeywordBlank)
		}
		if utf8.RuneCountInString(keyword) > TestQuestionKeywordRuneLimit {
			return nil, requestError(ErrorTestQuestionKeywordLength)
		}

		folded := asciiFoldTestQuestionKeyword(keyword)
		if _, exists := seen[folded]; exists {
			continue
		}
		seen[folded] = struct{}{}
		normalized = append(normalized, keyword)
		totalBytes += len(keyword)
	}

	if len(normalized) > TestQuestionKeywordCountLimit {
		return nil, requestError(ErrorTestQuestionKeywordCount)
	}
	if totalBytes > TestQuestionKeywordBytesLimit {
		return nil, requestError(ErrorTestQuestionKeywordBytes)
	}
	return &normalized, nil
}

func asciiFoldTestQuestionKeyword(value string) string {
	folded := []byte(value)
	for i, char := range folded {
		if char >= 'A' && char <= 'Z' {
			folded[i] = char + ('a' - 'A')
		}
	}
	return string(folded)
}

func normalizeTestQuestionOutput(question TestQuestion) TestQuestion {
	if question.Keywords == nil {
		question.Keywords = []string{}
		return question
	}
	question.Keywords = append([]string{}, question.Keywords...)
	return question
}

func (s *Service) StartQuestionAnswerBatch(_ context.Context, userID string, targetID string, input QuestionAnswerStartInput) (QuestionAnswerBatch, error) {
	reasoningEffort, err := normalizeQuestionAnswerReasoningEffort(input.ReasoningEffort)
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	models := uniqueNonEmpty(input.Models)
	questionIDs := uniqueNonEmpty(input.QuestionIDs)
	repeatCount, err := normalizeQuestionAnswerRepeatCount(input.RepeatCount)
	if err != nil {
		return QuestionAnswerBatch{}, err
	}
	if _, err := questionAnswerSubmissionCount(len(models), len(questionIDs), repeatCount); err != nil {
		return QuestionAnswerBatch{}, err
	}
	s.initializeQuestionAnswerRuntime()
	s.questionAnswerMu.Lock()
	startCtx := s.questionAnswerCtx
	closed := s.questionAnswerClosed
	s.questionAnswerMu.Unlock()
	if closed {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
	}

	session, target, account, adminAccountID, err := s.resolveManualTarget(startCtx, userID, targetID)
	if err != nil {
		if startCtx.Err() != nil {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerServiceStopped)
		}
		return QuestionAnswerBatch{}, err
	}
	if !target.TestConfiguration.usable() {
		return QuestionAnswerBatch{}, requestError(target.TestConfiguration.BlockedReason)
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
	run := &activeQuestionAnswerBatch{
		userID: userID, targetID: targetID, batchID: batchID, cred: cred,
		ctx: batchCtx, cancel: cancel, records: []QuestionAnswerRecord{},
		done: make(chan struct{}), finalizeSettled: make(chan struct{}),
		finalizeGeneration: 1, finalizeResults: make(map[uint64]error),
	}
	s.questionAnswerRuns[key] = run
	s.questionAnswerOrder = append(s.questionAnswerOrder, key)
	s.questionAnswerMu.Unlock()

	records, err := s.questionAnswers.CreateQuestionAnswerBatch(startCtx, userID, targetID, batchID, models, questionIDs, reasoningEffort, repeatCount, QuestionAnswerConfigurationSnapshot{AdminAccountID: adminAccountID, Memberships: target.TestMemberships, InventoryComplete: target.InventoryComplete})
	if err != nil {
		var startErr error
		stopReason := QuestionAnswerErrorStorage
		if startCtx.Err() != nil {
			startErr = requestError(ErrorQuestionAnswerServiceStopped)
			stopReason = QuestionAnswerErrorServiceShutdown
		} else if errors.Is(err, errQuestionAnswerActive) {
			startErr = requestError(ErrorQuestionAnswerActive)
		} else if errors.Is(err, errQuestionAnswerUnavailable) {
			startErr = requestError(ErrorTestQuestionDisabled)
		} else {
			startErr = err
		}
		if !isQuestionAnswerCreateResultUncertain(err) {
			s.discardQuestionAnswerStartReservation(key, run)
			return QuestionAnswerBatch{}, startErr
		}
		return QuestionAnswerBatch{}, s.finishFailedQuestionAnswerStart(key, run, nil, stopReason, startErr)
	}

	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		return QuestionAnswerBatch{}, s.finishFailedQuestionAnswerStart(key, run, records, QuestionAnswerErrorStorage, err)
	}
	s.questionAnswerMu.Lock()
	current := s.questionAnswerRuns[key]
	if current == run {
		run.records = cloneQuestionAnswerRecords(records)
	}
	closed = s.questionAnswerClosed || s.questionAnswerCtx.Err() != nil || current != run
	shutdownAttempt := s.questionAnswerShutdown
	if closed {
		if current == run && run.stopReason == "" {
			run.stopReason = QuestionAnswerErrorServiceShutdown
		}
		s.questionAnswerMu.Unlock()
		run.cancel()
		cleanupErr := s.waitQuestionAnswerShutdownAttempt(shutdownAttempt)
		if cleanupErr == nil && current == run && shutdownAttempt == nil {
			cleanupErr = s.finalizeFailedQuestionAnswerStart(key, run)
		}
		return QuestionAnswerBatch{}, errors.Join(requestError(ErrorQuestionAnswerServiceStopped), cleanupErr)
	}
	s.questionAnswerMu.Unlock()
	s.wakeQuestionAnswerDispatcher()
	return batch, nil
}

func (s *Service) discardQuestionAnswerStartReservation(key string, run *activeQuestionAnswerBatch) {
	s.questionAnswerMu.Lock()
	if current := s.questionAnswerRuns[key]; current != run {
		s.questionAnswerMu.Unlock()
		run.cancel()
		return
	}
	if !run.finalizing {
		s.removeQuestionAnswerRunLocked(key, run)
		close(run.done)
		s.questionAnswerMu.Unlock()
		run.cancel()
		return
	}
	settled := run.finalizeSettled
	s.questionAnswerMu.Unlock()
	<-settled
	s.questionAnswerMu.Lock()
	if current := s.questionAnswerRuns[key]; current == run {
		s.removeQuestionAnswerRunLocked(key, run)
		close(run.done)
	}
	s.questionAnswerMu.Unlock()
	run.cancel()
}

func (s *Service) finishFailedQuestionAnswerStart(key string, run *activeQuestionAnswerBatch, records []QuestionAnswerRecord, reason string, cause error) error {
	s.questionAnswerMu.Lock()
	if current := s.questionAnswerRuns[key]; current != run {
		shutdownAttempt := s.questionAnswerShutdown
		s.questionAnswerMu.Unlock()
		return errors.Join(cause, s.waitQuestionAnswerShutdownAttempt(shutdownAttempt))
	}
	run.records = cloneQuestionAnswerRecords(records)
	if run.stopReason == "" {
		run.stopReason = reason
	}
	shutdownAttempt := s.questionAnswerShutdown
	closed := s.questionAnswerClosed || s.questionAnswerCtx.Err() != nil
	s.questionAnswerMu.Unlock()
	run.cancel()
	if closed && shutdownAttempt != nil {
		return errors.Join(cause, s.waitQuestionAnswerShutdownAttempt(shutdownAttempt))
	}
	return errors.Join(cause, s.finalizeFailedQuestionAnswerStart(key, run))
}

func (s *Service) finalizeFailedQuestionAnswerStart(key string, run *activeQuestionAnswerBatch) error {
	s.questionAnswerMu.Lock()
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	s.questionAnswerMu.Unlock()
	stopCtx, cancel := s.questionAnswerStorageContext(context.Background())
	_, stopErr := s.questionAnswers.StopPendingQuestionAnswerBatch(stopCtx, run.userID, run.targetID, run.batchID, status, errorType)
	cancel()
	s.questionAnswerMu.Lock()
	shouldFinalize := s.beginQuestionAnswerRunFinalizationLocked(key, run)
	generation := run.finalizeGeneration
	settled := run.finalizeSettled
	s.questionAnswerMu.Unlock()
	if shouldFinalize {
		go func() { _ = s.finalizeQuestionAnswerRun(key, run) }()
	}
	<-settled
	s.questionAnswerMu.Lock()
	finalErr := run.finalizeResults[generation]
	s.questionAnswerMu.Unlock()
	return errors.Join(stopErr, finalErr)
}

func (s *Service) waitQuestionAnswerShutdownAttempt(attempt *questionAnswerShutdownAttempt) error {
	if attempt == nil {
		return nil
	}
	<-attempt.done
	s.questionAnswerMu.Lock()
	err := attempt.err
	s.questionAnswerMu.Unlock()
	return err
}

func (s *Service) LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) (QuestionAnswerBatch, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerBatch{}, err
	}
	for {
		run := s.questionAnswerFinalizationRun(userID, targetID, "")
		var records []QuestionAnswerRecord
		var err error
		if run != nil {
			records, err = s.questionAnswers.ListQuestionAnswerBatch(ctx, userID, targetID, run.batchID)
		} else {
			records, err = s.questionAnswers.LatestQuestionAnswerBatch(ctx, userID, targetID)
		}
		if err != nil {
			return QuestionAnswerBatch{}, err
		}
		projection, current := s.projectQuestionAnswerFinalization(userID, targetID, run, len(records) > 0)
		if !current {
			if err := ctx.Err(); err != nil {
				return QuestionAnswerBatch{}, err
			}
			continue
		}
		batch, err := buildQuestionAnswerBatch(records)
		batch.Finalization = projection
		return batch, err
	}
}

func (s *Service) questionAnswerFinalizationRun(userID, targetID, batchID string) *activeQuestionAnswerBatch {
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	run := s.questionAnswerRuns[questionAnswerRunKey(userID, targetID)]
	if run == nil || (batchID != "" && run.batchID != batchID) || (run.stopReason == "" && !run.finalizing && run.finalErr == nil) {
		return nil
	}
	return run
}

func (s *Service) projectQuestionAnswerFinalization(userID, targetID string, run *activeQuestionAnswerBatch, hasRecords bool) (*QuestionAnswerFinalization, bool) {
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	current := s.questionAnswerRuns[questionAnswerRunKey(userID, targetID)]
	if current != nil && current.stopReason == "" && !current.finalizing && current.finalErr == nil {
		current = nil
	}
	if current != run {
		return nil, false
	}
	if run == nil {
		return nil, true
	}
	projection := &QuestionAnswerFinalization{BatchID: run.batchID, State: "pending", Recovery: "unavailable"}
	if run.finalErr != nil {
		projection.State = "failed"
	}
	if !hasRecords {
		projection.Recovery = "service_shutdown"
	} else if run.finalErr != nil && !run.finalizing && run.inFlight == 0 {
		projection.Recovery = "cancel"
	}
	return projection, true
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
	batch, err := buildQuestionAnswerBatch(records)
	run := s.questionAnswerFinalizationRun(userID, targetID, strings.TrimSpace(batchID))
	if run != nil {
		batch.Finalization, _ = s.projectQuestionAnswerFinalization(userID, targetID, run, true)
	}
	return batch, err
}

func (s *Service) StopQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string) (QuestionAnswerBatch, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerBatch{}, err
	}
	batchID = strings.TrimSpace(batchID)
	key := questionAnswerRunKey(userID, targetID)
	s.questionAnswerMu.Lock()
	run := s.questionAnswerRuns[key]
	if run != nil && run.batchID != batchID {
		run = nil
	}
	entryGeneration := uint64(0)
	retryExisting := false
	if run != nil {
		s.ensureQuestionAnswerFinalizationLocked(run)
		entryGeneration = run.finalizeGeneration
		_, settled := run.finalizeResults[entryGeneration]
		retryExisting = settled && run.finalErr != nil && !run.finalizing && run.inFlight == 0
	}
	if run != nil && run.stopReason == "" {
		run.stopReason = string(QuestionAnswerCancelled)
	}
	status, errorType := questionAnswerRunFinalState(run, string(QuestionAnswerCancelled))
	s.questionAnswerMu.Unlock()
	if run != nil {
		run.cancel()
		s.wakeQuestionAnswerDispatcher()
	}
	stopCtx, cancel := s.questionAnswerStorageContext(ctx)
	found, stopErr := s.questionAnswers.StopPendingQuestionAnswerBatch(stopCtx, userID, targetID, batchID, status, errorType)
	cancel()
	if stopErr == nil && !found {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerBatchNotFound)
	}
	if run == nil {
		if stopErr != nil {
			return QuestionAnswerBatch{}, stopErr
		}
		return s.GetQuestionAnswerBatch(ctx, userID, targetID, batchID)
	}

	s.questionAnswerMu.Lock()
	if retryExisting {
		if current := s.questionAnswerRuns[key]; current == run && !run.finalizing && run.inFlight == 0 && run.finalizeGeneration == entryGeneration && run.finalErr != nil {
			s.resetQuestionAnswerFinalizationLocked(run)
		}
	}
	shouldFinalize := s.beginQuestionAnswerRunFinalizationLocked(key, run)
	generation := run.finalizeGeneration
	settled := run.finalizeSettled
	s.questionAnswerMu.Unlock()
	if shouldFinalize {
		go func() { _ = s.finalizeQuestionAnswerRun(key, run) }()
	}

	select {
	case <-settled:
	case <-ctx.Done():
		s.questionAnswerMu.Lock()
		finalErr := run.finalizeResults[generation]
		s.questionAnswerMu.Unlock()
		return QuestionAnswerBatch{}, errors.Join(stopErr, finalErr, ctx.Err())
	}
	s.questionAnswerMu.Lock()
	finalErr := run.finalizeResults[generation]
	s.questionAnswerMu.Unlock()
	if err := errors.Join(stopErr, finalErr); err != nil {
		return QuestionAnswerBatch{}, err
	}
	return s.GetQuestionAnswerBatch(ctx, userID, targetID, batchID)
}

func (s *Service) QuestionAnswerHistory(ctx context.Context, userID string, targetID string, page int, scope string) (QuestionAnswerHistory, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerHistory{}, err
	}
	if page < 1 {
		return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerHistoryPage)
	}
	if scope != "today" && scope != "all" {
		return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerHistoryScope)
	}
	history, err := s.questionAnswers.ListQuestionAnswerHistory(ctx, userID, targetID, page, scope)
	if err != nil {
		return QuestionAnswerHistory{}, requestError(ErrorQuestionAnswerStorage)
	}
	return history, nil
}

func (s *Service) GetQuestionAnswerSummary(ctx context.Context, userID, targetID string) (QuestionAnswerSummary, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerSummary{}, err
	}
	stats, err := s.questionAnswers.GetQuestionAnswerTodayStats(ctx, userID, targetID)
	if err != nil {
		return QuestionAnswerSummary{}, requestError(ErrorQuestionAnswerStorage)
	}
	return QuestionAnswerSummary{TargetID: targetID, TodayStats: stats}, nil
}

func (s *Service) SetQuestionAnswerJudgment(ctx context.Context, userID string, targetID string, recordID string, judgment QuestionAnswerJudgment, expectedUpdatedAt time.Time) (QuestionAnswerRecord, error) {
	if err := s.validateQuestionAnswerTarget(ctx, userID, targetID); err != nil {
		return QuestionAnswerRecord{}, err
	}
	if !validQuestionAnswerJudgment(judgment) || expectedUpdatedAt.IsZero() {
		return QuestionAnswerRecord{}, requestError(ErrorRequest)
	}
	record, err := s.questionAnswers.SetQuestionAnswerJudgment(ctx, userID, targetID, strings.TrimSpace(recordID), judgment, expectedUpdatedAt)
	if err != nil {
		if errors.Is(err, requestError(ErrorQuestionAnswerJudgmentConflict)) {
			return QuestionAnswerRecord{}, err
		}
		return QuestionAnswerRecord{}, requestError(ErrorQuestionAnswerStorage)
	}
	if record == nil {
		return QuestionAnswerRecord{}, requestError(ErrorQuestionAnswerJudgmentForbidden)
	}
	return *record, nil
}

func validQuestionAnswerJudgment(judgment QuestionAnswerJudgment) bool {
	return judgment == QuestionAnswerCorrect || judgment == QuestionAnswerIncorrect
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
	runs := make([]questionAnswerShutdownRun, 0, len(s.questionAnswerRuns))
	for key, run := range s.questionAnswerRuns {
		if run.stopReason == "" {
			run.stopReason = QuestionAnswerErrorServiceShutdown
		}
		s.ensureQuestionAnswerFinalizationLocked(run)
		generation := run.finalizeGeneration
		_, settled := run.finalizeResults[generation]
		runs = append(runs, questionAnswerShutdownRun{
			key: key, run: run, entryGeneration: generation,
			retryExisting: settled && run.finalErr != nil && !run.finalizing,
		})
	}
	rootStop := s.questionAnswerStop
	dispatcherDone := s.questionAnswerDispatcherDone
	attempt := s.questionAnswerShutdown
	startAttempt := attempt == nil
	if attempt != nil && questionAnswerChannelClosed(attempt.done) && attempt.err != nil {
		startAttempt = true
	}
	if startAttempt {
		attempt = &questionAnswerShutdownAttempt{done: make(chan struct{})}
		s.questionAnswerShutdown = attempt
	}
	s.questionAnswerMu.Unlock()
	if startAttempt {
		for _, item := range runs {
			item.run.cancel()
		}
		if rootStop != nil {
			rootStop()
		}
		s.wakeQuestionAnswerDispatcher()
		go s.runQuestionAnswerShutdown(attempt, runs, dispatcherDone)
	}

	select {
	case <-attempt.done:
		s.questionAnswerMu.Lock()
		err := attempt.err
		s.questionAnswerMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runQuestionAnswerShutdown(attempt *questionAnswerShutdownAttempt, runs []questionAnswerShutdownRun, dispatcherDone <-chan struct{}) {
	var stopErrors []error
	for _, item := range runs {
		s.questionAnswerMu.Lock()
		status, errorType := questionAnswerRunFinalState(item.run, QuestionAnswerErrorServiceShutdown)
		s.questionAnswerMu.Unlock()
		stopCtx, cancel := s.questionAnswerStorageContext(context.Background())
		_, err := s.questionAnswers.StopPendingQuestionAnswerBatch(stopCtx, item.run.userID, item.run.targetID, item.run.batchID, status, errorType)
		cancel()
		if err != nil {
			stopErrors = append(stopErrors, err)
		}
	}
	if dispatcherDone != nil {
		<-dispatcherDone
	}
	s.questionAnswerWG.Wait()

	type finalizationWait struct {
		run        *activeQuestionAnswerBatch
		generation uint64
		settled    <-chan struct{}
		start      bool
		key        string
	}
	waits := make([]finalizationWait, 0, len(runs))
	s.questionAnswerMu.Lock()
	for _, item := range runs {
		if current := s.questionAnswerRuns[item.key]; current != item.run {
			continue
		}
		if item.retryExisting && !item.run.finalizing && item.run.inFlight == 0 && item.run.finalizeGeneration == item.entryGeneration && item.run.finalErr != nil {
			s.resetQuestionAnswerFinalizationLocked(item.run)
		}
		start := s.beginQuestionAnswerRunFinalizationLocked(item.key, item.run)
		waits = append(waits, finalizationWait{
			run: item.run, generation: item.run.finalizeGeneration,
			settled: item.run.finalizeSettled, start: start, key: item.key,
		})
	}
	s.questionAnswerMu.Unlock()
	for _, item := range waits {
		if item.start {
			go func(key string, run *activeQuestionAnswerBatch) { _ = s.finalizeQuestionAnswerRun(key, run) }(item.key, item.run)
		}
	}
	for _, item := range waits {
		<-item.settled
		s.questionAnswerMu.Lock()
		finalErr := item.run.finalizeResults[item.generation]
		s.questionAnswerMu.Unlock()
		if finalErr != nil {
			stopErrors = append(stopErrors, finalErr)
		}
	}

	s.questionAnswerMu.Lock()
	attempt.err = errors.Join(stopErrors...)
	close(attempt.done)
	s.questionAnswerMu.Unlock()
}

func buildQuestionAnswerBatch(records []QuestionAnswerRecord) (QuestionAnswerBatch, error) {
	if len(records) > 0 {
		expected := records[0].RequestProtocol
		for _, record := range records {
			if (expected == nil) != (record.RequestProtocol == nil) || (record.RequestProtocol != nil && (!validTestProtocol(*record.RequestProtocol) || *record.RequestProtocol != *expected)) {
				return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
			}
		}
	}

	snapshots := make(map[string]string)
	for _, record := range records {
		if len(records) > 0 && (record.BatchID != records[0].BatchID || record.TargetID != records[0].TargetID) {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
		}
		tuple, _ := questionAnswerSnapshot(record)
		if old, exists := snapshots[record.QuestionID]; exists && old != tuple {
			return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
		}
		snapshots[record.QuestionID] = tuple
	}

	reasoningEffort, err := aggregateQuestionAnswerReasoningEffort(records)
	if err != nil {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
	}
	repeatCount, err := aggregateQuestionAnswerRepeatCount(records)
	if err != nil {
		return QuestionAnswerBatch{}, requestError(ErrorQuestionAnswerStorage)
	}
	batch := QuestionAnswerBatch{
		Records:         cloneQuestionAnswerRecords(records),
		ReasoningEffort: reasoningEffort,
		RepeatCount:     repeatCount,
		SubmittedCount:  len(records),
		Stats:           aggregateQuestionAnswerStats(records),
	}
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
			batch.RunningCount++
			batch.CurrentModel, batch.CurrentQuestion = record.ModelName, record.QuestionName
		case QuestionAnswerSucceeded:
			batch.CompletedCount++
		case QuestionAnswerFailed:
			batch.CompletedCount++
		case QuestionAnswerCancelled:
			batch.CompletedCount++
		}
	}
	return batch, nil
}

func addQuestionAnswerRecordStats(stats *QuestionAnswerStats, record QuestionAnswerRecord) {
	stats.Requests.Submitted++
	switch record.Status {
	case QuestionAnswerPending, QuestionAnswerRunning:
		stats.Requests.InProgress++
	case QuestionAnswerSucceeded:
		stats.Requests.Succeeded++
		if record.AnswerJudgment != nil && *record.AnswerJudgment == QuestionAnswerCorrect {
			stats.Reviews.Correct++
		} else if record.AnswerJudgment != nil && *record.AnswerJudgment == QuestionAnswerIncorrect {
			stats.Reviews.Incorrect++
		} else {
			stats.Reviews.Unreviewed++
		}
	case QuestionAnswerFailed:
		stats.Requests.Failed++
	case QuestionAnswerCancelled:
		stats.Requests.Cancelled++
	}
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

func normalizeQuestionAnswerRepeatCount(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 1, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 1 || value > QuestionAnswerRepeatCountLimit {
		return 0, requestError(ErrorQuestionAnswerRepeatCount)
	}
	return value, nil
}

func questionAnswerSubmissionCount(modelCount, questionCount, repeatCount int) (int, error) {
	if repeatCount < 1 || repeatCount > QuestionAnswerRepeatCountLimit {
		return 0, requestError(ErrorQuestionAnswerRepeatCount)
	}
	if modelCount == 0 || questionCount == 0 {
		return 0, requestError(ErrorQuestionAnswerSelection)
	}
	if modelCount > QuestionAnswerBatchRecordLimit || questionCount > QuestionAnswerBatchRecordLimit {
		return QuestionAnswerBatchRecordLimit + 1, requestError(ErrorQuestionAnswerBatchLimit)
	}
	combinations := modelCount * questionCount
	if combinations > QuestionAnswerBatchRecordLimit/repeatCount {
		return combinations * repeatCount, requestError(ErrorQuestionAnswerBatchLimit)
	}
	return combinations * repeatCount, nil
}

func aggregateQuestionAnswerRepeatCount(records []QuestionAnswerRecord) (int, error) {
	if len(records) == 0 {
		return 1, nil
	}
	type combination struct {
		modelName  string
		questionID string
	}
	models := make(map[string]struct{})
	questions := make(map[string]struct{})
	counts := make(map[combination]int)
	hasIndexedRecords := false
	for _, record := range records {
		if record.RepeatIndex != nil {
			hasIndexedRecords = true
		}
		models[record.ModelName] = struct{}{}
		tuple, _ := questionAnswerSnapshot(record)
		questions[tuple] = struct{}{}
		counts[combination{modelName: record.ModelName, questionID: tuple}]++
	}
	// Legacy records may contain a sparse model/question matrix. Derive their
	// repeat count from the observed combinations without inventing missing rows.
	if hasIndexedRecords && len(counts) != len(models)*len(questions) {
		return 0, errors.New("question answer batch has missing model-question combinations")
	}
	repeatCount := 0
	for _, count := range counts {
		if count < 1 || count > QuestionAnswerRepeatCountLimit {
			return 0, errors.New("question answer batch has invalid repeat count")
		}
		if repeatCount == 0 {
			repeatCount = count
			continue
		}
		if count != repeatCount {
			return 0, errors.New("question answer batch has inconsistent repeat counts")
		}
	}
	sequences := make(map[combination]map[int]bool)
	sawLegacy, sawIndexed := false, false
	for _, record := range records {
		if record.RepeatIndex == nil {
			sawLegacy = true
			continue
		}
		sawIndexed = true
		index := *record.RepeatIndex
		if index < 1 || index > repeatCount {
			return 0, errors.New("question answer batch has invalid repeat index")
		}
		tuple, _ := questionAnswerSnapshot(record)
		key := combination{record.ModelName, tuple}
		if sequences[key] == nil {
			sequences[key] = make(map[int]bool)
		}
		if sequences[key][index] {
			return 0, errors.New("question answer batch has duplicate repeat index")
		}
		sequences[key][index] = true
	}
	if sawLegacy && sawIndexed {
		return 0, errors.New("question answer batch has mixed repeat index snapshots")
	}

	return repeatCount, nil
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

// judgeQuestionAnswer shares the literal, ASCII-only folding contract with highlighting.
func judgeQuestionAnswer(answer string, keywords []string) (QuestionAnswerJudgment, bool) {
	folded := asciiFoldTestQuestionKeyword(answer)
	judgeable := false
	for _, keyword := range keywords {
		if strings.TrimSpace(keyword) == "" {
			continue
		}
		judgeable = true
		if strings.Contains(folded, asciiFoldTestQuestionKeyword(keyword)) {
			return QuestionAnswerCorrect, true
		}
	}
	if judgeable {
		return QuestionAnswerIncorrect, true
	}
	return QuestionAnswerUnreviewed, false
}

func questionAnswerJudgmentSourcePointer(source QuestionAnswerJudgmentSource) *QuestionAnswerJudgmentSource {
	return &source
}

func questionAnswerSnapshot(record QuestionAnswerRecord) (string, QuestionAnswerQuestionStats) {
	normalized := make([]string, 0, len(record.QuestionKeywordSnapshot))
	seen := make(map[string]bool)
	for _, keyword := range record.QuestionKeywordSnapshot {
		if strings.TrimSpace(keyword) == "" {
			continue
		}
		folded := asciiFoldTestQuestionKeyword(keyword)
		if !seen[folded] {
			normalized = append(normalized, folded)
			seen[folded] = true
		}
	}
	sort.Strings(normalized)
	tuple := struct {
		QuestionID         string   `json:"questionId"`
		QuestionBody       string   `json:"questionBody"`
		NormalizedKeywords []string `json:"normalizedKeywords"`
	}{record.QuestionID, record.QuestionBody, normalized}
	data, _ := json.Marshal(tuple)
	sum := sha256.Sum256(data)
	return string(data), QuestionAnswerQuestionStats{QuestionSnapshotKey: hex.EncodeToString(sum[:]), QuestionID: record.QuestionID, DisplayQuestionName: record.QuestionName, QuestionBody: record.QuestionBody, NormalizedKeywords: normalized, ByModel: []QuestionAnswerModelStats{}}
}

type questionAnswerStatsAccumulator struct {
	stats          QuestionAnswerStats
	models         map[string]*QuestionAnswerModelStats
	questions      map[string]*QuestionAnswerQuestionStats
	questionModels map[string]map[string]*QuestionAnswerModelStats
	names          map[string]QuestionAnswerRecord
}

func newQuestionAnswerStatsAccumulator() *questionAnswerStatsAccumulator {
	return &questionAnswerStatsAccumulator{stats: QuestionAnswerStats{ByModel: []QuestionAnswerModelStats{}, ByQuestion: []QuestionAnswerQuestionStats{}}, models: make(map[string]*QuestionAnswerModelStats), questions: make(map[string]*QuestionAnswerQuestionStats), questionModels: make(map[string]map[string]*QuestionAnswerModelStats), names: make(map[string]QuestionAnswerRecord)}
}

func addQuestionAnswerCounts(requests *QuestionAnswerRequestStats, reviews *QuestionAnswerReviewStats, r QuestionAnswerRequestStats, v QuestionAnswerReviewStats) {
	requests.Submitted += r.Submitted
	requests.InProgress += r.InProgress
	requests.Succeeded += r.Succeeded
	requests.Failed += r.Failed
	requests.Cancelled += r.Cancelled
	reviews.Unreviewed += v.Unreviewed
	reviews.Correct += v.Correct
	reviews.Incorrect += v.Incorrect
}

func (a *questionAnswerStatsAccumulator) add(record QuestionAnswerRecord, requests QuestionAnswerRequestStats, reviews QuestionAnswerReviewStats) {
	addQuestionAnswerCounts(&a.stats.Requests, &a.stats.Reviews, requests, reviews)
	model := a.models[record.ModelName]
	if model == nil {
		model = &QuestionAnswerModelStats{ModelName: record.ModelName}
		a.models[record.ModelName] = model
	}
	addQuestionAnswerCounts(&model.Requests, &model.Reviews, requests, reviews)
	tuple, question := questionAnswerSnapshot(record)
	q := a.questions[tuple]
	if q == nil {
		q = &question
		a.questions[tuple] = q
		a.questionModels[tuple] = make(map[string]*QuestionAnswerModelStats)
	}
	old, exists := a.names[tuple]
	if !exists || record.CreatedAt.After(old.CreatedAt) || (record.CreatedAt.Equal(old.CreatedAt) && record.ID > old.ID) {
		q.DisplayQuestionName = record.QuestionName
		a.names[tuple] = record
	}
	addQuestionAnswerCounts(&q.Requests, &q.Reviews, requests, reviews)
	qm := a.questionModels[tuple][record.ModelName]
	if qm == nil {
		qm = &QuestionAnswerModelStats{ModelName: record.ModelName}
		a.questionModels[tuple][record.ModelName] = qm
	}
	addQuestionAnswerCounts(&qm.Requests, &qm.Reviews, requests, reviews)
}

func (a *questionAnswerStatsAccumulator) result() QuestionAnswerStats {
	for _, model := range a.models {
		a.stats.ByModel = append(a.stats.ByModel, *model)
	}
	sort.Slice(a.stats.ByModel, func(i, j int) bool { return a.stats.ByModel[i].ModelName < a.stats.ByModel[j].ModelName })
	for tuple, q := range a.questions {
		for _, model := range a.questionModels[tuple] {
			q.ByModel = append(q.ByModel, *model)
		}
		sort.Slice(q.ByModel, func(i, j int) bool { return q.ByModel[i].ModelName < q.ByModel[j].ModelName })
		a.stats.ByQuestion = append(a.stats.ByQuestion, *q)
	}
	sort.Slice(a.stats.ByQuestion, func(i, j int) bool {
		left, right := a.stats.ByQuestion[i], a.stats.ByQuestion[j]
		if left.QuestionID != right.QuestionID {
			return left.QuestionID < right.QuestionID
		}
		return left.QuestionSnapshotKey < right.QuestionSnapshotKey
	})
	return a.stats
}

func aggregateQuestionAnswerStats(records []QuestionAnswerRecord) QuestionAnswerStats {
	a := newQuestionAnswerStatsAccumulator()
	for _, record := range records {
		var stats QuestionAnswerStats
		addQuestionAnswerRecordStats(&stats, record)
		a.add(record, stats.Requests, stats.Reviews)
	}
	return a.result()
}

func buildQuestionAnswerBatchSummary(records []QuestionAnswerRecord) (QuestionAnswerBatchSummary, error) {
	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		return QuestionAnswerBatchSummary{}, err
	}
	summary := QuestionAnswerBatchSummary{BatchID: batch.BatchID, ReasoningEffort: batch.ReasoningEffort, RepeatCount: batch.RepeatCount, Active: batch.Active, Stats: batch.Stats, Models: []string{}, Questions: batch.Stats.ByQuestion}
	for _, model := range batch.Stats.ByModel {
		summary.Models = append(summary.Models, model.ModelName)
	}
	for i, record := range records {
		if i == 0 {
			summary.CreatedAt = record.CreatedAt
			summary.RequestProtocol = record.RequestProtocol
		}
		if record.CreatedAt.Before(summary.CreatedAt) {
			summary.CreatedAt = record.CreatedAt
		}
		if record.StartedAt != nil && (summary.StartedAt == nil || record.StartedAt.Before(*summary.StartedAt)) {
			value := *record.StartedAt
			summary.StartedAt = &value
		}
		if record.CompletedAt != nil && (summary.CompletedAt == nil || record.CompletedAt.After(*summary.CompletedAt)) {
			value := *record.CompletedAt
			summary.CompletedAt = &value
		}
	}
	if summary.Active {
		summary.CompletedAt = nil
	}
	return summary, nil
}
