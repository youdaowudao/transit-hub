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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type fakeQuestionAnswerRepository struct {
	mu              sync.Mutex
	questions       map[string]TestQuestion
	records         []QuestionAnswerRecord
	nextID          int
	createCalls     int
	lastRepeatCount int
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
		question.Keywords = append([]string{}, question.Keywords...)
		result = append(result, question)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (f *fakeQuestionAnswerRepository) CreateTestQuestion(_ context.Context, _ string, name string, body string, keywords []string) (TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	question := TestQuestion{ID: f.id("q"), Name: name, Body: body, Keywords: append([]string{}, keywords...), Enabled: true, IsDefault: len(f.questions) == 0, CreatedAt: now, UpdatedAt: now}
	f.questions[question.ID] = question
	return question, nil
}

func (f *fakeQuestionAnswerRepository) UpdateTestQuestion(_ context.Context, _ string, questionID string, name string, body string, keywords *[]string) (*TestQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	question, ok := f.questions[questionID]
	if !ok {
		return nil, nil
	}
	question.Name, question.Body, question.UpdatedAt = name, body, time.Now()
	if keywords != nil {
		question.Keywords = append([]string{}, (*keywords)...)
	}
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

func (f *fakeQuestionAnswerRepository) CreateQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int) ([]QuestionAnswerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastRepeatCount = repeatCount
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
	created := make([]QuestionAnswerRecord, 0, len(models)*len(questions)*repeatCount)
	for _, model := range models {
		for _, question := range questions {
			for sample := 0; sample < repeatCount; sample++ {
				record := QuestionAnswerRecord{
					ID: f.id("r"), TargetID: targetID, BatchID: batchID, ModelName: model,
					QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body,
					QuestionKeywordSnapshot: append([]string{}, question.Keywords...),
					ReasoningEffort:         questionAnswerReasoningEffortPointer(reasoningEffort),
					Status:                  QuestionAnswerPending, CreatedAt: now, UpdatedAt: now,
				}
				created = append(created, record)
				f.records = append(f.records, record)
			}
		}
	}
	return cloneQuestionAnswerRecords(created), nil
}

func (f *fakeQuestionAnswerRepository) createMetrics() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls, f.lastRepeatCount, len(f.records)
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

func (f *fakeQuestionAnswerRepository) StopPendingQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	return f.stopQuestionAnswerRecords(targetID, batchID, status, errorType, false)
}

func (f *fakeQuestionAnswerRepository) FinalizeQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	return f.stopQuestionAnswerRecords(targetID, batchID, status, errorType, true)
}

func (f *fakeQuestionAnswerRepository) stopQuestionAnswerRecords(targetID string, batchID string, status QuestionAnswerStatus, errorType string, includeRunning bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for i := range f.records {
		if f.records[i].TargetID != targetID || f.records[i].BatchID != batchID {
			continue
		}
		found = true
		if f.records[i].Status == QuestionAnswerPending || (includeRunning && f.records[i].Status == QuestionAnswerRunning) {
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
	stats := QuestionAnswerStats{ByModel: []QuestionAnswerModelStats{}}
	modelIndexes := make(map[string]int)
	for i := len(f.records) - 1; i >= 0; i-- {
		record := f.records[i]
		if record.TargetID != targetID {
			continue
		}
		all = append(all, record)
		addQuestionAnswerRecordStats(&stats, record)
		modelIndex, exists := modelIndexes[record.ModelName]
		if !exists {
			modelIndex = len(stats.ByModel)
			modelIndexes[record.ModelName] = modelIndex
			stats.ByModel = append(stats.ByModel, QuestionAnswerModelStats{ModelName: record.ModelName})
		}
		modelStats := QuestionAnswerStats{Requests: stats.ByModel[modelIndex].Requests, Reviews: stats.ByModel[modelIndex].Reviews}
		addQuestionAnswerRecordStats(&modelStats, record)
		stats.ByModel[modelIndex].Requests = modelStats.Requests
		stats.ByModel[modelIndex].Reviews = modelStats.Reviews
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
	return QuestionAnswerHistory{
		Records: cloneQuestionAnswerRecords(all[start:end]), Page: page, PageSize: QuestionAnswerPageSize,
		TotalItems: len(all), TotalPages: totalPages, Stats: stats,
		TodayStats: QuestionAnswerStats{ByModel: []QuestionAnswerModelStats{}},
	}, nil
}

func (f *fakeQuestionAnswerRepository) SetQuestionAnswerJudgment(_ context.Context, _ string, targetID string, recordID string, judgment QuestionAnswerJudgment) (*QuestionAnswerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == recordID && f.records[i].TargetID == targetID && f.records[i].Status == QuestionAnswerSucceeded {
			f.records[i].AnswerJudgment = questionAnswerJudgmentPointer(judgment)
			f.records[i].ManualError = judgment == QuestionAnswerIncorrect
			result := f.records[i]
			return &result, nil
		}
	}
	return nil, nil
}

func questionAnswerJudgmentPointer(value QuestionAnswerJudgment) *QuestionAnswerJudgment {
	return &value
}

type inconsistentQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository
}

func (f *inconsistentQuestionAnswerRepository) CreateQuestionAnswerBatch(_ context.Context, _ string, targetID string, batchID string, _ []string, _ []string, _ QuestionAnswerReasoningEffort, _ int) ([]QuestionAnswerRecord, error) {
	medium := QuestionAnswerReasoningEffortMedium
	high := QuestionAnswerReasoningEffortHigh
	now := time.Now()
	return []QuestionAnswerRecord{
		{ID: "mixed-1", TargetID: targetID, BatchID: batchID, ModelName: "model-a", QuestionID: "q1", QuestionName: "Q1", QuestionBody: "question", ReasoningEffort: &medium, Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now},
		{ID: "mixed-2", TargetID: targetID, BatchID: batchID, ModelName: "model-a", QuestionID: "q2", QuestionName: "Q2", QuestionBody: "question 2", ReasoningEffort: &high, Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now},
	}, nil
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

func TestQuestionAnswerKeywordsNormalizeAndRejectCapacity(t *testing.T) {
	assertError := func(t *testing.T, input []string, want string) {
		t.Helper()
		_, err := normalizeTestQuestionKeywords(&input)
		if err == nil || err.Error() != want {
			t.Fatalf("normalize error = %v, want %s", err, want)
		}
	}

	t.Run("trim ASCII-fold deduplicate and preserve first spelling", func(t *testing.T) {
		input := []string{"  错误码  ", "Error", "error", "[done]", "Ä", "ä"}
		got, err := normalizeTestQuestionKeywords(&input)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"错误码", "Error", "[done]", "Ä", "ä"}
		if got == nil || !reflect.DeepEqual(*got, want) {
			t.Fatalf("keywords = %#v, want %#v", got, want)
		}
	})

	t.Run("missing and explicit empty remain distinct", func(t *testing.T) {
		got, err := normalizeTestQuestionKeywords(nil)
		if err != nil || got != nil {
			t.Fatalf("missing keywords = %#v err=%v, want nil nil", got, err)
		}
		empty := []string{}
		got, err = normalizeTestQuestionKeywords(&empty)
		if err != nil || got == nil || *got == nil || len(*got) != 0 {
			t.Fatalf("explicit empty keywords = %#v err=%v, want non-nil empty", got, err)
		}
	})

	t.Run("blank item is rejected before deduplication", func(t *testing.T) {
		assertError(t, []string{"valid", " \t\r\n "}, ErrorTestQuestionKeywordBlank)
	})

	t.Run("count limit accepts twenty and rejects twenty one", func(t *testing.T) {
		keywords := make([]string, TestQuestionKeywordCountLimit)
		for i := range keywords {
			keywords[i] = fmt.Sprintf("keyword-%02d", i)
		}
		if got, err := normalizeTestQuestionKeywords(&keywords); err != nil || got == nil || len(*got) != TestQuestionKeywordCountLimit {
			t.Fatalf("twenty keywords = %#v err=%v", got, err)
		}
		assertError(t, append(keywords, "overflow"), ErrorTestQuestionKeywordCount)
	})

	t.Run("rune limit accepts sixty four and rejects sixty five", func(t *testing.T) {
		valid := []string{strings.Repeat("界", TestQuestionKeywordRuneLimit)}
		if _, err := normalizeTestQuestionKeywords(&valid); err != nil {
			t.Fatalf("64-rune keyword: %v", err)
		}
		assertError(t, []string{strings.Repeat("界", TestQuestionKeywordRuneLimit+1)}, ErrorTestQuestionKeywordLength)
	})

	t.Run("UTF-8 byte limit accepts 2048 and rejects 2049", func(t *testing.T) {
		runes := []string{"😀", "😁", "😂", "😃", "😄", "😅", "😆", "😉"}
		keywords := make([]string, len(runes))
		for i, value := range runes {
			keywords[i] = strings.Repeat(value, TestQuestionKeywordRuneLimit)
		}
		if got, err := normalizeTestQuestionKeywords(&keywords); err != nil || got == nil || len(*got) != len(keywords) {
			t.Fatalf("2048-byte keywords = %#v err=%v", got, err)
		}
		assertError(t, append(keywords, "x"), ErrorTestQuestionKeywordBytes)
	})
}

func TestQuestionAnswerRepeatCountDefaultsRejectsAmbiguityAndCapsBeforeDiscovery(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		err  string
	}{
		{name: "missing defaults one", raw: "", want: 1},
		{name: "one", raw: "1", want: 1},
		{name: "ten", raw: "10", want: 10},
		{name: "null", raw: "null", err: ErrorQuestionAnswerRepeatCount},
		{name: "negative", raw: "-1", err: ErrorQuestionAnswerRepeatCount},
		{name: "zero", raw: "0", err: ErrorQuestionAnswerRepeatCount},
		{name: "eleven", raw: "11", err: ErrorQuestionAnswerRepeatCount},
		{name: "fraction", raw: "1.5", err: ErrorQuestionAnswerRepeatCount},
		{name: "string", raw: `"2"`, err: ErrorQuestionAnswerRepeatCount},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeQuestionAnswerRepeatCount(json.RawMessage(test.raw))
			if test.err == "" && (err != nil || got != test.want) {
				t.Fatalf("got=%d err=%v want=%d", got, err, test.want)
			}
			if test.err != "" && !errors.Is(err, requestError(test.err)) {
				t.Fatalf("err=%v want=%s", err, test.err)
			}
		})
	}

	models := make([]string, 17)
	modelPayload := struct {
		Data []map[string]string `json:"data"`
	}{Data: make([]map[string]string, 0, len(models))}
	for i := range models {
		models[i] = fmt.Sprintf("model-%02d", i+1)
		modelPayload.Data = append(modelPayload.Data, map[string]string{"id": models[i]})
	}
	var callsMu sync.Mutex
	modelDiscoveryCalls := 0
	upstreamAskCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			callsMu.Lock()
			modelDiscoveryCalls++
			callsMu.Unlock()
			_ = json.NewEncoder(w).Encode(modelPayload)
		case "/v1/chat/completions":
			callsMu.Lock()
			upstreamAskCalls++
			callsMu.Unlock()
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	repository := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "Q1", Body: "question 1", Enabled: true},
		TestQuestion{ID: "q2", Name: "Q2", Body: "question 2", Enabled: true},
		TestQuestion{ID: "q3", Name: "Q3", Body: "question 3", Enabled: true},
	)
	service := newQuestionAnswerService(server.URL, repository, newFakeRepository())
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	exactlyFifty, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: models[:5], QuestionIDs: []string{"q1"}, RepeatCount: json.RawMessage("10"),
	})
	if err != nil {
		t.Fatalf("start exactly fifty: %v", err)
	}
	if exactlyFifty.SubmittedCount != QuestionAnswerBatchRecordLimit {
		t.Fatalf("submitted=%d want=%d", exactlyFifty.SubmittedCount, QuestionAnswerBatchRecordLimit)
	}
	waitQuestionAnswerBatch(t, service, exactlyFifty.BatchID, false)
	createCalls, repeatCount, recordCount := repository.createMetrics()
	if createCalls != 1 || repeatCount != 10 || recordCount != QuestionAnswerBatchRecordLimit {
		t.Fatalf("repository metrics calls=%d repeat=%d records=%d", createCalls, repeatCount, recordCount)
	}
	callsMu.Lock()
	discoveryBefore := modelDiscoveryCalls
	askBefore := upstreamAskCalls
	callsMu.Unlock()

	_, err = service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: models, QuestionIDs: []string{"q1", "q2", "q3"}, RepeatCount: json.RawMessage("1"),
	})
	if !errors.Is(err, requestError(ErrorQuestionAnswerBatchLimit)) {
		t.Fatalf("total 51 error=%v want=%s", err, ErrorQuestionAnswerBatchLimit)
	}
	createCalls, repeatCount, recordCount = repository.createMetrics()
	if createCalls != 1 || repeatCount != 10 || recordCount != QuestionAnswerBatchRecordLimit {
		t.Fatalf("rejected total changed repository calls=%d repeat=%d records=%d", createCalls, repeatCount, recordCount)
	}
	callsMu.Lock()
	discoveryAfter := modelDiscoveryCalls
	askAfter := upstreamAskCalls
	callsMu.Unlock()
	if discoveryAfter != discoveryBefore || askAfter != askBefore {
		t.Fatalf("rejected total reached external calls: discovery %d->%d ask %d->%d", discoveryBefore, discoveryAfter, askBefore, askAfter)
	}
}

func TestQuestionAnswerKeywordsPreserveOldClientCreateAndUpdateSemantics(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "existing", Name: "Existing", Body: "Existing body", Keywords: []string{"keep"}, Enabled: true},
		TestQuestion{ID: "legacy", Name: "Legacy", Body: "Legacy body", Keywords: nil, Enabled: true},
	)
	service := &Service{questionAnswers: repo}

	created, err := service.CreateTestQuestion(context.Background(), "user", TestQuestionInput{Name: "Created", Body: "Created body"})
	if err != nil {
		t.Fatalf("create without keywords: %v", err)
	}
	if created.Keywords == nil || len(created.Keywords) != 0 {
		t.Fatalf("created keywords = %#v, want non-nil empty", created.Keywords)
	}

	updated, err := service.UpdateTestQuestion(context.Background(), "user", "existing", TestQuestionInput{Name: "Existing changed", Body: "Existing body changed"})
	if err != nil {
		t.Fatalf("update without keywords: %v", err)
	}
	if !reflect.DeepEqual(updated.Keywords, []string{"keep"}) {
		t.Fatalf("missing update keywords = %#v, want preserved", updated.Keywords)
	}

	empty := []string{}
	updated, err = service.UpdateTestQuestion(context.Background(), "user", "existing", TestQuestionInput{Name: "Existing cleared", Body: "Existing body cleared", Keywords: &empty})
	if err != nil {
		t.Fatalf("explicit keyword clear: %v", err)
	}
	if updated.Keywords == nil || len(updated.Keywords) != 0 {
		t.Fatalf("cleared keywords = %#v, want non-nil empty", updated.Keywords)
	}

	questions, err := service.ListTestQuestions(context.Background(), "user")
	if err != nil {
		t.Fatalf("list questions: %v", err)
	}
	for _, question := range questions {
		if question.Keywords == nil {
			t.Fatalf("question %s returned null keywords", question.ID)
		}
	}
}

func waitQuestionAnswerBatch(t *testing.T, service *Service, batchID string, wantActive bool) QuestionAnswerBatch {
	return waitQuestionAnswerBatchForTarget(t, service, "user1", "sub2api:ws1:acc-1", batchID, wantActive)
}

func waitQuestionAnswerBatchForTarget(t *testing.T, service *Service, userID string, targetID string, batchID string, wantActive bool) QuestionAnswerBatch {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := service.GetQuestionAnswerBatch(context.Background(), userID, targetID, batchID)
		if err == nil && batch.Active == wantActive {
			return batch
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("batch %s did not reach active=%v", batchID, wantActive)
	return QuestionAnswerBatch{}
}

func newMultiTargetQuestionAnswerService(serverURL string, qaRepo questionAnswerRepository, healthRepo *fakeRepository) *Service {
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "group-1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {
				{ID: "acc-a", Name: "account-a", Models: "model-a"},
				{ID: "acc-b", Name: "account-b", Models: "model-a"},
			},
		},
		credByAccount: map[string]upstream.ProbeCredential{
			"acc-a": {BaseURL: serverURL, Key: "secret-key-a"},
			"acc-b": {BaseURL: serverURL, Key: "secret-key-b"},
		},
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, healthRepo)
	service.modelDiscovery = NewModelDiscoveryRunner()
	service.questionAnswers = qaRepo
	service.questionAnswerHTTP = NewQuestionAnswerRunner()
	service.questionAnswerTTL = QuestionAnswerRequestTimeout
	service.initializeQuestionAnswerRuntime()
	return service
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

type controlledQuestionAnswerRoundTripper struct {
	started   chan string
	release   chan struct{}
	cancelled chan string

	mu        sync.Mutex
	active    int
	maxActive int
}

func newControlledQuestionAnswerRoundTripper(size int) *controlledQuestionAnswerRoundTripper {
	return &controlledQuestionAnswerRoundTripper{
		started:   make(chan string, size),
		release:   make(chan struct{}, size),
		cancelled: make(chan string, size),
	}
}

func (r *controlledQuestionAnswerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	question := ""
	if len(payload.Messages) > 0 {
		question = payload.Messages[0].Content
	}
	r.mu.Lock()
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	r.mu.Unlock()
	r.started <- question

	select {
	case <-r.release:
	case <-request.Context().Done():
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
		r.cancelled <- question
		return nil, request.Context().Err()
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	body := io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"answer"}}]}`))
	return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
}

func (r *controlledQuestionAnswerRoundTripper) peak() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

func TestQuestionAnswerGlobalPoolCapsFiveAndRotatesBatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	questions := make([]TestQuestion, 0, 12)
	aQuestionIDs := make([]string, 0, 6)
	bQuestionIDs := make([]string, 0, 6)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("q%d", i)
		body := fmt.Sprintf("A-%d", i)
		if i > 6 {
			body = fmt.Sprintf("B-%d", i-6)
		}
		questions = append(questions, TestQuestion{ID: id, Name: body, Body: body, Enabled: true})
		if i <= 6 {
			aQuestionIDs = append(aQuestionIDs, id)
		} else {
			bQuestionIDs = append(bQuestionIDs, id)
		}
	}
	repository := newFakeQuestionAnswerRepository(questions...)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	transport := newControlledQuestionAnswerRoundTripper(12)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	const targetA = "sub2api:ws1:acc-a"
	const targetB = "sub2api:ws1:acc-b"
	batchA, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: aQuestionIDs})
	if err != nil {
		t.Fatalf("start batch A: %v", err)
	}
	started := make([]string, 0, 12)
	for i := 0; i < questionAnswerConcurrency; i++ {
		select {
		case question := <-transport.started:
			started = append(started, question)
		case <-time.After(time.Second):
			t.Fatalf("initial global request %d did not start", i+1)
		}
	}
	batchB, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetB, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: bQuestionIDs})
	if err != nil {
		t.Fatalf("start batch B: %v", err)
	}
	select {
	case question := <-transport.started:
		t.Fatalf("global sixth request %q started before a slot opened", question)
	case <-time.After(50 * time.Millisecond):
	}

	for len(started) < 12 {
		transport.release <- struct{}{}
		select {
		case question := <-transport.started:
			started = append(started, question)
		case <-time.After(time.Second):
			t.Fatalf("request %d did not start after a slot opened", len(started)+1)
		}
	}
	if !strings.HasPrefix(started[5], "B-") {
		t.Fatalf("batch B was starved behind batch A: sixth eligible request=%q sequence=%v", started[5], started)
	}
	for i := 0; i < questionAnswerConcurrency; i++ {
		transport.release <- struct{}{}
	}
	waitQuestionAnswerBatchForTarget(t, service, "user1", targetA, batchA.BatchID, false)
	waitQuestionAnswerBatchForTarget(t, service, "user1", targetB, batchB.BatchID, false)
	if peak := transport.peak(); peak != questionAnswerConcurrency {
		t.Fatalf("global peak=%d want=%d", peak, questionAnswerConcurrency)
	}
	seen := make(map[string]int, len(started))
	for _, question := range started {
		seen[question]++
	}
	for i := 1; i <= 6; i++ {
		for _, question := range []string{fmt.Sprintf("A-%d", i), fmt.Sprintf("B-%d", i)} {
			if seen[question] != 1 {
				t.Fatalf("question %s started %d times; all starts=%v", question, seen[question], started)
			}
		}
	}
}

type cancellationHoldingRoundTripper struct {
	started              chan string
	release              chan struct{}
	cancellationObserved chan string
	allowCancelled       chan struct{}
}

func newCancellationHoldingRoundTripper(size int) *cancellationHoldingRoundTripper {
	return &cancellationHoldingRoundTripper{
		started: make(chan string, size), release: make(chan struct{}, size),
		cancellationObserved: make(chan string, size), allowCancelled: make(chan struct{}, size),
	}
}

func (r *cancellationHoldingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	question := payload.Messages[0].Content
	r.started <- question
	select {
	case <-r.release:
		body := io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"answer"}}]}`))
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	case <-request.Context().Done():
		r.cancellationObserved <- question
		<-r.allowCancelled
		return nil, request.Context().Err()
	}
}

func TestQuestionAnswerCancelKeepsSlotUntilRunningAskExits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	questions := make([]TestQuestion, 0, 6)
	aQuestionIDs := make([]string, 0, 5)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("cancel-q%d", i)
		body := fmt.Sprintf("A-cancel-%d", i)
		if i == 6 {
			body = "B-after-cancel"
		}
		questions = append(questions, TestQuestion{ID: id, Name: body, Body: body, Enabled: true})
		if i <= 5 {
			aQuestionIDs = append(aQuestionIDs, id)
		}
	}
	repository := newFakeQuestionAnswerRepository(questions...)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	transport := newCancellationHoldingRoundTripper(6)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	const targetA = "sub2api:ws1:acc-a"
	const targetB = "sub2api:ws1:acc-b"
	batchA, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: aQuestionIDs})
	if err != nil {
		t.Fatalf("start cancel batch A: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.started:
		case <-time.After(time.Second):
			t.Fatalf("A request %d did not start", i+1)
		}
	}
	batchB, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetB, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"cancel-q6"}})
	if err != nil {
		t.Fatalf("start waiting batch B: %v", err)
	}
	select {
	case question := <-transport.started:
		t.Fatalf("B started before A cancellation: %s", question)
	case <-time.After(50 * time.Millisecond):
	}
	stopResult := make(chan error, 1)
	go func() {
		_, stopErr := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batchA.BatchID)
		stopResult <- stopErr
	}()
	for i := 0; i < 5; i++ {
		select {
		case <-transport.cancellationObserved:
		case <-time.After(time.Second):
			t.Fatalf("A request %d did not observe cancellation", i+1)
		}
	}
	aRecords, err := repository.ListQuestionAnswerBatch(context.Background(), "user1", targetA, batchA.BatchID)
	if err != nil {
		t.Fatalf("list A while cancellation held: %v", err)
	}
	for _, record := range aRecords {
		if record.Status != QuestionAnswerRunning {
			t.Fatalf("A running record was finalized before Ask exited: %+v", record)
		}
	}
	select {
	case question := <-transport.started:
		t.Fatalf("B started while all cancelled A calls were still blocked: %s", question)
	default:
	}

	transport.allowCancelled <- struct{}{}
	select {
	case question := <-transport.started:
		if question != "B-after-cancel" {
			t.Fatalf("unexpected replacement request %q", question)
		}
	case <-time.After(time.Second):
		t.Fatal("B did not start after one cancelled Ask actually exited")
	}
	for i := 1; i < 5; i++ {
		transport.allowCancelled <- struct{}{}
	}
	transport.release <- struct{}{}
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("stop A: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop A did not settle after all Ask calls exited")
	}
	stoppedA := waitQuestionAnswerBatchForTarget(t, service, "user1", targetA, batchA.BatchID, false)
	for _, record := range stoppedA.Records {
		if record.Status != QuestionAnswerCancelled {
			t.Fatalf("A record after cancellation=%+v", record)
		}
	}
	completedB := waitQuestionAnswerBatchForTarget(t, service, "user1", targetB, batchB.BatchID, false)
	if len(completedB.Records) != 1 || completedB.Records[0].Status != QuestionAnswerSucceeded {
		t.Fatalf("B after cancellation=%+v", completedB)
	}
}

var errQuestionAnswerTestFinalize = errors.New("test finalizer failure")

type controlledFinalizeQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository

	mu                     sync.Mutex
	failOneCompletion      bool
	completionFailed       bool
	finalizeCalls          int
	stopPendingCalls       int
	stopPendingObserved    chan struct{}
	firstFinalizeStarted   chan struct{}
	firstFinalizeRelease   chan struct{}
	firstFinalizeReleaseMu sync.Once
	stopPendingRelease     chan struct{}
	alwaysWaitForDeadline  bool
}

func newControlledFinalizeQuestionAnswerRepository(base *fakeQuestionAnswerRepository, failOneCompletion bool) *controlledFinalizeQuestionAnswerRepository {
	return &controlledFinalizeQuestionAnswerRepository{
		fakeQuestionAnswerRepository: base,
		failOneCompletion:            failOneCompletion,
		stopPendingObserved:          make(chan struct{}, 16),
		firstFinalizeStarted:         make(chan struct{}),
		firstFinalizeRelease:         make(chan struct{}),
	}
}

func (r *controlledFinalizeQuestionAnswerRepository) CompleteQuestionAnswer(ctx context.Context, userID string, batchID string, recordID string, status QuestionAnswerStatus, answerBody string, errorType string) (bool, error) {
	r.mu.Lock()
	if r.failOneCompletion && !r.completionFailed {
		r.completionFailed = true
		r.mu.Unlock()
		return false, errors.New("test completion storage failure")
	}
	r.mu.Unlock()
	return r.fakeQuestionAnswerRepository.CompleteQuestionAnswer(ctx, userID, batchID, recordID, status, answerBody, errorType)
}

func (r *controlledFinalizeQuestionAnswerRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.stopPendingCalls++
	release := r.stopPendingRelease
	r.mu.Unlock()
	r.stopPendingObserved <- struct{}{}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *controlledFinalizeQuestionAnswerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.finalizeCalls++
	call := r.finalizeCalls
	alwaysWait := r.alwaysWaitForDeadline
	r.mu.Unlock()
	if call == 1 {
		close(r.firstFinalizeStarted)
	}
	if alwaysWait {
		<-ctx.Done()
		return true, ctx.Err()
	}
	if call == 1 {
		<-r.firstFinalizeRelease
		return true, errQuestionAnswerTestFinalize
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *controlledFinalizeQuestionAnswerRepository) releaseFirstFinalize() {
	r.firstFinalizeReleaseMu.Do(func() { close(r.firstFinalizeRelease) })
}

func (r *controlledFinalizeQuestionAnswerRepository) finalizeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalizeCalls
}

func TestQuestionAnswerStopDoesNotRetryFailureCreatedDuringSameCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "same-attempt-q", Name: "same-attempt", Body: "same-attempt", Enabled: true})
	repository := newControlledFinalizeQuestionAnswerRepository(base, false)
	repository.stopPendingRelease = make(chan struct{})
	repository.releaseFirstFinalize()
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	transport := newCancellationHoldingRoundTripper(1)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() {
		select {
		case <-repository.stopPendingRelease:
		default:
			close(repository.stopPendingRelease)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.ShutdownQuestionAnswers(ctx)
	}()

	const target = "sub2api:ws1:acc-a"
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", target, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"same-attempt-q"}})
	if err != nil {
		t.Fatalf("start same-attempt batch: %v", err)
	}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("same-attempt request did not start")
	}
	stopResult := make(chan error, 1)
	go func() {
		_, stopErr := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batch.BatchID)
		stopResult <- stopErr
	}()
	select {
	case <-repository.stopPendingObserved:
	case <-time.After(time.Second):
		t.Fatal("same-attempt stop did not block in StopPending")
	}
	select {
	case <-transport.cancellationObserved:
	case <-time.After(time.Second):
		t.Fatal("same-attempt request did not observe cancellation")
	}
	transport.allowCancelled <- struct{}{}
	select {
	case <-repository.firstFinalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("same-attempt finalizer did not start")
	}
	deadline := time.Now().Add(time.Second)
	for repository.finalizeCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("same-attempt first finalizer did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(repository.stopPendingRelease)
	select {
	case stopErr := <-stopResult:
		if !errors.Is(stopErr, errQuestionAnswerTestFinalize) {
			t.Fatalf("same-attempt stop error=%v want first finalizer failure", stopErr)
		}
	case <-time.After(time.Second):
		t.Fatal("same-attempt stop did not return")
	}
	if calls := repository.finalizeCount(); calls != 1 {
		t.Fatalf("same Stop created %d finalizers want=1", calls)
	}
	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batch.BatchID); err != nil {
		t.Fatalf("explicit retry after same-attempt failure: %v", err)
	}
	if calls := repository.finalizeCount(); calls != 2 {
		t.Fatalf("explicit retry finalizers=%d want=2", calls)
	}
}

func TestQuestionAnswerStopCallerDeadlineKeepsBackgroundFinalizerAndHeldSlots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	questions := make([]TestQuestion, 0, 10)
	aQuestionIDs := make([]string, 0, 5)
	bQuestionIDs := make([]string, 0, 5)
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("caller-deadline-q%d", i)
		body := fmt.Sprintf("A-caller-deadline-%d", i)
		if i > 5 {
			body = fmt.Sprintf("B-after-caller-deadline-%d", i-5)
		}
		questions = append(questions, TestQuestion{ID: id, Name: body, Body: body, Enabled: true})
		if i <= 5 {
			aQuestionIDs = append(aQuestionIDs, id)
		} else {
			bQuestionIDs = append(bQuestionIDs, id)
		}
	}
	base := newFakeQuestionAnswerRepository(questions...)
	repository := newControlledFinalizeQuestionAnswerRepository(base, true)
	repository.stopPendingRelease = make(chan struct{})
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	service.questionAnswerStorageTimeout = time.Second
	transport := newCancellationHoldingRoundTripper(10)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() {
		select {
		case <-repository.stopPendingRelease:
		default:
			close(repository.stopPendingRelease)
		}
		repository.releaseFirstFinalize()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.ShutdownQuestionAnswers(ctx)
	}()

	const targetA = "sub2api:ws1:acc-a"
	const targetB = "sub2api:ws1:acc-b"
	batchA, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: aQuestionIDs})
	if err != nil {
		t.Fatalf("start caller-deadline A: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.started:
		case <-time.After(time.Second):
			t.Fatalf("caller-deadline A request %d did not start", i+1)
		}
	}
	batchB, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetB, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: bQuestionIDs})
	if err != nil {
		t.Fatalf("start caller-deadline B: %v", err)
	}

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer callerCancel()
	stopResult := make(chan error, 1)
	go func() {
		_, stopErr := service.StopQuestionAnswerBatch(callerCtx, "user1", targetA, batchA.BatchID)
		stopResult <- stopErr
	}()
	select {
	case <-repository.stopPendingObserved:
	case <-time.After(time.Second):
		t.Fatal("caller-deadline stop did not reach blocked StopPending")
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.cancellationObserved:
			transport.allowCancelled <- struct{}{}
		case <-time.After(time.Second):
			t.Fatalf("caller-deadline A request %d did not exit", i+1)
		}
	}
	select {
	case <-repository.firstFinalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("background finalizer did not start while StopPending was blocked")
	}
	select {
	case stopErr := <-stopResult:
		if !errors.Is(stopErr, context.DeadlineExceeded) {
			t.Fatalf("caller-deadline stop error=%v want deadline", stopErr)
		}
	case <-time.After(time.Second):
		t.Fatal("caller-deadline stop did not return at caller deadline")
	}
	for i := 0; i < 4; i++ {
		select {
		case question := <-transport.started:
			if !strings.HasPrefix(question, "B-after-caller-deadline-") {
				t.Fatalf("unexpected request while four durable slots were available: %s", question)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiting batch used only %d of four safely released slots", i)
		}
	}
	select {
	case question := <-transport.started:
		t.Fatalf("waiting batch stole failed held slot before finalizer settled: %s", question)
	case <-time.After(50 * time.Millisecond):
	}

	repository.releaseFirstFinalize()
	keyA := questionAnswerRunKey("user1", targetA)
	deadline := time.Now().Add(time.Second)
	for {
		service.questionAnswerMu.Lock()
		run := service.questionAnswerRuns[keyA]
		failed := run != nil && !run.finalizing && errors.Is(run.finalErr, errQuestionAnswerTestFinalize)
		held := 0
		global := service.questionAnswerInFlight
		if run != nil {
			held = run.heldSlots
		}
		service.questionAnswerMu.Unlock()
		if failed {
			if held != 1 || global != 5 {
				t.Fatalf("failed background finalizer state held=%d global=%d want=1/5", held, global)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background finalizer did not settle after caller returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(repository.stopPendingRelease)
	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batchA.BatchID); err != nil {
		t.Fatalf("retry caller-deadline finalizer: %v", err)
	}
	select {
	case question := <-transport.started:
		if !strings.HasPrefix(question, "B-after-caller-deadline-") {
			t.Fatalf("unexpected request after caller-deadline retry: %s", question)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting batch did not start after successful retry")
	}
	for i := 0; i < 5; i++ {
		transport.release <- struct{}{}
	}
	waitQuestionAnswerBatchForTarget(t, service, "user1", targetB, batchB.BatchID, false)
}

func TestQuestionAnswerShutdownContinuesAfterCallerReturns(t *testing.T) {
	repository := newFakeQuestionAnswerRepository(TestQuestion{ID: "shutdown-pending-q", Name: "shutdown-pending", Body: "shutdown-pending", Enabled: true})
	records, err := repository.CreateQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", "shutdown-pending-batch", []string{"model-a"}, []string{"shutdown-pending-q"}, QuestionAnswerReasoningEffortMedium, 1)
	if err != nil {
		t.Fatalf("prepare pending shutdown batch: %v", err)
	}
	rootCtx, rootStop := context.WithCancel(context.Background())
	runCtx, runCancel := context.WithCancel(rootCtx)
	run := &activeQuestionAnswerBatch{
		userID: "user1", targetID: "sub2api:ws1:acc-a", batchID: "shutdown-pending-batch",
		ctx: runCtx, cancel: runCancel, records: records,
		done: make(chan struct{}), finalizeSettled: make(chan struct{}),
	}
	dispatcherDone := make(chan struct{})
	service := &Service{
		questionAnswers:   repository,
		questionAnswerCtx: rootCtx, questionAnswerStop: rootStop,
		questionAnswerRuns:  map[string]*activeQuestionAnswerBatch{questionAnswerRunKey(run.userID, run.targetID): run},
		questionAnswerOrder: []string{questionAnswerRunKey(run.userID, run.targetID)},
		questionAnswerWake:  make(chan struct{}, 1), questionAnswerDispatcherDone: dispatcherDone,
		questionAnswerStorageTimeout: 50 * time.Millisecond,
	}
	callerCtx, callerCancel := context.WithCancel(context.Background())
	callerCancel()
	if shutdownErr := service.ShutdownQuestionAnswers(callerCtx); !errors.Is(shutdownErr, context.Canceled) {
		t.Fatalf("early shutdown error=%v want caller cancellation", shutdownErr)
	}
	close(dispatcherDone)
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("background shutdown did not finalize pending run after caller returned")
	}
	service.questionAnswerMu.Lock()
	remaining := len(service.questionAnswerRuns)
	service.questionAnswerMu.Unlock()
	if remaining != 0 {
		t.Fatalf("background shutdown retained %d runs", remaining)
	}
}

var errQuestionAnswerStopPendingOnce = errors.New("test one-time stop-pending failure")

type stopPendingOnceQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository
	mu    sync.Mutex
	calls int
}

func (r *stopPendingOnceQuestionAnswerRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		return true, errQuestionAnswerStopPendingOnce
	}
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func TestQuestionAnswerShutdownNextCallClearsSettledStopPendingFailure(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "shutdown-retry-q", Name: "shutdown-retry", Body: "shutdown-retry", Enabled: true})
	repository := &stopPendingOnceQuestionAnswerRepository{fakeQuestionAnswerRepository: base}
	records, err := repository.CreateQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", "shutdown-retry-batch", []string{"model-a"}, []string{"shutdown-retry-q"}, QuestionAnswerReasoningEffortMedium, 1)
	if err != nil {
		t.Fatalf("prepare shutdown retry batch: %v", err)
	}
	rootCtx, rootStop := context.WithCancel(context.Background())
	runCtx, runCancel := context.WithCancel(rootCtx)
	run := &activeQuestionAnswerBatch{
		userID: "user1", targetID: "sub2api:ws1:acc-a", batchID: "shutdown-retry-batch",
		ctx: runCtx, cancel: runCancel, records: records,
		done: make(chan struct{}), finalizeSettled: make(chan struct{}),
	}
	dispatcherDone := make(chan struct{})
	close(dispatcherDone)
	key := questionAnswerRunKey(run.userID, run.targetID)
	service := &Service{
		questionAnswers:   repository,
		questionAnswerCtx: rootCtx, questionAnswerStop: rootStop,
		questionAnswerRuns:  map[string]*activeQuestionAnswerBatch{key: run},
		questionAnswerOrder: []string{key}, questionAnswerWake: make(chan struct{}, 1),
		questionAnswerDispatcherDone: dispatcherDone, questionAnswerStorageTimeout: time.Second,
	}
	if shutdownErr := service.ShutdownQuestionAnswers(context.Background()); !errors.Is(shutdownErr, errQuestionAnswerStopPendingOnce) {
		t.Fatalf("first shutdown error=%v want one-time StopPending failure", shutdownErr)
	}
	if shutdownErr := service.ShutdownQuestionAnswers(context.Background()); shutdownErr != nil {
		t.Fatalf("second explicit shutdown retained stale error: %v", shutdownErr)
	}
}

var (
	errQuestionAnswerPostCreateCommit   = errors.New("test post-create commit result unknown")
	errQuestionAnswerPostCreateStop     = errors.New("test post-create stop failure")
	errQuestionAnswerPostCreateFinalize = errors.New("test post-create finalize failure")
)

type postCreateShutdownQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository
	created chan struct{}
	release chan struct{}

	mu            sync.Mutex
	createErr     error
	stopCalls     int
	finalizeCalls int
}

func newPostCreateShutdownQuestionAnswerRepository(base *fakeQuestionAnswerRepository) *postCreateShutdownQuestionAnswerRepository {
	return &postCreateShutdownQuestionAnswerRepository{
		fakeQuestionAnswerRepository: base,
		created:                      make(chan struct{}),
		release:                      make(chan struct{}),
	}
}

func (r *postCreateShutdownQuestionAnswerRepository) CreateQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, models []string, questionIDs []string, reasoningEffort QuestionAnswerReasoningEffort, repeatCount int) ([]QuestionAnswerRecord, error) {
	records, err := r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, userID, targetID, batchID, models, questionIDs, reasoningEffort, repeatCount)
	if err != nil {
		return nil, err
	}
	close(r.created)
	<-r.release
	if r.createErr != nil {
		return nil, r.createErr
	}
	return records, nil
}

func (r *postCreateShutdownQuestionAnswerRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.stopCalls++
	call := r.stopCalls
	r.mu.Unlock()
	if call == 1 {
		return true, errQuestionAnswerPostCreateStop
	}
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *postCreateShutdownQuestionAnswerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.finalizeCalls++
	call := r.finalizeCalls
	r.mu.Unlock()
	if call == 1 {
		return true, errQuestionAnswerPostCreateFinalize
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func TestQuestionAnswerPostCreateShutdownFailureRetainsRetryOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "post-create-q", Name: "post-create", Body: "post-create", Enabled: true})
	repository := newPostCreateShutdownQuestionAnswerRepository(base)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	const target = "sub2api:ws1:acc-a"
	startResult := make(chan error, 1)
	go func() {
		_, startErr := service.StartQuestionAnswerBatch(context.Background(), "user1", target, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"post-create-q"}})
		startResult <- startErr
	}()
	select {
	case <-repository.created:
	case <-time.After(time.Second):
		t.Fatal("post-create repository did not commit before shutdown")
	}
	shutdownErr := service.ShutdownQuestionAnswers(context.Background())
	close(repository.release)
	startErr := <-startResult
	if !errors.Is(shutdownErr, errQuestionAnswerPostCreateStop) || !errors.Is(shutdownErr, errQuestionAnswerPostCreateFinalize) {
		t.Fatalf("post-create shutdown error=%v want stop and finalize failures", shutdownErr)
	}
	if !errors.Is(startErr, errQuestionAnswerPostCreateFinalize) || startErr.Error() == ErrorQuestionAnswerServiceStopped {
		t.Fatalf("post-create start error=%v want visible cleanup failure", startErr)
	}
	key := questionAnswerRunKey("user1", target)
	service.questionAnswerMu.Lock()
	run := service.questionAnswerRuns[key]
	service.questionAnswerMu.Unlock()
	if run == nil {
		t.Fatal("post-create cleanup failure lost retry ownership")
	}
	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, run.batchID); err != nil {
		t.Fatalf("post-create explicit cleanup retry: %v", err)
	}
	waitQuestionAnswerRunReleased(t, service)
}

func TestQuestionAnswerCreateErrorAfterPersistCleanupFailureRetainsOwnerForShutdownRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "post-create-error-q", Name: "post-create-error", Body: "post-create-error", Enabled: true})
	repository := newPostCreateShutdownQuestionAnswerRepository(base)
	repository.createErr = &uncertainQuestionAnswerCreateError{cause: errQuestionAnswerPostCreateCommit}
	close(repository.release)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	const target = "sub2api:ws1:acc-a"

	_, startErr := service.StartQuestionAnswerBatch(context.Background(), "user1", target, QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"post-create-error-q"},
	})
	if !errors.Is(startErr, errQuestionAnswerPostCreateCommit) || !errors.Is(startErr, errQuestionAnswerPostCreateStop) || !errors.Is(startErr, errQuestionAnswerPostCreateFinalize) {
		t.Fatalf("post-create error=%v want create, stop, and finalize failures", startErr)
	}
	key := questionAnswerRunKey("user1", target)
	service.questionAnswerMu.Lock()
	run := service.questionAnswerRuns[key]
	service.questionAnswerMu.Unlock()
	if run == nil {
		t.Fatal("post-create error cleanup failure lost retry ownership")
	}
	records, err := base.ListQuestionAnswerBatch(context.Background(), "user1", target, run.batchID)
	if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerPending {
		t.Fatalf("records before explicit retry=%+v err=%v want one pending record", records, err)
	}

	if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
		t.Fatalf("explicit shutdown retry: %v", err)
	}
	waitQuestionAnswerRunReleased(t, service)
	records, err = base.ListQuestionAnswerBatch(context.Background(), "user1", target, run.batchID)
	if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerFailed || records[0].ErrorType != QuestionAnswerErrorStorage {
		t.Fatalf("records after explicit retry=%+v err=%v want storage failure terminal", records, err)
	}
}

func TestQuestionAnswerHeldSlotFinalizerFailureRequiresExplicitRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	questions := make([]TestQuestion, 0, 10)
	aQuestionIDs := make([]string, 0, 5)
	bQuestionIDs := make([]string, 0, 5)
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("held-q%d", i)
		body := fmt.Sprintf("A-held-%d", i)
		if i > 5 {
			body = fmt.Sprintf("B-held-%d", i-5)
		}
		questions = append(questions, TestQuestion{ID: id, Name: body, Body: body, Enabled: true})
		if i <= 5 {
			aQuestionIDs = append(aQuestionIDs, id)
		} else {
			bQuestionIDs = append(bQuestionIDs, id)
		}
	}
	base := newFakeQuestionAnswerRepository(questions...)
	repository := newControlledFinalizeQuestionAnswerRepository(base, true)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	transport := newControlledQuestionAnswerRoundTripper(10)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() {
		repository.releaseFirstFinalize()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.ShutdownQuestionAnswers(ctx)
	}()

	const targetA = "sub2api:ws1:acc-a"
	const targetB = "sub2api:ws1:acc-b"
	batchA, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: aQuestionIDs})
	if err != nil {
		t.Fatalf("start held batch A: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.started:
		case <-time.After(time.Second):
			t.Fatalf("A held request %d did not start", i+1)
		}
	}
	batchB, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetB, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: bQuestionIDs})
	if err != nil {
		t.Fatalf("start held batch B: %v", err)
	}
	transport.release <- struct{}{}
	select {
	case <-repository.firstFinalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("storage failure did not reach the batch finalizer")
	}
	bStarts := make([]string, 0, 4)
	for len(bStarts) < 4 {
		select {
		case question := <-transport.started:
			if strings.HasPrefix(question, "B-held-") {
				bStarts = append(bStarts, question)
			}
		case <-time.After(time.Second):
			t.Fatalf("only %d B requests started while four durable slots were available", len(bStarts))
		}
	}
	select {
	case question := <-transport.started:
		t.Fatalf("held slot was reused before finalization: %s", question)
	case <-time.After(50 * time.Millisecond):
	}
	repository.releaseFirstFinalize()
	keyA := questionAnswerRunKey("user1", targetA)
	deadline := time.Now().Add(time.Second)
	for {
		service.questionAnswerMu.Lock()
		run := service.questionAnswerRuns[keyA]
		ready := run != nil && !run.finalizing && errors.Is(run.finalErr, errQuestionAnswerTestFinalize)
		held := 0
		global := service.questionAnswerInFlight
		if run != nil {
			held = run.heldSlots
		}
		service.questionAnswerMu.Unlock()
		if ready {
			if held != 1 || global != 5 {
				t.Fatalf("failed finalizer state held=%d global=%d want=1/5", held, global)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalizer failure was not retained on the active run")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batchA.BatchID); err != nil {
		t.Fatalf("explicit finalizer retry: %v", err)
	}
	select {
	case question := <-transport.started:
		if !strings.HasPrefix(question, "B-held-") {
			t.Fatalf("unexpected request after held release: %s", question)
		}
	case <-time.After(time.Second):
		t.Fatal("fifth B request did not start after successful finalizer retry")
	}
	for i := 0; i < 5; i++ {
		transport.release <- struct{}{}
	}
	waitQuestionAnswerBatchForTarget(t, service, "user1", targetB, batchB.BatchID, false)
	if repository.finalizeCount() != 2 {
		t.Fatalf("finalizer calls=%d want=2", repository.finalizeCount())
	}
}

func TestQuestionAnswerConcurrentStorageStopsShareSettledFailureThenRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "settled-q1", Name: "settled", Body: "settled", Enabled: true})
	repository := newControlledFinalizeQuestionAnswerRepository(base, false)
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	transport := newCancellationHoldingRoundTripper(1)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() {
		repository.releaseFirstFinalize()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.ShutdownQuestionAnswers(ctx)
	}()
	const targetA = "sub2api:ws1:acc-a"
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"settled-q1"}})
	if err != nil {
		t.Fatalf("start settled batch: %v", err)
	}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("settled request did not start")
	}
	stop := func(result chan<- error) {
		_, stopErr := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batch.BatchID)
		result <- stopErr
	}
	results := make(chan error, 2)
	go stop(results)
	select {
	case <-repository.stopPendingObserved:
	case <-time.After(time.Second):
		t.Fatal("first stop did not reach pending stop")
	}
	go stop(results)
	select {
	case <-repository.stopPendingObserved:
	case <-time.After(time.Second):
		t.Fatal("second stop did not share the active attempt")
	}
	select {
	case <-transport.cancellationObserved:
	case <-time.After(time.Second):
		t.Fatal("settled request did not observe cancellation")
	}
	transport.allowCancelled <- struct{}{}
	select {
	case <-repository.firstFinalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("shared finalizer did not start")
	}
	repository.releaseFirstFinalize()
	for i := 0; i < 2; i++ {
		select {
		case stopErr := <-results:
			if !errors.Is(stopErr, errQuestionAnswerTestFinalize) {
				t.Fatalf("concurrent stop error=%v want shared finalizer error", stopErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("concurrent stop %d did not settle", i+1)
		}
	}
	if repository.finalizeCount() != 1 {
		t.Fatalf("concurrent stops created %d finalizers want=1", repository.finalizeCount())
	}
	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batch.BatchID); err != nil {
		t.Fatalf("third explicit stop retry: %v", err)
	}
	if repository.finalizeCount() != 2 {
		t.Fatalf("retry finalizer calls=%d want=2", repository.finalizeCount())
	}
}

func TestQuestionAnswerFinalizerFailureStorageDeadlineKeepsHeldSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()
	base := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "deadline-a", Name: "deadline-a", Body: "deadline-a", Enabled: true},
	)
	repository := newControlledFinalizeQuestionAnswerRepository(base, true)
	repository.alwaysWaitForDeadline = true
	service := newMultiTargetQuestionAnswerService(server.URL, repository, newFakeRepository())
	service.questionAnswerStorageTimeout = 50 * time.Millisecond
	transport := newControlledQuestionAnswerRoundTripper(1)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	const targetA = "sub2api:ws1:acc-a"
	batchA, err := service.StartQuestionAnswerBatch(context.Background(), "user1", targetA, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"deadline-a"}})
	if err != nil {
		t.Fatalf("start deadline A: %v", err)
	}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("deadline A did not start")
	}
	transport.release <- struct{}{}
	select {
	case <-repository.firstFinalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("deadline finalizer did not start")
	}
	startedAt := time.Now()
	_, stopErr := service.StopQuestionAnswerBatch(context.Background(), "user1", targetA, batchA.BatchID)
	if !errors.Is(stopErr, context.DeadlineExceeded) {
		t.Fatalf("deadline stop error=%v want context deadline", stopErr)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline stop took %s want bounded return", elapsed)
	}
	service.questionAnswerMu.Lock()
	run := service.questionAnswerRuns[questionAnswerRunKey("user1", targetA)]
	held, global := 0, service.questionAnswerInFlight
	if run != nil {
		held = run.heldSlots
	}
	service.questionAnswerMu.Unlock()
	if run == nil || held != 1 || global != 1 {
		t.Fatalf("deadline finalizer released protected state run=%v held=%d global=%d", run != nil, held, global)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if shutdownErr := service.ShutdownQuestionAnswers(shutdownCtx); !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("shutdown finalizer deadline error=%v", shutdownErr)
	}
}

func TestQuestionAnswerBatchRunsFiveCombinationsAndRefillsOneOpenSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	questions := make([]TestQuestion, 0, 6)
	questionIDs := make([]string, 0, 6)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("q%d", i)
		questionIDs = append(questionIDs, id)
		questions = append(questions, TestQuestion{ID: id, Name: id, Body: "question " + id, Enabled: true})
	}
	qaRepo := newFakeQuestionAnswerRepository(questions...)
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	transport := newControlledQuestionAnswerRoundTripper(len(questionIDs))
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: questionIDs,
	})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.started:
		case <-time.After(time.Second):
			t.Fatalf("request %d did not start before the five-request concurrency window", i+1)
		}
	}
	select {
	case question := <-transport.started:
		t.Fatalf("sixth request %q started before a concurrency slot opened", question)
	case <-time.After(50 * time.Millisecond):
	}

	transport.release <- struct{}{}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("sixth request did not start after a concurrency slot opened")
	}
	if peak := transport.peak(); peak != 5 {
		t.Fatalf("peak concurrent requests = %d, want 5", peak)
	}
	for i := 0; i < 5; i++ {
		transport.release <- struct{}{}
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	for _, record := range completed.Records {
		if record.Status != QuestionAnswerSucceeded || record.AnswerBody != "answer" {
			t.Fatalf("completed record = %+v", record)
		}
	}
}

func TestQuestionAnswerCancelStopsFiveInFlightRequestsAndOneWaitingRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	questions := make([]TestQuestion, 0, 6)
	questionIDs := make([]string, 0, 6)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("q%d", i)
		questionIDs = append(questionIDs, id)
		questions = append(questions, TestQuestion{ID: id, Name: id, Body: "question " + id, Enabled: true})
	}
	qaRepo := newFakeQuestionAnswerRepository(questions...)
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	transport := newControlledQuestionAnswerRoundTripper(len(questionIDs))
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: questionIDs,
	})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.started:
		case <-time.After(time.Second):
			t.Fatalf("in-flight request %d did not start", i+1)
		}
	}
	stopped, err := service.StopQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batch.BatchID)
	if err != nil {
		t.Fatalf("stop batch: %v", err)
	}
	if stopped.Active || stopped.RunningCount != 0 {
		t.Fatalf("stopped batch remains active: %+v", stopped)
	}
	for _, record := range stopped.Records {
		if record.Status != QuestionAnswerCancelled {
			t.Fatalf("record after cancellation = %+v", record)
		}
	}
	for i := 0; i < 5; i++ {
		select {
		case <-transport.cancelled:
		case <-time.After(time.Second):
			t.Fatalf("in-flight request %d did not observe cancellation", i+1)
		}
	}
	select {
	case question := <-transport.started:
		t.Fatalf("waiting request %q started during cancellation", question)
	default:
	}
	waitQuestionAnswerRunReleased(t, service)
}

type cancellingMarkQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository

	markStarted chan struct{}
	markOnce    sync.Once
	mu          sync.Mutex
	stopCalls   []struct {
		status    QuestionAnswerStatus
		errorType string
	}
}

func newCancellingMarkQuestionAnswerRepository(base *fakeQuestionAnswerRepository) *cancellingMarkQuestionAnswerRepository {
	return &cancellingMarkQuestionAnswerRepository{
		fakeQuestionAnswerRepository: base,
		markStarted:                  make(chan struct{}),
	}
}

func (r *cancellingMarkQuestionAnswerRepository) MarkQuestionAnswerRunning(ctx context.Context, _ string, _ string, _ string) (bool, error) {
	r.markOnce.Do(func() { close(r.markStarted) })
	<-ctx.Done()
	return false, ctx.Err()
}

func (r *cancellingMarkQuestionAnswerRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.stopCalls = append(r.stopCalls, struct {
		status    QuestionAnswerStatus
		errorType string
	}{status: status, errorType: errorType})
	r.mu.Unlock()
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *cancellingMarkQuestionAnswerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.stopCalls = append(r.stopCalls, struct {
		status    QuestionAnswerStatus
		errorType string
	}{status: status, errorType: errorType})
	r.mu.Unlock()
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *cancellingMarkQuestionAnswerRepository) stops() []struct {
	status    QuestionAnswerStatus
	errorType string
} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]struct {
		status    QuestionAnswerStatus
		errorType string
	}(nil), r.stopCalls...)
}

func TestQuestionAnswerCancelDuringMarkRunningIsNotReportedAsStorageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "q1", Body: "question 1", Enabled: true})
	repository := newCancellingMarkQuestionAnswerRepository(base)
	service := newQuestionAnswerService(server.URL, base, newFakeRepository())
	service.questionAnswers = repository
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"q1"},
	})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	select {
	case <-repository.markStarted:
	case <-time.After(time.Second):
		t.Fatal("mark-running operation did not start")
	}
	stopped, err := service.StopQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", batch.BatchID)
	if err != nil {
		t.Fatalf("stop batch: %v", err)
	}
	waitQuestionAnswerRunReleased(t, service)

	if stopped.Active || len(stopped.Records) != 1 || stopped.Records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("stopped batch = %+v", stopped)
	}
	stopCalls := repository.stops()
	if len(stopCalls) != 2 {
		t.Fatalf("stop calls = %+v, want pending stop and finalizer", stopCalls)
	}
	for _, call := range stopCalls {
		if call.status != QuestionAnswerCancelled || call.errorType != "" {
			t.Fatalf("stop calls = %+v, want cancellation without a storage failure", stopCalls)
		}
	}
}

type concurrentCompletionFailureRepository struct {
	*fakeQuestionAnswerRepository

	mu            sync.Mutex
	completeCalls int
	stopCalls     int
	completes     chan struct{}
}

func newConcurrentCompletionFailureRepository(base *fakeQuestionAnswerRepository) *concurrentCompletionFailureRepository {
	return &concurrentCompletionFailureRepository{fakeQuestionAnswerRepository: base, completes: make(chan struct{})}
}

func (r *concurrentCompletionFailureRepository) CompleteQuestionAnswer(context.Context, string, string, string, QuestionAnswerStatus, string, string) (bool, error) {
	r.mu.Lock()
	r.completeCalls++
	if r.completeCalls == 2 {
		close(r.completes)
	}
	r.mu.Unlock()
	<-r.completes
	return false, errors.New("complete storage failure")
}

func (r *concurrentCompletionFailureRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	r.mu.Lock()
	r.stopCalls++
	r.mu.Unlock()
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *concurrentCompletionFailureRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID string, targetID string, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

func (r *concurrentCompletionFailureRepository) stopCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopCalls
}

func TestQuestionAnswerConcurrentStorageFailuresStopBatchOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
		case "/v1/chat/completions":
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	base := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "q1", Body: "question 1", Enabled: true},
		TestQuestion{ID: "q2", Name: "q2", Body: "question 2", Enabled: true},
	)
	repository := newConcurrentCompletionFailureRepository(base)
	service := newQuestionAnswerService(server.URL, base, newFakeRepository())
	service.questionAnswers = repository
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"q1", "q2"},
	})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if calls := repository.stopCount(); calls != 1 {
		t.Fatalf("batch stop calls = %d, want 1 after concurrent storage failures", calls)
	}
	for _, record := range completed.Records {
		if record.Status != QuestionAnswerFailed || record.ErrorType != QuestionAnswerErrorStorage {
			t.Fatalf("record after storage failure = %+v", record)
		}
	}
}

func TestQuestionAnswerBatchRunsEveryDeduplicatedCombinationWithoutMaxTokens(t *testing.T) {
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
			requests = append(requests, model+"|"+question+"|"+payload["reasoning_effort"].(string))
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
		Models: []string{"model-a", "model-a", "model-b"}, QuestionIDs: []string{"q1", "q1", "q2"}, ReasoningEffort: "high",
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
	wantRequests := []string{"model-a|question one|high", "model-a|question two|high", "model-b|question one|high", "model-b|question two|high"}
	sort.Strings(gotRequests)
	sort.Strings(wantRequests)
	if fmt.Sprint(gotRequests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests = %v, want %v", gotRequests, wantRequests)
	}
	gotRecords := make([]string, 0, len(completed.Records))
	for _, record := range completed.Records {
		gotRecords = append(gotRecords, record.ModelName+"|"+record.QuestionBody)
	}
	wantRecords := []string{"model-a|question one", "model-a|question two", "model-b|question one", "model-b|question two"}
	if fmt.Sprint(gotRecords) != fmt.Sprint(wantRecords) {
		t.Fatalf("record order = %v, want %v", gotRecords, wantRecords)
	}
	if batch.ReasoningEffort == nil || *batch.ReasoningEffort != QuestionAnswerReasoningEffortHigh || completed.ReasoningEffort == nil || *completed.ReasoningEffort != QuestionAnswerReasoningEffortHigh {
		t.Fatalf("reasoning effort snapshot = start=%v completed=%v", batch.ReasoningEffort, completed.ReasoningEffort)
	}
	if len(healthRepo.states) != 0 || len(healthRepo.events) != 0 || len(healthRepo.budgetClaims) != 0 {
		t.Fatalf("question answers touched health state: states=%v events=%v budget=%v", healthRepo.states, healthRepo.events, healthRepo.budgetClaims)
	}
}

func TestQuestionAnswerReasoningEffortDefaultsAndRejectsBeforeDiscovery(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode default reasoning request: %v", err)
		}
		if payload["reasoning_effort"] != string(QuestionAnswerReasoningEffortMedium) {
			t.Fatalf("default reasoning effort payload = %#v", payload["reasoning_effort"])
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	_, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"q1"}, ReasoningEffort: "unsupported",
	})
	if err == nil || err.Error() != ErrorQuestionAnswerReasoningEffort {
		t.Fatalf("invalid reasoning effort error = %v, want %s", err, ErrorQuestionAnswerReasoningEffort)
	}
	if requests != 0 || len(qaRepo.records) != 0 {
		t.Fatalf("invalid reasoning effort touched upstream or repository: requests=%d records=%d", requests, len(qaRepo.records))
	}

	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"q1"},
	})
	if err != nil {
		t.Fatalf("default reasoning effort start: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if completed.ReasoningEffort == nil || *completed.ReasoningEffort != QuestionAnswerReasoningEffortMedium || completed.Records[0].ReasoningEffort == nil || *completed.Records[0].ReasoningEffort != QuestionAnswerReasoningEffortMedium {
		t.Fatalf("default reasoning effort snapshot = batch=%v record=%v", completed.ReasoningEffort, completed.Records[0].ReasoningEffort)
	}
}

func TestQuestionAnswerUnsupportedReasoningEffortFailsOnceWithoutDowngrade(t *testing.T) {
	var chatRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
		case "/v1/chat/completions":
			chatRequests++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if payload["reasoning_effort"] != string(QuestionAnswerReasoningEffortXHigh) {
				t.Fatalf("reasoning effort payload = %#v", payload["reasoning_effort"])
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"reasoning_effort is unsupported"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{
		Models: []string{"model-a"}, QuestionIDs: []string{"q1"}, ReasoningEffort: string(QuestionAnswerReasoningEffortXHigh),
	})
	if err != nil {
		t.Fatalf("unsupported reasoning effort start: %v", err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	if chatRequests != 1 {
		t.Fatalf("chat request count = %d, want 1", chatRequests)
	}
	if completed.Records[0].Status != QuestionAnswerFailed || completed.Records[0].ErrorType != QuestionAnswerErrorInvalidResponse {
		t.Fatalf("unsupported reasoning effort record = %+v", completed.Records[0])
	}
}

func TestBuildQuestionAnswerBatchReasoningEffortAggregation(t *testing.T) {
	if batch, err := buildQuestionAnswerBatch(nil); err != nil || batch.ReasoningEffort != nil {
		t.Fatalf("empty batch aggregation = %+v err=%v", batch, err)
	}
	nilRecord := QuestionAnswerRecord{BatchID: "batch-old"}
	if batch, err := buildQuestionAnswerBatch([]QuestionAnswerRecord{nilRecord}); err != nil || batch.ReasoningEffort != nil {
		t.Fatalf("old batch aggregation = %+v err=%v", batch, err)
	}
	medium := QuestionAnswerReasoningEffortMedium
	if batch, err := buildQuestionAnswerBatch([]QuestionAnswerRecord{{BatchID: "batch-new", ReasoningEffort: &medium}}); err != nil || batch.ReasoningEffort == nil || *batch.ReasoningEffort != medium {
		t.Fatalf("consistent batch aggregation = %+v err=%v", batch, err)
	}
	high := QuestionAnswerReasoningEffortHigh
	if _, err := buildQuestionAnswerBatch([]QuestionAnswerRecord{{BatchID: "batch-mixed", ReasoningEffort: &medium}, {BatchID: "batch-mixed", ReasoningEffort: &high}}); err == nil || err.Error() != ErrorQuestionAnswerStorage {
		t.Fatalf("mixed batch aggregation error = %v, want %s", err, ErrorQuestionAnswerStorage)
	}
	if _, err := buildQuestionAnswerBatch([]QuestionAnswerRecord{{BatchID: "batch-mixed-null", ReasoningEffort: nil}, {BatchID: "batch-mixed-null", ReasoningEffort: &medium}}); err == nil || err.Error() != ErrorQuestionAnswerStorage {
		t.Fatalf("null and non-null batch aggregation error = %v, want %s", err, ErrorQuestionAnswerStorage)
	}
}

func TestQuestionAnswerBatchRepeatSummaryAndModelStats(t *testing.T) {
	records := []QuestionAnswerRecord{
		{ID: "b-q1-1", BatchID: "batch-repeat-summary", ModelName: "model-b", QuestionID: "q1", Status: QuestionAnswerPending},
		{ID: "b-q1-2", BatchID: "batch-repeat-summary", ModelName: "model-b", QuestionID: "q1", Status: QuestionAnswerRunning},
		{ID: "b-q2-1", BatchID: "batch-repeat-summary", ModelName: "model-b", QuestionID: "q2", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerCorrect)},
		{ID: "b-q2-2", BatchID: "batch-repeat-summary", ModelName: "model-b", QuestionID: "q2", Status: QuestionAnswerFailed},
		{ID: "a-q1-1", BatchID: "batch-repeat-summary", ModelName: "model-a", QuestionID: "q1", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerUnreviewed)},
		{ID: "a-q1-2", BatchID: "batch-repeat-summary", ModelName: "model-a", QuestionID: "q1", Status: QuestionAnswerCancelled},
		{ID: "a-q2-1", BatchID: "batch-repeat-summary", ModelName: "model-a", QuestionID: "q2", Status: QuestionAnswerFailed},
		{ID: "a-q2-2", BatchID: "batch-repeat-summary", ModelName: "model-a", QuestionID: "q2", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerIncorrect)},
	}
	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		t.Fatalf("build repeat batch: %v", err)
	}
	if batch.RepeatCount != 2 || batch.SubmittedCount != 8 {
		t.Fatalf("repeat summary repeat=%d submitted=%d want=2/8", batch.RepeatCount, batch.SubmittedCount)
	}
	gotRecordIDs := make([]string, 0, len(batch.Records))
	for _, record := range batch.Records {
		gotRecordIDs = append(gotRecordIDs, record.ID)
	}
	wantRecordIDs := []string{"b-q1-1", "b-q1-2", "b-q2-1", "b-q2-2", "a-q1-1", "a-q1-2", "a-q2-1", "a-q2-2"}
	if !reflect.DeepEqual(gotRecordIDs, wantRecordIDs) {
		t.Fatalf("record order=%v want=%v", gotRecordIDs, wantRecordIDs)
	}
	wantTotal := QuestionAnswerStats{
		Requests: QuestionAnswerRequestStats{Submitted: 8, InProgress: 2, Succeeded: 3, Failed: 2, Cancelled: 1},
		Reviews:  QuestionAnswerReviewStats{Unreviewed: 1, Correct: 1, Incorrect: 1},
		ByModel: []QuestionAnswerModelStats{
			{ModelName: "model-b", Requests: QuestionAnswerRequestStats{Submitted: 4, InProgress: 2, Succeeded: 1, Failed: 1}, Reviews: QuestionAnswerReviewStats{Correct: 1}},
			{ModelName: "model-a", Requests: QuestionAnswerRequestStats{Submitted: 4, Succeeded: 2, Failed: 1, Cancelled: 1}, Reviews: QuestionAnswerReviewStats{Unreviewed: 1, Incorrect: 1}},
		},
	}
	if !reflect.DeepEqual(batch.Stats, wantTotal) {
		t.Fatalf("batch stats=%+v want=%+v", batch.Stats, wantTotal)
	}

	empty, err := buildQuestionAnswerBatch(nil)
	if err != nil || empty.RepeatCount != 1 || empty.Stats.ByModel == nil || len(empty.Stats.ByModel) != 0 {
		t.Fatalf("empty batch=%+v err=%v want repeat=1 byModel=[]", empty, err)
	}
	nonRectangular := []QuestionAnswerRecord{
		{ModelName: "model-a", QuestionID: "q1"}, {ModelName: "model-a", QuestionID: "q1"}, {ModelName: "model-a", QuestionID: "q1"},
		{ModelName: "model-a", QuestionID: "q2"},
		{ModelName: "model-b", QuestionID: "q1"}, {ModelName: "model-b", QuestionID: "q1"},
		{ModelName: "model-b", QuestionID: "q2"}, {ModelName: "model-b", QuestionID: "q2"},
	}
	if _, err := buildQuestionAnswerBatch(nonRectangular); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
		t.Fatalf("non-rectangular repeat summary error=%v want=%s", err, ErrorQuestionAnswerStorage)
	}
	elevenSamples := make([]QuestionAnswerRecord, 11)
	for i := range elevenSamples {
		elevenSamples[i] = QuestionAnswerRecord{ModelName: "model-a", QuestionID: "q1"}
	}
	if _, err := buildQuestionAnswerBatch(elevenSamples); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
		t.Fatalf("eleven-sample repeat summary error=%v want=%s", err, ErrorQuestionAnswerStorage)
	}
}

func TestBuildQuestionAnswerBatchReportsActualRunningCount(t *testing.T) {
	records := []QuestionAnswerRecord{
		{BatchID: "batch-running", Status: QuestionAnswerRunning},
		{BatchID: "batch-running", Status: QuestionAnswerPending},
		{BatchID: "batch-running", Status: QuestionAnswerRunning},
		{BatchID: "batch-running", Status: QuestionAnswerSucceeded},
	}
	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		t.Fatalf("build batch: %v", err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode batch JSON: %v", err)
	}
	if payload["runningCount"] != float64(2) {
		t.Fatalf("runningCount = %#v, want 2", payload["runningCount"])
	}
}

func TestQuestionAnswerBatchSeparatesRequestAndReviewStats(t *testing.T) {
	var records []QuestionAnswerRecord
	if err := json.Unmarshal([]byte(`[
		{"id":"pending","batchId":"batch-stats","status":"pending","answerJudgment":null},
		{"id":"running","batchId":"batch-stats","status":"running","answerJudgment":null},
		{"id":"unreviewed","batchId":"batch-stats","status":"succeeded","answerJudgment":"unreviewed","manualError":false},
		{"id":"correct","batchId":"batch-stats","status":"succeeded","answerJudgment":"correct","manualError":false},
		{"id":"incorrect","batchId":"batch-stats","status":"succeeded","answerJudgment":"incorrect","manualError":true},
		{"id":"failed","batchId":"batch-stats","status":"failed","answerJudgment":null},
		{"id":"cancelled","batchId":"batch-stats","status":"cancelled","answerJudgment":null}
	]`), &records); err != nil {
		t.Fatalf("decode hand-checked records: %v", err)
	}

	batch, err := buildQuestionAnswerBatch(records)
	if err != nil {
		t.Fatalf("build batch: %v", err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	var payload struct {
		Stats struct {
			Requests struct {
				Submitted  int `json:"submitted"`
				InProgress int `json:"inProgress"`
				Succeeded  int `json:"succeeded"`
				Failed     int `json:"failed"`
				Cancelled  int `json:"cancelled"`
			} `json:"requests"`
			Reviews struct {
				Unreviewed int `json:"unreviewed"`
				Correct    int `json:"correct"`
				Incorrect  int `json:"incorrect"`
			} `json:"reviews"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode batch contract: %v", err)
	}
	requests := payload.Stats.Requests
	if requests.Submitted != 7 || requests.InProgress != 2 || requests.Succeeded != 3 || requests.Failed != 1 || requests.Cancelled != 1 {
		t.Fatalf("request stats = %+v, want submitted=7 inProgress=2 succeeded=3 failed=1 cancelled=1", requests)
	}
	reviews := payload.Stats.Reviews
	if reviews.Unreviewed != 1 || reviews.Correct != 1 || reviews.Incorrect != 1 {
		t.Fatalf("review stats = %+v, want unreviewed=1 correct=1 incorrect=1", reviews)
	}
	if requests.Submitted != requests.InProgress+requests.Succeeded+requests.Failed+requests.Cancelled {
		t.Fatalf("request stats do not reconcile: %+v", requests)
	}
	if requests.Succeeded != reviews.Unreviewed+reviews.Correct+reviews.Incorrect {
		t.Fatalf("review stats do not reconcile: requests=%+v reviews=%+v", requests, reviews)
	}
}

func TestQuestionAnswerReasoningEffortErrorHTTPContracts(t *testing.T) {
	badRequest := httptest.NewRecorder()
	writeError(badRequest, requestError(ErrorQuestionAnswerReasoningEffort))
	if badRequest.Code != http.StatusBadRequest || !strings.Contains(badRequest.Body.String(), ErrorQuestionAnswerReasoningEffort) {
		t.Fatalf("invalid effort response status=%d body=%s", badRequest.Code, badRequest.Body.String())
	}
	storageError := httptest.NewRecorder()
	writeError(storageError, requestError(ErrorQuestionAnswerStorage))
	if storageError.Code != http.StatusInternalServerError || !strings.Contains(storageError.Body.String(), ErrorQuestionAnswerStorage) {
		t.Fatalf("storage response status=%d body=%s", storageError.Code, storageError.Body.String())
	}
}

func TestQuestionAnswerBatchAggregationFailureReleasesActiveRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "question", Enabled: true})
	service := newQuestionAnswerService(server.URL, base, newFakeRepository())
	service.questionAnswers = &inconsistentQuestionAnswerRepository{fakeQuestionAnswerRepository: base}
	defer func() { _ = service.ShutdownQuestionAnswers(context.Background()) }()

	_, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1"}})
	if err == nil || err.Error() != ErrorQuestionAnswerStorage {
		t.Fatalf("mixed snapshot start error = %v, want %s", err, ErrorQuestionAnswerStorage)
	}
	service.questionAnswerMu.Lock()
	activeRuns := len(service.questionAnswerRuns)
	service.questionAnswerMu.Unlock()
	if activeRuns != 0 {
		t.Fatalf("active runs after aggregation failure = %d, want 0", activeRuns)
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
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: &contextBlockingRoundTripper{started: started, cancelled: cancelled}}}
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

func TestQuestionAnswerSlowTimeoutDoesNotBlockFastCombinationAndShutdownReleasesRun(t *testing.T) {
	fastCompleted := make(chan struct{})
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
		close(fastCompleted)
	}))
	defer server.Close()

	qaRepo := newFakeQuestionAnswerRepository(
		TestQuestion{ID: "q1", Name: "slow", Body: "slow question", Enabled: true},
		TestQuestion{ID: "q2", Name: "fast", Body: "fast question", Enabled: true},
	)
	service := newQuestionAnswerService(server.URL, qaRepo, newFakeRepository())
	service.questionAnswerTTL = 250 * time.Millisecond
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q1", "q2"}})
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	select {
	case <-fastCompleted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("fast combination was blocked behind the slow request")
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
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: &contextBlockingRoundTripper{started: started, cancelled: cancelled}}}
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
	started       chan struct{}
	cancelled     chan struct{}
	startedOnce   sync.Once
	cancelledOnce sync.Once
}

func (r *contextBlockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.startedOnce.Do(func() { close(r.started) })
	<-request.Context().Done()
	r.cancelledOnce.Do(func() { close(r.cancelled) })
	return nil, request.Context().Err()
}

func TestQuestionAnswerRunnerClosesResponseBodyAndDiscardsRawPayload(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(`{"choices":[{"message":{"content":"answer"}}],"usage":{"secret":"raw-marker"}}`)}
	runner := &QuestionAnswerRunner{client: &http.Client{Transport: fixedRoundTripper{body: body}}}
	answer, errorType := runner.Ask(context.Background(), upstream.ProbeCredential{BaseURL: "https://example.test", Key: "secret-key"}, "model-a", "question", QuestionAnswerReasoningEffortMedium)
	if answer != "answer" || errorType != "" {
		t.Fatalf("answer=%q errorType=%q", answer, errorType)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}
