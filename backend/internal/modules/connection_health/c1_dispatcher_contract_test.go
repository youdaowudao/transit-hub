package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type c1RoundTripFunc func(*http.Request) (*http.Response, error)

func (f c1RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Ask and discovery both use an in-memory transport. No listener or external
// service is involved; the fake repository only persists dispatcher inputs.
func TestC1QuestionAnswerDispatcherCarriesAtomicJudgment(t *testing.T) {
	for _, tc := range []struct {
		name, answer, judgment string
		keywords               []string
		source                 any
		code                   int
	}{
		{"correct", "ASCII HIT", "correct", []string{"hit"}, "automatic", 200},
		{"incorrect", "different", "incorrect", []string{"hit"}, "automatic", 200},
		{"manual-only", "answer", "unreviewed", nil, nil, 200},
		{"request-failure", "", "", []string{"hit"}, nil, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "C1 question", Keywords: tc.keywords, Enabled: true})
			service := newQuestionAnswerService("https://c1-fixture.invalid", repo, newFakeRepository())
			defer func() {
				if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
					t.Errorf("shutdown: %v", err)
				}
			}()
			transport := c1RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v1/models" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"}]}`)), Header: http.Header{}}, nil
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected request path: %s", r.URL.Path)
				}
				body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": tc.answer}}}})
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
			})
			service.modelDiscovery.client = &http.Client{Transport: transport}
			service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: transport}}
			batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, RepeatCount: json.RawMessage("3")})
			if err != nil {
				t.Fatal(err)
			}
			batch = waitQuestionAnswerBatch(t, service, batch.BatchID, false)
			if len(batch.Records) != 3 {
				t.Fatalf("records=%d want independent three", len(batch.Records))
			}
			indexes := map[any]bool{}
			for _, record := range batch.Records {
				value := c1JSON(t, record)
				if indexes[value["repeatIndex"]] || value["repeatIndex"] == nil {
					t.Fatalf("repeat index missing/duplicate: %v", value["repeatIndex"])
				}
				indexes[value["repeatIndex"]] = true
				if value["judgmentSource"] != tc.source {
					t.Fatalf("source=%v want=%v", value["judgmentSource"], tc.source)
				}
				if tc.code != 200 {
					if record.Status != QuestionAnswerFailed || record.AnswerJudgment != nil || record.AnswerBody != "" {
						t.Fatal("request failure must have no judgment or answer")
					}
				} else if record.Status != QuestionAnswerSucceeded || record.AnswerBody != tc.answer || record.AnswerJudgment == nil || string(*record.AnswerJudgment) != tc.judgment {
					t.Fatalf("dispatcher status=%s judgment=%v want succeeded/%s", record.Status, record.AnswerJudgment, tc.judgment)
				}
			}
		})
	}
}
