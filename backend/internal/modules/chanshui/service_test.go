package chanshui

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/connection_health"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type fakeWorkspace struct{}
type currentWorkspace struct{ ID string }

func (w *currentWorkspace) RequireCurrentID(context.Context, string) (string, error) {
	return w.ID, nil
}

func (fakeWorkspace) RequireCurrentID(context.Context, string) (string, error) {
	return "workspace-a", nil
}

type fakeTargets struct {
	calls          atomic.Int32
	protocol, auth string
}

func (f *fakeTargets) ListAuditTargets(context.Context, string) ([]connection_health.AuditTarget, error) {
	return []connection_health.AuditTarget{{TargetID: "account-a", AccountName: "账号A"}}, nil
}
func (f *fakeTargets) ResolveAuditContext(context.Context, string, string, string, bool) (connection_health.AuditContext, error) {
	f.calls.Add(1)
	protocol := f.protocol
	if protocol == "" {
		protocol = "chat"
	}
	return connection_health.AuditContext{WorkspaceID: "workspace-a", TargetID: "account-a", AccountID: "a", AccountName: "账号A", GroupID: "group-a", GroupName: "分组A", BaseURL: "https://fixture.invalid", Protocol: protocol, CanRun: true, Credential: upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "server-only-secret", AnthropicAuth: f.auth}}, nil
}

type fakeExecutor struct {
	fn              func(Envelope) (any, error)
	starts          atomic.Int32
	availableCalls  atomic.Int32
	finished        chan struct{}
	once            sync.Once
	credentialMu    sync.Mutex
	modelCredential Credential
	onStart         func(Envelope, Credential)
}

func fakeResponse(value any) *QueryResponse {
	body, _ := json.Marshal(value)
	return &QueryResponse{200, "application/json", int64(len(body)), io.NopCloser(strings.NewReader(string(body)))}
}
func (f *fakeExecutor) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	if e.Operation == "models" && c != nil {
		f.credentialMu.Lock()
		f.modelCredential = *c
		f.credentialMu.Unlock()
	}
	if e.Operation == "finish-interrupted" {
		defer f.once.Do(func() { close(f.finished) })
		raw, err := json.Marshal(e.Args)
		if err != nil {
			return nil, err
		}
		var payload struct {
			ReportID string         `json:"reportId"`
			Error    map[string]any `json:"error"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&payload); err != nil {
			return nil, problem(422, "contract", "finish-interrupted参数不符合CLI严格合同："+err.Error())
		}
	}
	if f.fn != nil {
		value, err := f.fn(e)
		if err != nil {
			return nil, err
		}
		return fakeResponse(value), nil
	}
	return fakeResponse(map[string]any{}), nil
}
func (f *fakeExecutor) Start(_ context.Context, e Envelope, cred Credential, _ int, _ int) (*Job, error) {
	f.starts.Add(1)
	if f.onStart != nil {
		f.onStart(e, cred)
	}
	done := make(chan struct{})
	close(done)
	return &Job{Done: done, stop: func(context.Context) error { return nil }, info: func() ExitInfo { return ExitInfo{Exited: true, Result: "success"} }}, nil
}
func TestStrictRecoveryCompletedReportAllowsSecondTask(t *testing.T) {
	s, f, _ := fixture(t)
	owner := Owner{"user-a", "workspace-a"}
	var storeMu sync.Mutex
	statuses := map[string]string{}
	requests := map[string]StartInput{}
	f.onStart = func(e Envelope, _ Credential) {
		storeMu.Lock()
		defer storeMu.Unlock()
		statuses[e.Args.(map[string]any)["reportId"].(string)] = "done"
	}
	f.fn = func(e Envelope) (any, error) {
		storeMu.Lock()
		defer storeMu.Unlock()
		args := e.Args.(map[string]any)
		id, _ := args["reportId"].(string)
		switch e.Operation {
		case "read-report":
			if statuses[id] == "" {
				return nil, problem(404, "report", "missing")
			}
			if request, ok := args["request"].(StartInput); ok && mustJSON(request) != mustJSON(requests[id]) {
				return nil, problem(409, "idempotency", "不同选择")
			}
			return map[string]any{"reportId": id, "status": statuses[id]}, nil
		case "create-report":
			statuses[id] = "queued"
			requests[id] = args["request"].(StartInput)
			return map[string]any{"memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "status": "queued"}, nil
		case "finish-interrupted":
			if statuses[id] != "done" {
				statuses[id] = "interrupted"
			}
			return map[string]any{"reportId": id, "status": statuses[id]}, nil
		default:
			t.Fatalf("unexpected %s", e.Operation)
			return nil, nil
		}
	}
	first := startInput()
	if _, err := s.Start(t.Context(), owner, first); err != nil {
		t.Fatal(err)
	}
	second := startInput()
	second.ReportID = strings.Repeat("b", 32)
	if _, err := s.Start(t.Context(), owner, second); err != nil {
		t.Fatalf("normal SQL completion did not release old task: %v", err)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if f.starts.Load() != 2 || statuses[first.ReportID] != "done" || statuses[second.ReportID] != "done" {
		t.Fatal("recovery replaced a completed report or blocked second task")
	}
}
func TestStrictRecoveryInterruptsExitedTaskWithoutExtraWireFields(t *testing.T) {
	s, f, _ := fixture(t)
	status := "running"
	active := &activeTask{Owner: Owner{"user-a", "workspace-a"}, ReportID: strings.Repeat("a", 32), Job: &Job{info: func() ExitInfo { return ExitInfo{Exited: true, Result: "oom-kill", OOM: true} }}}
	s.active = active
	f.fn = func(e Envelope) (any, error) {
		if e.Operation != "finish-interrupted" {
			t.Fatalf("unexpected %s", e.Operation)
		}
		args := e.Args.(map[string]any)
		detail := args["error"].(map[string]any)
		if !strings.Contains(detail["message"].(string), "内存") {
			t.Fatal("exit evidence not projected into message")
		}
		status = "interrupted"
		return map[string]any{"status": status}, nil
	}
	if err := s.recoverExited(t.Context(), active); err != nil || s.active != nil || status != "interrupted" || f.starts.Load() != 0 {
		t.Fatalf("strict CLI recovery failed: status=%s active=%v err=%v", status, s.active != nil, err)
	}
}
func TestServiceMapsConfiguredAnthropicAuthentication(t *testing.T) {
	for _, item := range []struct {
		name, source, want string
		reject             bool
	}{{"bearer", "authorization_bearer", "bearer", false}, {"api-key", "x_api_key", "x_api_key", false}, {"unknown", "unsupported", "", true}, {"missing", "", "", true}} {
		t.Run(item.name, func(t *testing.T) {
			s, f, targets := fixture(t)
			targets.protocol = "anthropic"
			targets.auth = item.source
			input := startInput()
			input.ExpectedContext.Protocol = "anthropic"
			var actual Credential
			f.onStart = func(_ Envelope, cred Credential) { actual = cred }
			f.fn = func(e Envelope) (any, error) {
				switch e.Operation {
				case "read-report":
					return nil, problem(404, "report", "missing")
				case "create-report":
					return map[string]any{"memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "status": "queued"}, nil
				case "finish-interrupted":
					return map[string]any{"status": "done"}, nil
				default:
					t.Fatalf("unexpected %s", e.Operation)
					return nil, nil
				}
			}
			_, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input)
			if item.reject {
				if err == nil || f.starts.Load() != 0 {
					t.Fatal("unknown or missing Messages authentication guessed instead of rejected")
				}
				return
			}
			if err != nil || actual.AnthropicAuth != item.want || actual.Protocol != "anthropic" {
				t.Fatalf("configured authentication mapping failed: source=%s received=%s err=%v", item.source, actual.AnthropicAuth, err)
			}
		})
	}
}
func (f *fakeExecutor) Available(context.Context) error { f.availableCalls.Add(1); return nil }
func (f *fakeExecutor) Recover(context.Context) error   { return nil }
func (f *fakeExecutor) Shutdown(context.Context) error  { return nil }
func fixture(t *testing.T) (*Service, *fakeExecutor, *fakeTargets) {
	t.Helper()
	executor := &fakeExecutor{finished: make(chan struct{})}
	targets := &fakeTargets{}
	s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, targets: targets, runner: executor, initialized: true}
	t.Cleanup(func() {
		if executor.starts.Load() > 0 {
			select {
			case <-executor.finished:
			case <-time.After(time.Second):
				t.Error("task completion observer was left running")
			}
			s.admission.Lock()
			s.admission.Unlock()
		}
	})
	return s, executor, targets
}
func startInput() StartInput {
	return StartInput{ReportID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TargetID: "account-a", GroupID: "group-a", Model: "model-a", SelectedSectionIDs: []string{"iq"}, ExpectedContext: ExpectedContext{"workspace-a", "account-a", "group-a", "https://fixture.invalid", "chat"}}
}
func TestStartSameIDReplayDoesNotResolveCredentialsOrRunAgain(t *testing.T) {
	s, f, targets := fixture(t)
	input := startInput()
	saved := input
	f.fn = func(e Envelope) (any, error) {
		if e.Operation == "read-report" {
			request, ok := e.Args.(map[string]any)["request"].(StartInput)
			if !ok {
				t.Fatal("replay comparison was not sent to storage")
			}
			if mustJSON(request) != mustJSON(saved) {
				return nil, problem(409, "idempotency", "此报告编号的检测选择不同")
			}
			return map[string]any{"reportId": saved.ReportID, "status": "done"}, nil
		}
		t.Fatalf("unexpected operation %s", e.Operation)
		return nil, nil
	}
	result, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if f.starts.Load() != 0 || targets.calls.Load() != 0 || result.(map[string]any)["processExited"] != true {
		t.Fatal("replay resolved credentials or executed again")
	}
	input.Model = "other"
	if _, err = s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input); asProblem(err).Status != 409 {
		t.Fatalf("changed replay=%v", err)
	}
}
func TestLargeReplayReturnsOnlyBoundedSummary(t *testing.T) {
	s, f, targets := fixture(t)
	input := startInput()
	input.SelectedQuestionIDs = make([]string, 15000)
	for i := range input.SelectedQuestionIDs {
		input.SelectedQuestionIDs[i] = "Q1"
	}
	if n := len(mustJSON(input)); n <= 64*1024 || n >= 128*1024 {
		t.Fatalf("fixture must cross summary limit but fit request budget: %d", n)
	}
	f.fn = func(e Envelope) (any, error) {
		if e.Operation != "read-report" {
			t.Fatalf("unexpected %s", e.Operation)
		}
		request, ok := e.Args.(map[string]any)["request"].(StartInput)
		if !ok || mustJSON(request) != mustJSON(input) {
			t.Fatal("storage comparison did not receive original choices")
		}
		return map[string]any{"reportId": input.ReportID, "status": "done"}, nil
	}
	result, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(mustJSON(result)) > 1024 || f.starts.Load() != 0 || targets.calls.Load() != 0 {
		t.Fatal("large replay loaded full request/started another detection")
	}
}
func TestAcceptedResponseLostReplayNeverCreatesSecondRun(t *testing.T) {
	s, f, targets := fixture(t)
	input := startInput()
	var stored *StartInput
	status := "queued"
	f.fn = func(e Envelope) (any, error) {
		switch e.Operation {
		case "read-report":
			if stored == nil {
				return nil, problem(404, "report", "missing")
			}
			request, ok := e.Args.(map[string]any)["request"].(StartInput)
			if !ok || mustJSON(request) != mustJSON(*stored) {
				return nil, problem(409, "idempotency", "不同选择")
			}
			return map[string]any{"reportId": input.ReportID, "status": status}, nil
		case "create-report":
			value := e.Args.(map[string]any)["request"].(StartInput)
			stored = &value
			return map[string]any{"memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "status": status}, nil
		case "finish-interrupted":
			status = "done"
			return map[string]any{"status": status}, nil
		default:
			t.Fatalf("unexpected %s", e.Operation)
			return nil, nil
		}
	}
	if _, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input); err != nil {
		t.Fatal(err)
	}
	resolvedBeforeReplay := targets.calls.Load()
	if _, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, input); err != nil {
		t.Fatal(err)
	}
	if f.starts.Load() != 1 || targets.calls.Load() != resolvedBeforeReplay {
		t.Fatal("response-lost replay resolved credentials or created another run")
	}
}
func TestStartCreatesSnapshotAndUsesServerCredential(t *testing.T) {
	s, f, targets := fixture(t)
	f.fn = func(e Envelope) (any, error) {
		switch e.Operation {
		case "read-report":
			return nil, problem(404, "report", "report missing")
		case "create-report":
			args := e.Args.(map[string]any)
			if _, ok := args["request"].(StartInput); !ok {
				t.Fatal("canonical request absent")
			}
			if strings.Contains(mustJSON(args), "server-only-secret") {
				t.Fatal("credential persisted in report args")
			}
			return map[string]any{"memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "status": "queued"}, nil
		case "finish-interrupted":
			return map[string]any{"status": "done"}, nil
		default:
			t.Fatalf("unexpected %s", e.Operation)
			return nil, nil
		}
	}
	if _, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, startInput()); err != nil {
		t.Fatal(err)
	}
	if f.starts.Load() != 1 || targets.calls.Load() != 2 {
		t.Fatal("expected one accepted task with admission and execution-time account checks")
	}
}
func mustJSON(value any) string { b, _ := json.Marshal(value); return string(b) }
func TestTerminalSQLDoesNotReleaseStillRunningProcess(t *testing.T) {
	s, f, targets := fixture(t)
	old := &activeTask{Owner: Owner{"other", "other"}, ReportID: "old", Job: &Job{info: func() ExitInfo { return ExitInfo{Exited: false} }}}
	s.active = old
	f.fn = func(e Envelope) (any, error) {
		if e.Operation == "read-report" {
			return nil, problem(404, "report", "missing")
		}
		if e.Operation == "create-report" {
			return map[string]any{"memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "status": "queued"}, nil
		}
		t.Fatalf("unexpected %s", e.Operation)
		return nil, nil
	}
	result, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, startInput())
	if err != nil || result.(map[string]any)["status"] != "queued" || f.starts.Load() != 0 || targets.calls.Load() != 1 || s.active != old {
		t.Fatalf("old live process was replaced or overlapped: result=%v error=%v", result, err)
	}
}
func TestRecoveryBusyRetainsHandleAndNextAttemptSavesWithoutModel(t *testing.T) {
	s, f, _ := fixture(t)
	active := &activeTask{Owner: Owner{"user-a", "workspace-a"}, ReportID: "old", Job: &Job{info: func() ExitInfo { return ExitInfo{Exited: true, OOM: true, Result: "oom-kill"} }}}
	s.active = active
	attempts := 0
	f.fn = func(e Envelope) (any, error) {
		attempts++
		if attempts == 1 {
			return nil, problem(429, "query", "busy")
		}
		return map[string]any{"status": "interrupted"}, nil
	}
	if err := s.recoverExited(t.Context(), active); err == nil || s.active != active || !strings.Contains(active.Diagnostic, "不会再次调用模型") {
		t.Fatal("busy recovery lost the task or diagnostic")
	}
	if err := s.recoverExited(t.Context(), active); err != nil || s.active != nil || f.starts.Load() != 0 {
		t.Fatalf("retry did not recover without execution: %v", err)
	}
}
func TestOtherWorkspaceRuntimeHidesAccountAndReport(t *testing.T) {
	s, _, _ := fixture(t)
	s.active = &activeTask{Owner: Owner{"other", "private"}, ReportID: "private-report", AccountName: "private-account"}
	result := mustJSON(s.runtime(t.Context(), Owner{"user-a", "workspace-a"}))
	if strings.Contains(result, "private") || strings.Contains(result, `"busy":true`) || !strings.Contains(result, "可以加入等待") {
		t.Fatalf("other owner leaked: %s", result)
	}
}
func TestContextNeverSerializesServerCredential(t *testing.T) {
	s, _, _ := fixture(t)
	request := httptest.NewRequest("GET", "/api/chanshui/targets/account-a/context?groupId=group-a", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-a"))
	request.Header.Set(WorkspaceHeader, "workspace-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 200 || strings.Contains(w.Body.String(), "server-only-secret") || !strings.Contains(w.Body.String(), "expectedContext") {
		t.Fatalf("unsafe context: %d %s", w.Code, w.Body)
	}
}
func TestDeletePresetUsesVersionAndSameOwner(t *testing.T) {
	s, f, _ := fixture(t)
	called := false
	f.fn = func(e Envelope) (any, error) {
		called = true
		if e.Operation != "delete-question" || e.Owner != (Owner{"user-a", "workspace-a"}) || e.Args.(map[string]any)["id"] != "Q1" {
			t.Fatalf("wrong deletion: %#v", e)
		}
		return map[string]any{"deleted": true}, nil
	}
	request := httptest.NewRequest("DELETE", "/api/chanshui/questions/Q1", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-a"))
	request.Header.Set(WorkspaceHeader, "workspace-a")
	request.Header.Set("If-Match", "1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 200 || !called {
		t.Fatalf("preset cannot be deleted: %d %s", w.Code, w.Body)
	}
	called = false
	request.Header.Del("If-Match")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 422 || called {
		t.Fatal("unversioned delete reached storage")
	}
}
func TestUnauthenticatedAPIAndDisabledFeatureDoNotExecute(t *testing.T) {
	s, f, _ := fixture(t)
	request := httptest.NewRequest("GET", "/api/chanshui/settings", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatalf("unauthorized=%d", w.Code)
	}
	s.cfg.ChanshuiEnabled = false
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-a"))
	request.Header.Set(WorkspaceHeader, "workspace-a")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 503 || f.availableCalls.Load() != 0 {
		t.Fatal("disabled feature executed")
	}
}
func TestStaticMissingAssetNeverFallsBackToVue(t *testing.T) {
	s, _, _ := fixture(t)
	s.webDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.webDir, "index.html"), []byte("detector"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path   string
		status int
	}{{"/chanshui/", 200}, {"/chanshui/_next/missing.js", 404}, {"/chanshui/settings", 404}, {"/chanshui/../private", 404}} {
		w := httptest.NewRecorder()
		s.StaticHandler().ServeHTTP(w, httptest.NewRequest("GET", item.path, nil))
		if w.Code != item.status {
			t.Fatalf("%s=%d", item.path, w.Code)
		}
	}
}
func TestReportDateFiltersUseSingaporeAndInclusiveEndDate(t *testing.T) {
	args := map[string]any{"scope": "archive", "from": "2026-01-01", "to": "2026-01-02"}
	if err := reportDates(args); err != nil {
		t.Fatal(err)
	}
	from := args["fromMs"].(int64)
	to := args["toMs"].(int64)
	if to-from != 48*time.Hour.Milliseconds() {
		t.Fatal("end date did not cover whole selected day")
	}
	if time.UnixMilli(from).UTC().Hour() != 16 {
		t.Fatal("not Singapore midnight")
	}
}
func TestNoShellUnrestrictedRunnerFallback(t *testing.T) {
	cfg := RunnerConfig{BinaryPath: "/outside/binary", DataDir: "/outside/data", ProjectDir: "/project", Manager: "user"}
	if err := NewRunner(cfg).Available(t.Context()); err == nil {
		t.Fatal("invalid deployment path accepted")
	}
}
func TestModelsUseResolvedCredentialAndPreserveUpstreamOrder(t *testing.T) {
	s, f, _ := fixture(t)
	f.fn = func(e Envelope) (any, error) {
		if e.Operation != "models" {
			t.Fatalf("unexpected operation %s", e.Operation)
		}
		return map[string]any{"models": []any{"z-model", map[string]any{"id": "gpt-6.1-sol"}, "a-model"}}, nil
	}
	body := mustJSON(map[string]any{"groupId": "group-a", "expectedContext": startInput().ExpectedContext})
	req := httptest.NewRequest("POST", "/api/chanshui/targets/account-a/models", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
	req.Header.Set(WorkspaceHeader, "workspace-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("models=%d %s", w.Code, w.Body)
	}
	var result struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Models) != 3 || result.Models[0].ID != "z-model" || result.Models[1].ID != "gpt-6.1-sol" || result.Models[2].ID != "a-model" {
		t.Fatal("upstream model order changed")
	}
	f.credentialMu.Lock()
	credential := f.modelCredential
	f.credentialMu.Unlock()
	if credential.Key != "server-only-secret" || credential.BaseURL != "https://fixture.invalid" || f.starts.Load() != 0 || strings.Contains(w.Body.String(), credential.Key) {
		t.Fatal("model read used wrong credential, started a task, or leaked a key")
	}
}
func TestModelsSpecificErrorIsRedactedAndDoesNotStartDetection(t *testing.T) {
	s, f, _ := fixture(t)
	f.fn = func(Envelope) (any, error) {
		return nil, problem(503, "models", "上游返回：server-only-secret 模型列表暂不可用，请稍后重试")
	}
	body := mustJSON(map[string]any{"groupId": "group-a", "expectedContext": startInput().ExpectedContext})
	req := httptest.NewRequest("POST", "/api/chanshui/targets/account-a/models", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
	req.Header.Set(WorkspaceHeader, "workspace-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 503 || strings.Contains(w.Body.String(), "server-only-secret") || !strings.Contains(w.Body.String(), "模型列表暂不可用") || f.starts.Load() != 0 {
		t.Fatalf("unsafe model failure: %d %s", w.Code, w.Body)
	}
}
func TestSettingsConflictPreservesCurrentRevisionFromCLI(t *testing.T) {
	for _, item := range []struct {
		name     string
		revision int
	}{{"modified", 7}, {"deleted", 0}} {
		t.Run(item.name, func(t *testing.T) {
			s, f, _ := fixture(t)
			f.fn = func(e Envelope) (any, error) {
				if e.Operation != "save-settings" {
					t.Fatalf("unexpected operation %s", e.Operation)
				}
				// The CLI error header is decoded into the same public error type by
				// Runner.Query. A conflict must let the browser reload the actual version.
				var upstreamError Problem
				raw, _ := json.Marshal(map[string]any{"message": "设置已被修改或删除，请重新读取后再保存", "step": "settings", "operationResult": "not_started", "retryable": true, "currentRevision": item.revision})
				if err := json.Unmarshal(raw, &upstreamError); err != nil {
					t.Fatal(err)
				}
				upstreamError.Status = 409
				return nil, &upstreamError
			}
			req := httptest.NewRequest("PUT", "/api/chanshui/settings", strings.NewReader(`{"revision":1}`))
			req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
			req.Header.Set(WorkspaceHeader, "workspace-a")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 409 || result["currentRevision"] != float64(item.revision) || result["operationResult"] != "not_started" || f.starts.Load() != 0 {
				t.Fatalf("conflict lost current version or executed: %d %s", w.Code, w.Body)
			}
		})
	}
}
func TestCreateResponseLostRecoversCommittedQueuedReportWithoutModel(t *testing.T) {
	s, f, _ := fixture(t)
	status := ""
	f.fn = func(e Envelope) (any, error) {
		switch e.Operation {
		case "read-report":
			if status == "" {
				return nil, problem(404, "report", "missing")
			}
			return map[string]any{"reportId": startInput().ReportID, "status": status}, nil
		case "create-report":
			status = "queued"
			return nil, problem(503, "query", "提交后读取响应中断")
		case "finish-interrupted":
			if status != "queued" {
				t.Fatal("recovery before persisted report")
			}
			status = "interrupted"
			return map[string]any{"status": status}, nil
		default:
			t.Fatalf("unexpected %s", e.Operation)
			return nil, nil
		}
	}
	_, err := s.Start(t.Context(), Owner{"user-a", "workspace-a"}, startInput())
	if err == nil || status != "interrupted" || s.active != nil || f.starts.Load() != 0 {
		t.Fatalf("lost create response left report=%s active=%v starts=%d err=%v", status, s.active != nil, f.starts.Load(), err)
	}
	if asProblem(err).OperationResult != "not_started" || asProblem(err).ReportID != startInput().ReportID {
		t.Fatal("pre-credential failure lost accurate execution result/report ID")
	}
}
func TestLateWorkspaceWritesCannotModifyNewCurrentWorkspace(t *testing.T) {
	for _, item := range []struct{ name, method, path, body string }{{"settings", "PUT", "/api/chanshui/settings", `{"revision":1,"concurrency":1}`}, {"preset-delete", "DELETE", "/api/chanshui/questions/Q1", ""}} {
		t.Run(item.name, func(t *testing.T) {
			s, f, _ := fixture(t)
			workspace := &currentWorkspace{ID: "workspace-a"}
			s.workspace = workspace
			settings := map[string]int{"workspace-a": 2, "workspace-b": 2}
			questions := map[string]bool{"workspace-a": true, "workspace-b": true}
			calls := 0
			f.fn = func(e Envelope) (any, error) {
				calls++
				switch e.Operation {
				case "save-settings":
					settings[e.Owner.WorkspaceID] = 1
				case "delete-question":
					questions[e.Owner.WorkspaceID] = false
				}
				return map[string]any{}, nil
			}
			req := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
			req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
			req.Header.Set("X-Chanshui-Workspace", "workspace-a")
			req.Header.Set("If-Match", "1")
			workspace.ID = "workspace-b"
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != 409 || calls != 0 || settings["workspace-a"] != 2 || settings["workspace-b"] != 2 || !questions["workspace-a"] || !questions["workspace-b"] {
				t.Fatalf("late write crossed workspace: status=%d calls=%d settings=%v questions=%v", w.Code, calls, settings, questions)
			}
		})
	}
}
func TestMissingWorkspaceSnapshotRejectsBeforeInitialization(t *testing.T) {
	s, f, _ := fixture(t)
	s.initialized = false
	calls := 0
	f.fn = func(Envelope) (any, error) { calls++; return map[string]any{}, nil }
	req := httptest.NewRequest("GET", "/api/chanshui/runtime", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 409 || calls != 0 || f.availableCalls.Load() != 0 {
		t.Fatalf("missing workspace initialized/query: %d calls=%d checks=%d", w.Code, calls, f.availableCalls.Load())
	}
}
