package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type fakeQuestionAnswerRepository struct {
	mu        sync.Mutex
	questions map[string]TestQuestion
	records   []QuestionAnswerRecord
	nextID    int
}

func newFakeQuestionAnswerRepository(questions ...TestQuestion) *fakeQuestionAnswerRepository {
	repo := &fakeQuestionAnswerRepository{questions: map[string]TestQuestion{}}
	for _, question := range questions {
		repo.questions[question.ID] = question
	}
	return repo
}

func (f *fakeQuestionAnswerRepository) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeQuestionAnswerRepository) ListTestQuestions(context.Context, string) ([]TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]TestQuestion, 0, len(f.questions))
	for _, question := range f.questions {
		result = append(result, question)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (f *fakeQuestionAnswerRepository) CreateTestQuestion(_ context.Context, _ string, name string, body string) (TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	question := TestQuestion{ID: f.id("q"), Name: name, Body: body, Enabled: true, IsDefault: len(f.questions) == 0, CreatedAt: now, UpdatedAt: now}
	f.questions[question.ID] = question
	return question, nil
}

func (f *fakeQuestionAnswerRepository) UpdateTestQuestion(_ context.Context, _ string, questionID string, name string, body string) (*TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	question, ok := f.questions[questionID]
	if !ok {
		return nil, nil
	}
	question.Name, question.Body, question.UpdatedAt = name, body, time.Now()
	f.questions[questionID] = question
	return &question, nil
}

func (f *fakeQuestionAnswerRepository) SetTestQuestionEnabled(_ context.Context, _ string, questionID string, enabled bool) (*TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	question, ok := f.questions[questionID]
	if !ok {
		return nil, nil
	}
	question.Enabled = enabled
	if !enabled {
		question.IsDefault = false
	}
	f.questions[questionID] = question
	return &question, nil
}

func (f *fakeQuestionAnswerRepository) SetDefaultTestQuestion(_ context.Context, _ string, questionID string) (*TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	question, ok := f.questions[questionID]
	if !ok {
		return nil, nil
	}
	if !question.Enabled {
		return nil, errQuestionAnswerUnavailable
	}
	for id, item := range f.questions {
		item.IsDefault = id == questionID
		f.questions[id] = item
	}
	question = f.questions[questionID]
	return &question, nil
}

func (f *fakeQuestionAnswerRepository) DeleteTestQuestion(_ context.Context, _ string, questionID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.questions[questionID]; !ok {
		return false, nil
	}
	delete(f.questions, questionID)
	return true, nil
}

func (f *fakeQuestionAnswerRepository) CreateQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, models []string, questionIDs []string) ([]QuestionAnswerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, record := range f.records {
		if record.TargetID == targetID && (record.Status == QuestionAnswerPending || record.Status == QuestionAnswerRunning) {
			return nil, errQuestionAnswerActive
		}
	}
	questions := make([]TestQuestion, 0, len(questionIDs))
	for _, questionID := range questionIDs {
		question, ok := f.questions[questionID]
		if !ok || !question.Enabled {
			return nil, errQuestionAnswerUnavailable
		}
		questions = append(questions, question)
	}
	now := time.Now()
	created := make([]QuestionAnswerRecord, 0, len(models)*len(questions))
	for _, model := range models {
		for _, question := range questions {
			record := QuestionAnswerRecord{
				ID: f.id("r"), TargetID: targetID, BatchID: batchID, ModelName: model,
				QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body,
				Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now,
			}
			created = append(created, record)
			f.records = append(f.records, record)
		}
	}
	return cloneQuestionAnswerRecords(created), nil
}

func (f *fakeQuestionAnswerRepository) MarkQuestionAnswerRunning(_ context.Context, _ string, batchID string, recordID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == recordID && f.records[i].BatchID == batchID && f.records[i].Status == QuestionAnswerPending {
			now := time.Now()
			f.records[i].Status, f.records[i].StartedAt, f.records[i].UpdatedAt = QuestionAnswerRunning, &now, now
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeQuestionAnswerRepository) CompleteQuestionAnswer(_ context.Context, _ string, batchID string, recordID string, status QuestionAnswerStatus, answerBody string, errorType string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == recordID && f.records[i].BatchID == batchID && f.records[i].Status == QuestionAnswerRunning {
			now := time.Now()
			f.records[i].Status, f.records[i].AnswerBody, f.records[i].ErrorType = status, answerBody, errorType
			f.records[i].CompletedAt, f.records[i].UpdatedAt = &now, now
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeQuestionAnswerRepository) StopQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for i := range f.records {
		if f.records[i].TargetID != targetID || f.records[i].BatchID != batchID {
			continue
		}
		found = true
		if f.records[i].Status == QuestionAnswerPending || f.records[i].Status == QuestionAnswerRunning {
			now := time.Now()
			f.records[i].Status, f.records[i].AnswerBody, f.records[i].ErrorType = status, "", errorType
			f.records[i].CompletedAt, f.records[i].UpdatedAt = &now, now
		}
	}
	return found, nil
}

func (f *fakeQuestionAnswerRepository) FailAbandonedQuestionAnswers(context.Context, string) (int64, error) {
	return 0, nil
}

func (f *fakeQuestionAnswerRepository) ListQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string) ([]QuestionAnswerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]QuestionAnswerRecord, 0)
	for _, record := range f.records {
		if record.TargetID == targetID && record.BatchID == batchID {
			result = append(result, record)
		}
	}
	return cloneQuestionAnswerRecords(result), nil
}

func (f *fakeQuestionAnswerRepository) LatestQuestionAnswerBatch(ctx context.Context, userID string, targetID string) ([]QuestionAnswerRecord, error) {
	f.mu.Lock()
	latestBatch := ""
	for i := len(f.records) - 1; i >= 0; i-- {
		if f.records[i].TargetID == targetID {
			latestBatch = f.records[i].BatchID
			break
		}
	}
	f.mu.Unlock()
	if latestBatch == "" {
		return []QuestionAnswerRecord{}, nil
	}
	return f.ListQuestionAnswerBatch(ctx, userID, targetID, latestBatch)
}

func (f *fakeQuestionAnswerRepository) ListQuestionAnswerHistory(_ context.Context, _ string, targetID string, page int) (QuestionAnswerHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]QuestionAnswerRecord, 0)
	stats := QuestionAnswerStats{}
	for i := len(f.records) - 1; i >= 0; i-- {
		record := f.records[i]
		if record.TargetID != targetID {
			continue
		}
		all = append(all, record)
		if record.Status == QuestionAnswerSucceeded || record.Status == QuestionAnswerFailed {
			stats.Total++
			if record.Status == QuestionAnswerFailed || record.ManualError {
				stats.Errors++
			} else {
				stats.Normal++
			}
		}
	}
	start := (page - 1) * QuestionAnswerPageSize
	if start > len(all) {
		start = len(all)
	}
	end := min(start+QuestionAnswerPageSize, len(all))
	totalPages := 0
	if len(all) > 0 {
		totalPages = (len(all) + QuestionAnswerPageSize - 1) / QuestionAnswerPageSize
	}
	return QuestionAnswerHistory{Records: cloneQuestionAnswerRecords(all[start:end]), Page: page, PageSize: QuestionAnswerPageSize, TotalItems: len(all), TotalPages: totalPages, Stats: stats}, nil
}

func (f *fakeQuestionAnswerRepository) SetQuestionAnswerManualError(_ context.Context, _ string, targetID string, recordID string, manualError bool) (*QuestionAnswerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == recordID && f.records[i].TargetID == targetID && f.records[i].Status == QuestionAnswerSucceeded {
			f.records[i].ManualError = manualError
			result := f.records[i]
			return &result, nil
		}
	}
	return nil, nil
}

func newQuestionAnswerService(serverURL string, qaRepo *fakeQuestionAnswerRepository, healthRepo *fakeRepository) *Service {
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "group-1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "account", Models: "model-a,model-b"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: serverURL, Key: "secret-key"}},
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, healthRepo)
	service.modelDiscovery = NewModelDiscoveryRunner()
	service.questionAnswers = qaRepo
	service.questionAnswerHTTP = NewQuestionAnswerRunner()
	service.questionAnswerTTL = QuestionAnswerRequestTimeout
	service.initializeQuestionAnswerRuntime()
	return service
}

func waitQuestionAnswerBatch(t *testing.T, service *Service, batchID string, wantActive bool) QuestionAnswerBatch {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := service.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batchID)
		if err == nil && batch.Active == wantActive {
			return batch
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("batch %s did not reach active=%v", batchID, wantActive)
	return QuestionAnswerBatch{}
}

func waitQuestionAnswerRunReleased(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.questionAnswerMu.Lock()
		active := len(service.questionAnswerRuns)
		service.questionAnswerMu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("question answer run was not released")
}

func TestQuestionAnswerBatchRunsDeduplicatedCombinationsInOrderWithoutMaxTokens(t *testing.T) {
	var mu sync.Mutex
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
		case "/v1/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if _, exists := payload["max_tokens"]; exists {
				t.Fatalf("question answer request must omit max_tokens: %#v", payload)
			}
			messages := payload["messages"].([]any)
			question := messages[0].(map[string]any)["content"].(string)
			model := payload["model"].(string)
			mu.Lock()
			requests = append(requests, model+"|"+question)
			mu.Unlock()
			_, _ = io.WriteString(w, fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, model+" answer"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "Q1", Body: "question one", Enabled: true, IsDefault: true},
		TestQuestion{ID: "q2", Name: "Q2", Body: "question two", Enabled: true},
	)
	healthRepo := newFakeRepository()
	service := newQuestionAnswerService(server.URL, qaRepo, healthRepo)
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a", "model-a", "model-b"}, QuestionIDs: []string{"q1", "q1", "q2"},
	})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if len(completed.Records) != 4 {
		t.Fatalf("records = %d, want 4", len(completed.Records))
	}
	mu.Lock()
	gotRequests := append([]string(nil), requests...)
	mu.Unlock()
	wantRequests := []string{"model-a|question one", "model-a|question two", "model-b|question one", "model-b|question two"}
	if fmt.Sprint(gotRequests) != fmt.Sprint(wantRequests) {
		t.Fatalf("request order = %v, want %v", gotRequests, wantRequests)
	}
	if len(healthRepo.states) != 0 || len(healthRepo.events) != 0 || len(healthRepo.budgetClaims) != 0 {
		t.Fatalf("question answers touched health state: states=%v events=%v budget=%v", healthRepo.states, healthRepo.events, healthRepo.budgetClaims)
	}
}

func TestQuestionAnswerBatchReturnsBeforeCompletionAndSurvivesCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"done"}}]}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	batch, err := service.StartQuestionAnswerBatch(requestCtx, "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background request did not start")
	}
	close(release)
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if completed.Records[0].Status != QuestionAnswerSucceeded || completed.Records[0].AnswerBody != "done" {
		t.Fatalf("caller cancellation stopped background batch: %+v", completed.Records[0])
	}
}

func TestQuestionAnswerFailureStoresOnlySafeErrorType(t *testing.T) {
	const rawMarker = "upstream-raw-marker-93af"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		w.Header().Set("X-Upstream-Debug", rawMarker)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"`+rawMarker+`"},"usage":{"secret":"`+rawMarker+`"}}`)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	record := completed.Records[0]
	if record.Status != QuestionAnswerFailed || record.ErrorType != QuestionAnswerErrorServer || record.AnswerBody != "" {
		t.Fatalf("failed record = %+v", record)
	}
	stored := fmt.Sprintf("%+v", record)
	if strings.Contains(stored, rawMarker) || strings.Contains(stored, "secret-key") {
		t.Fatalf("record leaked raw upstream data or credentials: %s", stored)
	}
}

func TestQuestionAnswerBatchRejectsDuplicateStartAndCancelIsIdempotent(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: contextBlockingRoundTripper{started: started, cancelled: cancelled}}}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	<-started
	_, err = service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err == nil || err.Error() != ErrorQuestionAnswerActive {
		t.Fatalf("duplicate start error = %v, want %s", err, ErrorQuestionAnswerActive)
	}
	stopped, err := service.StopQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batch.BatchID)
	if err != nil || stopped.Records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("stop batch: batch=%+v err=%v", stopped, err)
	}
	again, err := service.StopQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batch.BatchID)
	if err != nil || again.Records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("idempotent stop: batch=%+v err=%v", again, err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("active request context was not cancelled")
	}
	waitQuestionAnswerRunReleased(t, service)
	responseBody := &trackingBody{Reader: strings.NewReader(`{"choices":[{"message":{"content":"new batch"}}]}`)}
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: fixedRoundTripper{body: responseBody}}}
	restarted, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err != nil {
		t.Fatalf("restart immediately after stop: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, restarted.BatchID, false)
	if completed.Records[0].Status != QuestionAnswerSucceeded || completed.Records[0].AnswerBody != "new batch" {
		t.Fatalf("restarted batch record = %+v", completed.Records[0])
	}
}

func TestQuestionAnswerTimeoutContinuesNextCombinationAndShutdownReleasesRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if strings.Contains(payload.Messages[0].Content, "slow") {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"fast answer"}}]}`)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "slow", Body: "slow question", Enabled: true},
		TestQuestion{ID: "q2", Name: "fast", Body: "fast question", Enabled: true},
	)
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	service.questionAnswerTTL = 20 * time.Millisecond
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1", "q2"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if completed.Records[0].Status != QuestionAnswerFailed || completed.Records[0].ErrorType != QuestionAnswerErrorTimeout {
		t.Fatalf("slow record = %+v", completed.Records[0])
	}
	if completed.Records[1].Status != QuestionAnswerSucceeded || completed.Records[1].AnswerBody != "fast answer" {
		t.Fatalf("fast record = %+v", completed.Records[1])
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.ShutdownQuestionAnswers(shutdownCtx); err != nil {
		t.Fatalf("shutdown question answers: %v", err)
	}
	service.questionAnswerMu.Lock()
	active := len(service.questionAnswerRuns)
	service.questionAnswerMu.Unlock()
	if active != 0 {
		t.Fatalf("active runs after shutdown = %d", active)
	}
}

func TestQuestionAnswerShutdownFailsActiveBatchAndReleasesRun(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "Q1", Body: "question one", Enabled: true},
		TestQuestion{ID: "q2", Name: "Q2", Body: "question two", Enabled: true},
	)
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: contextBlockingRoundTripper{started: started, cancelled: cancelled}}}
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1", "q2"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background request did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.ShutdownQuestionAnswers(shutdownCtx); err != nil {
		t.Fatalf("shutdown question answers: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel active request")
	}
	waitQuestionAnswerRunReleased(t, service)
	records, err := qaRepo.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batch.BatchID)
	if err != nil {
		t.Fatalf("list batch after shutdown: %v", err)
	}
	for _, record := range records {
		if record.Status != QuestionAnswerFailed || record.ErrorType != QuestionAnswerErrorServiceShutdown || record.AnswerBody != "" {
			t.Fatalf("record after shutdown = %+v", record)
		}
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type fixedRoundTripper struct{ body *trackingBody }

func (r fixedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: r.body, Header: make(http.Header)}, nil
}

type contextBlockingRoundTripper struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func (r contextBlockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.once.Do(func() { close(r.started) })
	<-request.Context().Done()
	close(r.cancelled)
	return nil, request.Context().Err()
}

func TestQuestionAnswerRunnerClosesResponseBodyAndDiscardsRawPayload(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"choices":[{"message":{"content":"answer"}}],"usage":{"secret":"raw-marker"}}`)}
	runner := &QuestionAnswerRunner{client: &http.Client{Transport: fixedRoundTripper{body: body}}}
	answer, errorType := runner.Ask(context.Background(), upstream.ProbeCredential{BaseURL: "https://example.test", Key: "secret-key"}, "model-a", "question")
	if answer != "answer" || errorType != "" {
		t.Fatalf("answer=%q errorType=%q", answer, errorType)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}
