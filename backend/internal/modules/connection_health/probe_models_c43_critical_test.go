package connection_health

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// Primary-agent business assertions, frozen before C4.3 implementation. Existing
// C3.2 fixtures explicitly require TEST_DATABASE_URL and clean their own schemas.
func c43CriticalFixture(t *testing.T) *c32REDFixture {
	t.Helper()
	f := newC32REDFixture(t)
	f.service.backgroundTasksDisabled = true
	f.accounts = map[string]map[string]any{}
	mappings := []map[string]string{
		{"A": "A", "B": "B", "C": "C"}, {"B": "B", "C": "C"}, {"C": "C"},
		{"other": "other"}, {"A": "renamed", "B": "B"}, {}, {"other": "other"},
	}
	for n, mapping := range mappings {
		id := strconv.Itoa(n + 1)
		f.accounts[id] = c32REDAccount(n+1, mapping)
		f.accounts[id]["credentials"].(map[string]any)["api_key"] = "fixture-" + id
	}
	f.accounts["7"]["extra"].(map[string]any)["openai_passthrough"] = true
	f.service.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(r *http.Request) (*http.Response, error) {
		body := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	return f
}
func c43CriticalInput(models ...string) PolicyInput {
	input := PolicyInput{Name: "C43 critical", Enabled: true, ModelPattern: "*", ProbeIntervalSeconds: 60, AutoDegradeEnabled: true, DailyProbeBudget: 1000}
	for _, name := range models {
		input.ModelTargets = append(input.ModelTargets, ModelTargetInput{ModelName: name, ProviderFamily: ProviderOpenAI, Enabled: true})
	}
	return input
}
func c43CriticalPolicy(t *testing.T, f *c32REDFixture, first bool, models ...string) Policy {
	t.Helper()
	p, err := f.service.SavePolicy(t.Context(), c32REDUser, c43CriticalInput(models...))
	if err != nil {
		t.Fatal(err)
	}
	if first {
		if _, err = f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET probe_mode='first_available' WHERE id=$1`, p.ID); err != nil {
			t.Fatal(err)
		}
		for n, m := range p.ModelTargets {
			if _, err = f.pool.Exec(t.Context(), `UPDATE connection_health_model_targets SET created_at='2026-01-01'::timestamptz+$2::int*interval '1 microsecond' WHERE id=$1`, m.ID, n); err != nil {
				t.Fatal(err)
			}
		}
	}
	for id := range f.accounts {
		if err = f.service.repo.ReplacePolicyAssignments(t.Context(), c32REDUser, c32REDWorkspace, c32REDTarget(id), []string{p.ID}); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := f.service.repo.GetPolicy(t.Context(), p.ID, c32REDUser, c32REDWorkspace)
	if err != nil || saved == nil {
		t.Fatal(err)
	}
	return *saved
}
func c43CriticalSelected(id string) string {
	return map[string]string{"1": "A", "2": "B", "3": "C", "4": "C", "5": "B", "6": "A", "7": "A"}[id]
}
func c43CriticalEvents(t *testing.T, f *c32REDFixture) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	rows, err := f.pool.Query(t.Context(), `SELECT connection_id,model_name FROM connection_health_events WHERE source='scheduled' ORDER BY model_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, model string
		if err = rows.Scan(&id, &model); err != nil {
			t.Fatal(err)
		}
		result[id] = append(result[id], model)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestProbeModelC43K1ScheduledChoosesOne(t *testing.T) {
	f := c43CriticalFixture(t)
	c43CriticalPolicy(t, f, true, "A", "B", "C")
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	// Seven accounts exceed the workspace cap of six; admit the remainder on the next tick.
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	got := c43CriticalEvents(t, f)
	for id := range f.accounts {
		want := []string{c43CriticalSelected(id)}
		if !reflect.DeepEqual(got[c32REDTarget(id)], want) {
			t.Errorf("account %s scheduled models=%v want=%v", id, got[c32REDTarget(id)], want)
		}
	}
}
func c43CriticalPage(t *testing.T, f *c32REDFixture) map[string][]string {
	t.Helper()
	groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]string{}
	for _, g := range groups {
		for _, a := range g.Accounts {
			models := []string{}
			for _, m := range a.ModelHealth {
				models = append(models, m.ModelName)
			}
			for _, m := range a.UnprobedModels {
				models = append(models, m.ModelName)
			}
			sort.Strings(models)
			result[a.ID] = models
		}
	}
	return result
}
func TestProbeModelC43K2PageShowsOnlySelected(t *testing.T) {
	f := c43CriticalFixture(t)
	c43CriticalPolicy(t, f, true, "A", "B", "C")
	got := c43CriticalPage(t, f)
	for id := range f.accounts {
		want := []string{c43CriticalSelected(id)}
		if !reflect.DeepEqual(got[id], want) {
			t.Errorf("account %s page models=%v want=%v", id, got[id], want)
		}
	}
}
func c43CriticalState(t *testing.T, f *c32REDFixture, id, model string, state State, recent bool) {
	t.Helper()
	at := time.Now().UTC().Add(-2 * time.Minute)
	if recent {
		at = time.Now().UTC()
	}
	protocol := TestProtocolChatCompletions
	latency := 200
	s := defaultTargetState(c32REDUser, c32REDWorkspace, AdminProbeTarget{TargetID: c32REDTarget(id)}, model)
	s.State = state
	s.RuleVersion = RuleVersionV2
	s.LastProbeAt = &at
	s.LastSuccessAt = &at
	s.LastLatencyMs = &latency
	s.LastSuccessLatencyMs = &latency
	s.LastFirstTokenMs = &latency
	s.LastProbeProtocol = &protocol
	s.CounterProtocol = &protocol
	s.HealthEvidenceProtocol = &protocol
	s.LastSuccessProtocol = &protocol
	s.HealthEvidenceStatus = HealthEvidenceValid
	if state == StateSuspended {
		until := time.Now().UTC().Add(time.Hour)
		s.CooldownUntil = &until
		s.ConsecutiveFailures = 3
		s.LastFailureAt = &at
		s.CurrentWeight = 0
	}
	if err := f.service.repo.UpsertState(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}
func c43CriticalPriorityResult(t *testing.T, first bool) (int, []int) {
	t.Helper()
	f := c43CriticalFixture(t)
	f.accounts = map[string]map[string]any{"1": f.accounts["2"]}
	f.accounts["1"]["id"] = 1
	models := []string{"B"}
	if first {
		models = []string{"A", "B", "C"}
	}
	p := c43CriticalPolicy(t, f, first, models...)
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET priority_mode='multiplier' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	c43CriticalState(t, f, "1", "B", StateHealthy, true)
	policies, err := f.service.repo.ListPolicies(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	mult := 1.0
	lookup := upstreamMultiplierLookup{byAccount: map[string]upstreamMultiplierResolution{"1": {status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: &mult}}}}
	items, complete, err := f.service.priorityInventoryForSnapshot(snapshot, c32REDWorkspace, map[string][]Policy{c32REDTarget("1"): policies}, nil, nil, nil, lookup)
	if err != nil || !complete {
		t.Fatal(err, complete)
	}
	states, err := f.service.repo.ListStatesByWorkspace(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	f.service.syncWorkspacePriorities(t.Context(), f.session, c32REDUser, c32REDWorkspace, items, complete, states, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	value := f.accounts["1"]["priority"]
	priority, ok := value.(int)
	if !ok {
		priority = int(value.(float64))
	}
	return priority, append([]int{}, f.priorityWrites...)
}
func TestProbeModelC43K3PriorityMatchesSingleModel(t *testing.T) {
	expected, writes := c43CriticalPriorityResult(t, false)
	got, gotWrites := c43CriticalPriorityResult(t, true)
	if len(writes) != 1 {
		t.Fatal("single-model fixture must perform one Priority write", expected, writes)
	}
	if got != expected || !reflect.DeepEqual(gotWrites, writes) {
		t.Fatalf("selected Priority=%d writes=%v; single-model Priority=%d writes=%v", got, gotWrites, expected, writes)
	}
}
func TestProbeModelC43K4OldSuspendedModelDoesNotStopAccount(t *testing.T) {
	f := c43CriticalFixture(t)
	f.accounts = map[string]map[string]any{"1": f.accounts["1"], "2": f.accounts["2"], "3": f.accounts["3"]}
	f.mapping("1", map[string]string{"B": "B"})
	f.mapping("2", map[string]string{"A": "A", "B": "B", "C": "C"})
	f.mapping("3", map[string]string{"A": "A", "B": "B", "C": "C"})
	p := c43CriticalPolicy(t, f, true, "A", "B", "C")
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET auto_remote_action_enabled=true WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	for id := range f.accounts {
		for _, model := range []string{"A", "B", "C"} {
			state := StateHealthy
			if id == "1" && model == "A" {
				state = StateSuspended
			}
			c43CriticalState(t, f, id, model, state, model != "B")
		}
	}
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accounts["1"]["status"] != "active" {
		t.Fatalf("selected B is healthy but account was stopped: status=%v writes=%v", f.accounts["1"]["status"], f.statusWrites)
	}
}
func TestProbeModelC43K5BothWritesPersistModeAndOrder(t *testing.T) {
	for _, path := range []string{"policy", "group quick policy"} {
		t.Run(path, func(t *testing.T) {
			f := c43CriticalFixture(t)
			payload := map[string]any{"name": "C43 order", "enabled": true, "autoDegradeEnabled": true, "probeMode": "first_available", "modelTargets": []map[string]any{{"modelName": "C", "providerFamily": "openai", "enabled": true}, {"modelName": "A", "providerFamily": "openai", "enabled": true}, {"modelName": "B", "providerFamily": "openai", "enabled": true}}}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var in PolicyInput
			if err = json.Unmarshal(encoded, &in); err != nil {
				t.Fatal(err)
			}
			var id string
			if path == "policy" {
				p, e := f.service.SavePolicy(t.Context(), c32REDUser, in)
				if e != nil {
					t.Fatal(e)
				}
				id = p.ID
			} else {
				c, e := f.service.SetAdminGroupPolicyConfiguration(t.Context(), c32REDUser, "1", AdminGroupPolicyConfigurationInput{QuickPolicy: &in})
				if e != nil {
					t.Fatal(e)
				}
				if len(c.PolicyIDs) != 1 {
					t.Fatal(c)
				}
				id = c.PolicyIDs[0]
			}
			saved, err := f.service.repo.GetPolicy(t.Context(), id, c32REDUser, c32REDWorkspace)
			if err != nil || saved == nil {
				t.Fatal(err)
			}
			if saved.ProbeMode != "first_available" {
				t.Errorf("saved mode=%s want first_available", saved.ProbeMode)
			}
			names := []string{}
			for _, m := range saved.ModelTargets {
				names = append(names, m.ModelName)
			}
			if !reflect.DeepEqual(names, []string{"C", "A", "B"}) {
				t.Errorf("saved order=%v want C,A,B", names)
			}
			rows, err := f.pool.Query(t.Context(), `SELECT created_at FROM connection_health_model_targets WHERE policy_id=$1 ORDER BY created_at,id`, id)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			stamps := []time.Time{}
			for rows.Next() {
				var at time.Time
				if err = rows.Scan(&at); err != nil {
					t.Fatal(err)
				}
				stamps = append(stamps, at)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(stamps) != 3 || !stamps[1].After(stamps[0]) || !stamps[2].After(stamps[1]) {
				t.Errorf("model creation times must strictly increase: %v", stamps)
			}
		})
	}
}
func TestProbeModelC43K6OmittedModeIsPreserved(t *testing.T) {
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, true, "A", "B", "C")
	in := c43CriticalInput("A", "B", "C")
	in.ID = p.ID
	in.Enabled = false
	saved, err := f.service.SavePolicy(t.Context(), c32REDUser, in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ProbeMode != "first_available" {
		t.Fatalf("omitted mode update reset mode to %q", saved.ProbeMode)
	}
}
func TestProbeModelC43E1DefaultStillProbesAllAndNoHints(t *testing.T) {
	f := c43CriticalFixture(t)
	c43CriticalPolicy(t, f, false, "A", "B", "C")
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	// Seven accounts exceed the workspace cap of six; admit the remainder on the next tick.
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	events := c43CriticalEvents(t, f)
	page := c43CriticalPage(t, f)
	for id := range f.accounts {
		want := []string{"A", "B", "C"}
		if !reflect.DeepEqual(events[c32REDTarget(id)], want) || !reflect.DeepEqual(page[id], want) {
			t.Errorf("default account %s events=%v page=%v want=%v", id, events[c32REDTarget(id)], page[id], want)
		}
	}
	groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"modelSelection"`, `"skippedModels"`} {
		if strings.Contains(string(encoded), key) {
			t.Errorf("default page emitted new hint: %s", key)
		}
	}
}
func TestProbeModelC43E2OtherPlatformRemainsUnchanged(t *testing.T) {
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, true, "A", "B", "C")
	inv, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	inv.session.Platform = upstream.PlatformNewAPI
	check := func(policy Policy) adminMonitoringScope {
		assignments := []GroupPolicyAssignment{{UserID: c32REDUser, AdminAccountID: c32REDWorkspace, AdminGroupID: "1", PolicyID: policy.ID}}
		scope, e := buildAdminMonitoringScope(c32REDUser, c32REDWorkspace, *inv, []Policy{policy}, nil, assignments, nil)
		if e != nil {
			t.Fatal(e)
		}
		return scope
	}
	got := check(p)
	p.ProbeMode = "real_model"
	want := check(p)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("other platform monitoring scope changed", got, want)
	}
	// Compare the existing platform adapter's actual model output via a page read.
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "1", Name: "c3-red-group"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"1": {{ID: "1", Name: "channel", Models: "A,B,C", BaseURL: "https://fixture.invalid", Type: "apikey", Platform: "openai"}}}}
	f.service.SetPlatformGroupReader(reader)
	f.service.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	if err = f.service.repo.ReplacePolicyAssignments(t.Context(), c32REDUser, c32REDWorkspace, buildTargetID(string(upstream.PlatformNewAPI), c32REDWorkspace, "1"), []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	first := c43CriticalPage(t, f)
	if _, err = f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET probe_mode='real_model'`); err != nil {
		t.Fatal(err)
	}
	defaultPage := c43CriticalPage(t, f)
	if !reflect.DeepEqual(first, defaultPage) || !reflect.DeepEqual(first["1"], []string{"A", "B", "C"}) {
		t.Fatal("other platform model output changed", first, defaultPage)
	}
}
func TestProbeModelC43E3MonitoringAccountsUnchanged(t *testing.T) {
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, true, "A", "B", "C")
	inv, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	assignments := []PolicyAssignment{}
	for id := range f.accounts {
		assignments = append(assignments, PolicyAssignment{UserID: c32REDUser, AdminAccountID: c32REDWorkspace, TargetID: c32REDTarget(id), PolicyID: p.ID})
	}
	check := func(policy Policy) adminMonitoringScope {
		s, e := buildAdminMonitoringScope(c32REDUser, c32REDWorkspace, *inv, []Policy{policy}, assignments, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	got := check(p)
	p.ProbeMode = "real_model"
	want := check(p)
	if !reflect.DeepEqual(got.monitoredByGroup, want.monitoredByGroup) {
		t.Fatal("monitoring account set changed", got.monitoredByGroup, want.monitoredByGroup)
	}
	for id := range f.accounts {
		if !adminMonitoringScopeContainsTarget(got, c32REDTarget(id)) {
			t.Errorf("account %s lost monitoring", id)
		}
	}
}
