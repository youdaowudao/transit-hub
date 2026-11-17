package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func concurrentFixture(t *testing.T, count, concurrency int, configure ...func(*store.Settings)) (*store.Store, store.Owner, string, Credential) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	o := store.Owner{UserID: "concurrency-fixture", WorkspaceID: "isolated"}
	settings, err := s.Settings(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Concurrency = concurrency
	for _, option := range configure {
		option(&settings)
	}
	if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
		t.Fatal(err)
	}
	for _, preset := range questions.Presets() {
		q, err := s.Question(t.Context(), o, preset.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.DeleteQuestion(t.Context(), o, q.ID, q.Revision); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	for i := 0; i < count; i++ {
		q := questions.Definition{ID: fmt.Sprintf("custom:%032x", i+1), Title: fmt.Sprintf("并发隔离题%d", i), Kind: "iq", Enabled: true, Order: i, Template: fmt.Sprintf("fixture-request-%d", i), Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual"}}
		if i >= 32 {
			q.Kind = "fingerprint_behavior"
			q.Parameters = map[string]any{"generator": "fingerprint_behavior_v1", "slot": 0, "probeKind": "refusal-x2"}
		}
		if _, err = s.SaveQuestion(t.Context(), o, q, 0, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.ID)
	}
	id := strings.Repeat("e", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "isolated-fixture-key", Protocol: "chat"}
	if _, err = s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "isolated", Model: "fixture", SelectedQuestionIDs: ids, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	return s, o, id, c
}

func TestConfiguredConcurrencyTwoThirtyTwoAndSixtyFourAndNotificationSafety(t *testing.T) {
	for _, limit := range []int{2, 32, 64} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			count := limit + 5
			s, o, id, c := concurrentFixture(t, count, limit)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var active, peak, calls, notifying, overlap atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				n := active.Add(1)
				defer active.Add(-1)
				calls.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				if int(n) == limit {
					once.Do(func() { close(release) })
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return fixtureResponse(r, "已保存回答"), nil
			})
			err := RunOne(ctx, s, o, id, c, Config{Transport: fake, Notify: func(string, map[string]any) {
				if notifying.Add(1) != 1 {
					overlap.Add(1)
				}
				runtime.Gosched()
				notifying.Add(-1)
			}})
			if err != nil || calls.Load() != int32(count) || peak.Load() != int32(limit) || overlap.Load() != 0 {
				t.Fatalf("limit=%d count=%d calls=%d peak=%d concurrentNotify=%d error=%v", limit, count, calls.Load(), peak.Load(), overlap.Load(), err)
			}
			cursor := ""
			total := 0
			for {
				page, err := s.ListItems(ctx, o, id, store.Filters{Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					var result itemResult
					if err = json.Unmarshal(item.Result, &result); err != nil {
						t.Fatal(err)
					}
					if item.Attempts != 1 || result.Attempts != 1 {
						t.Fatalf("mixed item attempts %+v result=%+v", item, result)
					}
					total++
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if total != count {
				t.Fatalf("saved %d questions, want%d", total, count)
			}
		})
	}
}

func TestCancellationStopsFetchingQueuedQuestionsAndKeepsSavedAttempts(t *testing.T) {
	s, o, id, c := concurrentFixture(t, 8, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	done := make(chan error, 1)
	go func() { done <- RunOne(ctx, s, o, id, c, Config{Transport: fake}) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("configured requests did not start together")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled workers did not exit")
	}
	if calls.Load() != 2 {
		t.Fatal("cancelled task fetched waiting questions", calls.Load())
	}
	report, err := s.Report(context.Background(), o, id)
	if err != nil || report.Status != "cancelled" || report.ItemCounts["queued"] != 6 || report.ItemCounts["error"] != 2 {
		t.Fatalf("cancel did not preserve processed range: %+v %v", report, err)
	}
}

func TestAttemptAdmissionFailureDoesNotCountAnUnsentHTTPRequest(t *testing.T) {
	var sent atomic.Int32
	transport, _ := NewTransport(32, 1, false, fixtureTransport(func(r *http.Request) (*http.Response, error) { sent.Add(1); return fixtureResponse(r, "ok"), nil }))
	transport.BeforeAttempt = func(context.Context) error { return fmt.Errorf("isolated attempt write unavailable") }
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://fixture.invalid", nil)
	if _, err := transport.RoundTrip(req); err == nil || sent.Load() != 0 || transport.Attempts.Load() != 0 {
		t.Fatal("failed admission counted or sent a request", err, sent.Load(), transport.Attempts.Load())
	}
}

func TestNotApplicableQuestionHasNoHTTPAttempts(t *testing.T) {
	s, o, id, c := setup(t, []string{"thinking"}, "chat")
	var calls atomic.Int32
	err := RunOne(t.Context(), s, o, id, c, Config{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureResponse(r, "unexpected"), nil
	})})
	if err != nil || calls.Load() != 0 {
		t.Fatal("non-applicable question called the model", err, calls.Load())
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	var result itemResult
	json.Unmarshal(page.Items[0].Result, &result)
	if page.Items[0].Attempts != 0 || result.Attempts != 0 || result.Judgment.Status != "not_applicable" {
		t.Fatalf("non-applicable result got a paid attempt: %+v %+v", page.Items[0], result)
	}
	r, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Attempts int `json:"attempts"`
	}
	json.Unmarshal(r.Summary.Summary, &summary)
	if summary.Attempts != 0 {
		t.Fatal("non-applicable task consumed global attempts", summary.Attempts)
	}
}

func TestGlobalRequestBudgetStopsParallelRequestsWithoutInflatingItemAttempts(t *testing.T) {
	s, o, id, c := concurrentFixture(t, 40, 32, func(s *store.Settings) { s.MaxAttempts = 16 })
	var calls atomic.Int32
	err := RunOne(t.Context(), s, o, id, c, Config{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureResponse(r, "已保存回答"), nil
	})})
	if err != nil || calls.Load() != 16 {
		t.Fatalf("parallel budget: calls=%d err=%v", calls.Load(), err)
	}
	cursor := ""
	attempts := 0
	withAttempt := 0
	for {
		page, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			var result itemResult
			json.Unmarshal(item.Result, &result)
			if item.Attempts != result.Attempts {
				t.Fatal("unsent request inflated result attempts", item.Attempts, result.Attempts)
			}
			attempts += item.Attempts
			if item.Attempts > 0 {
				withAttempt++
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if attempts != 16 || withAttempt != 16 {
		t.Fatalf("request budget leaked into unrelated items: attempts=%d items=%d", attempts, withAttempt)
	}
}

func TestConcurrentRetriesStayWithTheirOwnQuestion(t *testing.T) {
	s, o, id, c := concurrentFixture(t, 6, 3, func(s *store.Settings) { s.Retries = 1 })
	var mu sync.Mutex
	seen := map[string]int{}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		question := ""
		for i := 0; i < 6; i++ {
			candidate := fmt.Sprintf("fixture-request-%d", i)
			if strings.Contains(string(raw), candidate) {
				question = candidate
				break
			}
		}
		mu.Lock()
		seen[question]++
		n := seen[question]
		mu.Unlock()
		if n == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated retry"}}`)), Request: r}, nil
		}
		return fixtureResponse(r, "已保存回答"), nil
	})
	if err := RunOne(t.Context(), s, o, id, c, Config{Transport: fake}); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	count := 0
	for {
		page, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			var result itemResult
			json.Unmarshal(item.Result, &result)
			if item.Attempts != 2 || result.Attempts != 2 {
				t.Fatal("retry counted against a different question", item.ItemID, item.Attempts, result.Attempts)
			}
			count++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if count != 6 || len(seen) != 6 {
		t.Fatal("concurrent retry lost a question", count, seen)
	}
}
