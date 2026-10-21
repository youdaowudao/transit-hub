package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func c43Source(mapping map[string]string) probeModelSource {
	return probeModelSourceFromAccount(upstream.AdminGroupAccountInfo{Type: "apikey", Platform: "openai", ModelMapping: mapping, ModelMappingKnown: true})
}
func c43OrderedPolicy(id string, names ...string) Policy {
	p := Policy{ID: id, Enabled: true, ProbeMode: ProbeModeFirstAvailable, ProbeIntervalSeconds: 60}
	for _, name := range names {
		p.ModelTargets = append(p.ModelTargets, ModelTarget{ModelName: name, Enabled: true})
	}
	return p
}
func TestProbeModelC43Confirmed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source probeModelSource
		model  string
		want   bool
	}{
		{"unsupported type", probeModelSource{accountType: "oauth", platform: "openai", modelMappingKnown: true}, "A", false},
		{"unsupported platform", probeModelSource{accountType: "apikey", platform: "gemini", modelMappingKnown: true}, "A", false},
		{"unreadable", probeModelSource{accountType: "apikey", platform: "openai"}, "A", false},
		{"absent", c43Source(map[string]string{"B": "B"}), "A", false},
		{"exact", c43Source(map[string]string{"A": "A"}), "A", true},
		{"renamed", c43Source(map[string]string{"A": "renamed"}), "A", false},
		{"empty", c43Source(nil), "A", true},
		{"passthrough", probeModelSource{accountType: "apikey", platform: "openai", openAIPassthrough: true, modelMapping: map[string]string{"A": "renamed"}}, "A", true},
		{"wildcard identity", c43Source(map[string]string{"A*": "A-model"}), "A-model", true},
		{"wildcard renamed", c43Source(map[string]string{"A*": "other"}), "A-model", false},
		{"exact overrides wildcard", c43Source(map[string]string{"A*": "other", "A-model": "A-model"}), "A-model", true},
		{"anthropic", probeModelSource{accountType: "apikey", platform: "anthropic", modelMappingKnown: true, modelMapping: map[string]string{"A": "A"}}, "A", true},
		{"anthropic does not use OpenAI passthrough", probeModelSource{accountType: "apikey", platform: "anthropic", openAIPassthrough: true, modelMappingKnown: true, modelMapping: map[string]string{"B": "B"}}, "A", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeModelConfirmed(tc.source, tc.model); got != tc.want {
				t.Fatalf("confirmed=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestProbeModelC43SelectionTableAndHints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		source     probeModelSource
		want, kind string
		skipped    []string
	}{
		{"all", c43Source(map[string]string{"A": "A", "B": "B", "C": "C"}), "A", "", nil},
		{"second", c43Source(map[string]string{"B": "B", "C": "C"}), "B", "fallback", []string{"A"}},
		{"last supported", c43Source(map[string]string{"C": "C"}), "C", "fallback", []string{"A", "B"}},
		{"none", c43Source(map[string]string{"other": "other"}), "C", "last_unconfirmed", nil},
		{"renamed first", c43Source(map[string]string{"A": "alias", "B": "B"}), "B", "fallback", []string{"A"}},
		{"empty whitelist", c43Source(nil), "A", "", nil},
		{"passthrough", probeModelSource{accountType: "apikey", platform: "openai", openAIPassthrough: true}, "A", "", nil},
		{"unreadable", probeModelSource{accountType: "apikey", platform: "openai"}, "C", "last_unconfirmed", nil},
		{"not API key", probeModelSource{accountType: "oauth", platform: "openai", modelMappingKnown: true}, "C", "last_unconfirmed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := c43OrderedPolicy("p", "A", "B", "C")
			got := selectProbeModel(p, tc.source)
			if got.modelName != tc.want || got.kind != tc.kind || !reflect.DeepEqual(got.skippedModels, tc.skipped) {
				t.Fatalf("selection=%+v", got)
			}
		})
	}
	p := c43OrderedPolicy("p", "disabled", " ", " A ", "B")
	p.ModelTargets[0].Enabled = false
	got := selectProbeModel(p, c43Source(map[string]string{"B": "B"}))
	if got.modelName != "B" || !reflect.DeepEqual(got.skippedModels, []string{"A"}) {
		t.Fatal(got)
	}
	p = c43OrderedPolicy("p", "C")
	if got = selectProbeModel(p, probeModelSource{}); got.modelName != "C" || got.kind != "" || len(got.skippedModels) != 0 {
		t.Fatal(got)
	}
	p.ModelTargets[0].Enabled = false
	if got = selectProbeModel(p, c43Source(nil)); got.modelName != "" || got.kind != "" {
		t.Fatal(got)
	}
}
func TestProbeModelC43MultiplePoliciesAndApplicability(t *testing.T) {
	ordered := c43OrderedPolicy("ordered", "A", "B")
	all := c43OrderedPolicy("all", "B", "C")
	all.ProbeMode = ProbeModeRealModel
	ordered.ModelTargets[1].ProviderFamily, ordered.ModelTargets[1].MaxProbeTokens, ordered.ModelTargets[1].ProbePrompt = ProviderOpenAI, 3, "ordered prompt"
	all.ModelTargets[0].ProviderFamily, all.ModelTargets[0].MaxProbeTokens, all.ModelTargets[0].ProbePrompt = ProviderAnthropic, 7, "all prompt"
	source := c43Source(map[string]string{"B": "B"})
	check := func(policies []Policy, kind string, policyID string) {
		t.Helper()
		specs := candidateModelSpecs(nil, policies, "sub2api", source, true)
		if len(specs) != 2 {
			t.Fatal(specs)
		}
		b := specs[0]
		if b.modelName != "B" || b.modelSelection != kind || b.policy.ID != policyID || len(b.policies) != 2 {
			t.Fatal(b)
		}

		winner := all.ModelTargets[0]
		if policyID == ordered.ID {
			winner = ordered.ModelTargets[1]
		}
		if b.providerFamily != winner.ProviderFamily || b.maxProbeTokens != winner.MaxProbeTokens || b.probePrompt != winner.ProbePrompt {
			t.Fatalf("winner parameters=(%s,%d,%q) want=(%s,%d,%q)", b.providerFamily, b.maxProbeTokens, b.probePrompt, winner.ProviderFamily, winner.MaxProbeTokens, winner.ProbePrompt)
		}
		if kind == "fallback" && !reflect.DeepEqual(b.skippedModels, []string{"A"}) {
			t.Fatal("replacement winner did not retain its skipped models", b)
		}
		if kind == "" && len(b.skippedModels) != 0 {
			t.Fatal("default winner retained ordered-policy skipped models", b)
		}
		if specs[1].modelName != "C" || specs[1].modelSelection != "" {
			t.Fatal(specs)
		}
	}
	// Existing preference replaces the winner; hint must follow that winner, not merged sources.
	check([]Policy{ordered, all}, "", "all")
	ordered.ID = "a-ordered"
	check([]Policy{all, ordered}, "fallback", "a-ordered")
	check([]Policy{ordered, all}, "fallback", "a-ordered") // Merge losing sources without replacing winner parameters or hint.
	target := AdminProbeTarget{Platform: "sub2api"}
	specs := candidateModelSpecs(target.Models, []Policy{ordered}, target.Platform, target.modelSource, true)
	if len(specs) != 1 || specs[0].modelName != "B" || specs[0].modelSelection != "last_unconfirmed" {
		t.Fatal(specs)
	}
	for _, tc := range []struct {
		name, platform string
		models         []string
		narrow         bool
		want           []string
	}{
		{"top-level models", "sub2api", []string{"A"}, true, []string{"A"}},
		{"model management", "sub2api", nil, false, []string{"A", "B"}},
		{"other platform", "newapi", nil, true, []string{"A", "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := candidateModelSpecsForPlatform(tc.models, []Policy{ordered}, tc.platform, source, tc.narrow)
			got := []string{}
			for _, s := range specs {
				got = append(got, s.modelName)
				if s.modelSelection != "" || len(s.skippedModels) != 0 {
					t.Fatal(s)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, tc.want)
			}
		})
	}
}
func c43InputBody(in PolicyInput) map[string]any {
	encoded, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(encoded, &out)
	if in.ProbeMode == "" {
		delete(out, "probeMode")
	}
	return out
}
func c43AssertSaved(t *testing.T, f *c32REDFixture, id, mode string, ordered bool) {
	t.Helper()
	p, err := f.service.repo.GetPolicy(t.Context(), id, c32REDUser, c32REDWorkspace)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	if p.ProbeMode != mode {
		t.Fatal(p.ProbeMode, mode)
	}
	stamps := []time.Time{}
	for _, m := range p.ModelTargets {
		stamps = append(stamps, m.CreatedAt)
	}
	for n := 1; n < len(stamps); n++ {
		if ordered {
			if stamps[n].Sub(stamps[n-1]) != time.Microsecond {
				t.Fatal(stamps)
			}
		} else if !stamps[n].Equal(stamps[0]) {
			t.Fatal(stamps)
		}
	}
}
func c43PolicyCount(t *testing.T, f *c32REDFixture) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_policies`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestProbeModelC43HTTPSaveAllEntrypoints(t *testing.T) {
	for _, path := range []string{"policy", "quick"} {
		t.Run(path, func(t *testing.T) {
			f := c43CriticalFixture(t)
			for _, tc := range []struct {
				name, mode, strategy, want string
				status                     int
			}{
				{"ordered", ProbeModeFirstAvailable, "health_probe", ProbeModeFirstAvailable, 200},
				{"explicit default", ProbeModeRealModel, "health_probe", ProbeModeRealModel, 200},
				{"omitted", "", "health_probe", ProbeModeRealModel, 200},
				{"unknown", "invalid", "health_probe", "", 400},
				{"multiplier", ProbeModeFirstAvailable, StrategyModeMultiplierOnly, ProbeModeRealModel, 200},
			} {
				t.Run(tc.name, func(t *testing.T) {
					in := c43CriticalInput("C", "A", "B")
					in.ProbeMode = tc.mode
					in.StrategyMode = tc.strategy
					body := c43InputBody(in)
					method, url := http.MethodPost, "policies"
					if path == "quick" {
						method, url = http.MethodPut, "admin-groups/1/policy-configuration"
						body = map[string]any{"quickPolicy": body}
					}
					before := c43PolicyCount(t, f)
					got := f.expect(method, url, body, tc.status)
					if tc.status != 200 {
						if c43PolicyCount(t, f) != before {
							t.Fatal("rejected save wrote policy")
						}
						return
					}
					var id string
					if path == "policy" {
						id = got["id"].(string)
						if got["probeMode"] != tc.want {
							t.Fatal(got)
						}
					} else {
						id = got["policyIds"].([]any)[0].(string)
					}
					c43AssertSaved(t, f, id, tc.want, tc.want == ProbeModeFirstAvailable)
					if tc.strategy == StrategyModeMultiplierOnly {
						p, _ := f.service.repo.GetPolicy(t.Context(), id, c32REDUser, c32REDWorkspace)
						if len(p.ModelTargets) != 0 {
							t.Fatal(p)
						}
					}
				})
			}
		})
	}
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, true, "C", "A", "B")
	for _, mode := range []string{"", ProbeModeRealModel, ProbeModeFirstAvailable} {
		in := c43CriticalInput("C", "A", "B")
		in.ProbeMode = mode
		in.Enabled = false
		got := f.expect(http.MethodPut, "policies/"+p.ID, c43InputBody(in), 200)
		want := mode
		if want == "" {
			want = ProbeModeFirstAvailable
		}
		if got["probeMode"] != want {
			t.Fatal(got)
		}
		c43AssertSaved(t, f, p.ID, want, want == ProbeModeFirstAvailable)
	}
}
func TestProbeModelC43NonSub2APIHTTPAndSessionFailure(t *testing.T) {
	for _, path := range []string{"policy", "quick"} {
		t.Run(path, func(t *testing.T) {
			f := c43CriticalFixture(t)
			f.service.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
			f.service.SetPlatformGroupReader(fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "1", Name: "g"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"1": {{ID: "1", Models: "A,B,C"}}}})
			for _, mode := range []string{ProbeModeFirstAvailable, ""} {
				in := c43CriticalInput("C", "A", "B")
				in.ProbeMode = mode
				body := c43InputBody(in)
				method, url := http.MethodPost, "policies"
				if path == "quick" {
					method, url = http.MethodPut, "admin-groups/1/policy-configuration"
					body = map[string]any{"quickPolicy": body}
				}
				status := 200
				if mode == ProbeModeFirstAvailable {
					status = 400
				}
				before := c43PolicyCount(t, f)
				got := f.expect(method, url, body, status)
				if status == 400 {
					if c43PolicyCount(t, f) != before {
						t.Fatal("non-Sub2API rejection wrote policy")
					}
					continue
				}
				var id string
				if path == "policy" {
					id = got["id"].(string)
				} else {
					id = got["policyIds"].([]any)[0].(string)
				}
				c43AssertSaved(t, f, id, ProbeModeRealModel, false)
			}
		})
	}
	f := c43CriticalFixture(t)
	f.service.mySites = c43MissingSession{}
	in := c43CriticalInput("A")
	in.ProbeMode = ProbeModeFirstAvailable
	f.expect(http.MethodPost, "policies", c43InputBody(in), 400)
	f.expect(http.MethodPut, "admin-groups/1/policy-configuration", map[string]any{"quickPolicy": c43InputBody(in)}, 400)
	if c43PolicyCount(t, f) != 0 {
		t.Fatal("failed session wrote policy")
	}
	// The default mode must not acquire a session merely to save a policy.
	in.ProbeMode = ""
	f.expect(http.MethodPost, "policies", c43InputBody(in), 200)
}

func TestProbeModelC43HTTPRejectedUpdatesKeepOriginal(t *testing.T) {
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, true, "C", "A", "B")
	for _, platform := range []upstream.Platform{upstream.PlatformSub2API, upstream.PlatformNewAPI} {
		f.service.mySites = fakeMySitesReader{session: upstream.Session{Platform: platform}}
		in := c43CriticalInput("B", "A")
		in.ProbeMode = "invalid"
		if platform == upstream.PlatformNewAPI {
			in.ProbeMode = ProbeModeFirstAvailable
		}
		f.expect(http.MethodPut, "policies/"+p.ID, c43InputBody(in), 400)
		saved, err := f.service.repo.GetPolicy(t.Context(), p.ID, c32REDUser, c32REDWorkspace)
		if err != nil || saved == nil {
			t.Fatal(err)
		}
		if saved.ProbeMode != ProbeModeFirstAvailable || len(saved.ModelTargets) != 3 || saved.ModelTargets[0].ModelName != "C" {
			t.Fatal("rejected update changed original policy", saved)
		}
	}
}

type c43MissingSession struct{ fakeMySitesReader }

func (c43MissingSession) RequireSession(context.Context, string, string) (upstream.Session, error) {
	return upstream.Session{}, errors.New("session absent")
}

type c43InterleavedRepository struct {
	healthRepository
	afterRead func()
	fired     bool
}

func (r *c43InterleavedRepository) GetPolicy(ctx context.Context, id, user, workspace string) (*Policy, error) {
	p, err := r.healthRepository.GetPolicy(ctx, id, user, workspace)
	if err == nil && !r.fired {
		r.fired = true
		r.afterRead()
	}
	return p, err
}
func TestProbeModelC43InterleavedSavePreservesDatabaseMode(t *testing.T) {
	f := c43CriticalFixture(t)
	p := c43CriticalPolicy(t, f, false, "A", "B", "C")
	base := f.service.repo
	hook := &c43InterleavedRepository{healthRepository: base}
	hook.afterRead = func() {
		f.service.repo = base
		defer func() { f.service.repo = hook }()
		in := c43CriticalInput("C", "B", "A")
		in.ID = p.ID
		in.ProbeMode = ProbeModeFirstAvailable
		if _, err := f.service.SavePolicy(t.Context(), c32REDUser, in); err != nil {
			t.Fatal(err)
		}
	}
	f.service.repo = hook
	in := c43CriticalInput("B", "A", "C")
	in.ID = p.ID
	saved, err := f.service.SavePolicy(t.Context(), c32REDUser, in)
	if err != nil {
		t.Fatal(err)
	}
	if !hook.fired || saved.ProbeMode != ProbeModeFirstAvailable {
		t.Fatal(saved)
	}
	c43AssertSaved(t, f, p.ID, ProbeModeFirstAvailable, true)
	names := []string{}
	for _, m := range saved.ModelTargets {
		names = append(names, m.ModelName)
	}
	if !reflect.DeepEqual(names, []string{"B", "A", "C"}) {
		t.Fatal("last save must still overwrite other fields", names)
	}
}
func TestProbeModelC43ManualProbeAndMetadata(t *testing.T) {
	f := c43CriticalFixture(t)
	c43CriticalPolicy(t, f, true, "A", "B", "C")
	if _, err := f.service.ProbeTarget(t.Context(), c32REDUser, c32REDTarget("2"), []string{"A"}); err == nil || err.Error() != ErrorNoMatchingModels {
		t.Fatal("nonselected model must be rejected", err)
	}
	models, err := f.service.ProbeTarget(t.Context(), c32REDUser, c32REDTarget("2"), []string{"B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ModelName != "B" || models[0].ModelSelection != "fallback" || !reflect.DeepEqual(models[0].SkippedModels, []string{"A"}) {
		t.Fatal(models)
	}
	groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range groups[0].Accounts {
		if a.ID == "2" {
			if len(a.ModelHealth) != 1 || a.ModelHealth[0].ModelSelection != "fallback" {
				t.Fatal(a)
			}
		}
		if a.ID == "4" {
			if len(a.UnprobedModels) != 1 || a.UnprobedModels[0].ModelSelection != "last_unconfirmed" {
				t.Fatal(a)
			}
		}
	}
}
func TestProbeModelC43TopLevelModelsPreserved(t *testing.T) {
	f := c43CriticalFixture(t)
	f.accounts = map[string]map[string]any{"1": f.accounts["1"]}
	f.accounts["1"]["models"] = "A"
	f.mapping("1", map[string]string{"B": "B"})
	c43CriticalPolicy(t, f, true, "A", "B")
	page := c43CriticalPage(t, f)
	if !reflect.DeepEqual(page["1"], []string{"A"}) {
		t.Fatal(page)
	}
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	events := c43CriticalEvents(t, f)
	if !reflect.DeepEqual(events[c32REDTarget("1")], []string{"A"}) {
		t.Fatal(events)
	}
	inv, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.service.loadAdminMonitoringScope(t.Context(), c32REDUser, c32REDWorkspace, *inv)
	if err != nil || !adminMonitoringScopeContainsTarget(scope, c32REDTarget("1")) {
		t.Fatal(scope, err)
	}
}
func TestProbeModelC43ModelControlChecksAllConfiguredModels(t *testing.T) {
	for _, mode := range []string{ProbeModeRealModel, ProbeModeFirstAvailable} {
		t.Run(mode, func(t *testing.T) {
			f := c43CriticalFixture(t)
			f.mapping("1", map[string]string{"B": "B"})
			c43CriticalPolicy(t, f, mode == ProbeModeFirstAvailable, "A", "B")
			c43CriticalState(t, f, "1", "A", StateSuspended, true)
			refresh, err := f.service.refreshAdminTarget(t.Context(), f.session, c32REDWorkspace, "1")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.configureTestTarget(t.Context(), c32REDUser, c32REDWorkspace, &refresh.target, refresh.memberships, true); err != nil {
				t.Fatal(err)
			}
			detail := upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: refresh.account}
			for _, op := range []string{"add", "restore"} {
				item := ModelControlItem{TargetID: c32REDTarget("1"), ModelName: "A"}
				object := modelControlTarget{}
				if op == "restore" {
					object.ClosedEntries = map[string]string{"A": "A"}
				}
				plan, err := f.service.computeModelControlPlan(t.Context(), c32REDUser, c32REDWorkspace, op, item, object, detail, refresh, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if plan.Preview.BlockReasonKey != modelControlError("RequestHealthSuspended") || plan.Preview.RequestHealth.Allowed == nil || *plan.Preview.RequestHealth.Allowed {
					t.Fatal(op, plan.Preview)
				}
			}
		})
	}
}

type c43ComparisonResult struct {
	summary        AdminGroupHealthSummary
	states         []State
	unprobed       []string
	priority       int
	priorityWrites []int
	status         string
	statusWrites   []string
}

func c43Comparison(t *testing.T, first bool, selectedState string) c43ComparisonResult {
	t.Helper()
	f := c43CriticalFixture(t)
	f.mapping("1", map[string]string{"B": "B"})
	f.mapping("2", map[string]string{"B": "B"})
	f.mapping("3", map[string]string{"B": "B"})
	f.accounts = map[string]map[string]any{"1": f.accounts["1"], "2": f.accounts["2"], "3": f.accounts["3"]}
	names := []string{"B"}
	if first {
		names = []string{"A", "B", "C"}
	}
	p := c43CriticalPolicy(t, f, first, names...)
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_policies SET priority_mode='multiplier',auto_remote_action_enabled=true WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	for id := range f.accounts {
		if id != "1" {
			c43CriticalState(t, f, id, "B", StateHealthy, true)
			continue
		}
		if selectedState != "none" {
			state := StateHealthy
			if selectedState == "suspended" {
				state = StateSuspended
			}
			c43CriticalState(t, f, id, "B", state, true)
		}
		c43CriticalState(t, f, id, "A", StateSuspended, true)
		c43CriticalState(t, f, id, "C", StateHealthy, true)
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
	return out
}
func TestProbeModelC43SingleModelComparison(t *testing.T) {
	for _, state := range []string{"none", "healthy", "suspended"} {
		t.Run(state, func(t *testing.T) {
			a := c43Comparison(t, false, state)
			b := c43Comparison(t, true, state)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("single=%+v ordered=%+v", a, b)
			}
			if state == "healthy" && len(a.priorityWrites) == 0 {
				t.Fatal("healthy comparison must include real Priority writes")
			}
			if state == "suspended" && (a.status != "inactive" || len(a.statusWrites) == 0) {
				t.Fatal("suspended comparison must include whole-account stop", a)
			}
		})
	}
}

// c43SwitchAfterValidatedSession makes workspace changes deterministic: the
// original Sub2API session is returned, then the current workspace switches
// before the service can resolve the group for its write.
type c43SwitchAfterValidatedSession struct {
	fakeMySitesReader
	afterFirstSession func()
	sessionReads      int
}

func (r *c43SwitchAfterValidatedSession) RequireSession(ctx context.Context, user, workspace string) (upstream.Session, error) {
	r.sessionReads++
	if workspace == c32REDWorkspace {
		session, err := r.fakeMySitesReader.RequireSession(ctx, user, workspace)
		if r.sessionReads == 1 {
			r.afterFirstSession()
		}
		return session, err
	}
	return upstream.Session{Platform: upstream.PlatformNewAPI}, nil
}
func c43WorkspaceWriteCounts(t *testing.T, f *c32REDFixture, workspace string) [3]int {
	t.Helper()
	var counts [3]int
	for index, table := range []string{"connection_health_policies", "connection_health_group_policy_assignments", "connection_health_priority_workspace_sync_states"} {
		if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE user_id=$1 AND admin_account_id=$2", c32REDUser, workspace).Scan(&counts[index]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}
func TestProbeModelC43QuickPolicyRejectsWorkspaceSwitchAfterValidation(t *testing.T) {
	f := c43CriticalFixture(t)
	reader := &c43SwitchAfterValidatedSession{fakeMySitesReader: fakeMySitesReader{session: f.session}, afterFirstSession: func() { f.service.accounts = fakeAdminAccountResolver{id: "ws2"} }}
	f.service.mySites = reader
	groups := &c43CountGroupReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "1", Name: "shared-group", Multiplier: float64Ptr(1)}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"1": {{ID: "1", Models: "A,B,C", Platform: "openai", Type: "apikey"}}}}}
	f.service.SetPlatformGroupReader(groups)
	beforeA, beforeB := c43WorkspaceWriteCounts(t, f, c32REDWorkspace), c43WorkspaceWriteCounts(t, f, "ws2")
	in := c43CriticalInput("C", "A", "B")
	in.ProbeMode = ProbeModeFirstAvailable
	_, err := f.service.SetAdminGroupPolicyConfiguration(t.Context(), c32REDUser, "1", AdminGroupPolicyConfigurationInput{QuickPolicy: &in})
	if err == nil || err.Error() != ErrorRequest {
		t.Errorf("workspace-switch quick save error=%v want=%s", err, ErrorRequest)
	}
	if reader.sessionReads != 1 {
		t.Errorf("session reads=%d want 1 (validated A; switched workspace session must not be read)", reader.sessionReads)
	}
	if groups.groupReads != 0 {
		t.Errorf("switched workspace group reads=%d want 0", groups.groupReads)
	}
	if got := c43WorkspaceWriteCounts(t, f, c32REDWorkspace); got != beforeA {
		t.Errorf("original workspace writes=%v before=%v", got, beforeA)
	}
	if got := c43WorkspaceWriteCounts(t, f, "ws2"); got != beforeB {
		t.Errorf("switched workspace writes=%v before=%v", got, beforeB)
	}
}
func TestProbeModelC43OrdinarySaveKeepsValidatedWorkspace(t *testing.T) {
	f := c43CriticalFixture(t)
	reader := &c43SwitchAfterValidatedSession{fakeMySitesReader: fakeMySitesReader{session: f.session}, afterFirstSession: func() { f.service.accounts = fakeAdminAccountResolver{id: "ws2"} }}
	f.service.mySites = reader
	in := c43CriticalInput("C", "A", "B")
	in.ProbeMode = ProbeModeFirstAvailable
	saved, err := f.service.SavePolicy(t.Context(), c32REDUser, in)
	if err != nil {
		t.Fatal(err)
	}
	if reader.sessionReads != 1 || saved.AdminAccountID != c32REDWorkspace || saved.ProbeMode != ProbeModeFirstAvailable {
		t.Fatal("ordinary save did not retain the validated workspace", saved, reader.sessionReads)
	}
	if got := c43WorkspaceWriteCounts(t, f, "ws2"); got != [3]int{} {
		t.Fatal("ordinary save wrote switched workspace", got)
	}
}

func TestProbeModelC43DefaultQuickPolicyKeepsExistingWorkspaceFlow(t *testing.T) {
	f := c43CriticalFixture(t)
	reader := &c43SwitchAfterValidatedSession{fakeMySitesReader: fakeMySitesReader{session: f.session}, afterFirstSession: func() { f.service.accounts = fakeAdminAccountResolver{id: "ws2"} }}
	f.service.mySites = reader
	result, err := f.service.SetAdminGroupPolicyConfiguration(t.Context(), c32REDUser, "1", AdminGroupPolicyConfigurationInput{QuickPolicy: func() *PolicyInput { in := c43CriticalInput("C", "A", "B"); return &in }()})
	if err != nil {
		t.Fatal(err)
	}
	if reader.sessionReads != 1 || len(result.PolicyIDs) != 1 {
		t.Fatal("default quick save added a session check", result, reader.sessionReads)
	}
	c43AssertSaved(t, f, result.PolicyIDs[0], ProbeModeRealModel, false)
	if got := c43WorkspaceWriteCounts(t, f, "ws2"); got != [3]int{} {
		t.Fatal("default quick save wrote switched workspace", got)
	}
}

// Errors are deliberately plain errors to exercise the real HTTP error mapper.
type c43ExpiringSessionReader struct {
	fakeMySitesReader
	afterFirstSession func()
	failAtRead        int
	reads             int
	expired           error
}

func (r *c43ExpiringSessionReader) RequireSession(ctx context.Context, user, workspace string) (upstream.Session, error) {
	r.reads++
	if r.reads >= r.failAtRead {
		return upstream.Session{}, r.expired
	}
	session, err := r.fakeMySitesReader.RequireSession(ctx, user, workspace)
	if r.reads == 1 && r.afterFirstSession != nil {
		r.afterFirstSession()
	}
	return session, err
}

type c43CountGroupReader struct {
	fakePlatformGroupReader
	groupReads int
}

func (r *c43CountGroupReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.groupReads++
	return r.fakePlatformGroupReader.FetchAdminAllGroups(session)
}
func TestProbeModelC43QuickPolicySessionFailureHTTP(t *testing.T) {
	for _, tc := range []struct {
		name               string
		mode               string
		switchWorkspace    bool
		failAtRead, status int
		key                string
	}{
		{"switch to workspace without session", ProbeModeFirstAvailable, true, 2, http.StatusBadRequest, ErrorRequest},
		{"same workspace second session expired", ProbeModeFirstAvailable, false, 2, http.StatusBadRequest, ErrorRequest},
		{"default retains session error", "", false, 1, http.StatusInternalServerError, ErrorUnknown},
		{"explicit default retains session error", ProbeModeRealModel, false, 1, http.StatusInternalServerError, ErrorUnknown},
		{"unknown probe mode without session", "future_mode", false, 1, http.StatusBadRequest, ErrorRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := c43CriticalFixture(t)
			expired := errors.New("fixture session expired")
			reader := &c43ExpiringSessionReader{fakeMySitesReader: fakeMySitesReader{session: f.session}, failAtRead: tc.failAtRead, expired: expired, afterFirstSession: func() {
				if tc.switchWorkspace {
					f.service.accounts = fakeAdminAccountResolver{id: "ws2"}
				}
			}}
			f.service.mySites = reader
			groups := &c43CountGroupReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "1", Name: "shared-group"}}}}
			f.service.SetPlatformGroupReader(groups)
			beforeA, beforeB := c43WorkspaceWriteCounts(t, f, c32REDWorkspace), c43WorkspaceWriteCounts(t, f, "ws2")
			in := c43CriticalInput("C", "A", "B")
			in.ProbeMode = tc.mode
			code, result := f.request(http.MethodPut, "admin-groups/1/policy-configuration", map[string]any{"quickPolicy": c43InputBody(in)})
			if code != tc.status || result["message"] != tc.key {
				t.Errorf("status=%d response=%v want status=%d message=%s", code, result, tc.status, tc.key)
			}
			if groups.groupReads != 0 {
				t.Errorf("group reads=%d want zero after session/workspace rejection", groups.groupReads)
			}
			if tc.mode == "future_mode" && reader.reads != 0 {
				t.Errorf("unknown-mode session reads=%d want 0", reader.reads)
			}
			if got := c43WorkspaceWriteCounts(t, f, c32REDWorkspace); got != beforeA {
				t.Errorf("original workspace writes=%v before=%v", got, beforeA)
			}
			if got := c43WorkspaceWriteCounts(t, f, "ws2"); got != beforeB {
				t.Errorf("switched workspace writes=%v before=%v", got, beforeB)
			}
			if tc.mode == "" || tc.mode == ProbeModeRealModel {
				_, err := f.service.SetAdminGroupPolicyConfiguration(t.Context(), c32REDUser, "1", AdminGroupPolicyConfigurationInput{QuickPolicy: &in})
				if err != expired {
					t.Errorf("default service error=%v want original error instance", err)
				}
			}
		})
	}
}

// C5.2 business expectations, authored by the primary agent before implementation.
func TestProbeModelC43CNSelection(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(platform, func(t *testing.T) {
			for _, tc := range []struct {
				name        string
				mapping     map[string]string
				known       bool
				accountType string
				want, kind  string
				skipped     []string
			}{
				{"first", map[string]string{"A": "A"}, true, "apikey", "A", "", nil},
				{"absent first", map[string]string{"B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
				{"renamed first", map[string]string{"A": "other", "B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
				{"absent all", map[string]string{"other": "other"}, true, "apikey", "C", "last_unconfirmed", nil},
				{"renamed all", map[string]string{"A": "other", "B": "other", "C": "other"}, true, "apikey", "C", "last_unconfirmed", nil},
				{"empty", nil, true, "apikey", "C", "last_unconfirmed", nil},
				{"unknown", map[string]string{"A": "A"}, false, "apikey", "C", "last_unconfirmed", nil},
				{"unsupported type", map[string]string{"A": "A"}, true, "oauth", "C", "last_unconfirmed", nil},
				{"wildcard identity", map[string]string{"A*": "A"}, true, "apikey", "A", "", nil},
				{"wildcard renamed", map[string]string{"A*": "other", "B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
				{"exact wins", map[string]string{"A*": "other", "A": "A"}, true, "apikey", "A", "", nil},
				{"exact rename wins", map[string]string{"A*": "A", "A": "other", "B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
				{"longest wins", map[string]string{"*": "other", "A*": "A"}, true, "apikey", "A", "", nil},
				{"longest rename wins", map[string]string{"*": "A", "A*": "other", "B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
				{"no new wildcard", map[string]string{"A?": "A", "B": "B"}, true, "apikey", "B", "fallback", []string{"A"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					source := probeModelSource{accountType: tc.accountType, platform: platform, modelMappingKnown: tc.known, modelMapping: tc.mapping, openAIPassthrough: true}
					got := selectProbeModel(c43OrderedPolicy("p", "A", "B", "C"), source)
					if got.modelName != tc.want || got.kind != tc.kind || !reflect.DeepEqual(got.skippedModels, tc.skipped) {
						t.Fatalf("selection=%+v want=%s/%s/%v", got, tc.want, tc.kind, tc.skipped)
					}
					single := selectProbeModel(c43OrderedPolicy("p", "A"), source)
					if single.modelName != "A" || single.kind != "" || len(single.skippedModels) != 0 {
						t.Fatal(single)
					}
					p := c43OrderedPolicy("p", "disabled", " ")
					p.ModelTargets[0].Enabled = false
					if empty := selectProbeModel(p, source); empty.modelName != "" || empty.kind != "" {
						t.Fatal(empty)
					}
				})
			}
			// Actual candidate output, rather than a boolean helper assertion.
			source := probeModelSource{accountType: "apikey", platform: platform, modelMappingKnown: true, modelMapping: map[string]string{"A": "A"}}
			specs := candidateModelSpecs(nil, []Policy{c43OrderedPolicy("p", "A", "B")}, "sub2api", source, true)
			if len(specs) != 1 || specs[0].modelName != "A" || specs[0].modelSelection != "" {
				t.Fatal(specs)
			}
		})
	}
}
