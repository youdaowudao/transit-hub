package connection_health

import (
	"context"
	"reflect"
	"testing"
)

func TestQuestionAnswerRoundRobinSelectorPreservesBatchRecordOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{
		questionAnswerCtx:      ctx,
		questionAnswerRuns:     make(map[string]*activeQuestionAnswerBatch),
		questionAnswerOrder:    []string{"batch-a", "batch-b"},
		questionAnswerInFlight: 0,
	}
	runA := &activeQuestionAnswerBatch{
		ctx: ctx, records: []QuestionAnswerRecord{{ID: "a-1"}, {ID: "a-2"}, {ID: "a-3"}},
	}
	runB := &activeQuestionAnswerBatch{
		ctx: ctx, records: []QuestionAnswerRecord{{ID: "b-1"}, {ID: "b-2"}, {ID: "b-3"}},
	}
	service.questionAnswerRuns["batch-a"] = runA
	service.questionAnswerRuns["batch-b"] = runB

	got := make([]string, 0, 5)
	for i := 0; i < questionAnswerConcurrency; i++ {
		service.questionAnswerMu.Lock()
		key, run, record, ok := service.nextQuestionAnswerDispatchLocked()
		service.questionAnswerMu.Unlock()
		if !ok || run == nil {
			t.Fatalf("dispatch %d unavailable: key=%q run=%v", i+1, key, run)
		}
		got = append(got, record.ID)
	}
	want := []string{"a-1", "b-1", "a-2", "b-2", "a-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch order=%v want=%v", got, want)
	}
	if service.questionAnswerInFlight != questionAnswerConcurrency || runA.inFlight != 3 || runB.inFlight != 2 || runA.next != 3 || runB.next != 2 {
		t.Fatalf("selector counters global=%d A(inFlight=%d next=%d) B(inFlight=%d next=%d)", service.questionAnswerInFlight, runA.inFlight, runA.next, runB.inFlight, runB.next)
	}
	service.questionAnswerMu.Lock()
	_, _, _, ok := service.nextQuestionAnswerDispatchLocked()
	service.questionAnswerMu.Unlock()
	if ok {
		t.Fatal("selector exceeded the global five-slot cap")
	}
}
