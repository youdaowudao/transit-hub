package chanshui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

type singleAskQueueCapture struct {
	*queueFixtureExecutor
	captureMu sync.Mutex
	body      json.RawMessage
}

func (f *singleAskQueueCapture) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	if e.Operation == "create-report" {
		a := e.Args.(map[string]any)
		raw, _ := json.Marshal(a["singleAsk"])
		f.captureMu.Lock()
		f.body = append(json.RawMessage(nil), raw...)
		f.captureMu.Unlock()
	}
	return f.queueFixtureExecutor.Query(ctx, e, c, internal)
}

func TestSingleAskUsesExistingFIFOAndCopiesImmutablePayload(t *testing.T) {
	s, f, _ := queueFixture(t)
	capture := &singleAskQueueCapture{queueFixtureExecutor: f}
	s.runner = capture
	o := Owner{"user-a", "workspace-a"}
	first, second := queueInput("a"), queueInput("b")
	second.SingleAsk = json.RawMessage(`{"prompt":"原样单问","effort":"low","thinking":{"type":"enabled","budget_tokens":4096}}`)
	want := string(second.SingleAsk)
	if _, err := s.Start(t.Context(), o, first); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, first.ReportID)
	if _, err := s.Start(t.Context(), o, second); err != nil {
		t.Fatal(err)
	}
	second.SingleAsk[2] = 'X'
	f.mu.Lock()
	queued := f.requests[second.ReportID]
	running := len(f.order)
	f.mu.Unlock()
	if string(queued.SingleAsk) != want || running != 1 {
		t.Fatal("single ask bypassed FIFO or retained caller-owned bytes", string(queued.SingleAsk), running)
	}
	capture.captureMu.Lock()
	forwarded := string(capture.body)
	capture.captureMu.Unlock()
	var got, expected any
	_ = json.Unmarshal([]byte(forwarded), &got)
	_ = json.Unmarshal([]byte(want), &expected)
	if mustJSON(got) != mustJSON(expected) {
		t.Fatal("single ask not forwarded to original snapshot", forwarded)
	}
	f.release(first.ReportID, true)
	nextStarted(t, f, second.ReportID)
	f.release(second.ReportID, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
}

func TestReferenceInfoIsOwnerScopedReadAndNeverStartsModel(t *testing.T) {
	s, f, targets := fixture(t)
	queries := 0
	f.fn = func(e Envelope) (any, error) {
		if e.Operation != "reference-info" || e.Owner != (Owner{"user-a", "workspace-a"}) {
			t.Fatal("reference metadata bypassed owner or wrong operation", e)
		}
		queries++
		return map[string]any{"supported_models": []string{"original-model"}, "fingerprint_count": 1}, nil
	}
	for _, workspace := range []string{"workspace-a", "foreign-workspace"} {
		r := httptest.NewRequest("GET", "/api/chanshui/reference-info", nil)
		r.Header.Set(WorkspaceHeader, workspace)
		r = r.WithContext(authctx.WithUserID(r.Context(), "user-a"))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if workspace == "workspace-a" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "original-model") {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 409 {
			t.Fatal("foreign workspace can read reference adapter", w.Code)
		}
	}
	if queries != 1 || f.starts.Load() != 0 || targets.calls.Load() != 0 {
		t.Fatal("reference read started model or resolved private credentials", queries, f.starts.Load(), targets.calls.Load())
	}
}
