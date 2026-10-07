package connection_health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func TestQuestionAnswerC1StructuredVersionStatsAndStableNames(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	records := []QuestionAnswerRecord{
		{ID: "old", QuestionID: "q1", QuestionName: "Old name", QuestionBody: "Body", QuestionKeywordSnapshot: []string{"B", "a"}, ModelName: "z", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerCorrect), ManualError: true, CreatedAt: now},
		{ID: "z-latest", QuestionID: "q1", QuestionName: "Latest name", QuestionBody: "Body", QuestionKeywordSnapshot: []string{"A", "b", "a", ""}, ModelName: "a", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerIncorrect), ManualError: false, CreatedAt: now.Add(time.Second)},
		{ID: "a-latest", QuestionID: "q1", QuestionName: "Tie loses", QuestionBody: "Body", QuestionKeywordSnapshot: []string{"a", "B"}, ModelName: "z", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerUnreviewed), CreatedAt: now.Add(time.Second)},
		{ID: "body-version", QuestionID: "q1", QuestionName: "Body version", QuestionBody: "Changed", QuestionKeywordSnapshot: []string{"a", "b"}, ModelName: "a", Status: QuestionAnswerFailed, CreatedAt: now},
		{ID: "keyword-version", QuestionID: "q1", QuestionName: "Keyword version", QuestionBody: "Body", QuestionKeywordSnapshot: []string{"a", "c"}, ModelName: "z", Status: QuestionAnswerCancelled, CreatedAt: now},
		{ID: "different-id", QuestionID: "q2", QuestionName: "Other", QuestionBody: "Body", QuestionKeywordSnapshot: []string{"a", "b"}, ModelName: "a", Status: QuestionAnswerRunning, CreatedAt: now},
	}
	stats := aggregateQuestionAnswerStats(records)
	if stats.Requests != (QuestionAnswerRequestStats{Submitted: 6, InProgress: 1, Succeeded: 3, Failed: 1, Cancelled: 1}) || stats.Reviews != (QuestionAnswerReviewStats{Correct: 1, Incorrect: 1, Unreviewed: 1}) {
		t.Fatalf("account stats=%+v", stats)
	}
	if len(stats.ByQuestion) != 4 || len(stats.ByModel) != 2 || stats.ByModel[0].ModelName != "a" || stats.ByModel[1].ModelName != "z" {
		t.Fatalf("dimensions=%+v", stats)
	}
	canonical := `{"questionId":"q1","questionBody":"Body","normalizedKeywords":["a","b"]}`
	hash := sha256.Sum256([]byte(canonical))
	key := hex.EncodeToString(hash[:])
	found := false
	var requests QuestionAnswerRequestStats
	var reviews QuestionAnswerReviewStats
	for i, q := range stats.ByQuestion {
		if i > 0 {
			previous := stats.ByQuestion[i-1]
			if previous.QuestionID > q.QuestionID || (previous.QuestionID == q.QuestionID && previous.QuestionSnapshotKey > q.QuestionSnapshotKey) {
				t.Fatal("question order unstable")
			}
		}
		addQuestionAnswerCounts(&requests, &reviews, q.Requests, q.Reviews)
		var modelRequests QuestionAnswerRequestStats
		var modelReviews QuestionAnswerReviewStats
		for _, model := range q.ByModel {
			addQuestionAnswerCounts(&modelRequests, &modelReviews, model.Requests, model.Reviews)
		}
		if modelRequests != q.Requests || modelReviews != q.Reviews {
			t.Fatal("question/model counts do not reconcile")
		}
		if q.QuestionSnapshotKey == key {
			found = true
			if q.DisplayQuestionName != "Latest name" || q.QuestionBody != "Body" || q.QuestionID != "q1" || !reflect.DeepEqual(q.NormalizedKeywords, []string{"a", "b"}) || q.Requests.Submitted != 3 || q.Reviews.Correct != 1 || q.Reviews.Incorrect != 1 || q.Reviews.Unreviewed != 1 {
				t.Fatalf("canonical question=%+v", q)
			}
		}
	}
	if !found || requests != stats.Requests || reviews != stats.Reviews {
		t.Fatal("version identity or conservation failed")
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	if !reflect.DeepEqual(stats, aggregateQuestionAnswerStats(records)) {
		t.Fatal("record order changed aggregate or latest label")
	}
}

func TestQuestionAnswerC1RepeatIndexMatrixAndSnapshotErrors(t *testing.T) {
	for _, repeat := range []int{1, 3, 5, 10} {
		t.Run(strconv.Itoa(repeat), func(t *testing.T) {
			records := []QuestionAnswerRecord{}
			for _, model := range []string{"model-b", "model-a"} {
				for _, question := range []string{"q1", "q2"} {
					for index := 1; index <= repeat; index++ {
						value := index
						records = append(records, QuestionAnswerRecord{ID: model + question + string(rune('a'+index)), BatchID: "batch", TargetID: "target", ModelName: model, QuestionID: question, QuestionBody: question, RepeatIndex: &value, Status: QuestionAnswerPending})
					}
				}
			}
			batch, err := buildQuestionAnswerBatch(records)
			if err != nil || batch.RepeatCount != repeat || batch.Stats.Requests.Submitted != 4*repeat || len(batch.Records) != 4*repeat {
				t.Fatalf("repeat=%d batch=%+v err=%v", repeat, batch, err)
			}
			duplicate := cloneQuestionAnswerRecords(records)
			duplicate[0].RepeatIndex = duplicate[1].RepeatIndex
			if repeat > 1 {
				if _, err := buildQuestionAnswerBatch(duplicate); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
					t.Fatal("duplicate repeat index accepted")
				}
			}
			mixed := cloneQuestionAnswerRecords(records)
			mixed[0].RepeatIndex = nil
			if _, err := buildQuestionAnswerBatch(mixed); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
				t.Fatal("mixed historical/new sequence accepted")
			}
			contradictory := cloneQuestionAnswerRecords(records)
			contradictory[0].QuestionBody = "other snapshot"
			if _, err := buildQuestionAnswerBatch(contradictory); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
				t.Fatal("same question id with contradictory snapshots accepted")
			}
			for i := range records {
				records[i].RepeatIndex = nil
			}
			legacy, err := buildQuestionAnswerBatch(records)
			if err != nil || legacy.RepeatCount != repeat {
				t.Fatal("historical sample count was lost")
			}
		})
	}
}

func TestQuestionAnswerC1BatchSummaryTimesAndNoAnswers(t *testing.T) {
	created := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	started := created.Add(time.Second)
	completed := created.Add(3 * time.Second)
	late := created.Add(4 * time.Second)
	records := []QuestionAnswerRecord{
		{ID: "a", BatchID: "batch", ModelName: "model", QuestionID: "q", Status: QuestionAnswerSucceeded, AnswerBody: "private long answer", CreatedAt: created, StartedAt: &started, CompletedAt: &completed},
		{ID: "b", BatchID: "batch", ModelName: "model", QuestionID: "q", Status: QuestionAnswerRunning, CreatedAt: created.Add(time.Second), CompletedAt: &late},
	}
	summary, err := buildQuestionAnswerBatchSummary(records)
	if err != nil || !summary.Active || summary.CompletedAt != nil || summary.StartedAt == nil || !summary.StartedAt.Equal(started) || !summary.CreatedAt.Equal(created) || summary.RepeatCount != 2 {
		t.Fatalf("active summary=%+v err=%v", summary, err)
	}
	records[1].Status = QuestionAnswerCancelled
	summary, err = buildQuestionAnswerBatchSummary(records)
	if err != nil || summary.Active || summary.CompletedAt == nil || !summary.CompletedAt.Equal(late) {
		t.Fatalf("terminal summary=%+v err=%v", summary, err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "answerBody") || strings.Contains(string(encoded), "private long answer") {
		t.Fatal("history summary leaked answer bodies")
	}
}

type questionAnswerLocalSummaryRepository struct {
	*fakeQuestionAnswerRepository
	calls int
	stats QuestionAnswerSummaryStats
	err   error
}

func (r *questionAnswerLocalSummaryRepository) GetQuestionAnswerTodayStats(context.Context, string, string) (QuestionAnswerSummaryStats, error) {
	r.calls++
	return r.stats, r.err
}

func TestQuestionAnswerC1SummaryHandlerIsLocalAndMinimal(t *testing.T) {
	repo := &questionAnswerLocalSummaryRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(), stats: QuestionAnswerSummaryStats{Requests: QuestionAnswerRequestStats{Submitted: 6, InProgress: 1, Succeeded: 3, Failed: 1, Cancelled: 1}, Reviews: QuestionAnswerReviewStats{Correct: 1, Incorrect: 1, Unreviewed: 1}}}
	priority := &fakeTargetPriorityActioner{}
	service := &Service{questionAnswers: repo, accounts: fakeAdminAccountResolver{id: "ws1"}, priorityActions: priority}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := func(path, contract string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(questionAnswerContractHeader, contract)
		if authenticated {
			req = req.WithContext(authctx.WithUserID(req.Context(), "user"))
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	path := "/api/connection-health/targets/sub2api:ws1:missing-account/question-answers/summary"
	response := request(path, "3", true)
	if response.Code != 200 {
		t.Fatalf("summary=%d %s", response.Code, response.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["targetId"] == nil || payload["todayStats"] == nil {
		t.Fatalf("summary fields=%v", payload)
	}
	var today map[string]json.RawMessage
	if err := json.Unmarshal(payload["todayStats"], &today); err != nil {
		t.Fatal(err)
	}
	if len(today) != 2 || today["requests"] == nil || today["reviews"] == nil {
		t.Fatal("summary returned aggregate dimensions")
	}
	repo.stats = QuestionAnswerSummaryStats{}
	response = request(path, "3", true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"submitted":0`) {
		t.Fatal("missing local records did not return zero counts")
	}
	before := repo.calls
	for _, tc := range []struct {
		path, contract string
		authenticated  bool
		status         int
	}{
		{path, "2", true, 409}, {path, "3", false, 401},
		{"/api/connection-health/targets/sub2api:foreign:account/question-answers/summary", "3", true, 400},
		{"/api/connection-health/targets/invalid/question-answers/summary", "3", true, 400},
	} {
		response = request(tc.path, tc.contract, tc.authenticated)
		if response.Code != tc.status {
			t.Fatalf("rejection=%d want=%d", response.Code, tc.status)
		}
	}
	if repo.calls != before || len(priority.calls) != 0 {
		t.Fatal("invalid scope touched storage or Priority")
	}
	repo.err = errors.New("local storage failed")
	response = request(path, "3", true)
	if response.Code != 500 || !strings.Contains(response.Body.String(), ErrorQuestionAnswerStorage) {
		t.Fatalf("summary storage failure=%d %s", response.Code, response.Body.String())
	}
}

func TestQuestionAnswerC1CompletionRejectsHalfStates(t *testing.T) {
	correct := QuestionAnswerCorrect
	unreviewed := QuestionAnswerUnreviewed
	automatic := QuestionAnswerJudgmentAutomatic
	manual := QuestionAnswerJudgmentManual
	for _, completion := range []QuestionAnswerCompletion{
		{Status: QuestionAnswerSucceeded, AnswerJudgment: &correct},
		{Status: QuestionAnswerSucceeded, AnswerJudgment: &correct, JudgmentSource: &manual},
		{Status: QuestionAnswerSucceeded, AnswerJudgment: &unreviewed, JudgmentSource: &automatic},
		{Status: QuestionAnswerCancelled, AnswerJudgment: &correct, JudgmentSource: &automatic},
		{Status: QuestionAnswerFailed, AnswerBody: "answer"},
		{Status: QuestionAnswerRunning},
	} {
		if validateQuestionAnswerCompletion(completion) == nil {
			t.Fatalf("half state accepted: %+v", completion)
		}
	}
}

func TestQuestionAnswerC1LegacySparseBatchRemainsReadable(t *testing.T) {
	records := []QuestionAnswerRecord{}
	for i := 0; i < 25; i++ {
		records = append(records, QuestionAnswerRecord{ID: strconv.Itoa(i), BatchID: "legacy-sparse", ModelName: "model-" + strconv.Itoa(i), QuestionID: "q-" + strconv.Itoa(i), QuestionBody: "Body", Status: QuestionAnswerSucceeded, AnswerJudgment: questionAnswerJudgmentPointer(QuestionAnswerUnreviewed)})
	}
	summary, err := buildQuestionAnswerBatchSummary(records)
	if err != nil || summary.RepeatCount != 1 || summary.Stats.Requests.Submitted != 25 || len(summary.Models) != 25 || len(summary.Questions) != 25 {
		t.Fatalf("historical sparse batch=%+v err=%v", summary, err)
	}
	for i := range records {
		index := 1
		records[i].RepeatIndex = &index
	}
	if _, err := buildQuestionAnswerBatchSummary(records); !errors.Is(err, requestError(ErrorQuestionAnswerStorage)) {
		t.Fatal("new incomplete model/question matrix accepted")
	}
}
