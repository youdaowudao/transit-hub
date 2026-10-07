package connection_health

import (
	"context"
	"errors"
	"testing"
)

func TestC5APIOnlyConnectionHealthNeverStartsBackgroundRuntime(t *testing.T) {
	s := NewServiceWithBackgroundTasks(NewRepository(nil), nil, nil, nil, false)
	if !s.backgroundTasksDisabled || !s.refreshRunClosed || s.questionAnswerCtx != nil || s.questionAnswerDispatcherDone != nil {
		t.Fatal("API-only constructor started a dispatcher or admitted automatic refresh")
	}
	s.initializeQuestionAnswerRuntime()
	s.StartScheduler(context.Background())
	_, err := s.StartQuestionAnswerBatch(context.Background(), "user", "target", QuestionAnswerStartInput{})
	if !errors.Is(err, requestError(ErrorQuestionAnswerServiceStopped)) {
		t.Fatal("API-only allowed an on-demand background dispatcher")
	}
	if _, _, admitted := s.registerMultiplierRefreshJob(context.Background()); admitted {
		t.Fatal("API-only admitted background metadata refresh")
	}
	s.triggerPrioritySync("user", "workspace", "generation")
	s.triggerHealthPrioritySyncAfterCommit("user", "workspace")
	if len(s.priorityTriggerRunning) != 0 || len(s.priorityHealthRunning) != 0 || s.questionAnswerCtx != nil {
		t.Fatal("API-only request reactivated background workers")
	}
	if err := s.Shutdown(context.Background()); err != nil || s.questionAnswerCtx != nil {
		t.Fatal("API-only shutdown initialized background work")
	}
}

func TestC5APIOnlyNormalConnectionHealthStillInitializesDispatcher(t *testing.T) {
	s := NewService(NewRepository(nil), nil, nil, nil)
	if s.backgroundTasksDisabled || s.refreshRunClosed || s.questionAnswerCtx == nil || s.questionAnswerDispatcherDone == nil {
		t.Fatal("normal construction lost its background runtime")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.questionAnswerDispatcherDone:
	default:
		t.Fatal("normal dispatcher was not drained")
	}
}
