package connection_health

import (
	"reflect"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func modelControlTestAccount(mapping map[string]string) upstream.AdminGroupAccountInfo {
	yes := true
	zero := 0.0
	return upstream.AdminGroupAccountInfo{ID: "1", Platform: "openai", Type: "apikey", Status: "active", Schedulable: &yes, ModelMapping: mapping, ModelMappingKnown: true, TempUnschedulableKnown: true, OverloadKnown: true, RateLimitKnown: true, ExpiresAtKnown: true, AutoPauseOnExpired: &yes, QuotaKnown: true, QuotaLimit: &zero, ModelRateLimitsKnown: true}
}
func TestModelControlDecisionAndRoundSelection(t *testing.T) {
	rule := ModelControlRule{MinAccuracyPercent: 50, MinJudgedAnswers: 3, IncludeManual: true, IncludeScheduled: true}
	for _, tc := range []struct {
		name  string
		round *modelControlRound
		want  string
	}{{"none", nil, "no_evidence"}, {"running", &modelControlRound{Running: true, Unreviewed: 1}, "testing"}, {"review", &modelControlRound{Correct: 10, Unreviewed: 1}, "awaiting_review"}, {"insufficient", &modelControlRound{Correct: 2}, "insufficient"}, {"all_failed", &modelControlRound{Failed: 3}, "insufficient"}, {"below", &modelControlRound{Correct: 1, Incorrect: 2}, "close_recommended"}, {"boundary", &modelControlRound{Correct: 2, Incorrect: 2}, "usable"}, {"above", &modelControlRound{Correct: 2, Incorrect: 1}, "usable"}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateModelControlDecision(tc.round, rule); got.Decision != tc.want {
				t.Fatalf("decision=%+v", got)
			}
		})
	}
	rounds := []modelControlRound{{BatchID: "new", Source: "manual", Running: true}, {BatchID: "scheduled", Source: "scheduled", Correct: 3}, {BatchID: "older", Source: "manual", Incorrect: 3}}
	latest, previous, _ := selectModelControlRounds(rounds, rule)
	if latest.BatchID != "new" || previous.BatchID != "scheduled" {
		t.Fatal("testing reference did not select latest terminal")
	}
	rule = modelControlWorkspaceRule(ModelControlSettings{60, 4, 2}, "M")
	if !rule.IncludeManual || !rule.IncludeScheduled || rule.Version != 2 || rule.MinAccuracyPercent != 60 || rule.MinJudgedAnswers != 4 {
		t.Fatal("workspace rule did not preserve both source types")
	}
	rounds = append([]modelControlRound{{BatchID: "run-now", Source: "run_now", Correct: 3}}, rounds...)
	latest, _, _ = selectModelControlRounds(rounds, rule)
	if latest.BatchID != "run-now" {
		t.Fatal("run-now source excluded")
	}

}
func TestModelControlSupportsAndCloseBranches(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mapping       map[string]string
		edit          func(*upstream.AdminGroupAccountInfo)
		state, reason string
	}{{"aliases", map[string]string{"a": "A", "alias": "A", "b": "B"}, nil, "serving", ""}, {"type", map[string]string{"a": "A"}, func(a *upstream.AdminGroupAccountInfo) { a.Type = "oauth" }, "not_isolatable", "AccountTypeUnsupported"}, {"platform", map[string]string{"a": "A"}, func(a *upstream.AdminGroupAccountInfo) { a.Platform = "grok" }, "not_isolatable", "AccountTypeUnsupported"}, {"pass", map[string]string{"a": "A"}, func(a *upstream.AdminGroupAccountInfo) { a.OpenAIPassthrough = true }, "not_isolatable", "OpenAIPassthrough"}, {"empty", map[string]string{}, nil, "not_isolatable", "EmptyMapping"}, {"wild_target", map[string]string{"a*": "A", "b": "B"}, nil, "not_isolatable", "WildcardMapping"}, {"missing", map[string]string{"b": "B"}, nil, "not_provided", "NotProvided"}, {"wild_alias", map[string]string{"aa": "A", "a*": "B"}, nil, "not_isolatable", "WildcardFallback"}, {"last", map[string]string{"a": "A", "alias": "A"}, nil, "last_model", "LastModel"}} {
		t.Run(tc.name, func(t *testing.T) {
			a := modelControlTestAccount(tc.mapping)
			if tc.edit != nil {
				tc.edit(&a)
			}
			p := planModelClose(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, "A")
			if p.State != tc.state || tc.reason != "" && p.ReasonKey != modelControlError(tc.reason) {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "aliases" && (!reflect.DeepEqual(p.Entries, map[string]string{"a": "A", "alias": "A"}) || !reflect.DeepEqual(p.AfterMapping, map[string]string{"b": "B"})) {
				t.Fatal("alias close modified unrelated model")
			}
		})
	}
	a := modelControlTestAccount(map[string]string{"gpt-*": "up"})
	for _, key := range []string{"gpt-a", "gpt-*"} {
		if supported, known := modelControlSupports(a, key); !supported || !known {
			t.Fatal("supported wildcard key rejected")
		}
	}
	if supported, known := modelControlSupports(a, "other"); supported || !known {
		t.Fatal("unsupported key allowed")
	}
	a.ModelMappingKnown = false
	if _, known := modelControlSupports(a, "other"); known {
		t.Fatal("unknown mapping treated as known")
	}
	a.OpenAIPassthrough = true
	if supported, known := modelControlSupports(a, "other"); !supported || !known {
		t.Fatal("OpenAI passthrough did not bypass")
	}
	a.Platform = "anthropic"
	if _, known := modelControlSupports(a, "other"); known {
		t.Fatal("Anthropic used OpenAI passthrough")
	}
	a = modelControlTestAccount(map[string]string{})
	if supported, known := modelControlSupports(a, "any-public-model"); !supported || !known {
		t.Fatal("known empty whitelist did not allow the default source")
	}
	a.ModelMapping = map[string]string{"exact": "upstream"}
	if supported, known := modelControlSupports(a, "exact"); !supported || !known {
		t.Fatal("exact public key did not count as a source")
	}
	a.Type = "oauth"
	if supported, known := modelControlSupports(a, "exact"); supported || known {
		t.Fatal("unsupported account type claimed a known source")
	}
}
func TestModelControlRestoreAndAttributionTable(t *testing.T) {
	a := modelControlTestAccount(map[string]string{"same": "A", "changed": "X", "other": "B"})
	closed := map[string]string{"missing": "A", "same": "A", "changed": "A"}
	p := planModelRestore(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, closed)
	if p.NoRemoteWrite || p.AfterMapping["changed"] != "X" || len(p.Entries) != 1 || len(p.ManualRestored) != 1 || len(p.ManualChanged) != 1 {
		t.Fatalf("restore=%+v", p)
	}
	a.Type = "oauth"
	if p := planModelRestore(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, closed); p.ReasonKey != modelControlError("RestoreTypeChanged") {
		t.Fatal("type conflict absent")
	}
	a.Type = "apikey"
	a.OpenAIPassthrough = true
	if p := planModelRestore(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, closed); p.ReasonKey != modelControlError("RestorePassthrough") {
		t.Fatal("pass conflict absent")
	}
	a.OpenAIPassthrough = false
	a.ModelMapping = map[string]string{}
	if p := planModelRestore(upstream.Sub2APIModelControlAccount{AdminGroupAccountInfo: a}, closed); p.ReasonKey != modelControlError("RestoreMappingEmpty") {
		t.Fatal("empty conflict absent")
	}
	mapping := map[string]string{"same": "A", "changed": "X", "unrelated": "B"}
	for _, op := range []string{"close", "restore"} {
		pending := &modelControlPending{Operation: op, Entries: closed}
		attr := attributeModelControlEntries(mapping, closed, pending)
		if len(attr.ClosedEntries) != 1 || attr.ClosedEntries["missing"] != "A" || attr.Outcome != "partial" {
			t.Fatalf("%s attribution=%+v", op, attr)
		}
		states := map[string]string{}
		for _, entry := range attr.Entries {
			states[entry.Key] = entry.State
		}
		wantStates := map[string]string{"missing": "closed", "same": "not_deleted", "changed": "manually_changed"}
		if op == "restore" {
			wantStates = map[string]string{"missing": "still_closed", "same": "restored", "changed": "manually_changed"}
		}
		if !reflect.DeepEqual(states, wantStates) {
			t.Fatalf("%s per-entry recognition=%v", op, states)
		}
		for _, all := range []bool{false, true} {
			m := map[string]string{}
			if all {
				m = cloneModelMapping(closed)
			}
			out := attributeModelControlEntries(m, closed, pending).Outcome
			want := "failed"
			if op == "close" && !all || op == "restore" && all {
				want = "succeeded"
			}
			if out != want {
				t.Fatalf("%s all=%t outcome=%s", op, all, out)
			}
		}
	}
}
func TestModelControlFingerprintAndRestrictionSources(t *testing.T) {
	now := time.Now()
	a := modelControlTestAccount(map[string]string{"a": "A"})
	b := a
	b.ID = "2"
	inventory := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: []upstream.AdminGroupAccountInfo{a, b}}, {group: upstream.AdminGroupInfo{ID: "g2"}, accounts: []upstream.AdminGroupAccountInfo{a, b}}}}
	count := func(b upstream.AdminGroupAccountInfo, hp map[string]bool, reserved map[string]map[string]bool) []modelSourceCount {
		inv := inventory
		inv.groups = append([]adminInventoryGroup{}, inventory.groups...)
		for n := range inv.groups {
			inv.groups[n].accounts = []upstream.AdminGroupAccountInfo{a, b}
		}
		return countModelSources(inv, a, a.ModelMapping, hp, reserved, now)
	}
	for _, tc := range []struct {
		name string
		edit func(*upstream.AdminGroupAccountInfo)
	}{{"temp", func(a *upstream.AdminGroupAccountInfo) { v := now.Add(time.Hour); a.TempUnschedulableUntil = &v }}, {"temp_unknown", func(a *upstream.AdminGroupAccountInfo) { a.TempUnschedulableKnown = false }}, {"overload", func(a *upstream.AdminGroupAccountInfo) { v := now.Add(time.Hour); a.OverloadUntil = &v }}, {"global", func(a *upstream.AdminGroupAccountInfo) { v := now.Add(time.Hour); a.RateLimitResetAt = &v }}, {"expires", func(a *upstream.AdminGroupAccountInfo) { v := now.Add(-time.Hour); a.ExpiresAt = &v }}, {"expires_unknown", func(a *upstream.AdminGroupAccountInfo) { a.ExpiresAtKnown = false }}, {"quota", func(a *upstream.AdminGroupAccountInfo) { v := 1.0; a.QuotaLimit = &v; a.QuotaUsed = &v }}, {"quota_unknown", func(a *upstream.AdminGroupAccountInfo) { a.QuotaKnown = false }}, {"model", func(a *upstream.AdminGroupAccountInfo) {
		v := now.Add(time.Hour)
		a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "A", ResetAt: &v, Known: true}}
	}}, {"model_unknown", func(a *upstream.AdminGroupAccountInfo) { a.ModelRateLimitsKnown = false }}, {"sched", func(a *upstream.AdminGroupAccountInfo) { v := false; a.Schedulable = &v }}, {"status", func(a *upstream.AdminGroupAccountInfo) { a.Status = "inactive" }}} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := b
			tc.edit(&candidate)
			counts := count(candidate, nil, nil)
			if len(counts) != 2 || *counts[0].Count != 0 || *counts[1].Count != 0 {
				t.Fatalf("restriction counted=%v", counts)
			}
		})
	}
	if got := count(b, nil, nil); *got[0].Count != 1 || !got[0].OK {
		t.Fatal("valid source missing")
	}
	if got := count(b, map[string]bool{"2": true}, nil); *got[0].Count != 0 {
		t.Fatal("health pending counted")
	}
	if got := count(b, nil, map[string]map[string]bool{"2": {"a": true}}); *got[0].Count != 0 {
		t.Fatal("reserved model counted")
	}
	if got := count(b, nil, map[string]map[string]bool{"2": {"*": true}}); *got[0].Count != 0 {
		t.Fatal("account pending counted")
	}
	inventory.groupsComplete = false
	if got := countModelSources(inventory, a, a.ModelMapping, nil, nil, now); got[0].Count != nil {
		t.Fatal("incomplete inventory produced numeric source count")
	}
	mapping := map[string]string{"b": "B", "a": "A"}
	if modelControlPlanFingerprint("close", mapping, a.ModelMapping) != modelControlPlanFingerprint("close", map[string]string{"a": "A", "b": "B"}, a.ModelMapping) {
		t.Fatal("fingerprint depends on map order")
	}
	off := false
	on := true
	if modelControlPlanFingerprint("close_account", mapping, mapping, &off) == modelControlPlanFingerprint("close_account", mapping, mapping, &on) {
		t.Fatal("schedulable absent from account fingerprint")
	}
}

func TestModelControlAllKnownRestrictionBranches(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	for _, restriction := range []string{"temp", "overload", "global", "expiration", "total_quota", "daily_quota", "weekly_quota", "model_key", "model_alias", "model_wildcard"} {
		for _, state := range []string{"inactive", "active", "unknown"} {
			t.Run(restriction+"/"+state, func(t *testing.T) {
				a := modelControlTestAccount(map[string]string{"a": "A"})
				known := state != "unknown"
				deadline := &past
				if state == "active" {
					deadline = &future
				}
				switch restriction {
				case "temp":
					a.TempUnschedulableKnown = known
					a.TempUnschedulableUntil = deadline
				case "overload":
					a.OverloadKnown = known
					a.OverloadUntil = deadline
				case "global":
					a.RateLimitKnown = known
					a.RateLimitResetAt = deadline
				case "expiration":
					a.ExpiresAtKnown = known
					a.ExpiresAt = &future
					if state == "active" {
						a.ExpiresAt = &past
					}
				case "total_quota", "daily_quota", "weekly_quota":
					limit := 2.0
					used := 1.0
					if state == "active" {
						used = 2
					}
					a.QuotaKnown = known
					switch restriction {
					case "total_quota":
						a.QuotaLimit = &limit
						a.QuotaUsed = &used
					case "daily_quota":
						a.QuotaDailyLimit = &limit
						a.QuotaDailyUsed = &used
					case "weekly_quota":
						a.QuotaWeeklyLimit = &limit
						a.QuotaWeeklyUsed = &used
					}
				default:
					model := "a"
					if restriction == "model_alias" {
						model = "A"
					}
					if restriction == "model_wildcard" {
						a.ModelMapping = map[string]string{"a*": "A"}
						model = "A"
					}
					a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: model, Known: known, ResetAt: deadline}}
				}
				want := state == "inactive"
				if got := modelControlSourceSchedulable(a, "a", now); got != want {
					t.Fatalf("source=%t want=%t", got, want)
				}
			})
		}
	}
	a := modelControlTestAccount(map[string]string{"a": "A"})
	off := false
	a.AutoPauseOnExpired = &off
	a.ExpiresAt = &past
	if !modelControlSourceSchedulable(a, "a", now) {
		t.Fatal("expired account without auto-pause excluded")
	}
	a.AutoPauseOnExpired = nil
	if modelControlSourceSchedulable(a, "a", now) {
		t.Fatal("unknown auto-pause counted")
	}
	mapping := map[string]string{"a*": "broad", "abc*": "specific"}
	if modelControlMappedModel(mapping, "abcdef") != "specific" {
		t.Fatal("wildcard mapping did not use longest matching prefix")
	}
}
