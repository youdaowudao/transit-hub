package connection_health

import (
	"context"
	"errors"
	"log"
	"time"
)

func (s *Service) wakeQuestionAnswerDispatcher() {
	select {
	case s.questionAnswerWake <- struct{}{}:
	default:
	}
}

func (s *Service) questionAnswerStorageContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	s.questionAnswerMu.Lock()
	timeout := s.questionAnswerStorageTimeout
	s.questionAnswerMu.Unlock()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

func (s *Service) runQuestionAnswerDispatcher() {
	defer close(s.questionAnswerDispatcherDone)
	for {
		key, run, record, ok := s.nextQuestionAnswerDispatch()
		if ok {
			s.questionAnswerWG.Add(1)
			go func() {
				defer s.questionAnswerWG.Done()
				s.executeQuestionAnswerDispatch(key, run, record)
			}()
			continue
		}
		select {
		case <-s.questionAnswerCtx.Done():
			return
		case <-s.questionAnswerWake:
		}
	}
}

func (s *Service) nextQuestionAnswerDispatch() (key string, run *activeQuestionAnswerBatch, record QuestionAnswerRecord, ok bool) {
	defer func() {
		if recover() != nil {
			key, run, record, ok = "", nil, QuestionAnswerRecord{}, false
			log.Printf("[connection-health] question selection panic recovered")
		}
	}()
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	return s.nextQuestionAnswerDispatchLocked()
}

// nextQuestionAnswerDispatchLocked requires questionAnswerMu to be held by the caller.
func (s *Service) nextQuestionAnswerDispatchLocked() (string, *activeQuestionAnswerBatch, QuestionAnswerRecord, bool) {
	if s.questionAnswerClosed || s.questionAnswerInFlight >= s.questionAnswerConcurrency() || len(s.questionAnswerOrder) == 0 {
		return "", nil, QuestionAnswerRecord{}, false
	}
	start := 0
	if s.questionAnswerLastKey != "" {
		for i, key := range s.questionAnswerOrder {
			if key == s.questionAnswerLastKey {
				start = (i + 1) % len(s.questionAnswerOrder)
				break
			}
		}
	}
	for offset := 0; offset < len(s.questionAnswerOrder); offset++ {
		key := s.questionAnswerOrder[(start+offset)%len(s.questionAnswerOrder)]
		run := s.questionAnswerRuns[key]
		if run == nil || run.stopReason != "" || run.ctx.Err() != nil || run.next >= len(run.records) {
			continue
		}
		record := run.records[run.next]
		run.next++
		run.inFlight++
		s.questionAnswerInFlight++
		s.questionAnswerLastKey = key
		return key, run, record, true
	}
	return "", nil, QuestionAnswerRecord{}, false
}

func (s *Service) executeQuestionAnswerDispatch(key string, run *activeQuestionAnswerBatch, record QuestionAnswerRecord) {
	terminalDurable := false
	defer func() { s.finishQuestionAnswerDispatch(key, run, terminalDurable) }()
	defer func() {
		if recover() != nil {
			s.stopQuestionAnswerRunForInternalError(run)
			terminalDurable = s.completeStoppedQuestionAnswerRecord(run, record)
			log.Printf("[connection-health] question dispatch panic recovered")
		}
	}()

	running, err := s.questionAnswers.MarkQuestionAnswerRunning(run.ctx, run.userID, run.batchID, record.ID)
	if err != nil {
		if s.questionAnswerRunStopReason(run) == "" {
			s.stopQuestionAnswerRunForStorage(run)
		}
		return
	}
	if !running {
		terminalDurable = true
		return
	}
	if s.questionAnswerRunStopReason(run) != "" {
		terminalDurable = s.completeStoppedQuestionAnswerRecord(run, record)
		return
	}

	protocol := TestProtocolChatCompletions
	if record.RequestProtocol != nil {
		protocol = *record.RequestProtocol
	}
	ttl := s.questionAnswerTTL
	if protocol == TestProtocolMessages {
		ttl = s.questionAnswerMessagesTTL
	}
	itemCtx, cancel := context.WithTimeout(run.ctx, ttl)
	defer cancel()
	result := s.questionAnswerHTTP.AskDetailed(itemCtx, run.cred, record.ModelName, record.QuestionBody, questionAnswerReasoningEffortOrDefault(record.ReasoningEffort), protocol)
	itemErr := itemCtx.Err()
	cancel()
	if s.questionAnswerRunStopReason(run) != "" {
		terminalDurable = s.completeStoppedQuestionAnswerRecord(run, record)
		return
	}

	completion := questionAnswerCompletionFromAsk(result, errors.Is(itemErr, context.DeadlineExceeded), record, run.cred.Key)
	completed, err := s.questionAnswers.CompleteQuestionAnswer(run.ctx, run.userID, run.batchID, record.ID, completion)
	if err == nil && completed {
		terminalDurable = true
		return
	}
	s.stopQuestionAnswerRunForStorage(run)
}

func questionAnswerCompletionFromAsk(result QuestionAnswerAskResult, deadlineExceeded bool, record QuestionAnswerRecord, keys ...string) QuestionAnswerCompletion {
	completion := QuestionAnswerCompletion{Status: QuestionAnswerSucceeded, AnswerBody: result.Answer, ErrorType: result.ErrorType}
	if deadlineExceeded || result.ErrorType != "" {
		completion.Status = QuestionAnswerFailed
		completion.AnswerBody = ""
		completion.UpstreamExcerpt = result.UpstreamExcerpt
		if result.UpstreamStatus >= 100 && result.UpstreamStatus <= 999 {
			status := result.UpstreamStatus
			completion.UpstreamStatus = &status
		}
		if deadlineExceeded {
			completion.ErrorType = QuestionAnswerErrorTimeout
			if record.RequestProtocol != nil && *record.RequestProtocol == TestProtocolResponses && completion.UpstreamExcerpt != "" {
				key := ""
				if len(keys) > 0 {
					key = keys[0]
				}
				completion.UpstreamExcerpt = questionAnswerUpstreamExcerpt([]byte(responsesQuestionAnswerDeadlinePrefix+completion.UpstreamExcerpt), key)
			}
		}
	} else {
		judgment, judgeable := judgeQuestionAnswer(result.Answer, record.QuestionKeywordSnapshot)
		completion.AnswerJudgment = &judgment
		if judgeable {
			completion.JudgmentSource = questionAnswerJudgmentSourcePointer(QuestionAnswerJudgmentAutomatic)
		}
	}
	return completion
}

func (s *Service) questionAnswerRunStopReason(run *activeQuestionAnswerBatch) string {
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	return run.stopReason
}

func (s *Service) stopQuestionAnswerRunForStorage(run *activeQuestionAnswerBatch) {
	s.questionAnswerMu.Lock()
	firstStop := false
	if run.stopReason == "" {
		run.stopReason = QuestionAnswerErrorStorage
		firstStop = true
	}
	s.questionAnswerMu.Unlock()
	if !firstStop {
		return
	}
	run.cancel()
	s.wakeQuestionAnswerDispatcher()
	stopCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	_, _ = s.questionAnswers.StopPendingQuestionAnswerBatch(
		stopCtx, run.userID, run.targetID, run.batchID, QuestionAnswerFailed, QuestionAnswerErrorStorage,
	)
	cancel()
}

func (s *Service) stopQuestionAnswerRunForInternalError(run *activeQuestionAnswerBatch) {
	s.questionAnswerMu.Lock()
	firstStop := run.stopReason == ""
	if firstStop {
		run.stopReason = QuestionAnswerErrorInternal
	}
	s.questionAnswerMu.Unlock()
	if !firstStop {
		return
	}
	run.cancel()
	s.wakeQuestionAnswerDispatcher()
	stopCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	defer func() {
		if recover() != nil {
			// Finalization still owns the batch and retries the durable terminal.
			log.Printf("[connection-health] internal-error batch stop could not be saved")
		}
	}()
	_, _ = s.questionAnswers.StopPendingQuestionAnswerBatch(stopCtx, run.userID, run.targetID, run.batchID, QuestionAnswerFailed, QuestionAnswerErrorInternal)
}

func (s *Service) completeStoppedQuestionAnswerRecord(run *activeQuestionAnswerBatch, record QuestionAnswerRecord) (durable bool) {
	defer func() {
		if recover() != nil {
			log.Printf("[connection-health] stopped question completion panic recovered")
		}
	}()
	s.questionAnswerMu.Lock()
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	s.questionAnswerMu.Unlock()
	completeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	completed, err := s.questionAnswers.CompleteQuestionAnswer(completeCtx, run.userID, run.batchID, record.ID, QuestionAnswerCompletion{Status: status, ErrorType: errorType})
	cancel()
	return err == nil && completed
}

func (s *Service) finishQuestionAnswerDispatch(key string, run *activeQuestionAnswerBatch, terminalDurable bool) {
	s.questionAnswerMu.Lock()
	if run.inFlight > 0 {
		run.inFlight--
	}
	released := false
	if terminalDurable {
		if s.questionAnswerInFlight > 0 {
			s.questionAnswerInFlight--
		}
		released = true
	} else {
		run.heldSlots++
	}
	naturalComplete := false
	if current := s.questionAnswerRuns[key]; current == run && run.stopReason == "" && run.next >= len(run.records) && run.inFlight == 0 && run.heldSlots == 0 {
		s.removeQuestionAnswerRunLocked(key, run)
		close(run.done)
		naturalComplete = true
	}
	shouldFinalize := false
	if !naturalComplete {
		shouldFinalize = s.beginQuestionAnswerRunFinalizationLocked(key, run)
	}
	s.questionAnswerMu.Unlock()
	if naturalComplete {
		run.cancel()
	}
	if shouldFinalize {
		go func() { _ = s.finalizeQuestionAnswerRun(key, run) }()
	}
	if released {
		s.wakeQuestionAnswerDispatcher()
	}
}

// beginQuestionAnswerRunFinalizationLocked requires questionAnswerMu to be held by the caller.
func (s *Service) beginQuestionAnswerRunFinalizationLocked(key string, run *activeQuestionAnswerBatch) bool {
	s.ensureQuestionAnswerFinalizationLocked(run)
	if current := s.questionAnswerRuns[key]; current != run || run.finalizing || run.inFlight != 0 {
		return false
	}
	if run.stopReason == "" && run.next < len(run.records) {
		return false
	}
	select {
	case <-run.finalizeSettled:
		return false
	default:
	}
	run.finalizing = true
	return true
}

func (s *Service) ensureQuestionAnswerFinalizationLocked(run *activeQuestionAnswerBatch) {
	if run.finalizeGeneration == 0 {
		run.finalizeGeneration = 1
	}
	if run.finalizeSettled == nil {
		run.finalizeSettled = make(chan struct{})
	}
	if run.finalizeResults == nil {
		run.finalizeResults = make(map[uint64]error)
	}
}

func (s *Service) resetQuestionAnswerFinalizationLocked(run *activeQuestionAnswerBatch) {
	s.ensureQuestionAnswerFinalizationLocked(run)
	run.finalizeGeneration++
	run.finalizeSettled = make(chan struct{})
	run.finalErr = nil
}

func questionAnswerChannelClosed(channel <-chan struct{}) bool {
	if channel == nil {
		return false
	}
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func (s *Service) finalizeQuestionAnswerRun(key string, run *activeQuestionAnswerBatch) (finalErr error) {
	locked := false
	var generation uint64
	var settled chan struct{}
	defer func() {
		if recover() == nil {
			return
		}
		if !locked {
			s.questionAnswerMu.Lock()
		}
		s.ensureQuestionAnswerFinalizationLocked(run)
		if generation == 0 {
			generation = run.finalizeGeneration
			settled = run.finalizeSettled
		}
		run.finalErr = errQuestionAnswerInternal
		run.finalizeResults[generation] = errQuestionAnswerInternal
		run.finalizing = false
		if !questionAnswerChannelClosed(settled) {
			close(settled)
		}
		s.questionAnswerMu.Unlock()
		finalErr = errQuestionAnswerInternal
		log.Printf("[connection-health] question finalization panic recovered")
	}()
	s.questionAnswerMu.Lock()
	locked = true
	s.ensureQuestionAnswerFinalizationLocked(run)
	generation, settled = run.finalizeGeneration, run.finalizeSettled
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	s.questionAnswerMu.Unlock()
	locked = false

	finalizeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	_, err := s.questionAnswers.FinalizeQuestionAnswerBatch(finalizeCtx, run.userID, run.targetID, run.batchID, status, errorType)
	cancel()

	s.questionAnswerMu.Lock()
	locked = true
	if err != nil {
		run.finalErr = err
		run.finalizeResults[generation] = err
		run.finalizing = false
		close(settled)
		s.questionAnswerMu.Unlock()
		locked = false
		return err
	}
	run.finalErr = nil
	run.finalizeResults[generation] = nil
	run.finalizing = false
	s.questionAnswerInFlight -= run.heldSlots
	run.heldSlots = 0
	s.removeQuestionAnswerRunLocked(key, run)
	close(run.done)
	close(settled)
	s.questionAnswerMu.Unlock()
	locked = false
	run.cancel()
	s.wakeQuestionAnswerDispatcher()
	return nil
}

// questionAnswerRunFinalState requires questionAnswerMu when run is non-nil.
func questionAnswerRunFinalState(run *activeQuestionAnswerBatch, fallback string) (QuestionAnswerStatus, string) {
	reason := fallback
	if run != nil && run.stopReason != "" {
		reason = run.stopReason
	}
	switch reason {
	case string(QuestionAnswerCancelled):
		return QuestionAnswerCancelled, ""
	case QuestionAnswerErrorServiceShutdown:
		return QuestionAnswerFailed, QuestionAnswerErrorServiceShutdown
	case QuestionAnswerErrorInternal:
		return QuestionAnswerFailed, QuestionAnswerErrorInternal
	default:
		return QuestionAnswerFailed, QuestionAnswerErrorStorage
	}
}

// removeQuestionAnswerRunLocked requires questionAnswerMu to be held by the caller.
func (s *Service) removeQuestionAnswerRunLocked(key string, run *activeQuestionAnswerBatch) {
	if current := s.questionAnswerRuns[key]; current != run {
		return
	}
	delete(s.questionAnswerRuns, key)
	removedIndex := -1
	for i, orderedKey := range s.questionAnswerOrder {
		if orderedKey == key {
			removedIndex = i
			break
		}
	}
	if removedIndex < 0 {
		if len(s.questionAnswerOrder) == 0 {
			s.questionAnswerLastKey = ""
		}
		return
	}
	if s.questionAnswerLastKey == key {
		if len(s.questionAnswerOrder) == 1 {
			s.questionAnswerLastKey = ""
		} else {
			previous := removedIndex - 1
			if previous < 0 {
				previous = len(s.questionAnswerOrder) - 1
			}
			s.questionAnswerLastKey = s.questionAnswerOrder[previous]
		}
	}
	s.questionAnswerOrder = append(s.questionAnswerOrder[:removedIndex], s.questionAnswerOrder[removedIndex+1:]...)
	if len(s.questionAnswerOrder) == 0 {
		s.questionAnswerLastKey = ""
	}
}

// SetQuestionAnswerConcurrency applies successful singleton commits in version
// order. Held slots remain part of inFlight when capacity is reduced.
func (s *Service) SetQuestionAnswerConcurrency(limit int, version int64) {
	if s.backgroundTasksDisabled || limit < 1 || limit > 50 || version < 0 {
		return
	}
	s.questionAnswerMu.Lock()
	if s.questionAnswerConcurrencyLoaded && version <= s.questionAnswerConcurrencyVersion {
		s.questionAnswerMu.Unlock()
		return
	}
	s.questionAnswerConcurrencyLimit, s.questionAnswerConcurrencyVersion, s.questionAnswerConcurrencyLoaded = limit, version, true
	s.questionAnswerMu.Unlock()
	s.wakeQuestionAnswerDispatcher()
}

// questionAnswerMu is held by the dispatcher caller.
func (s *Service) questionAnswerConcurrency() int {
	if s.questionAnswerConcurrencyLimit < 1 {
		return questionAnswerConcurrency
	}
	return s.questionAnswerConcurrencyLimit
}
