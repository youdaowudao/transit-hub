package connection_health

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// Primary-agent selection and complete-path expectations were written before
// C5.2 implementation; later assertions also preserve the existing action guards.
// Upstream traffic uses httptest or an in-memory transport, never a real provider.
func c52Fixture(t *testing.T, platform string) *c32REDFixture {
	t.Helper()
	f := c43CriticalFixture(t)
	f.groupPlatform = platform
	for _, a := range f.accounts {
		a["platform"] = platform
		a["extra"] = map[string]any{}
	}
	f.accounts["7"]["credentials"].(map[string]any)["model_mapping"] = "invalid"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_model_control_auto(user_id,admin_account_id,auto_close_enabled,auto_restore_enabled,health_coordination_enabled) VALUES($1,$2,false,false,false)`, c32REDUser, c32REDWorkspace); err != nil {
		t.Fatal(err)
	}
	var answers int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_question_answer_records`).Scan(&answers); err != nil || answers != 0 {
		t.Fatal("fixture must have no question answers", answers, err)
	}
	return f
}

func c52Models(t *testing.T, groups []AdminGroupHealth, id, want, kind string) {
	t.Helper()
	for _, g := range groups {
		for _, a := range g.Accounts {
			if a.ID != id {
				continue
			}
			if len(a.ModelHealth)+len(a.UnprobedModels) != 1 {
				t.Fatalf("account %s models=%v/%v", id, a.ModelHealth, a.UnprobedModels)
			}
			if len(a.ModelHealth) == 1 {
				m := a.ModelHealth[0]
				if m.ModelName != want || m.ModelSelection != kind {
					t.Fatal(m)
				}
				c52Skipped(t, want, kind, m.SkippedModels)
			} else {
				m := a.UnprobedModels[0]
				if m.ModelName != want || m.ModelSelection != kind {
					t.Fatal(m)
				}
				c52Skipped(t, want, kind, m.SkippedModels)
			}
			return
		}
	}
	t.Fatal("account missing", id)
}

func c52Skipped(t *testing.T, model, kind string, got []string) {
	t.Helper()
	want := []string{}
	if kind == "fallback" {
		want = []string{"A"}
		if model == "C" {
			want = append(want, "B")
		}
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("skipped=%v want=%v", got, want)
	}
}

func TestProbeModelC43CNCompletePath(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			f := c52Fixture(t, platform)
			c43CriticalPolicy(t, f, true, "A", "B", "C")
			var mu sync.Mutex
			requests := map[string][]string{}
			f.service.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("protocol path=%s", r.URL.Path)
				}
				model, _ := body["model"].(string)
				if body["max_tokens"] != float64(1) || body["stream"] != true {
					t.Errorf("selected request lost token/stream parameters: %v", body)
				}
				id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
				mu.Lock()
				requests[id] = append(requests[id], model)
				mu.Unlock()
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}, nil
			})}}
			selected := map[string]string{"1": "A", "2": "B", "3": "C", "4": "C", "5": "B", "6": "C", "7": "C"}
			kinds := map[string]string{"2": "fallback", "3": "fallback", "4": "last_unconfirmed", "5": "fallback", "6": "last_unconfirmed", "7": "last_unconfirmed"}
			groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
			if err != nil {
				t.Fatal(err)
			}
			if groups[0].Platform != platform {
				t.Fatal("group/account platforms must match", groups[0].Platform)
			}
			for id, want := range selected {
				c52Models(t, groups, id, want, kinds[id])
			}
			f.service.runSchedulerTickSafely(t.Context())
			events := c43CriticalEvents(t, f)
			mu.Lock()
			for id, want := range selected {
				if !reflect.DeepEqual(events[c32REDTarget(id)], []string{want}) || !reflect.DeepEqual(requests[id], []string{want}) {
					t.Errorf("account %s events=%v requests=%v want=%s", id, events[c32REDTarget(id)], requests[id], want)
				}
			}
			mu.Unlock()
			groups, err = f.service.AdminGroups(t.Context(), c32REDUser)
			if err != nil {
				t.Fatal(err)
			}
			for id, want := range selected {
				c52Models(t, groups, id, want, kinds[id])
			}
			// JSON is the real handler, not just the service method.
			formal := f.expect(http.MethodPost, "targets/"+c32REDTarget("1")+"/probe", map[string]any{"models": []string{"A"}}, 200)
			if models := formal["groups"].([]any); len(models) != 1 || models[0].(map[string]any)["modelName"] != "A" {
				t.Fatal("formal JSON selected output", formal)
			}
			f.expect(http.MethodPost, "targets/"+c32REDTarget("1")+"/probe", map[string]any{"models": []string{"B"}}, 400)
			request := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/"+c32REDTarget("1")+"/probe-stream", strings.NewReader(`{"models":["A"]}`))
			request = request.WithContext(authctx.WithUserID(request.Context(), c32REDUser))
			response := httptest.NewRecorder()
			f.mux.ServeHTTP(response, request)
			if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"modelName":"A"`) || strings.Contains(response.Body.String(), `"modelName":"B"`) {
				t.Fatal("SSE selected output", response.Code, response.Body.String())
			}
			before, err := f.service.repo.ListStatesByConnection(t.Context(), c32REDTarget("1"))
			if err != nil {
				t.Fatal(err)
			}
			var eventsBefore, eventsAfter int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_events WHERE connection_id=$1`, c32REDTarget("1")).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			manual, err := f.service.ManualProbeTarget(t.Context(), c32REDUser, c32REDTarget("1"), []string{"B"})
			if err != nil || len(manual) != 1 || manual[0].ModelName != "B" || !manual[0].Healthy {
				t.Fatal(manual, err)
			}
			after, err := f.service.repo.ListStatesByConnection(t.Context(), c32REDTarget("1"))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("one-shot wrote health", err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_events WHERE connection_id=$1`, c32REDTarget("1")).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
				t.Fatal("one-shot wrote events", eventsBefore, eventsAfter, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(requests["1"], []string{"A", "A", "A", "B"}) {
				t.Fatal("formal/unselected/one-shot sends", requests["1"])
			}
		})
	}
}

func TestProbeModelC43CNExecutionRechecksSelection(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			f := c52Fixture(t, platform)
			f.accounts = map[string]map[string]any{"1": f.accounts["1"]}
			f.mapping("1", map[string]string{"B": "B"})
			c43CriticalPolicy(t, f, true, "A", "B")
			policies, err := f.service.repo.ListPolicies(t.Context(), c32REDUser, c32REDWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			assignments, err := f.service.repo.ListPolicyAssignmentsByWorkspace(t.Context(), c32REDUser, c32REDWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			jobs := f.service.collectAdminProbeJobsWithGroupsAndCache(t.Context(), policies, assignments, nil, nil, adminInventoryCache{}, nil)
			if len(jobs) != 1 || len(jobs[0].dueSpecs) != 1 || jobs[0].dueSpecs[0].modelName != "B" {
				t.Fatal("queued B", jobs)
			}
			f.mapping("1", map[string]string{"A": "A"})
			runProtocolServiceJob(f.service, jobs[0])
			if events := c43CriticalEvents(t, f); len(events) != 0 {
				t.Fatal("stale queued B executed", events)
			}
			states, err := f.service.repo.ListStatesByConnection(t.Context(), c32REDTarget("1"))
			if err != nil || len(states) != 0 {
				t.Fatal("stale job wrote health", states, err)
			}
			f.service.runSchedulerTickSafely(t.Context())
			if events := c43CriticalEvents(t, f); !reflect.DeepEqual(events[c32REDTarget("1")], []string{"A"}) {
				t.Fatal("fresh scheduler failed to select A", events)
			}
		})
	}
}
func c52Comparison(t *testing.T, platform string, first bool, selectedState string) c43ComparisonResult {
	t.Helper()
	f := c52Fixture(t, platform)
	f.mapping("1", map[string]string{"A": "A", "B": "B"})
	f.mapping("2", map[string]string{"A": "A", "B": "B"})
	f.mapping("3", map[string]string{"A": "A", "B": "B"})
	f.accounts = map[string]map[string]any{"1": f.accounts["1"], "2": f.accounts["2"], "3": f.accounts["3"]}
	names := []string{"A"}
	if first {
		names = []string{"A", "B", "C"}
	}
	p := c43CriticalPolicy(t, f, first, names...)
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET priority_mode='multiplier',auto_remote_action_enabled=true WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	for id := range f.accounts {
		if id != "1" {
			c43CriticalState(t, f, id, "A", StateHealthy, true)
			continue
		}
		if selectedState != "none" {
			state := StateHealthy
			if selectedState == "suspended" || selectedState == "actions_off" {
				state = StateSuspended
			}
			c43CriticalState(t, f, id, "A", state, true)
		}
		c43CriticalState(t, f, id, "B", StateSuspended, true)
		c43CriticalState(t, f, id, "C", StateHealthy, true)
	}
	if selectedState == "actions_off" {
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET auto_remote_action_enabled=false WHERE id=$1`, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	out := c43ComparisonResult{states: []State{}, unprobed: []string{}}
	groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
	if err != nil {
		t.Fatal(err)
	}
	out.summary = groups[0].HealthSummary
	out.summary.LastProbeAt = nil
	for _, account := range groups[0].Accounts {
		if account.ID != "1" {
			continue
		}
		for _, model := range account.ModelHealth {
			out.states = append(out.states, model.State)
		}
		for _, model := range account.UnprobedModels {
			out.unprobed = append(out.unprobed, model.ModelName)
		}
	}
	policies, err := f.service.repo.ListPolicies(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	lookup := upstreamMultiplierLookup{byAccount: map[string]upstreamMultiplierResolution{}}
	assignments := map[string][]Policy{}
	for id := range f.accounts {
		lookup.byAccount[id] = upstreamMultiplierResolution{status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: float64Ptr(1)}}
		assignments[c32REDTarget(id)] = policies
	}
	items, complete, err := f.service.priorityInventoryForSnapshot(snapshot, c32REDWorkspace, assignments, nil, nil, nil, lookup)
	if err != nil || !complete {
		t.Fatal(err, complete)
	}
	states, err := f.service.repo.ListStatesByWorkspace(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.service.loadAdminMonitoringScope(t.Context(), c32REDUser, c32REDWorkspace, *snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, found := findActionInventoryTarget(c32REDTarget("1"), *snapshot)
	if !found {
		t.Fatal("missing target")
	}
	for _, group := range snapshot.groups {
		for _, account := range group.accounts {
			if account.ID == "1" {
				target = adminProbeTargetFromAccount(f.session, c32REDWorkspace, group.group.ID, group.group.Name, account)
			}
		}
	}
	target.InventoryComplete = true
	if err = f.service.configureTestTarget(t.Context(), c32REDUser, c32REDWorkspace, &target, []adminTargetMembership{{groupID: "1", groupName: "c3-red-group"}}, true); err != nil {
		t.Fatal(err)
	}
	specs := candidateModelSpecs(target.Models, policies, target.Platform, target.modelSource, true)
	actionResult, actionErr := f.service.reconcileTargetRemoteActionWithFloor(t.Context(), c32REDUser, c32REDWorkspace, f.session, target, specs, f.service.sub2APIFloorGuardFor(c32REDUser, c32REDWorkspace), snapshot, scope)
	_ = actionResult
	if err = actionErr; err != nil {
		t.Fatal(err)
	}
	out.status = f.accounts["1"]["status"].(string)
	out.statusWrites = append([]string{}, f.statusWrites...)
	f.service.syncWorkspacePriorities(t.Context(), f.session, c32REDUser, c32REDWorkspace, map[string]*priorityTargetInventory{c32REDTarget("1"): items[c32REDTarget("1")]}, complete, states, nil)
	switch value := f.accounts["1"]["priority"].(type) {
	case int:
		out.priority = value
	case float64:
		out.priority = int(value)
	default:
		t.Fatalf("unexpected fixture Priority type %T", value)
	}
	out.priorityWrites = append([]int{}, f.priorityWrites...)
	// The old unselected B failure must remain stored, not be deleted to hide it.
	retained, err := f.service.repo.ListStatesByConnection(t.Context(), c32REDTarget("1"))
	if err != nil {
		t.Fatal(err)
	}
	foundOld := false
	for _, state := range retained {
		if state.ModelName == "B" && state.State == StateSuspended {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatal("unselected old failure was lost")
	}
	return out
}

func TestProbeModelC43CNDownstreamMatchesSingleModel(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		for _, state := range []string{"none", "healthy", "suspended", "actions_off"} {
			t.Run(platform+"/"+state, func(t *testing.T) {
				single := c52Comparison(t, platform, false, state)
				ordered := c52Comparison(t, platform, true, state)
				if !reflect.DeepEqual(single, ordered) {
					t.Fatalf("single=%+v ordered=%+v", single, ordered)
				}
				if state == "healthy" && (len(single.priorityWrites) == 0 || single.status != "active" || len(single.statusWrites) != 0) {
					t.Fatal("healthy A must write Priority and preserve active account", single)
				}
				if state == "suspended" && (single.status != "inactive" || len(single.statusWrites) == 0) {
					t.Fatal("failed selected A must still stop account", single)
				}
				if (state == "none" || state == "actions_off") && (single.status != "active" || len(single.statusWrites) != 0) {
					t.Fatal("insufficient evidence/actions off must preserve account", single)
				}
				if state == "none" && len(single.priorityWrites) != 0 {
					t.Fatal("missing evidence changed Priority", single)
				}
			})
		}
	}
}
func TestProbeModelC43CNManagementBoundaries(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			a := upstream.AdminGroupAccountInfo{ID: "1", Type: "apikey", Platform: platform, ModelMappingKnown: true, ModelMapping: map[string]string{"A": "A"}}
			if supported, known := modelControlSupports(a, "A"); supported || known {
				t.Fatal("model management expanded", supported, known)
			}
			if _, _, eligible := modelControlUpstreamKeys(a, "A"); eligible {
				t.Fatal("model supply expanded")
			}
			detail := upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}
			if planModelClose(detail, "A").ReasonKey == "" || planModelRestore(detail, map[string]string{"A": "A"}).ReasonKey == "" || planModelAdd(detail, "B").ReasonKey == "" {
				t.Fatal("model writes allowed for CN account")
			}
			p := c43OrderedPolicy("p", "A", "B")
			specs := candidateModelSpecs(nil, []Policy{p}, "sub2api", probeModelSourceFromAccount(a), false)
			if len(specs) != 2 || specs[0].modelName != "A" || specs[1].modelName != "B" {
				t.Fatal("narrow=false changed", specs)
			}
		})
	}
}

func TestProbeModelC43CNApplicabilityAndAutoOwner(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			f := c52Fixture(t, platform)
			f.accounts = map[string]map[string]any{"1": f.accounts["1"]}
			p := c43CriticalPolicy(t, f, true, "A", "B")
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET priority_mode='multiplier' WHERE id=$1`, p.ID); err != nil {
				t.Fatal(err)
			}
			result, err := f.service.SetTargetPriorityOwner(t.Context(), c32REDUser, c32REDTarget("1"), TargetPriorityOwnerInput{Mode: "auto"})
			if err != nil || result.Result == AccountEditNotSent {
				t.Fatal("return automatic management", result, err)
			}
			source := probeModelSource{accountType: "apikey", platform: platform, modelMappingKnown: true, modelMapping: map[string]string{"A": "A"}}
			ordered := c43OrderedPolicy("one", "A", "B")
			other := c43OrderedPolicy("two", "B", "C")
			source.modelMapping["B"] = "B"
			if specs := candidateModelSpecs(nil, []Policy{ordered, other}, "sub2api", source, true); len(specs) != 2 || specs[0].modelName != "A" || specs[1].modelName != "B" {
				t.Fatal("multiple policies", specs)
			}
			for _, tc := range []struct {
				name   string
				policy Policy
				models []string
				want   int
			}{
				{"real_model", func() Policy { p := ordered; p.ProbeMode = ProbeModeRealModel; return p }(), nil, 2},
				{"top-level", ordered, []string{"B"}, 1},
				{"disabled", func() Policy { p := ordered; p.Enabled = false; return p }(), nil, 0},
				{"multiplier-only", func() Policy { p := ordered; p.StrategyMode = StrategyModeMultiplierOnly; return p }(), nil, 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					specs := candidateModelSpecs(tc.models, []Policy{tc.policy}, "sub2api", source, true)
					if len(specs) != tc.want {
						t.Fatal(specs)
					}
					if tc.name == "top-level" && specs[0].modelName != "B" {
						t.Fatal(specs)
					}
				})
			}
			if specs := candidateModelSpecs(nil, nil, "sub2api", source, true); len(specs) != 0 {
				t.Fatal(specs)
			}
		})
	}
}

func TestProbeModelC43CNSelectedFailureStillStopsAccount(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			f := c52Fixture(t, platform)
			f.accounts = map[string]map[string]any{"1": f.accounts["1"], "2": f.accounts["2"], "3": f.accounts["3"]}
			for id := range f.accounts {
				f.mapping(id, map[string]string{"A": "A", "B": "B"})
			}
			p := c43CriticalPolicy(t, f, true, "A", "B")
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET auto_remote_action_enabled=true WHERE id=$1`, p.ID); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"2", "3"} {
				c43CriticalState(t, f, id, "A", StateHealthy, true)
			}
			// Existing action evidence gates require a prior successful request as
			// well as current failures. Supply that prerequisite without bypassing it.
			c43CriticalState(t, f, "1", "A", StateHealthy, true)
			c43CriticalState(t, f, "1", "B", StateSuspended, true)
			requests := 0
			f.service.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "A" {
					t.Errorf("failed probe sent unselected model %v", body["model"])
				}
				requests++
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated fixture failure"}}`))}, nil
			})}}
			for n := 0; n < 3; n++ {
				models, err := f.service.ProbeTarget(t.Context(), c32REDUser, c32REDTarget("1"), []string{"A"})
				if err != nil || len(models) != 1 || models[0].ModelName != "A" || models[0].LastErrorKey == "" {
					t.Fatal(models, err)
				}
			}
			if requests != 3 || f.accounts["1"]["status"] != "active" || len(f.statusWrites) != 0 {
				t.Fatal("formal manual probes must preserve original inactive-action guard", requests, f.statusWrites)
			}
			states, err := f.service.repo.ListStatesByConnection(t.Context(), c32REDTarget("1"))
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range states {
				if s.ModelName == "A" && (s.ConsecutiveFailures != 3 || s.State != StateSuspended) {
					t.Fatal(s)
				}
			}
			// Scheduled coordination can stop the account; the formal manual path
			// above deliberately cannot. Exercise that existing action entry next.
			refresh, err := f.service.refreshAdminTarget(t.Context(), f.session, c32REDWorkspace, "1")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.configureTestTarget(t.Context(), c32REDUser, c32REDWorkspace, &refresh.target, refresh.memberships, true); err != nil {
				t.Fatal(err)
			}
			policies, err := f.service.repo.ListPolicies(t.Context(), c32REDUser, c32REDWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := f.service.loadAdminMonitoringScope(t.Context(), c32REDUser, c32REDWorkspace, refresh.inventory)
			if err != nil {
				t.Fatal(err)
			}
			specs := candidateModelSpecs(refresh.target.Models, policies, refresh.target.Platform, refresh.target.modelSource, true)
			_, err = f.service.reconcileTargetRemoteActionWithFloor(t.Context(), c32REDUser, c32REDWorkspace, f.session, refresh.target, specs, f.service.sub2APIFloorGuardFor(c32REDUser, c32REDWorkspace), &refresh.inventory, scope)
			if err != nil {
				t.Fatal(err)
			}
			if f.accounts["1"]["status"] != "inactive" || len(f.statusWrites) == 0 {
				t.Fatal("selected failure must retain automatic stop behavior", f.statusWrites)
			}
		})
	}
}
