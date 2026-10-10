package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
type c3REDFixture struct {
	t              *testing.T
	pool           *pgxpool.Pool
	service        *Service
	mux            *http.ServeMux
	mu             sync.Mutex
	accounts       map[string]map[string]any
	writes         []map[string]any
	scheduleWrites int
	bulkCode       int
	failDetail     bool
	failInventory  bool
	afterWrite     func()
	beforeDetail   func()
	clock          int
}

const c3REDUser = "c3-red-user"
const c3REDWorkspace = "ws1"

func c3REDTarget(id string) string { return "sub2api:" + c3REDWorkspace + ":" + id }

func c3REDAccount(id int, mapping map[string]string) map[string]any {
	return map[string]any{"id": id, "name": fmt.Sprintf("c3-red-%d", id), "platform": "openai", "type": "apikey", "status": "active", "schedulable": true, "priority": 100, "concurrency": 1, "group_ids": []int{1}, "groups": []map[string]any{{"id": 1, "name": "c3-red-group"}}, "credentials": map[string]any{"model_mapping": mapping, "api_key": "fixture-only", "base_url": "http://127.0.0.1:1"}, "extra": map[string]any{"openai_passthrough": false}, "temp_unschedulable_until": nil, "overload_until": nil, "rate_limit_reset_at": nil, "expires_at": nil, "auto_pause_on_expired": false, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
}

func c3REDClone(value any) any {
	encoded, _ := json.Marshal(value)
	var result any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func newC3REDFixture(t *testing.T) *c3REDFixture {
	t.Helper()
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := &c3REDFixture{t: t, pool: pool, bulkCode: 200, accounts: map[string]map[string]any{"1": c3REDAccount(1, map[string]string{"a": "A", "a-alias": "A", "b": "B"}), "2": c3REDAccount(2, map[string]string{"a": "A", "a-alias": "A", "b": "B"})}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	platform := upstream.NewPlatformService(upstream.NewHTTPClient(server.Client()))
	f.service = NewServiceWithBackgroundTasks(repo, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}}, nil, platform, true)
	f.service.accounts = fakeAdminAccountResolver{id: c3REDWorkspace}
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

func (f *c3REDFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(code int, data any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}
	if r.URL.Path == "/api/v1/admin/groups" {
		write(200, map[string]any{"items": []map[string]any{{"id": 1, "name": "c3-red-group", "platform": "openai", "rate_multiplier": 1}}, "total": 1, "page": 1, "page_size": 100})
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
			items = append(items, c3REDClone(f.accounts[id]))
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
		f.writes = append(f.writes, c3REDClone(body).(map[string]any))
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
		hook := f.afterWrite
		if hook != nil {
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		write(200, map[string]any{"success_count": 1, "failed_count": 0, "results": []map[string]any{{"account_id": ids[0], "success": true}}})
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
		if len(parts) > 1 && parts[1] == "schedulable" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.scheduleWrites++
			account["schedulable"] = body["schedulable"]
			write(200, c3REDClone(account))
			return
		}
		if f.failDetail {
			write(503, map[string]any{})
			return
		}
		if hook := f.beforeDetail; hook != nil {
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		write(200, c3REDClone(account))
		return
	}
	write(404, map[string]any{})
}

func (f *c3REDFixture) request(method, path string, body any) (int, map[string]any) {
	f.t.Helper()
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/connection-health/"+path, strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(questionAnswerContractHeaderName, questionAnswerContractVersion)
	r = r.WithContext(authctx.WithUserID(r.Context(), c3REDUser))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	result := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &result)
	}
	return w.Code, result
}

func (f *c3REDFixture) expect(method, path string, body any, status int) map[string]any {
	f.t.Helper()
	code, result := f.request(method, path, body)
	if code != status {
		f.t.Fatalf("%s %s status=%d want=%d body=%v", method, path, code, status, result)
	}
	return result
}

func (f *c3REDFixture) round(id, model string, correct, incorrect, unreviewed int) string {
	f.t.Helper()
	f.clock++
	batch := fmt.Sprintf("c3-round-%s-%d", id, f.clock)
	stamp := time.Now().UTC().Add(time.Duration(f.clock) * time.Second)
	for index := 0; index < correct+incorrect+unreviewed; index++ {
		judgment := "unreviewed"
		if index < correct {
			judgment = "correct"
		} else if index < correct+incorrect {
			judgment = "incorrect"
		}
		_, err := f.pool.Exec(f.t.Context(), `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at) VALUES($1,$2,$3,$4,$5,'q','C3 RED','fixture','succeeded',$6,$7,$7)`, fmt.Sprintf("%s-%d", batch, index), c3REDUser, c3REDTarget(id), batch, model, judgment, stamp)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	return batch
}

func (f *c3REDFixture) manage(id, model string) map[string]any {
	f.t.Helper()
	return f.expect("POST", "model-control/managed", map[string]any{"targetId": c3REDTarget(id), "modelName": model}, 200)
}

func (f *c3REDFixture) item(id, model string) map[string]any {
	f.t.Helper()
	result := f.expect("GET", "model-control/targets/"+c3REDTarget(id), nil, 200)
	for _, raw := range result["items"].([]any) {
		item := raw.(map[string]any)
		if item["modelName"] == model {
			return item
		}
	}
	f.t.Fatalf("managed model %s missing: %v", model, result)
	return nil
}

func (f *c3REDFixture) preview(id, model, operation string) map[string]any {
	f.t.Helper()
	item := f.item(id, model)
	return f.expect("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget(id), "modelName": model, "operation": operation, "basis": item["basis"]}, 200)
}

func (f *c3REDFixture) execute(id, model, operation string, preview map[string]any) (int, map[string]any) {
	f.t.Helper()
	route := operation
	if route == "close_account" {
		route = "close-account"
	}
	return f.request("POST", "model-control/"+route, map[string]any{"targetId": c3REDTarget(id), "modelName": model, "basis": preview["item"].(map[string]any)["basis"], "planFingerprint": preview["planFingerprint"], "confirmWithoutEvidence": true})
}

func (f *c3REDFixture) close(id, model string) map[string]any {
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

func (f *c3REDFixture) verify(id string) map[string]any {
	f.t.Helper()
	return f.expect("POST", "model-control/verify", map[string]any{"targetIds": []string{c3REDTarget(id)}}, 200)
}

func (f *c3REDFixture) change(id string, change func(map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.accounts[id])
}
func (f *c3REDFixture) mapping(id string, mapping map[string]string) {
	f.change(id, func(account map[string]any) { account["credentials"].(map[string]any)["model_mapping"] = mapping })
}
func (f *c3REDFixture) writeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.writes) }
func (f *c3REDFixture) closed(id, model string) map[string]any {
	return f.item(id, model)["control"].(map[string]any)["closedEntries"].(map[string]any)
}
func (f *c3REDFixture) agePending(id, model string, seconds int) {
	f.t.Helper()
	_, err := f.pool.Exec(f.t.Context(), `UPDATE connection_health_model_control_targets SET pending=jsonb_set(pending,'{sendStartedAt}',to_jsonb(clock_timestamp()-make_interval(secs=>$4))) WHERE user_id=$1 AND target_id=$2 AND model_name=$3`, c3REDUser, c3REDTarget(id), model, seconds)
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *c3REDFixture) eventCount(event string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.t.Context(), `SELECT count(*) FROM connection_health_model_control_events WHERE user_id=$1 AND event_type=$2`, c3REDUser, event).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestC3ModelControlRED01PerModelNewestRoundNeverFallsBack(t *testing.T) {
	f := newC3REDFixture(t)
	a := f.round("1", "A", 1, 2, 0)
	f.round("1", "B", 3, 0, 0)
	f.manage("1", "A")
	f.manage("1", "B")
	if item := f.item("1", "A"); item["decision"] != "close_recommended" || item["basis"].(map[string]any)["batchId"] != a {
		t.Fatalf("A lost its own evidence: %v", item)
	}
	if f.item("1", "B")["decision"] != "usable" {
		t.Fatal("B must use its own round")
	}
	f.round("1", "A", 2, 1, 0)
	if f.item("1", "A")["decision"] != "usable" {
		t.Fatal("new A round did not replace old A")
	}
	f.round("1", "A", 2, 0, 0)
	if f.item("1", "A")["decision"] != "insufficient" {
		t.Fatal("insufficient newest round fell back")
	}
}

func TestC3ModelControlRED02ScheduledSourceWindow(t *testing.T) {
	f := newC3REDFixture(t)
	batch := f.round("1", "A", 1, 2, 0)
	_, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedules(id,user_id,admin_account_id,name,target_mode,selected_account_target_ids,model_names,question_ids,reasoning_effort,repeat_count,peak_start,peak_end,peak_interval_minutes,off_peak_interval_minutes,enabled,created_by) VALUES('c3-plan',$1,'ws1','C3 plan','accounts',ARRAY[$2],ARRAY['A'],ARRAY['q'],'medium',1,'08:00','18:00',30,60,true,$1)`, c3REDUser, c3REDTarget("1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedule_executions(id,user_id,admin_account_id,schedule_id,trigger,scheduled_for,status,config_snapshot) VALUES('c3-execution',$1,'ws1','c3-plan','scheduled',now(),'completed','{}')`, c3REDUser)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_schedule_execution_targets(id,execution_id,target_id,account_snapshot,requested_models,status) VALUES($1,'c3-execution',$2,'{}',ARRAY['A'],'batch_created')`, batch, c3REDTarget("1"))
	if err != nil {
		t.Fatal(err)
	}
	f.round("1", "A", 3, 0, 0)
	item := f.manage("1", "A")
	if item["decision"] != "usable" || !item["rule"].(map[string]any)["includeManual"].(bool) || !item["rule"].(map[string]any)["includeScheduled"].(bool) {
		t.Fatal("workspace rule must include manual and scheduled sources")
	}
	for i := 0; i < 20; i++ {
		f.round("1", "A", 3, 0, 0)
	}
	pair := modelControlPair{c3REDTarget("1"), "A"}
	rounds, err := f.service.modelControls.ListModelControlRounds(t.Context(), c3REDUser, []modelControlPair{pair})
	if err != nil || len(rounds[pair]) != 20 {
		t.Fatal("model rounds no longer bounded", rounds, err)
	}
	for _, round := range rounds[pair] {
		if round.BatchID == batch {
			t.Fatal("searched beyond twenty model rounds")
		}
	}

}

func TestC3ModelControlRED03CloseOnlyMappingAliases(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	result := f.close("1", "A")
	if result["outcome"] != "closed" || f.writeCount() != 1 {
		t.Fatalf("close result=%v writes=%d", result, f.writeCount())
	}
	f.mu.Lock()
	body := f.writes[0]
	f.mu.Unlock()
	want := map[string]any{"account_ids": []any{float64(1)}, "credentials": map[string]any{"model_mapping": map[string]any{"b": "B"}}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("unexpected mutation fields: %v", body)
	}
	if !reflect.DeepEqual(f.closed("1", "A"), map[string]any{"a": "A", "a-alias": "A"}) {
		t.Fatal("alias ownership missing")
	}
}

func TestC3ModelControlRED04LastModelUsesExistingSchedulableCore(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.mapping("1", map[string]string{"a": "A"})
			if limited {
				f.change("2", func(a map[string]any) { a["rate_limit_reset_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano) })
			}
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			preview := f.preview("1", "A", "close")
			if preview["blockReasonKey"] == "" {
				t.Fatal("last model was allowed as mapping close")
			}
			if f.writeCount() != 0 {
				t.Fatal("last model wrote mapping")
			}
			preview = f.preview("1", "A", "close_account")
			if limited {
				if preview["blockReasonKey"] == "" {
					t.Fatal("limited survivor counted")
				}
				f.expect("POST", "targets/"+c3REDTarget("1")+"/schedulable", map[string]any{"schedulable": false}, 200)
				if f.scheduleWrites != 1 || f.writeCount() != 0 {
					t.Fatal("original manual scheduling entry changed its checks")
				}
				return
			}
			code, result := f.execute("1", "A", "close_account", preview)
			if code != 200 || f.scheduleWrites != 1 || f.writeCount() != 0 || len(f.closed("1", "A")) != 0 {
				t.Fatalf("schedulable delegation failed: %d %v", code, result)
			}
		})
	}
}

func TestC3ModelControlRED05SameModelFloorPersistsAndCanRetry(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("2", map[string]string{"b": "B"})
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	preview := f.preview("1", "A", "close")
	if preview["blockReasonKey"] == "" {
		t.Fatal("other model falsely counted as same-model source")
	}
	control := f.item("1", "A")["control"].(map[string]any)
	if control["lastAttempt"] == nil || !strings.Contains(fmt.Sprint(control["lastAttempt"]), "a") {
		t.Fatal("floor reason not persisted")
	}
	if f.eventCount("close_blocked") != 1 || f.writeCount() != 0 {
		t.Fatal("blocked close not recorded or wrote remote")
	}
	f.mapping("2", map[string]string{"a": "A", "a-alias": "A"})
	f.change("2", func(a map[string]any) { a["rate_limit_reset_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano) })
	if f.preview("1", "A", "close")["blockReasonKey"] == "" {
		t.Fatal("globally rate-limited survivor counted")
	}
	f.change("2", func(a map[string]any) { a["rate_limit_reset_at"] = nil })
	f.close("1", "A")
}

func TestC3ModelControlRED06UnsupportedMappingStatesNeverWrite(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"passthrough", func(a map[string]any) { a["extra"] = map[string]any{"openai_passthrough": true} }},
		{"legacy_passthrough", func(a map[string]any) {
			a["extra"] = map[string]any{"openai_passthrough": "invalid", "openai_oauth_passthrough": true}
		}},
		{"empty", func(a map[string]any) { a["credentials"].(map[string]any)["model_mapping"] = map[string]string{} }},
		{"wildcard", func(a map[string]any) {
			a["credentials"].(map[string]any)["model_mapping"] = map[string]string{"gpt-*": "A", "b": "B"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.change("1", tc.change)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			if p["blockReasonKey"] == "" || f.writeCount() != 0 {
				t.Fatalf("unsupported account allowed: %v", p)
			}
		})
	}
}

func TestC3ModelControlRED07RestoreDoesNotOverwriteHumanValue(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.close("1", "A")
	f.mapping("1", map[string]string{"a": "X", "b": "B"})
	p := f.preview("1", "A", "restore")
	code, result := f.execute("1", "A", "restore", p)
	if code != 200 || result["outcome"] != "restored" {
		t.Fatalf("restore failed: %d %v", code, result)
	}
	f.mu.Lock()
	mapping := c3REDClone(f.accounts["1"]["credentials"].(map[string]any)["model_mapping"])
	f.mu.Unlock()
	if !reflect.DeepEqual(mapping, map[string]any{"a": "X", "a-alias": "A", "b": "B"}) || len(f.closed("1", "A")) != 0 || f.eventCount("manual_takeover") == 0 {
		t.Fatal("restore overwrote human value or retained ownership")
	}
}

func TestC3ModelControlRED08ReceiptsAndUnknownGate(t *testing.T) {
	for _, status := range []int{400, 502, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			f.bulkCode = status
			if status == 200 {
				f.afterWrite = func() { f.mu.Lock(); f.failDetail = true; f.mu.Unlock() }
			}
			code, result := f.execute("1", "A", "close", p)
			if code != 200 {
				t.Fatalf("close failed HTTP contract: %d %v", code, result)
			}
			control := f.item("1", "A")["control"].(map[string]any)
			if status == 400 {
				if control["pending"] != nil || control["unconfirmedClose"] != nil || len(f.closed("1", "A")) != 0 {
					t.Fatal("explicit refusal left uncertain deletion")
				}
				return
			}
			if result["outcome"] != "unknown" || control["pending"] == nil {
				t.Fatal("unknown write reported success or forgot pending")
			}
			f.expect("POST", "model-control/close", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "basis": f.item("1", "A")["basis"], "planFingerprint": p["planFingerprint"]}, 409)
			f.mu.Lock()
			f.failDetail = false
			f.mu.Unlock()
			if status == 502 {
				f.verify("1")
				if f.item("1", "A")["control"].(map[string]any)["pending"] == nil {
					t.Fatal("unknown write finalized inside 30 seconds")
				}
				f.agePending("1", "A", 31)
			}
			f.verify("1")
			control = f.item("1", "A")["control"].(map[string]any)
			if control["pending"] != nil {
				t.Fatal("verify did not finalize")
			}
			if status == 200 && control["unconfirmedClose"] != nil {
				t.Fatal("explicit applied receipt created unconfirmed close")
			}
			if status == 502 && control["unconfirmedClose"] == nil {
				t.Fatal("unknown rejection lost late-write reservation")
			}
			if f.writeCount() != 1 {
				t.Fatal("verify sent reverse mutation")
			}
		})
	}
}

func TestC3ModelControlRED09PartialAliasesRemainOwned(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.afterWrite = func() { f.mapping("1", map[string]string{"a-alias": "A", "b": "B"}) }
	result := f.close("1", "A")
	if result["outcome"] != "partial" || !reflect.DeepEqual(f.closed("1", "A"), map[string]any{"a": "A"}) {
		t.Fatalf("partial aliases lost: %v", result)
	}
	f.afterWrite = nil
	f.close("1", "A")
	if len(f.closed("1", "A")) != 2 {
		t.Fatal("second close replaced earlier ownership")
	}
}

func TestC3ModelControlRED10PartialHumanRestoreDoesNotClearOtherAlias(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.close("1", "A")
	f.mapping("1", map[string]string{"a": "A", "b": "B"})
	f.verify("1")
	if !reflect.DeepEqual(f.closed("1", "A"), map[string]any{"a-alias": "A"}) {
		t.Fatal("human restore cleared remaining closed alias")
	}
}

func TestC3ModelControlRED11PendingPreventsRemoveAndRulesHaveForeignKey(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	item := f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.bulkCode = 502
	f.execute("1", "A", "close", p)
	f.expect("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": f.item("1", "A")["version"]}, 409)
	f.expect("DELETE", "model-control/rules", map[string]any{"modelName": "A", "expectedVersion": item["rule"].(map[string]any)["version"]}, 404)
	if f.item("1", "A")["control"].(map[string]any)["pending"] == nil {
		t.Fatal("rejected removal destroyed pending")
	}
}

func TestC3ModelControlRED12HistoryDropsAllTimeAndPreservesToday(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	result := f.expect("GET", "targets/"+c3REDTarget("1")+"/question-answers/history", nil, 200)
	if _, exists := result["allTimeStats"]; exists {
		t.Fatal("allTimeStats must be absent")
	}
	stats := result["todayStats"].(map[string]any)["reviews"].(map[string]any)
	if stats["correct"] != float64(1) || stats["incorrect"] != float64(2) {
		t.Fatalf("today stats changed: %v", stats)
	}
}

func TestC3ModelControlRED13ChangedMappingRequiresNewConfirmation(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "a2": "A", "b": "B"})
	f.mapping("2", map[string]string{"a": "A", "a-alias": "A", "a2": "A"})
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "modelControlPlanChanged") || f.writeCount() != 0 {
		t.Fatalf("changed mapping was not rejected: %d %v", code, result)
	}
	f.close("1", "A")
	if len(f.closed("1", "A")) != 3 {
		t.Fatal("new confirmation missed new alias")
	}
}

func TestC3ModelControlRED14DeletedAccountRequiresCompleteInventory(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			f.bulkCode = 502
			f.execute("1", "A", "close", p)
			f.mu.Lock()
			delete(f.accounts, "1")
			f.failInventory = incomplete
			f.mu.Unlock()
			f.verify("1")
			control := f.item("1", "A")["control"].(map[string]any)
			if incomplete {
				if control["pending"] == nil {
					t.Fatal("missing detail alone finalized pending")
				}
				return
			}
			if control["pending"] != nil || control["observation"].(map[string]any)["state"] != "account_missing" || f.eventCount("account_missing_resolved") == 0 {
				t.Fatal("confirmed missing account not finalized before 30-second gate")
			}
			f.expect("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": f.item("1", "A")["version"], "abandonClosed": true}, 204)
		})
	}
}

func TestC3ModelControlRED15SchedulableObservationTracksHumanReopen(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("1", map[string]string{"a": "A"})
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close_account")
	code, result := f.execute("1", "A", "close_account", p)
	if code != 200 {
		t.Fatalf("close account: %d %v", code, result)
	}
	if f.item("1", "A")["control"].(map[string]any)["observation"].(map[string]any)["accountSchedulable"] != false {
		t.Fatal("schedulable close observation missing")
	}
	f.round("1", "A", 3, 0, 0)
	items := f.expect("GET", "model-control/items?view=attention", nil, 200)["items"].([]any)
	if len(items) != 1 {
		t.Fatal("usable manually paused account missing from attention")
	}
	f.expect("POST", "targets/"+c3REDTarget("1")+"/schedulable", map[string]any{"schedulable": true}, 200)
	f.verify("1")
	if f.item("1", "A")["control"].(map[string]any)["observation"].(map[string]any)["accountSchedulable"] != true {
		t.Fatal("manual reopen not observed")
	}
}

func TestC3ModelControlRED16AlreadyRestoredHasZeroRemoteWrite(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.close("1", "A")
	f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B"})
	p := f.preview("1", "A", "restore")
	if p["blockReasonKey"] != "" && p["blockReasonKey"] != nil {
		t.Fatalf("already restored was blocked: %v", p)
	}
	code, result := f.execute("1", "A", "restore", p)
	if code != 200 || result["outcome"] != "restored" || f.writeCount() != 1 || len(f.closed("1", "A")) != 0 {
		t.Fatalf("zero-write restore failed: %d %v", code, result)
	}
}

func TestC3ModelControlRED17PreviewBlocksPersistAndEventsDeduplicate(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("2", map[string]string{"b": "B"})
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.preview("1", "A", "close")
	f.preview("1", "A", "close")
	if f.item("1", "A")["control"].(map[string]any)["lastAttempt"] == nil || f.eventCount("close_blocked") != 1 {
		t.Fatal("preview block missing or duplicated event")
	}
	f.mapping("2", map[string]string{"a": "A", "a-alias": "A"})
	f.close("1", "A")
	f.mapping("1", map[string]string{})
	f.preview("1", "A", "restore")
	if f.item("1", "A")["control"].(map[string]any)["conflictReason"] == "" {
		t.Fatal("restore conflict was not persisted")
	}
}

func TestC3ModelControlRED18CloseAccountFingerprintChangesBeforeEligibility(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("1", map[string]string{"a": "A"})
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close_account")
	f.mapping("1", map[string]string{"a": "A", "b": "B"})
	code, result := f.execute("1", "A", "close_account", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "modelControlPlanChanged") || f.scheduleWrites != 0 || f.writeCount() != 0 {
		t.Fatalf("changed last-model plan wrote: %d %v", code, result)
	}
}

func TestC3ModelControlRED19ChangedBlockedPlanPersistsNewAlias(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "a2": "A", "b": "B"})
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || f.writeCount() != 0 || !strings.Contains(fmt.Sprint(result), "modelControlPlanChanged") {
		t.Fatal("plan conflict did not reject write")
	}
	attempt := f.item("1", "A")["control"].(map[string]any)["lastAttempt"]
	if attempt == nil || !strings.Contains(fmt.Sprint(attempt), "a2") {
		t.Fatalf("new blocked plan was not persisted: %v", attempt)
	}
	n := f.eventCount("close_blocked")
	f.preview("1", "A", "close")
	if f.eventCount("close_blocked") != n {
		t.Fatal("same block duplicated event")
	}
}

func TestC3ModelControlRED20LeaseLossAtReadbackCannotChangeOwnership(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.afterWrite = func() {
		_, err := f.pool.Exec(context.Background(), `UPDATE connection_health_runtime_leases SET owner_id='other-owner',expires_at=clock_timestamp()-interval '1 second'`)
		if err != nil {
			t.Error(err)
		}
	}
	result := f.close("1", "A")
	if result["outcome"] != "unknown" || len(f.closed("1", "A")) != 0 {
		t.Fatal("lost lease executor changed ownership or reported success")
	}
	f.verify("1")
	if len(f.closed("1", "A")) != 2 {
		t.Fatal("new lease holder failed to reconcile applied receipt")
	}
}

func TestC3ModelControlRED21UnknownCloseDetectsLateApplyWithin24Hours(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			f.bulkCode = 502
			f.close("1", "A")
			f.agePending("1", "A", 31)
			f.verify("1")
			if expired {
				_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET unconfirmed_close=jsonb_set(unconfirmed_close,'{sentAt}',to_jsonb(clock_timestamp()-interval '25 hours')) WHERE user_id=$1`, c3REDUser)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.mapping("1", map[string]string{"b": "B"})
			f.verify("1")
			if expired {
				if len(f.closed("1", "A")) != 0 {
					t.Fatal("expired uncertain write acquired ownership")
				}
				return
			}
			if len(f.closed("1", "A")) != 2 || f.eventCount("close_late_applied") == 0 {
				t.Fatal("late applied close was not recovered")
			}
		})
	}
}

func TestC3ModelControlRED22OldBlockCannotOverwriteNewSuccess(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET attempt_at=clock_timestamp()+interval '1 hour',last_attempt=NULL WHERE user_id=$1`, c3REDUser)
	if err != nil {
		t.Fatal(err)
	}
	f.mapping("2", map[string]string{"b": "B"})
	f.preview("1", "A", "close")
	if f.item("1", "A")["control"].(map[string]any)["lastAttempt"] != nil || f.eventCount("close_blocked") != 0 {
		t.Fatal("older blocked conclusion revived cleared success")
	}
}

func TestC3ModelControlRED23PreparedPast60SecondsAppearsInAttention(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 3, 0, 0)
	f.manage("1", "A")
	for _, age := range []int{30, 61} {
		pending := map[string]any{"id": "c3-prepared", "operation": "close", "phase": "prepared", "entries": map[string]string{"a": "A"}, "afterMapping": map[string]string{"b": "B"}, "basis": map[string]any{"batchId": "old", "ruleVersion": 1, "decision": "close_recommended"}, "startedAt": time.Now().Add(-time.Duration(age) * time.Second).Format(time.RFC3339Nano), "actorUserId": c3REDUser}
		encoded, _ := json.Marshal(pending)
		_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$2::jsonb WHERE user_id=$1`, c3REDUser, string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		items := f.expect("GET", "model-control/items?view=attention", nil, 200)["items"].([]any)
		want := 0
		if age > 60 {
			want = 1
		}
		if len(items) != want {
			t.Fatalf("prepared age=%d attention=%d want=%d", age, len(items), want)
		}
	}
}

func TestC3ModelControlRED24NotSentReceiptFinalizesWithoutUnconfirmedClose(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	pending := map[string]any{"id": "c3-not-sent", "operation": "close", "phase": "sending", "receipt": "not_sent", "entries": map[string]string{"a": "A", "a-alias": "A"}, "afterMapping": map[string]string{"b": "B"}, "basis": map[string]any{"batchId": "old", "ruleVersion": 1, "decision": "close_recommended"}, "startedAt": time.Now().Format(time.RFC3339Nano), "sendStartedAt": time.Now().Format(time.RFC3339Nano), "actorUserId": c3REDUser}
	encoded, _ := json.Marshal(pending)
	_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$2::jsonb WHERE user_id=$1`, c3REDUser, string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	f.verify("1")
	control := f.item("1", "A")["control"].(map[string]any)
	if control["pending"] != nil || control["unconfirmedClose"] != nil || len(f.closed("1", "A")) != 0 || f.writeCount() != 0 {
		t.Fatal("not-sent receipt was treated as unknown or delayed")
	}
}

func TestC3ModelControlRED25UnconfirmedCloseReservationRemoveAndTakeover(t *testing.T) {
	for _, branch := range []string{"floor", "abandon", "changed"} {
		t.Run(branch, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			f.bulkCode = 502
			f.close("1", "A")
			f.agePending("1", "A", 31)
			f.verify("1")
			f.verify("1")
			if f.item("1", "A")["control"].(map[string]any)["unconfirmedClose"] == nil {
				t.Fatal("same original values released late-write reservation")
			}
			switch branch {
			case "floor":
				f.bulkCode = 200
				f.round("2", "A", 1, 2, 0)
				f.manage("2", "A")
				p := f.preview("2", "A", "close")
				if p["blockReasonKey"] == "" || f.writeCount() != 1 {
					t.Fatal("unconfirmed source counted for floor")
				}
			case "abandon":
				item := f.item("1", "A")
				input := map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": item["version"]}
				f.expect("DELETE", "model-control/managed", input, 409)
				input["abandonClosed"] = true
				f.expect("DELETE", "model-control/managed", input, 204)
				events := f.expect("GET", "model-control/events", nil, 200)
				if !strings.Contains(fmt.Sprint(events), "a-alias") {
					t.Fatal("abandon event lost uncertain entries")
				}
			case "changed":
				f.mapping("1", map[string]string{"a": "X", "a-alias": "X", "b": "B"})
				f.verify("1")
				item := f.item("1", "A")
				if item["control"].(map[string]any)["unconfirmedClose"] != nil {
					t.Fatal("human replacement did not release unconfirmed entries")
				}
				f.expect("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": item["version"]}, 204)
			}
		})
	}
}
