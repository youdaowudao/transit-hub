package connection_health

import (
	"context"
	"errors"
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
		s.questionAnswerMu.Lock()
		key, run, record, ok := s.nextQuestionAnswerDispatchLocked()
		s.questionAnswerMu.Unlock()
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

// nextQuestionAnswerDispatchLocked requires questionAnswerMu to be held by the caller.
func (s *Service) nextQuestionAnswerDispatchLocked() (string, *activeQuestionAnswerBatch, QuestionAnswerRecord, bool) {
	if s.questionAnswerClosed || s.questionAnswerInFlight >= questionAnswerConcurrency || len(s.questionAnswerOrder) == 0 {
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

	itemCtx, cancel := context.WithTimeout(run.ctx, s.questionAnswerTTL)
	answer, errorType := s.questionAnswerHTTP.Ask(itemCtx, run.cred, record.ModelName, record.QuestionBody, questionAnswerReasoningEffortOrDefault(record.ReasoningEffort))
	itemErr := itemCtx.Err()
	cancel()
	if s.questionAnswerRunStopReason(run) != "" {
		terminalDurable = s.completeStoppedQuestionAnswerRecord(run, record)
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
	completed, err := s.questionAnswers.CompleteQuestionAnswer(run.ctx, run.userID, run.batchID, record.ID, status, answer, errorType)
	if err == nil && completed {
		terminalDurable = true
		return
	}
	s.stopQuestionAnswerRunForStorage(run)
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
	_, _ = s.questionAnswers.StopPendingQuestionAnswerBatch(
		stopCtx, run.userID, run.targetID, run.batchID, QuestionAnswerFailed, QuestionAnswerErrorStorage,
	)
	cancel()
}

func (s *Service) completeStoppedQuestionAnswerRecord(run *activeQuestionAnswerBatch, record QuestionAnswerRecord) bool {
	s.questionAnswerMu.Lock()
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	s.questionAnswerMu.Unlock()
	completeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	completed, err := s.questionAnswers.CompleteQuestionAnswer(completeCtx, run.userID, run.batchID, record.ID, status, "", errorType)
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

func (s *Service) finalizeQuestionAnswerRun(key string, run *activeQuestionAnswerBatch) error {
	s.questionAnswerMu.Lock()
	s.ensureQuestionAnswerFinalizationLocked(run)
	generation := run.finalizeGeneration
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	s.questionAnswerMu.Unlock()

	finalizeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	_, err := s.questionAnswers.FinalizeQuestionAnswerBatch(finalizeCtx, run.userID, run.targetID, run.batchID, status, errorType)
	cancel()

	s.questionAnswerMu.Lock()
	settled := run.finalizeSettled
	if err != nil {
		run.finalErr = err
		run.finalizeResults[generation] = err
		run.finalizing = false
		close(settled)
		s.questionAnswerMu.Unlock()
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
