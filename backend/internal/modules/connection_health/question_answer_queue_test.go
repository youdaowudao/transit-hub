package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func queueFixtureRun(s *Service, user, target, batch string, count int) *activeQuestionAnswerBatch {
	ctx, cancel := context.WithCancel(s.questionAnswerCtx)
	run := &activeQuestionAnswerBatch{userID: user, targetID: target, batchID: batch, ctx: ctx, cancel: cancel, progress: map[string]*QuestionAnswerProgress{}}
	for i := 0; i < count; i++ {
		repeat := i%2 + 1
		run.records = append(run.records, QuestionAnswerRecord{ID: fmt.Sprintf("%s-%03d", batch, i), TargetID: target, BatchID: batch, ModelName: fmt.Sprintf("model-%d", i/4), QuestionName: fmt.Sprintf("question-%d", i/2%2), RepeatIndex: &repeat, CreatedAt: time.Now().UTC(), QuestionBody: "MUST-NOT-RETURN-PROMPT", AnswerBody: "MUST-NOT-RETURN-ANSWER"})
	}
	key := questionAnswerRunKey(user, target)
	s.questionAnswerRuns[key] = run
	s.questionAnswerOrder = append(s.questionAnswerOrder, key)
	return run
}
func queueIDs(queue QuestionAnswerQueue) []string {
	result := []string{}
	for _, item := range queue.Queued {
		result = append(result, item.RecordID)
	}
	return result
}

func TestQuestionAnswerQueueRotationChangesMatchDispatchWithoutMutatingPreview(t *testing.T) {
	for _, scenario := range []string{"ordinary", "new-account", "cancelled", "exhausted", "deleted-last-key"} {
		t.Run(scenario, func(t *testing.T) {
			s := panicRegressionService(t, nil)
			a := queueFixtureRun(s, "user1", "sub2api:ws1:a", "a", 6)
			b := queueFixtureRun(s, "user1", "sub2api:ws1:b", "b", 4)
			s.questionAnswerConcurrencyLimit = 50
			s.nextQuestionAnswerDispatchLocked()
			switch scenario {
			case "new-account":
				queueFixtureRun(s, "user1", "sub2api:ws1:c", "c", 3)
			case "cancelled":
				b.cancel()
			case "exhausted":
				b.next = len(b.records)
			case "deleted-last-key":
				s.removeQuestionAnswerRunLocked(questionAnswerRunKey("user1", a.targetID), a)
			}
			beforeNext, beforeFlight := b.next, s.questionAnswerInFlight
			copied := s.copyQuestionAnswerQueue()
			first := copied.project("user1", "ws1")
			second := copied.project("user1", "ws1")
			if !reflect.DeepEqual(first, second) || b.next != beforeNext || s.questionAnswerInFlight != beforeFlight {
				t.Fatal("queue preview mutated its copy or live cursors")
			}
			want := []string{}
			for {
				_, _, record, ok := s.nextQuestionAnswerDispatchLocked()
				if !ok {
					break
				}
				want = append(want, record.ID)
			}
			if !reflect.DeepEqual(queueIDs(first), want) {
				t.Fatalf("prediction=%v dispatch=%v", queueIDs(first), want)
			}
			for i, item := range first.Queued {
				if item.Position != i+1 {
					t.Fatal("positions are not consecutive")
				}
			}
		})
	}
}

func TestQuestionAnswerQueueIdentityAndContractOnlyExposeOwnCanonicalTargets(t *testing.T) {
	s := panicRegressionService(t, nil)
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	queueFixtureRun(s, "user1", "sub2api:ws1:own", "own", 2)
	for i, identity := range [][2]string{{"another-user", "sub2api:ws1:a"}, {"user1", "sub2api:ws2:a"}, {"user1", "newapi:ws1:a"}, {"user1", "sub2api:ws1:a:alias"}, {"user1", "sub2api:ws1: a"}} {
		run := queueFixtureRun(s, identity[0], identity[1], fmt.Sprintf("PRIVATE-%d", i), 1)
		run.inFlight = 1
		run.heldSlots = 1
		run.stopReason = QuestionAnswerErrorStorage
		s.questionAnswerInFlight += 2
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	status, body, raw := queueREDRead(t, mux, "ws1")
	if status != 200 || strings.Contains(raw, "PRIVATE") || strings.Contains(raw, "MUST-NOT") || strings.Contains(raw, "another-user") {
		t.Fatalf("unsafe queue: HTTP%d %s", status, raw)
	}
	if body["workspaceOccupied"] != float64(0) || body["heldCount"] != float64(0) || body["availableSlots"] != float64(5) {
		t.Fatalf("foreign occupancy detail leaked: %s", raw)
	}
	for _, tc := range []struct {
		user, contract, workspace string
		status                    int
	}{{"", "3", "ws1", 401}, {"user1", "2", "ws1", 409}, {"user1", "3", "", 409}, {"user1", "3", "ws2", 409}} {
		request := httptest.NewRequest(http.MethodGet, "/api/connection-health/question-answer-queue?workspace="+tc.workspace, nil)
		if tc.user != "" {
			request = request.WithContext(authctx.WithUserID(request.Context(), tc.user))
		}
		request.Header.Set(questionAnswerContractHeaderName, tc.contract)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != tc.status {
			t.Fatalf("scope guard status=%d want%d", recorder.Code, tc.status)
		}
		if tc.user != "" && tc.contract == "3" && tc.workspace == "" && !strings.Contains(recorder.Body.String(), "本次未提供工作区编号") {
			t.Fatal("missing workspace was described as an observed change")
		}
	}
}

func TestQuestionAnswerQueuePreparationHandoffSelectedStoppingAndFinalization(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "q", Enabled: true})
	s := panicRegressionService(t, repo)
	reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", "sub2api:ws1:a", "creating")
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseQuestionAnswerTargetStart(reservation)
	if q := s.copyQuestionAnswerQueue().project("user1", "ws1"); len(q.PreparingTargets) != 1 || len(q.Queued) != 0 {
		t.Fatal("preparation invented pending records")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	go func() {
		_, err := s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
			close(entered)
			<-release
			records, err := repo.CreateQuestionAnswerBatch(ctx, "user1", reservation.targetID, "creating", []string{"m"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 2)
			return records, true, err
		})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("core creation did not start")
	}
	if q := s.copyQuestionAnswerQueue().project("user1", "ws1"); len(q.PreparingTargets) != 1 {
		t.Fatal("handoff duplicated or lost preparation")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	run := s.questionAnswerRuns[questionAnswerRunKey("user1", reservation.targetID)]
	first, second := run.records[0].ID, run.records[1].ID
	_, _, _, ok := s.nextQuestionAnswerDispatchLocked()
	if !ok {
		t.Fatal("could not select")
	}
	q := s.copyQuestionAnswerQueue().project("user1", "ws1")
	if len(q.Processing) != 1 || q.Processing[0].RecordID != first || len(q.Queued) != 1 || q.Queued[0].RecordID != second || q.Processing[0].Runtime.Diagnostics.RequestStartedAt != nil {
		t.Fatal("selected before Mark was not a truthful processing row")
	}
	run.stopReason = string(QuestionAnswerCancelled)
	run.cancel()
	q = s.copyQuestionAnswerQueue().project("user1", "ws1")
	if len(q.Queued) != 0 || len(q.Stopping) != 1 || len(q.FinalizingBatches) != 1 || len(q.PreparingTargets) != 0 {
		t.Fatalf("stop projection counts=%+v", q.Counts)
	}
	// All records may already be confirmed while the final batch operation fails.
	_, err = repo.FinalizeQuestionAnswerBatch(context.Background(), "user1", run.targetID, run.batchID, QuestionAnswerCancelled, "")
	if err != nil {
		t.Fatal(err)
	}
	run.next = len(run.records)
	run.inFlight = 0
	run.progress = nil
	run.cred.Key = "isolated-private-queue-key"
	run.finalErr = errors.New("isolated final batch confirmation failed " + run.cred.Key)
	run.heldSlots = 2
	s.questionAnswerInFlight = 2
	q = s.copyQuestionAnswerQueue().project("user1", "ws1")
	raw, _ := json.Marshal(q)
	if q.HeldCount != 2 || q.WorkspaceOccupied != 2 || len(q.FinalizingBatches) != 1 || q.FinalizingBatches[0].HeldCount != 2 || q.FinalizingBatches[0].State != "failed" || !strings.Contains(q.FinalizingBatches[0].Message, "isolated final batch confirmation") {
		t.Fatalf("held finalization disappeared: %s", raw)
	}
	for _, forbidden := range []string{"recovery", "service_shutdown", "\"cancel\"", "unsavedCount", run.cred.Key} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("queue guessed storage recovery: %s", raw)
		}
	}
	if q.Counts != (QuestionAnswerQueueCounts{0, 0, 0, 0, 1}) {
		t.Fatalf("counts differ from arrays: %+v", q.Counts)
	}
	// Empty local records also do not prove that the database has no records.
	run.records, run.next, run.heldSlots, s.questionAnswerInFlight = nil, 0, 0, 0
	q = s.copyQuestionAnswerQueue().project("user1", "ws1")
	stored, err := repo.ListQuestionAnswerBatch(context.Background(), "user1", run.targetID, run.batchID)
	raw, _ = json.Marshal(q)
	if err != nil || len(stored) != 2 || len(q.FinalizingBatches) != 1 || strings.Contains(string(raw), "recovery") || strings.Contains(string(raw), "service_shutdown") {
		t.Fatal("empty local creation guessed a database outcome")
	}
}

type queueLockCheckingError struct {
	service *Service
	called  *atomic.Bool
	locked  *atomic.Bool
}

func (e queueLockCheckingError) Error() string {
	e.called.Store(true)
	if e.service.questionAnswerMu.TryLock() {
		e.service.questionAnswerMu.Unlock()
	} else {
		e.locked.Store(true)
	}
	return "final save rejected a safe isolated value"
}
func TestQuestionAnswerQueueTenThousandRowsSimulateAndFormatOutsideServiceLock(t *testing.T) {
	// Nil stores/runners make any added SQL or remote access fail this test.
	s := panicRegressionService(t, nil)
	s.questionAnswerHTTP = nil
	for i := 0; i < 100; i++ {
		queueFixtureRun(s, "user1", fmt.Sprintf("sub2api:ws1:a%d", i), fmt.Sprintf("b%d", i), 100)
	}
	called, locked := &atomic.Bool{}, &atomic.Bool{}
	last := s.questionAnswerRuns[s.questionAnswerOrder[99]]
	last.finalErr = queueLockCheckingError{s, called, locked}
	last.stopReason = QuestionAnswerErrorStorage
	last.next = 100
	snapshot := s.copyQuestionAnswerQueue()
	if called.Load() {
		t.Fatal("error formatted under the copy lock")
	}
	q := snapshot.project("user1", "ws1")
	if !called.Load() || locked.Load() {
		t.Fatal("final cause was not formatted outside the service lock")
	}
	if len(q.Queued) != 9900 || q.Queued[0].RecordID != "b0-000" || q.Queued[9899].RecordID != "b98-099" {
		t.Fatalf("queue silently truncated or reordered %d rows", len(q.Queued))
	}
	// Remove the diagnostic formatter from this second proof, then deliberately
	// hold the service lock while projecting the already copied 10,000 identities.
	snapshot.runs[s.questionAnswerOrder[99]].finalErr = nil
	snapshot.runs[s.questionAnswerOrder[99]].eligible = true
	snapshot.runs[s.questionAnswerOrder[99]].next = 0
	s.questionAnswerMu.Lock()
	finished := make(chan QuestionAnswerQueue, 1)
	go func() { finished <- snapshot.project("user1", "ws1") }()
	select {
	case all := <-finished:
		if len(all.Queued) != 10000 {
			t.Errorf("full queue=%d", len(all.Queued))
		}
	case <-time.After(time.Second):
		t.Error("simulation acquired the service lock")
	}
	s.questionAnswerMu.Unlock()
}

type queueScheduledRepository struct {
	questionAnswerScheduleRepository
	base *fakeQuestionAnswerRepository
}

func (r queueScheduledRepository) CreateScheduledQuestionAnswerBatch(ctx context.Context, user, workspace, execution, target, batch string) ([]QuestionAnswerRecord, bool, error) {
	records, err := r.base.CreateQuestionAnswerBatch(ctx, user, target, batch, []string{"model-a", "model-b", "model-c", "model-d", "model-e"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 10)
	return records, true, err
}
func TestQuestionAnswerQueueManualAndScheduledCoreShareCapacityOneAndFiftyIncludingHeld(t *testing.T) {
	for _, limit := range []int{1, 50} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true})
			s := panicRegressionService(t, repo)
			s.questionAnswerConcurrencyLimit = limit
			reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", "sub2api:ws1:manual", "manual")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
				records, err := repo.CreateQuestionAnswerBatch(ctx, "user1", reservation.targetID, "manual", []string{"model-a", "model-b", "model-c", "model-d", "model-e"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 10)
				return records, true, err
			})
			s.releaseQuestionAnswerTargetStart(reservation)
			if err != nil {
				t.Fatal(err)
			}
			execution := QuestionAnswerScheduleExecution{ID: "scheduled-execution", UserID: "user1", AdminAccountID: "ws1"}
			target := QuestionAnswerScheduleExecutionTarget{TargetID: "sub2api:ws1:scheduled", ID: "scheduled-batch"}
			handle := panicPathsScheduleHandle(t, s, execution)
			handle.preparation = questionAnswerSchedulePreparation{credentials: map[string]upstream.ProbeCredential{target.TargetID: {BaseURL: "https://unit.invalid", Key: "fixture"}}}
			s.questionAnswerSchedules = queueScheduledRepository{base: repo}
			s.buildQuestionAnswerScheduleTarget(handle, execution, target)
			s.questionAnswerInFlight = 1
			heldRun := queueFixtureRun(s, "user1", "sub2api:ws1:held", "held", 0)
			heldRun.stopReason, heldRun.heldSlots = QuestionAnswerErrorStorage, 1
			heldRun.cancel()
			count := 0
			seen := map[string]bool{}
			for {
				_, run, _, ok := s.nextQuestionAnswerDispatchLocked()
				if !ok {
					break
				}
				count++
				seen[run.targetID] = true
			}
			q := s.copyQuestionAnswerQueue().project("user1", "ws1")
			if count != limit-1 || q.AvailableSlots != 0 || q.HeldCount != 1 || q.WorkspaceOccupied != limit || len(q.Queued) != 101-limit {
				t.Fatalf("shared cap/held changed: count%d queue%+v", count, q.Counts)
			}
			if limit == 50 && len(seen) != 2 {
				t.Fatal("scheduled and manual runs did not share selection")
			}
		})
	}
}

func TestQuestionAnswerQueueDefaultFifteenActuallyRefillsWhileFourteenHTTPCallsRemain(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true})
	s := panicRegressionService(t, repo)
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	s.questionAnswerTTL = 10 * time.Second
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:a", "real-a", 10)
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:b", "real-b", 10)
	started := make(chan struct{}, 20)
	release := make(chan struct{}, 20)
	var active, peak atomic.Int64
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"answer"}}]}`))}, nil
	})
	s.questionAnswerDispatcherDone = make(chan struct{})
	go s.runQuestionAnswerDispatcher()
	defer func() { s.questionAnswerStop(); <-s.questionAnswerDispatcherDone; s.questionAnswerWG.Wait() }()
	for i := 0; i < 15; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only%d HTTP calls started", i)
		}
	}
	q := s.copyQuestionAnswerQueue().project("user1", "ws1")
	if len(q.Processing) != 15 || len(q.Queued) != 5 || q.AvailableSlots != 0 {
		t.Fatalf("window=%+v", q.Counts)
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("did not refill with fourteen calls still blocked")
	}
	if active.Load() != 15 || peak.Load() != 15 {
		t.Fatalf("window changed: active%d peak%d", active.Load(), peak.Load())
	}
	for i := 0; i < 19; i++ {
		release <- struct{}{}
	}
	waitQuestionAnswerBatchForTarget(t, s, "user1", "sub2api:ws1:a", "real-a", false)
	waitQuestionAnswerBatchForTarget(t, s, "user1", "sub2api:ws1:b", "real-b", false)
}
