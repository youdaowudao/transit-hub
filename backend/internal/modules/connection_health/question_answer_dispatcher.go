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
	key, ok := nextQuestionAnswerChoice(s.questionAnswerOrder, s.questionAnswerLastKey, func(key string) bool {
		run := s.questionAnswerRuns[key]
		return run != nil && run.stopReason == "" && run.ctx.Err() == nil && run.next < len(run.records)
	})
	if ok {
		run := s.questionAnswerRuns[key]
		record := run.records[run.next]
		if run.progress == nil {
			run.progress = make(map[string]*QuestionAnswerProgress)
		}
		run.progress[record.ID] = newQuestionAnswerProgress(time.Now())
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
	defer func() {
		if terminalDurable {
			s.questionAnswerMu.Lock()
			delete(run.progress, record.ID)
			s.questionAnswerMu.Unlock()
		}
		s.finishQuestionAnswerDispatch(key, run, terminalDurable)
	}()
	s.questionAnswerMu.Lock()
	progress := run.progress[record.ID]
	s.questionAnswerMu.Unlock()
	defer func() {
		if recover() != nil {
			s.stopQuestionAnswerRunForInternalError(run)
			terminalDurable = s.completeStoppedQuestionAnswerRecord(run, record)
			log.Printf("[connection-health] question dispatch panic recovered")
		}
	}()

	running, err := func() (bool, error) {
		markCtx, cancel := s.questionAnswerStorageContext(run.ctx)
		defer cancel()
		return s.questionAnswers.MarkQuestionAnswerRunning(markCtx, run.userID, run.batchID, record.ID)
	}()
	if err != nil {
		s.recordQuestionAnswerStorageFailure(run, record, err, true)
		return
	}
	if !running {
		terminalDurable, err = func() (bool, error) {
			readCtx, cancel := s.questionAnswerStorageContext(run.ctx)
			defer cancel()
			return s.confirmQuestionAnswerRecordTerminal(readCtx, run, record)
		}()
		if !terminalDurable {
			s.recordQuestionAnswerStorageFailure(run, record, err, true)
		}
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
	progress.startRequest(ttl, time.Now())
	defer cancel()
	result := s.questionAnswerHTTP.AskDetailedWithProgress(itemCtx, run.cred, record.ModelName, record.QuestionBody, questionAnswerReasoningEffortOrDefault(record.ReasoningEffort), progress, protocol)
	itemErr := itemCtx.Err()
	cancel()
	candidate := questionAnswerCompletionFromAsk(result, errors.Is(itemErr, context.DeadlineExceeded), record, run.cred.Key)
	s.questionAnswerMu.Lock()
	completion, sealed := s.freezeQuestionAnswerCompletionLocked(run, record, candidate)
	s.questionAnswerMu.Unlock()
	if !sealed {
		return
	}
	completed, err := func() (bool, error) {
		completeCtx, cancel := s.questionAnswerStorageContext(run.ctx)
		defer cancel()
		return s.questionAnswers.CompleteQuestionAnswer(completeCtx, run.userID, run.batchID, record.ID, completion)
	}()
	if err == nil && completed {
		s.questionAnswerMu.Lock()
		s.clearQuestionAnswerCompletionLocked(run, record.ID)
		s.questionAnswerMu.Unlock()
		terminalDurable = true
		return
	}
	s.recordQuestionAnswerStorageFailure(run, record, err, false)

}

func questionAnswerCompletionFromAsk(result QuestionAnswerAskResult, deadlineExceeded bool, record QuestionAnswerRecord, keys ...string) QuestionAnswerCompletion {
	completion := QuestionAnswerCompletion{Status: QuestionAnswerSucceeded, AnswerBody: result.Answer, ErrorType: result.ErrorType, Diagnostics: cloneQuestionAnswerDiagnostics(result.Diagnostics)}
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

}

func (s *Service) completeStoppedQuestionAnswerRecord(run *activeQuestionAnswerBatch, record QuestionAnswerRecord) (durable bool) {
	defer func() {
		if recover() != nil {
			log.Printf("[connection-health] stopped question completion panic recovered")
		}
	}()
	s.questionAnswerMu.Lock()
	status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
	completion, sealed := s.freezeQuestionAnswerCompletionLocked(run, record, QuestionAnswerCompletion{Status: status, ErrorType: errorType})
	s.questionAnswerMu.Unlock()
	if !sealed {
		return false
	}
	completeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	completed, err := s.questionAnswers.CompleteQuestionAnswer(completeCtx, run.userID, run.batchID, record.ID, completion)
	if err != nil || !completed {
		s.recordQuestionAnswerStorageFailure(run, record, err, false)
	} else {
		s.questionAnswerMu.Lock()
		s.clearQuestionAnswerCompletionLocked(run, record.ID)
		s.questionAnswerMu.Unlock()
	}
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
	type pendingSave struct {
		record     QuestionAnswerRecord
		completion QuestionAnswerCompletion
	}
	pending := make([]pendingSave, 0, len(run.pendingCompletions))
	for _, record := range run.records {
		if completion, ok := run.pendingCompletions[record.ID]; ok {
			pending = append(pending, pendingSave{record, cloneQuestionAnswerCompletion(completion)})
		}
	}
	completeSelection := len(pending) == len(run.pendingCompletions)
	s.questionAnswerMu.Unlock()
	locked = false

	finalizeCtx, cancel := s.questionAnswerStorageContext(context.Background())
	defer cancel()
	var err error
	if !completeSelection {
		err = errors.New("待保存结果没有对应的已选中记录，尚未确认结束；请按批次和记录编号核查后台日志")
	}
	for _, item := range pending {
		if err != nil {
			break
		}
		var confirmed bool
		confirmed, err = s.confirmFrozenQuestionAnswerCompletion(finalizeCtx, run, item.record, item.completion)
		if err == nil && !confirmed {
			err = errors.New("这条题目的保存结果尚未确认，批次暂时保留执行位置；请重试保存并结束")
		}
		if err != nil {
			text := questionAnswerSafeCause(err, run.cred.Key)
			s.questionAnswerMu.Lock()
			if progress := run.progress[item.record.ID]; progress != nil {
				progress.mu.Lock()
				progress.step, progress.storageError = "confirming", text
				progress.mu.Unlock()
			}
			s.questionAnswerMu.Unlock()
		}
	}
	if err == nil {
		_, err = s.questionAnswers.FinalizeQuestionAnswerBatch(finalizeCtx, run.userID, run.targetID, run.batchID, status, errorType)
	}
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
