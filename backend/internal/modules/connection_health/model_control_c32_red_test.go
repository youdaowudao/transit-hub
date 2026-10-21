package connection_health

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// D1 business expectations fixed by the primary agent before implementation.
// Uses the existing explicitly enabled, isolated PostgreSQL schema; no process
// or listener starts unless TEST_DATABASE_URL is supplied.
func (f *c32REDFixture) freshRound(id, model string, correct, incorrect int) {
	f.t.Helper()
	// Use actual elapsed time, never a future evidence timestamp.
	time.Sleep(2 * time.Millisecond)
	now := time.Now().UTC()
	f.roundAt(id, model, correct, incorrect, 0, now, now)
}
func (f *c32REDFixture) switches(close, restore, health bool) bool {
	f.t.Helper()
	code, current := f.request("GET", "model-control/auto-settings", nil)
	if code == 404 { // measured on d278aa3: ServeMux unknown route
		code, _ = f.request("PUT", "model-control/auto-settings", map[string]any{})
		if code != 404 {
			f.t.Fatalf("baseline unknown PUT status=%d", code)
		}
		f.t.Log("baseline has no automatic settings route; continue to business result")
		return false
	}
	if code != 200 {
		f.t.Fatalf("read auto settings status=%d result=%v", code, current)
	}
	result := f.expect("PUT", "model-control/auto-settings", map[string]any{"autoCloseEnabled": close, "autoRestoreEnabled": restore, "healthCoordinationEnabled": health, "verifyIntervalMinutes": 10, "retryIntervalMinutes": 5, "expectedVersion": current["version"]}, 200)
	if result["autoCloseEnabled"] != close || result["autoRestoreEnabled"] != restore || result["healthCoordinationEnabled"] != health {
		f.t.Fatal("saved switches differ", result)
	}
	return true
}
func (f *c32REDFixture) scan() {
	f.t.Helper()
	started := time.Now().UTC()
	f.service.runSchedulerTickSafely(f.t.Context())
	waitSchedulerPathsIdle(f.t, f.service)
	f.t.Logf("probe calls=%d hard failures=%d", f.probeCalls, f.probeFailures)
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, settings := f.request("GET", "model-control/auto-settings", nil)
		if code == 404 {
			return
		}
		if code != 200 {
			f.t.Fatalf("scan status=%d result=%v", code, settings)
		}
		if stamp, ok := settings["lastScanFinishedAt"].(string); ok {
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err == nil && at.After(started) {
				return
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatal("scan did not finish", settings)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (f *c32REDFixture) closedSources() {
	f.t.Helper()
	for _, id := range []string{"1", "2"} {
		f.manage(id, "A")
		f.freshRound(id, "A", 1, 2)
		f.close(id, "A")
	}
}
func (f *c32REDFixture) monitor(ids []string, models ...string) {
	f.t.Helper()
	targets := []ModelTargetInput{}
	for _, model := range models {
		targets = append(targets, ModelTargetInput{ModelName: model, ProviderFamily: ProviderOpenAI, Enabled: true})
	}
	policy, err := f.service.SavePolicy(f.t.Context(), c32REDUser, PolicyInput{Name: "C32 RED health", Enabled: true, ModelPattern: "*", ProbeIntervalSeconds: 60, FailureThreshold: 3, SuccessThreshold: 2, CooldownSeconds: 60, ObservationSeconds: 60, RecoveryStepPercent: 25, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true, PriorityMode: PriorityModeMultiplier, ModelTargets: targets})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, id := range ids {
		if err = f.service.repo.ReplacePolicyAssignments(f.t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget(id), []string{policy.ID}); err != nil {
			f.t.Fatal(err)
		}
	}
	// The existing Sub2API v2 rule suspends on the third confirmed failure.
	// Supply two prior confirmed failures; this tick must execute the third.
	now := time.Now().UTC().Add(-2 * time.Minute)
	protocol := TestProtocolChatCompletions
	st := defaultTargetState(c32REDUser, c32REDWorkspace, AdminProbeTarget{TargetID: c32REDTarget(f.probeFailAccount)}, "A")
	st.State, st.CurrentWeight, st.ConsecutiveFailures = StateDegraded, 75, 2
	st.RuleVersion = RuleVersionV2
	st.CounterProtocol, st.HealthEvidenceProtocol = &protocol, &protocol
	st.HealthEvidenceStatus = HealthEvidenceValid
	st.FailingSince, st.LastFailureAt = &now, &now
	if err := f.service.repo.UpsertState(f.t.Context(), st); err != nil {
		f.t.Fatal(err)
	}
	// A deterministic in-memory HTTP transport still passes through the real
	// probe decoder, CommitTargetProbe, scheduler and action execution chain.
	f.service.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.probeCalls++
		f.mu.Unlock()
		code, text := http.StatusOK, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`
		if body["model"] == "A" && r.Header.Get("Authorization") == "Bearer fixture-"+f.probeFailAccount {
			f.mu.Lock()
			f.probeFailures++
			f.mu.Unlock()
			code = http.StatusUnauthorized
			text = `{"error":{"message":"fixture hard failure"}}`
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(text))}, nil
	})}}
}
func (f *c32REDFixture) knownMultiplierPriority() {
	f.t.Helper()
	ctx := f.t.Context()
	policies, err := f.service.repo.ListPolicies(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		f.t.Fatal(err)
	}
	states, err := f.service.repo.ListStatesByWorkspace(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		f.t.Fatal(err)
	}
	syncs, err := f.service.repo.ListPrioritySyncStates(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		f.t.Fatal(err)
	}
	inv, err := f.service.loadAdminInventory(ctx, c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		f.t.Fatal(err)
	}
	items := map[string]*priorityTargetInventory{}
	multiplier := float64(1)
	for _, group := range inv.groups {
		for _, a := range group.accounts {
			if a.ID != "1" && a.ID != "2" {
				continue
			}
			targetID := c32REDTarget(a.ID)
			items[targetID] = &priorityTargetInventory{
				target:  AdminProbeTarget{TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: a.ID, AccountStatus: a.Status, Schedulable: a.Schedulable, InventoryComplete: true, Models: []string{"A", "B"}, TestMemberships: []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "c3-red-group"}}},
				account: a, policies: policies, currentPriority: *a.Priority, priorityPresent: true, snapshotStartedAt: inv.snapshotStartedAt,
				upstreamMultiplier: upstreamMultiplierResolution{status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: &multiplier}},
			}
		}
	}
	f.service.syncWorkspacePriorities(ctx, f.session, c32REDUser, c32REDWorkspace, items, true, states, syncs)
	if len(f.priorityWrites) == 0 {
		f.t.Fatal("known-multiplier Priority chain did not write any value")
	}
	f.t.Logf("known-multiplier Priority writes=%v", f.priorityWrites)
}

func (f *c32REDFixture) inactiveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, status := range f.statusWrites {
		if status == "inactive" {
			n++
		}
	}
	return n
}
func (f *c32REDFixture) danger(kind string) (int, map[string]any) {
	f.t.Helper()
	switch kind {
	case "status":
		_, err := f.service.runCompatibilityStatusAction(f.t.Context(), c32REDUser, c32REDWorkspace, f.session, "3", "inactive")
		w := httptestRecorderForC32(err)
		return w.code, w.body
	case "schedulable":
		return f.request("POST", "targets/"+c32REDTarget("3")+"/schedulable", map[string]any{"schedulable": false})
	default:
		err := f.service.DeleteManagedSub2APIAccount(f.t.Context(), c32REDUser, c32REDWorkspace, f.session, "3", ActionSourceManualDelete)
		w := httptestRecorderForC32(err)
		return w.code, w.body
	}
}

type c32HTTPResult struct {
	code int
	body map[string]any
}

func httptestRecorderForC32(err error) c32HTTPResult {
	if err == nil {
		return c32HTTPResult{200, map[string]any{}}
	}
	// The exact production response mapper is used for the shared safe-delete
	// and compatibility service entrypoints, which have no standalone route.
	w := httptest.NewRecorder()
	writeError(w, err)
	result := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return c32HTTPResult{w.Code, result}
}

func TestModelControlC32RED1HealthLastModelSource(t *testing.T) {
	f := newC32REDFixture(t)
	f.closedSources()
	f.switches(false, false, true)
	f.monitor([]string{"1", "2", "3"}, "A")
	f.scan()
	if n := f.inactiveCount(); n != 0 {
		t.Errorf("last A source must remain active; inactive requests=%d", n)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_events WHERE connection_id=$1 AND remote_action='skipped_sub2api_group_last_model_source' AND admin_group_id='1'`, c32REDTarget("3")).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("last-model protection event with group 1 count=%d want=1", count)
	}
}
func TestModelControlC32RED2UnmonitoredManualDanger(t *testing.T) {
	for _, kind := range []string{"status", "schedulable", "delete"} {
		t.Run(kind, func(t *testing.T) {
			f := newC32REDFixture(t)
			f.closedSources()
			f.switches(false, false, true)
			code, body := f.danger(kind)
			if code != 409 || body["message"] != "admin.connectionHealth.errors.sub2apiGroupLastModelSource" || body["groupId"] != "1" {
				t.Errorf("unmonitored last A source %s response=%d/%v want 409/new key/group1", kind, code, body)
			}
			if f.inactiveCount() != 0 || f.scheduleWrites != 0 || len(f.deleteWrites) != 0 {
				t.Errorf("protected action reached main site: status=%v schedule=%d delete=%v", f.statusWrites, f.scheduleWrites, f.deleteWrites)
			}
		})
	}
}
func TestModelControlC32RED3ClosedProbeCannotSuspendAccount(t *testing.T) {
	f := newC32REDFixture(t)
	f.probeFailAccount = "1"
	f.manage("1", "A")
	f.freshRound("1", "A", 1, 2)
	f.close("1", "A")
	f.switches(false, false, true)
	f.monitor([]string{"1", "2"}, "A", "B")
	f.scan()
	f.knownMultiplierPriority()
	if n := f.inactiveCount(); n != 0 {
		t.Errorf("closed A failure must not suspend account with healthy B; inactive requests=%d", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range f.priorityWrites {
		if value >= 59000 {
			t.Errorf("closed A failure wrote suspended Priority=%d", value)
		}
	}
}
func TestModelControlC32RED4OnlyNewEvidenceAutoCloses(t *testing.T) {
	f := newC32REDFixture(t)
	f.manage("1", "A")
	f.freshRound("1", "A", 1, 2)
	f.switches(true, false, false)
	f.scan()
	if n := f.writeCount(); n != 0 {
		t.Fatalf("pre-enable round wrote whitelist %d times", n)
	}
	f.freshRound("1", "A", 1, 2)
	f.scan()
	if n := f.writeCount(); n != 1 {
		t.Errorf("post-enable completed round whitelist writes=%d want=1", n)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_events WHERE event_type='close_succeeded' AND actor_user_id=''`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("automatic close success actor empty count=%d want=1", count)
	}
}
func TestModelControlC32RED5ExternalChangeWaitsForNewRound(t *testing.T) {
	f := newC32REDFixture(t)
	f.manage("1", "A")
	f.freshRound("1", "A", 1, 2)
	f.close("1", "A")
	f.switches(true, false, false)
	f.freshRound("1", "A", 1, 2)
	before := f.writeCount()
	f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B"})
	f.scan()
	if closed := f.closed("1", "A"); len(closed) != 0 {
		t.Errorf("background read must remove externally restored ownership: %v", closed)
	}
	if n := f.writeCount(); n != before {
		t.Errorf("round preceding external change wrote whitelist: %d want=%d", n, before)
	}
	item := f.item("1", "A")
	auto, _ := item["auto"].(map[string]any)
	if auto["state"] != "waiting" {
		t.Errorf("changed model must wait for new round; auto=%v", auto)
	}
	f.freshRound("1", "A", 1, 2)
	f.scan()
	if n := f.writeCount(); n != before+1 {
		t.Errorf("new round after external change writes=%d want=%d", n, before+1)
	}
}
func TestModelControlC32Equivalent6DefaultBackgroundDoesNothing(t *testing.T) {
	f := newC32REDFixture(t)
	f.manage("1", "A")
	f.freshRound("1", "A", 1, 2)
	f.close("1", "A")
	beforeClosed := f.closed("1", "A")
	beforeWrites, beforeDetail := f.writeCount(), f.detailReads
	var eventsBefore, eventsAfter int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if f.writeCount() != beforeWrites || f.detailReads != beforeDetail || eventsAfter != eventsBefore {
		t.Fatalf("default background changed requests/events: writes=%d/%d details=%d/%d events=%d/%d", f.writeCount(), beforeWrites, f.detailReads, beforeDetail, eventsAfter, eventsBefore)
	}
	if !reflect.DeepEqual(f.closed("1", "A"), beforeClosed) {
		t.Fatal("default background changed ownership")
	}
	code, settings := f.request("GET", "model-control/auto-settings", nil)
	if code == 200 {
		if settings["lastScanStartedAt"] != nil {
			t.Fatal("default scan ran", settings)
		}
		var n int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_model_control_targets WHERE auto_verify_at IS NOT NULL OR auto_state IS NOT NULL`).Scan(&n); err != nil || n != 0 {
			t.Fatal("default wrote automatic state", n, err)
		}
	} else if code != 404 {
		t.Fatal("unexpected settings status", code)
	}
}
func TestModelControlC32Equivalent7DefaultHealthAndManualPreserved(t *testing.T) {
	t.Run("health", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.closedSources()
		f.monitor([]string{"1", "2", "3"}, "A")
		f.service.runSchedulerTickSafely(t.Context())
		waitSchedulerPathsIdle(t, f.service)
		t.Logf("probe calls=%d hard failures=%d", f.probeCalls, f.probeFailures)
		rows, err := f.pool.Query(t.Context(), `SELECT model_name, result, COALESCE(probe_disposition,''), COALESCE(remote_action,'') FROM connection_health_events ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a, b, c, d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			t.Logf("model=%s result=%s disposition=%s action=%s", a, b, c, d)
		}
		rows.Close()
		if f.inactiveCount() == 0 {
			t.Fatal("default must preserve health suspension")
		}
		c32AssertDefaultFields(t, f)
	})
	for _, kind := range []string{"status", "schedulable", "delete"} {
		t.Run(kind, func(t *testing.T) {
			f := newC32REDFixture(t)
			f.closedSources()
			code, body := f.danger(kind)
			if code != 200 {
				t.Fatal("default manual action changed", kind, code, body)
			}
			if f.inactiveCount()+f.scheduleWrites+len(f.deleteWrites) != 1 {
				t.Fatal("default action did not reach main site")
			}
			c32AssertDefaultFields(t, f)
		})
	}
	t.Run("closed-probe", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.probeFailAccount = "1"
		f.manage("1", "A")
		f.freshRound("1", "A", 1, 2)
		f.close("1", "A")
		f.monitor([]string{"1", "2"}, "A", "B")
		f.service.runSchedulerTickSafely(t.Context())
		waitSchedulerPathsIdle(t, f.service)
		f.knownMultiplierPriority()
		hasSuspendedPriority := false
		for _, value := range f.priorityWrites {
			if value >= 59000 {
				hasSuspendedPriority = true
			}
		}
		if !hasSuspendedPriority {
			t.Fatal("default closed failure must still write suspended Priority")
		}
		t.Logf("probe calls=%d hard failures=%d", f.probeCalls, f.probeFailures)
		rows, err := f.pool.Query(t.Context(), `SELECT model_name, result, COALESCE(probe_disposition,''), COALESCE(remote_action,'') FROM connection_health_events ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a, b, c, d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			t.Logf("model=%s result=%s disposition=%s action=%s", a, b, c, d)
		}
		rows.Close()
		if f.inactiveCount() == 0 {
			t.Fatal("default closed probe must still suspend")
		}
		c32AssertDefaultFields(t, f)
	})
}
func c32AssertDefaultFields(t *testing.T, f *c32REDFixture) {
	t.Helper()
	items := f.expect("GET", "model-control/items", nil, 200)
	for _, raw := range items["items"].([]any) {
		item := raw.(map[string]any)
		if _, ok := item["auto"]; ok {
			t.Fatal("default auto field appeared")
		}
		if _, ok := item["healthCoordination"]; ok {
			t.Fatal("default coordination field appeared")
		}
	}
	groups := f.expect("GET", "admin-groups", nil, 200)
	rows, ok := groups["groups"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatal("default health assertion did not receive any groups")
	}
	accountCount := 0
	for _, raw := range rows {
		group, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("default health assertion received an invalid group")
		}
		accounts, ok := group["accounts"].([]any)
		if !ok {
			t.Fatal("default health assertion did not receive account rows")
		}
		accountCount += len(accounts)
	}
	if accountCount < 2 {
		t.Fatalf("default health assertion checked %d accounts, want at least two", accountCount)
	}
	data, _ := json.Marshal(groups)
	for _, key := range []string{`"modelControlClosed"`, `"modelControlNoActiveProbe"`, `"modelFloorLastSources"`} {
		if strings.Contains(string(data), key) {
			t.Fatal("default health field appeared", key)
		}
	}
}
