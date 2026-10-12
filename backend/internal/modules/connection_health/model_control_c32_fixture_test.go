package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// These black-box business expectations are authored before implementation.
// Explicit TEST_DATABASE_URL opts into isolated PostgreSQL and httptest; the
// default test run starts neither a listener nor an external process.
type c32REDFixture struct {
	groupPlatform    string
	probeCalls       int
	probeFailures    int
	probeFailAccount string
	t                *testing.T
	pool             *pgxpool.Pool
	service          *Service
	mux              *http.ServeMux
	mu               sync.Mutex
	accounts         map[string]map[string]any
	writes           []map[string]any
	scheduleWrites   int
	bulkCode         int
	failDetail       bool
	failInventory    bool
	afterWrite       func()
	beforeDetail     func()
	clock            int
	detailReads      int
	statusWrites     []string
	priorityWrites   []int
	deleteWrites     []string
	session          upstream.Session
}

const c32REDUser = "c32-red-user"
const c32REDWorkspace = "ws1"

func c32REDTarget(id string) string { return "sub2api:" + c32REDWorkspace + ":" + id }

func c32REDAccount(id int, mapping map[string]string) map[string]any {
	return map[string]any{"id": id, "name": fmt.Sprintf("c3-red-%d", id), "platform": "openai", "type": "apikey", "status": "active", "schedulable": true, "priority": 100, "concurrency": 1, "group_ids": []int{1}, "groups": []map[string]any{{"id": 1, "name": "c3-red-group"}}, "credentials": map[string]any{"model_mapping": mapping, "api_key": "fixture-only", "base_url": "http://127.0.0.1:1"}, "extra": map[string]any{"openai_passthrough": false}, "temp_unschedulable_until": nil, "overload_until": nil, "rate_limit_reset_at": nil, "expires_at": nil, "auto_pause_on_expired": false, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
}

func c32REDClone(value any) any {
	encoded, _ := json.Marshal(value)
	var result any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func newC32REDFixture(t *testing.T) *c32REDFixture {
	t.Helper()
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	taskBEnsurePriorityWorkspaceTable(t, pool)
	f := &c32REDFixture{probeFailAccount: "3", t: t, pool: pool, bulkCode: 200, accounts: map[string]map[string]any{"1": c32REDAccount(1, map[string]string{"a": "A", "a-alias": "A", "b": "B"}), "2": c32REDAccount(2, map[string]string{"a": "A", "a-alias": "A", "b": "B"})}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	platform := upstream.NewPlatformService(upstream.NewHTTPClient(server.Client()))
	f.session = upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	f.service = NewServiceWithBackgroundTasks(repo, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}}, nil, platform, true)
	f.accounts["3"] = c32REDAccount(3, map[string]string{"a": "A", "a-alias": "A", "b": "B"})
	for id, account := range f.accounts {
		account["credentials"].(map[string]any)["api_key"] = "fixture-" + id
	}
	f.service.accounts = fakeAdminAccountResolver{id: c32REDWorkspace}
	f.service.SetPlatformGroupReader(platform)
	f.mux = http.NewServeMux()
	RegisterRoutes(f.mux, f.service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.service.closeActionAdmission()
		if err := f.service.drainActionDispatches(ctx); err != nil {
			t.Error(err)
		}
		if err := f.service.ShutdownQuestionAnswers(ctx); err != nil {
			t.Error(err)
		}
		if err := f.service.ShutdownQuestionAnswerSchedules(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *c32REDFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(code int, data any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}
	if r.URL.Path == "/api/v1/admin/groups" {
		platform := f.groupPlatform
		if platform == "" {
			platform = "openai"
		}
		write(200, map[string]any{"items": []map[string]any{{"id": 1, "name": "c3-red-group", "platform": platform, "rate_multiplier": 1}}, "total": 1, "page": 1, "page_size": 100})
		return
	}
	if r.URL.Path == "/api/v1/admin/accounts" {
		if f.failInventory {
			write(503, map[string]any{})
			return
		}
		ids := make([]string, 0, len(f.accounts))
		for id := range f.accounts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		items := []any{}
		for _, id := range ids {
			items = append(items, c32REDClone(f.accounts[id]))
		}
		write(200, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
		return
	}
	if r.URL.Path == "/api/v1/admin/accounts/bulk-update" {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			write(400, map[string]any{})
			return
		}
		if value, ok := body["status"].(string); ok {
			f.statusWrites = append(f.statusWrites, value)
		}
		if value, ok := body["priority"].(float64); ok {
			f.priorityWrites = append(f.priorityWrites, int(value))
		}
		if _, ok := body["credentials"]; ok {
			f.writes = append(f.writes, c32REDClone(body).(map[string]any))
		}
		if f.bulkCode != 200 {
			w.WriteHeader(f.bulkCode)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": f.bulkCode, "message": "Invalid request"})
			return
		}
		ids := body["account_ids"].([]any)
		id := strconv.Itoa(int(ids[0].(float64)))
		if credentials, ok := body["credentials"].(map[string]any); ok {
			f.accounts[id]["credentials"].(map[string]any)["model_mapping"] = credentials["model_mapping"]
		}
		if value, ok := body["status"]; ok {
			f.accounts[id]["status"] = value
		}
		if value, ok := body["priority"]; ok {
			f.accounts[id]["priority"] = value
		}
		hook := f.afterWrite
		if hook != nil {
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		write(200, map[string]any{"success_count": 1, "failed_count": 0, "results": []map[string]any{{"account_id": ids[0], "success": true}}})
		return
	}
	if r.URL.Path == "/api/v1/admin/accounts/data" {
		accounts := []any{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if a := f.accounts[id]; a != nil {
				accounts = append(accounts, c32REDClone(a))
			}
		}
		write(200, map[string]any{"accounts": accounts, "proxies": []any{}})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"), "/")
		id := parts[0]
		account := f.accounts[id]
		if account == nil {
			write(404, map[string]any{})
			return
		}
		if r.Method == "DELETE" {
			f.deleteWrites = append(f.deleteWrites, id)
			delete(f.accounts, id)
			write(200, map[string]any{"message": "Account deleted successfully"})
			return
		}
		if len(parts) > 1 && parts[1] == "schedulable" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.scheduleWrites++
			account["schedulable"] = body["schedulable"]
			write(200, c32REDClone(account))
			return
		}
		f.detailReads++
		if f.failDetail {
			write(503, map[string]any{})
			return
		}
		if hook := f.beforeDetail; hook != nil {
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		write(200, c32REDClone(account))
		return
	}
	f.t.Logf("unhandled fake route: %s %s", r.Method, r.URL.Path)
	write(404, map[string]any{})
}

func (f *c32REDFixture) request(method, path string, body any) (int, map[string]any) {
	f.t.Helper()
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/connection-health/"+path, strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(questionAnswerContractHeaderName, questionAnswerContractVersion)
	r = r.WithContext(authctx.WithUserID(r.Context(), c32REDUser))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	result := map[string]any{}
	if w.Body.Len() > 0 {
		var payload any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			if w.Code != http.StatusNotFound {
				f.t.Fatalf("%s %s returned invalid JSON: %v", method, path, err)
			}
		} else {
			switch decoded := payload.(type) {
			case map[string]any:
				result = decoded
			case []any:
				result["groups"] = decoded
			default:
				f.t.Fatalf("%s %s returned unexpected JSON shape %T", method, path, payload)
			}
		}
	}
	return w.Code, result
}

func (f *c32REDFixture) expect(method, path string, body any, status int) map[string]any {
	f.t.Helper()
	code, result := f.request(method, path, body)
	if code != status {
		f.t.Fatalf("%s %s status=%d want=%d body=%v", method, path, code, status, result)
	}
	return result
}

func (f *c32REDFixture) roundAt(id, model string, correct, incorrect, unreviewed int, created, completed time.Time) string {
	f.t.Helper()
	f.clock++
	batch := fmt.Sprintf("c3-round-%s-%d", id, f.clock)
	for index := 0; index < correct+incorrect+unreviewed; index++ {
		judgment := "unreviewed"
		if index < correct {
			judgment = "correct"
		} else if index < correct+incorrect {
			judgment = "incorrect"
		}
		_, err := f.pool.Exec(f.t.Context(), `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at,updated_at) VALUES($1,$2,$3,$4,$5,'q','C32 RED','fixture','succeeded',$6,$7,$8,$8)`, fmt.Sprintf("%s-%d", batch, index), c32REDUser, c32REDTarget(id), batch, model, judgment, created, completed)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	return batch
}

func (f *c32REDFixture) manage(id, model string) map[string]any {
	f.t.Helper()
	return f.expect("POST", "model-control/managed", map[string]any{"targetId": c32REDTarget(id), "modelName": model}, 200)
}

func (f *c32REDFixture) item(id, model string) map[string]any {
	f.t.Helper()
	result := f.expect("GET", "model-control/targets/"+c32REDTarget(id), nil, 200)
	for _, raw := range result["items"].([]any) {
		item := raw.(map[string]any)
		if item["modelName"] == model {
			return item
		}
	}
	f.t.Fatalf("managed model %s missing: %v", model, result)
	return nil
}

func (f *c32REDFixture) preview(id, model, operation string) map[string]any {
	f.t.Helper()
	item := f.item(id, model)
	return f.expect("POST", "model-control/preview", map[string]any{"targetId": c32REDTarget(id), "modelName": model, "operation": operation, "basis": item["basis"]}, 200)
}

func (f *c32REDFixture) execute(id, model, operation string, preview map[string]any) (int, map[string]any) {
	f.t.Helper()
	route := operation
	if route == "close_account" {
		route = "close-account"
	}
	return f.request("POST", "model-control/"+route, map[string]any{"targetId": c32REDTarget(id), "modelName": model, "basis": preview["item"].(map[string]any)["basis"], "planFingerprint": preview["planFingerprint"], "confirmWithoutEvidence": true})
}

func (f *c32REDFixture) close(id, model string) map[string]any {
	f.t.Helper()
	preview := f.preview(id, model, "close")
	if preview["blockReasonKey"] != "" && preview["blockReasonKey"] != nil {
		f.t.Fatalf("close unexpectedly blocked: %v", preview)
	}
	code, result := f.execute(id, model, "close", preview)
	if code != 200 {
		f.t.Fatalf("close status=%d: %v", code, result)
	}
	return result
}

func (f *c32REDFixture) verify(id string) map[string]any {
	f.t.Helper()
	return f.expect("POST", "model-control/verify", map[string]any{"targetIds": []string{c32REDTarget(id)}}, 200)
}

func (f *c32REDFixture) change(id string, change func(map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.accounts[id])
}
func (f *c32REDFixture) mapping(id string, mapping map[string]string) {
	f.change(id, func(account map[string]any) { account["credentials"].(map[string]any)["model_mapping"] = mapping })
}
func (f *c32REDFixture) writeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.writes) }
func (f *c32REDFixture) closed(id, model string) map[string]any {
	return f.item(id, model)["control"].(map[string]any)["closedEntries"].(map[string]any)
}
func (f *c32REDFixture) agePending(id, model string, seconds int) {
	f.t.Helper()
	_, err := f.pool.Exec(f.t.Context(), `UPDATE connection_health_model_control_targets SET pending=jsonb_set(pending,'{sendStartedAt}',to_jsonb(clock_timestamp()-make_interval(secs=>$4))) WHERE user_id=$1 AND target_id=$2 AND model_name=$3`, c32REDUser, c32REDTarget(id), model, seconds)
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *c32REDFixture) eventCount(event string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.t.Context(), `SELECT count(*) FROM connection_health_model_control_events WHERE user_id=$1 AND event_type=$2`, c32REDUser, event).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}
