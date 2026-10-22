package connection_health

import (
	"context"
	"errors"
)

func cloneQuestionAnswerCompletion(value QuestionAnswerCompletion) QuestionAnswerCompletion {
	value.Diagnostics = cloneQuestionAnswerDiagnostics(value.Diagnostics)
	if value.UpstreamStatus != nil {
		status := *value.UpstreamStatus
		value.UpstreamStatus = &status
	}
	if value.AnswerJudgment != nil {
		judgment := *value.AnswerJudgment
		value.AnswerJudgment = &judgment
	}
	if value.JudgmentSource != nil {
		source := *value.JudgmentSource
		value.JudgmentSource = &source
	}
	return value
}

// questionAnswerMu is held: ownership and selection come from the current run,
// never from an API flag or the database status alone.
func (s *Service) selectedQuestionAnswerRecordLocked(run *activeQuestionAnswerBatch, record QuestionAnswerRecord) bool {
	if run == nil || s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)] != run || record.ID == "" || record.TargetID != run.targetID || record.BatchID != run.batchID {
		return false
	}
	for i := 0; i < run.next && i < len(run.records); i++ {
		item := run.records[i]
		if item.ID == record.ID && item.TargetID == run.targetID && item.BatchID == run.batchID {
			return true
		}
	}
	return false
}

// This is the only decision point. Stop reasons and the first immutable result
// are checked and stored in one service-lock section. Later stops reuse it.
func (s *Service) freezeQuestionAnswerCompletionLocked(run *activeQuestionAnswerBatch, record QuestionAnswerRecord, candidate QuestionAnswerCompletion) (QuestionAnswerCompletion, bool) {
	if !s.selectedQuestionAnswerRecordLocked(run, record) {
		return QuestionAnswerCompletion{}, false
	}
	if saved, ok := run.pendingCompletions[record.ID]; ok {
		return cloneQuestionAnswerCompletion(saved), true
	}
	progress := run.progress[record.ID]
	if candidate.Diagnostics == nil && progress != nil {
		candidate.Diagnostics = progress.snapshot(record.ID).Diagnostics
	}
	if run.stopReason != "" {
		status, errorType := questionAnswerRunFinalState(run, QuestionAnswerErrorStorage)
		candidate = QuestionAnswerCompletion{Status: status, ErrorType: errorType, Diagnostics: candidate.Diagnostics}
	}
	if candidate.Diagnostics != nil && candidate.Diagnostics.RequestStartedAt == nil {
		candidate.Diagnostics.FailureStep = "starting"
	}
	candidate = cloneQuestionAnswerCompletion(candidate)
	if run.pendingCompletions == nil {
		run.pendingCompletions = make(map[string]QuestionAnswerCompletion)
	}
	run.pendingCompletions[record.ID] = candidate
	if progress != nil {
		progress.mu.Lock()
		progress.step = "saving"
		status := candidate.Status
		progress.pendingResultStatus = &status
		if candidate.Diagnostics != nil {
			progress.diagnostics = *cloneQuestionAnswerDiagnostics(candidate.Diagnostics)
		}
		progress.mu.Unlock()
	}
	return cloneQuestionAnswerCompletion(candidate), true
}

func (s *Service) allowPendingQuestionAnswerCompletionLocked(run *activeQuestionAnswerBatch, record QuestionAnswerRecord, completion QuestionAnswerCompletion) bool {
	return s.selectedQuestionAnswerRecordLocked(run, record) && questionAnswerPendingCompletionEvidence(completion)
}

func (s *Service) clearQuestionAnswerCompletionLocked(run *activeQuestionAnswerBatch, recordID string) {
	if s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)] == run {
		delete(run.pendingCompletions, recordID)
		delete(run.progress, recordID)
	}
}

func (s *Service) recordQuestionAnswerStorageFailure(run *activeQuestionAnswerBatch, record QuestionAnswerRecord, cause error, starting bool) {
	if cause == nil {
		cause = errors.New("保存这题的最终结果时没有取得写入确认，需重新确认当前记录的结束状态")
	}
	text := questionAnswerSafeCause(cause, run.cred.Key)
	s.questionAnswerMu.Lock()
	progress := run.progress[record.ID]
	s.questionAnswerMu.Unlock()
	if starting {
		progress.failure("starting", cause, run.cred.Key)
	}
	s.questionAnswerMu.Lock()
	wasStopped := run.stopReason != "" || run.ctx.Err() != nil
	if run.stopReason == "" {
		run.stopReason = QuestionAnswerErrorStorage
	}
	if starting {
		_, _ = s.freezeQuestionAnswerCompletionLocked(run, record, QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorStorage})
	}
	if errors.Is(cause, context.Canceled) && !starting {
		if wasStopped {
			text = "保存请求被停止操作中断，将确认保存结果"
		} else {
			text = "保存请求被取消，取消来源尚未确认，将核查保存结果"
		}
	}
	if progress != nil {
		progress.mu.Lock()
		progress.storageError, progress.step = text, "confirming"
		progress.mu.Unlock()
	}
	s.questionAnswerMu.Unlock()
	run.cancel()
	s.wakeQuestionAnswerDispatcher()
}

func (s *Service) confirmQuestionAnswerRecordTerminal(ctx context.Context, run *activeQuestionAnswerBatch, record QuestionAnswerRecord) (bool, error) {
	records, err := s.questionAnswers.ListQuestionAnswerBatch(ctx, run.userID, run.targetID, run.batchID)
	if err != nil {
		return false, err
	}
	for _, current := range records {
		if current.ID == record.ID && current.TargetID == run.targetID && current.BatchID == run.batchID {
			if current.Status == QuestionAnswerSucceeded || current.Status == QuestionAnswerFailed || current.Status == QuestionAnswerCancelled {
				return true, nil
			}
			return false, errors.New("记录这题开始时未取得写入确认，重新读取后仍未确认结束；这条题目没有调用答题接口")
		}
	}
	return false, errors.New("记录这题开始时未取得写入确认，重新读取时没有找到同一归属的题目记录；这条题目没有调用答题接口")
}

func (s *Service) confirmFrozenQuestionAnswerCompletion(ctx context.Context, run *activeQuestionAnswerBatch, record QuestionAnswerRecord, completion QuestionAnswerCompletion) (bool, error) {
	s.questionAnswerMu.Lock()
	allowPending := s.allowPendingQuestionAnswerCompletionLocked(run, record, completion)
	s.questionAnswerMu.Unlock()
	confirmed, err := s.questionAnswers.ConfirmOrSaveQuestionAnswerCompletion(ctx, run.userID, run.targetID, run.batchID, record.ID, completion, allowPending)
	if err == nil && confirmed {
		s.questionAnswerMu.Lock()
		s.clearQuestionAnswerCompletionLocked(run, record.ID)
		s.questionAnswerMu.Unlock()
	}
	return confirmed, err
}
