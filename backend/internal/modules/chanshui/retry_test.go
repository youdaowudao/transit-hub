package chanshui

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"transithub/backend/internal/shared/authctx"
)

type retryQueueExecutor struct {
	*queueFixtureExecutor
	retries     map[string]RetryInput
	lost        bool
	loseRead    bool
	readBlocked bool
}

func (f *retryQueueExecutor) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	f.mu.Lock()
	blocked := f.readBlocked
	f.mu.Unlock()
	if e.Operation == "read-report" && blocked {
		return nil, problem(503, "query", "隔离测试：查询仍在清理")
	}
	args, _ := e.Args.(map[string]any)
	id, _ := args["reportId"].(string)
	if e.Operation == "retry-report" {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.owners[id] != e.Owner {
			return nil, problem(404, "report", "找不到报告")
		}
		raw, _ := json.Marshal(args)
		var input RetryInput
		_ = json.Unmarshal(raw, &input)
		f.retries[id] = input
		f.statuses[id] = "queued"
		if f.lost {
			f.lost = false
			f.readBlocked = f.loseRead
			return nil, problem(503, "query", "隔离测试：保存后返回中断")
		}
		return fakeResponse(f.viewLocked(id)), nil
	}
	response, err := f.queueFixtureExecutor.Query(ctx, e, c, internal)
	if err != nil {
		return nil, err
	}
	if e.Operation == "read-report" {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		return fakeResponse(f.viewLocked(id)), nil
	}
	return response, nil
}
func (f *retryQueueExecutor) viewLocked(id string) map[string]any {
	input := f.requests[id]
	v := map[string]any{"reportId": id, "targetId": input.TargetID, "accountId": "a", "groupId": input.GroupID, "baseURL": "https://fixture.invalid", "protocol": "chat", "model": input.Model, "status": f.statuses[id], "memoryLimitMiB": 128, "totalTimeoutSeconds": 60}
	if retry, ok := f.retries[id]; ok {
		v["retry"] = map[string]any{"id": retry.RetryID, "expectedRetryId": retry.ExpectedRetryID, "itemIds": retry.ItemIDs}
	}
	return v
}
func retryServiceFixture(t *testing.T) (*Service, *retryQueueExecutor, Owner, string) {
	t.Helper()
	s, base, _ := queueFixture(t)
	f := &retryQueueExecutor{queueFixtureExecutor: base, retries: map[string]RetryInput{}}
	s.runner = f
	o := Owner{"user-a", "workspace-a"}
	input := queueInput("a")
	if _, e := s.Start(t.Context(), o, input); e != nil {
		t.Fatal(e)
	}
	nextStarted(t, base, input.ReportID)
	base.release(input.ReportID, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
	return s, f, o, input.ReportID
}
func TestRetryUsesSameFIFOAndRejectsStaleStopAcrossRounds(t *testing.T) {
	s, f, o, id := retryServiceFixture(t)
	input := RetryInput{RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q2"}}
	if _, e := s.Retry(t.Context(), o, id, input); e != nil {
		t.Fatal(e)
	}
	nextStarted(t, f.queueFixtureExecutor, id)
	if _, e := s.Retry(t.Context(), o, id, input); e != nil {
		t.Fatal("same active retry must only confirm", e)
	}
	if _, e := s.Cancel(t.Context(), o, id); e == nil {
		t.Fatal("old unversioned stop affected retry")
	}
	f.mu.Lock()
	alive := !f.jobs[id].exited.Load()
	count := len(f.order)
	f.mu.Unlock()
	if !alive || count != 2 {
		t.Fatal("old stop killed new retry or replay started twice")
	}
	f.release(id, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
	if _, e := s.Retry(t.Context(), o, id, input); e != nil {
		t.Fatal("same completed retry must only confirm", e)
	}
	next := RetryInput{RetryID: strings.Repeat("c", 32), ExpectedRetryID: input.RetryID, ItemIDs: []string{"Q2"}}
	if _, e := s.Retry(t.Context(), o, id, next); e != nil {
		t.Fatal(e)
	}
	nextStarted(t, f.queueFixtureExecutor, id)
	if _, e := s.CancelRound(t.Context(), o, id, input.RetryID); e == nil {
		t.Fatal("late stop killed subsequent round")
	}
	if _, e := s.Retry(t.Context(), o, id, input); e == nil {
		t.Fatal("old retry replay accepted across rounds")
	}
	if _, e := s.CancelRound(t.Context(), o, id, next.RetryID); e != nil {
		t.Fatal("current round cannot stop", e)
	}
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 3 || f.maxRunning != 1 {
		t.Fatal("retry duplicated or overlapped", f.order)
	}
}
func TestRetryLostAcceptanceRecoversOriginalWithoutExecutingOrCreating(t *testing.T) {
	s, f, o, id := retryServiceFixture(t)
	input := RetryInput{RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q2"}}
	f.mu.Lock()
	f.lost = true
	f.mu.Unlock()
	if _, e := s.Retry(t.Context(), o, id, input); e == nil {
		t.Fatal("lost acceptance was treated as confirmed")
	}
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.blocked) == 0 && !s.recoveryRunning })
	if _, e := s.Retry(t.Context(), o, id, input); e != nil {
		t.Fatal("same operation cannot inspect recovered state", e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 1 || len(f.statuses) != 1 || f.statuses[id] != "interrupted" {
		t.Fatal("lost return started paid calls or left orphan", f.order, f.statuses)
	}
}

func TestRetryUnconfirmedReadPausesExistingQueueAndCanRecover(t *testing.T) {
	s, f, o, id := retryServiceFixture(t)
	input := RetryInput{RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q2"}}
	f.mu.Lock()
	f.lost = true
	f.loseRead = true
	f.mu.Unlock()
	if _, e := s.Retry(t.Context(), o, id, input); e == nil {
		t.Fatal("lost acceptance was confirmed")
	}
	s.mu.Lock()
	blocked := len(s.blocked)
	s.mu.Unlock()
	if blocked != 1 {
		t.Fatal("unknown saved state did not pause queue")
	}
	f.mu.Lock()
	f.readBlocked = false
	f.mu.Unlock()
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.blocked) == 0 && !s.recoveryRunning })
	if _, e := s.Retry(t.Context(), o, id, input); e != nil {
		t.Fatal("same operation cannot inspect automatically recovered state", e)
	}
	s.mu.Lock()
	blocked = len(s.blocked)
	s.mu.Unlock()
	if blocked != 0 || f.status(id) != "interrupted" {
		t.Fatal("unconfirmed state did not recover")
	}
}
func TestRetryHTTPRouteAndVersionedCancelRejectUnapprovedFields(t *testing.T) {
	s, f, _, id := retryServiceFixture(t)
	send := func(action, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/chanshui/reports/"+id+"/"+action, strings.NewReader(body))
		req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
		req.Header.Set(WorkspaceHeader, "workspace-a")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	bad := send("retry", `{"retryId":"`+strings.Repeat("b", 32)+`","itemIds":["Q2"],"model":"other"}`)
	if bad.Code != 422 {
		t.Fatal("route allowed target-changing payload", bad.Code, bad.Body.String())
	}
	good := send("retry", `{"retryId":"`+strings.Repeat("b", 32)+`","itemIds":["Q2"]}`)
	if good.Code != 202 {
		t.Fatal(good.Code, good.Body.String())
	}
	nextStarted(t, f.queueFixtureExecutor, id)
	old := send("cancel", `{}`)
	if old.Code != 409 {
		t.Fatal("HTTP old stop affected retry", old.Code)
	}
	current := send("cancel", `{"expectedRetryId":"`+strings.Repeat("b", 32)+`"}`)
	if current.Code != 200 {
		t.Fatal(current.Code, current.Body.String())
	}
}
