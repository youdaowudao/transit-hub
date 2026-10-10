package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlV2911SummaryPathsHaveSameStatesDatesAndWorkspaceRules(t *testing.T) {
	f := newC3REDFixture(t)
	now := time.Now().UTC()
	cases := []struct {
		model, state, reason, status string
		closed                       bool
		checked                      *time.Time
		today                        bool
	}{
		{"a", "serving", "", "open", false, &now, true},
		{"b", "not_isolatable", "OpenAIPassthrough", "open", false, nil, false},
		{"c", "not_isolatable", "AccountReadFailed", "unknown", false, &now, false},
		{"d", "account_missing", "", "closed", true, nil, false},
		{"e", "not_provided", "", "not_provided", false, nil, false},
		{"f", "account_missing", "", "account_missing", false, nil, false},
		{"g", "not_isolatable", "AccountTypeUnsupported", "unknown", false, nil, false},
	}
	// Old model rows intentionally disagree with the active workspace settings.
	for _, tc := range cases {
		f.round("1", tc.model, 1, 2, 0)
		f.manage("1", tc.model)
		if !tc.today {
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET created_at=created_at-interval '1 day' WHERE model_name=$1`, tc.model); err != nil {
				t.Fatal(err)
			}
		}

	}
	for _, tc := range cases {
		closed := map[string]string{}
		if tc.closed {
			closed[tc.model] = tc.model
		}
		reason := ""
		if tc.reason != "" {
			reason = modelControlError(tc.reason)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET observed_state=$1,observed_reason=$2,observed_at=$3,closed_entries=$4 WHERE model_name=$5`, tc.state, reason, tc.checked, closed, tc.model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_rules SET min_accuracy_percent=1,include_manual=false,include_scheduled=true`); err != nil {
		t.Fatal(err)
	}
	f.expect("PUT", "model-control/settings", map[string]any{"minAccuracyPercent": 60, "minJudgedAnswers": 3, "expectedVersion": 0}, 200)
	repo := f.service.questionAnswers.(*Repository)
	sqlSummary, err := repo.queryModelControlAccountSummaries(t.Context(), c3REDUser, c3REDWorkspace, []string{c3REDTarget("1")})
	if err != nil {
		t.Fatal(err)
	}
	serviceSummary, err := listModelControlAccountSummaries(t.Context(), repo, c3REDUser, c3REDWorkspace, []string{c3REDTarget("1")})
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range serviceSummary {
		for i := range summary.Models {
			if checked := summary.Models[i].CheckedAt; checked != nil {
				stamp := checked.UTC()
				summary.Models[i].CheckedAt = &stamp
			}
		}
	}
	sqlJSON, _ := json.Marshal(sqlSummary)
	serviceJSON, _ := json.Marshal(serviceSummary)
	if string(sqlJSON) != string(serviceJSON) {
		t.Fatalf("paths disagree SQL=%s service=%s", sqlJSON, serviceJSON)
	}
	summary := sqlSummary[c3REDTarget("1")]
	if summary.Open != 2 || summary.Closed != 1 || summary.Attention != 3 || len(summary.Models) != len(cases) {
		t.Fatalf("summary=%+v", summary)
	}
	for i, tc := range cases {
		actual := summary.Models[i]
		wantDecision := "no_evidence"
		if tc.today {
			wantDecision = "close_recommended"
		}
		if actual.ModelName != tc.model || actual.Status != tc.status || actual.Decision != wantDecision || (actual.CheckedAt == nil) != (tc.checked == nil) {
			t.Fatalf("model=%+v case=%+v", actual, tc)
		}
		if tc.checked != nil && !actual.CheckedAt.Equal(tc.checked.Truncate(time.Microsecond)) {
			t.Fatal("stored checked time not returned")
		}
	}
	for _, ws := range []string{"other-workspace", c3REDWorkspace} {
		isolated, err := repo.queryModelControlAccountSummaries(t.Context(), "other-user", ws, []string{c3REDTarget("1")})
		if err != nil || len(isolated) != 0 {
			t.Fatal("summary crossed user/workspace", isolated, err)
		}
	}
	items, _, err := f.service.modelControlItems(t.Context(), c3REDUser, c3REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	counts := modelControlCounts(items)
	if counts != (ModelControlCounts{Total: 7, Open: 2, Closed: 1, Attention: 3, Untested: 6}) {
		t.Fatal(counts)
	}
}

func TestModelControlV2911SupplyAllRestrictionBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	no := false
	limit, used := 10.0, 9.0
	for _, tc := range []struct {
		name          string
		edit          func(*upstream.AdminGroupAccountInfo)
		open, unknown bool
	}{
		{"inactive", func(a *upstream.AdminGroupAccountInfo) { a.Status = "inactive" }, false, false},
		{"unknown-status-no-keys", func(a *upstream.AdminGroupAccountInfo) {
			a.Status = "???"
			a.ModelMapping = map[string]string{"x": "X"}
		}, false, false},
		{"temporary-block", func(a *upstream.AdminGroupAccountInfo) { a.TempUnschedulableUntil = &future }, false, false},
		{"overload-block", func(a *upstream.AdminGroupAccountInfo) { a.OverloadUntil = &future }, false, false},
		{"rate-block", func(a *upstream.AdminGroupAccountInfo) { a.RateLimitResetAt = &future }, false, false},
		{"expiry-block", func(a *upstream.AdminGroupAccountInfo) { a.ExpiresAt = &past }, false, false},
		{"expired-auto-pause-off", func(a *upstream.AdminGroupAccountInfo) { a.ExpiresAt = &past; a.AutoPauseOnExpired = &no }, true, false},
		{"elapsed-restrictions", func(a *upstream.AdminGroupAccountInfo) {
			a.TempUnschedulableUntil = &past
			a.OverloadUntil = &past
			a.RateLimitResetAt = &past
		}, true, false},
		{"missing-temp", func(a *upstream.AdminGroupAccountInfo) { a.TempUnschedulableKnown = false }, false, true},
		{"missing-overload", func(a *upstream.AdminGroupAccountInfo) { a.OverloadKnown = false }, false, true},
		{"missing-rate", func(a *upstream.AdminGroupAccountInfo) { a.RateLimitKnown = false }, false, true},
		{"missing-quota", func(a *upstream.AdminGroupAccountInfo) { a.QuotaKnown = false }, false, true},
		{"missing-expiry", func(a *upstream.AdminGroupAccountInfo) { a.ExpiresAtKnown = false }, false, true},
		{"missing-auto-pause", func(a *upstream.AdminGroupAccountInfo) { a.AutoPauseOnExpired = nil }, false, true},
		{"daily-quota-exhausted", func(a *upstream.AdminGroupAccountInfo) { a.QuotaDailyLimit = &limit; a.QuotaDailyUsed = &limit }, false, false},
		{"weekly-quota-exhausted", func(a *upstream.AdminGroupAccountInfo) { a.QuotaWeeklyLimit = &limit; a.QuotaWeeklyUsed = &limit }, false, false},
		{"daily-quota-missing", func(a *upstream.AdminGroupAccountInfo) { a.QuotaDailyLimit = &limit }, false, true},
		{"weekly-quota-missing", func(a *upstream.AdminGroupAccountInfo) { a.QuotaWeeklyLimit = &limit }, false, true},
		{"quota-available", func(a *upstream.AdminGroupAccountInfo) { a.QuotaLimit = &limit; a.QuotaUsed = &used }, true, false},
		{"one-alias-limited", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "a", ResetAt: &future, Known: true}}
		}, true, false},
		{"both-aliases-limited", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "a", ResetAt: &future, Known: true}, {Model: "b", ResetAt: &future, Known: true}}
		}, false, false},
		{"upstream-limited", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "M", ResetAt: &future, Known: true}}
		}, false, false},
		{"other-model-limit", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "X", ResetAt: &future, Known: true}}
		}, true, false},
		{"elapsed-model-limit", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "M", ResetAt: &past, Known: true}}
		}, true, false},
		{"one-alias-unknown", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "a", Known: false}}
		}, true, false},
		{"upstream-limit-unknown", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "M", Known: false}}
		}, false, true},
		{"unknown-account-all-keys-blocked", func(a *upstream.AdminGroupAccountInfo) {
			a.Schedulable = nil
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "M", ResetAt: &future, Known: true}}
		}, false, false},
		{"passthrough-limits-use-M", func(a *upstream.AdminGroupAccountInfo) {
			a.OpenAIPassthrough = true
			a.ModelMapping = map[string]string{"M": "X"}
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "M", ResetAt: &future, Known: true}}
		}, false, false},
		{"wildcard-limits-use-key", func(a *upstream.AdminGroupAccountInfo) {
			a.ModelMapping = map[string]string{"gpt-*": "M"}
			a.ModelRateLimits = []upstream.Sub2APIModelRateLimit{{Model: "gpt-*", ResetAt: &future, Known: true}}
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := modelControlTestAccount(map[string]string{"a": "M", "b": "M"})
			tc.edit(&a)
			keys, known, eligible := modelControlUpstreamKeys(a, "M")
			if !eligible {
				t.Fatal("valid API key excluded")
			}
			open, unknown := modelControlSupplyState(a, keys, known, "M", now)
			if open != tc.open || unknown != tc.unknown {
				t.Fatalf("open=%v unknown=%v want=%v/%v", open, unknown, tc.open, tc.unknown)
			}
		})
	}
}

func TestModelControlV2911GroupSupplyDeduplicatesAndCountsClosedAcrossTypes(t *testing.T) {
	now := time.Now()
	accounts := []upstream.AdminGroupAccountInfo{}
	summaries := map[string]*ModelControlAccountSummary{}
	for i := 0; i < 12; i++ {
		a := modelControlTestAccount(map[string]string{"alias": "M"})
		a.ID = fmt.Sprint(i)
		a.Name = "account-" + a.ID
		accounts = append(accounts, a)
		summaries[buildTargetID("sub2api", "ws", a.ID)] = &ModelControlAccountSummary{Models: []ModelControlSummaryModel{{ModelName: "M", Status: "closed"}, {ModelName: "A", Status: "unknown"}}}
	}
	oauth := modelControlTestAccount(nil)
	oauth.ID = "oauth"
	oauth.Type = "oauth"
	oauth.Name = "changed-type"
	other := modelControlTestAccount(nil)
	other.ID = "grok"
	other.Platform = "grok"
	accounts = append(accounts, accounts[0], oauth, other)
	summaries[buildTargetID("sub2api", "ws", oauth.ID)] = &ModelControlAccountSummary{Models: []ModelControlSummaryModel{{ModelName: "M", Status: "closed"}}}
	supply := buildModelControlGroupSupply(accounts, "sub2api", "ws", summaries, now)
	if supply == nil || supply.OtherTypeAccounts != 2 || len(supply.Items) != 2 || supply.Items[0].ModelName != "A" {
		t.Fatal(supply)
	}
	item := supply.Items[1]
	if item.Open != 12 || item.Closed != 13 || item.Unknown != 0 || len(item.OpenAccounts) != 10 || len(item.ClosedAccounts) != 10 {
		t.Fatal(item)
	}
	if buildModelControlGroupSupply(accounts, "sub2api", "other", summaries, now) != nil {
		t.Fatal("supply crossed workspace")
	}
}

func TestModelControlV2911AddAdmissionRejectsAllIneligibleStates(t *testing.T) {
	good := ModelControlItem{Decision: "usable", Control: modelControlControl{Observation: modelControlObservation{State: "not_provided"}}}
	if modelControlAdmission(good, "add") != nil {
		t.Fatal("good add rejected")
	}
	for _, tc := range []struct {
		name string
		edit func(*ModelControlItem)
	}{
		{"closed", func(i *ModelControlItem) { i.Control.ClosedEntries = map[string]string{"M": "M"} }},
		{"serving", func(i *ModelControlItem) { i.Control.Observation.State = "serving" }},
		{"unverified", func(i *ModelControlItem) { i.Control.Observation.State = "unverified" }},
		{"untested", func(i *ModelControlItem) { i.Decision = "no_evidence" }},
		{"testing", func(i *ModelControlItem) { i.Decision = "testing" }},
		{"insufficient", func(i *ModelControlItem) { i.Decision = "insufficient" }},
		{"pending-other-model", func(i *ModelControlItem) { i.Control.AccountPending = &modelControlAccountPending{ModelName: "B"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := good
			tc.edit(&item)
			if modelControlAdmission(item, "add") == nil {
				t.Fatal("ineligible add admitted")
			}
		})
	}
}

func TestModelControlV2911AddBlockReasonsPersistEntriesAndNeverConflict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping map[string]string
		edit    func(map[string]any)
		reason  string
	}{
		{"type", map[string]string{"b": "B"}, func(a map[string]any) { a["type"] = "oauth" }, "AddTypeUnsupported"},
		{"pass", map[string]string{"b": "B"}, func(a map[string]any) { a["extra"].(map[string]any)["openai_passthrough"] = true }, "AddPassthrough"},
		{"unknown", map[string]string{"b": "B"}, nil, "AccountReadFailed"},
		{"empty", map[string]string{}, nil, "AddMappingEmpty"},
		{"provided", map[string]string{"alias": "A"}, nil, "AlreadyProvided"},
		{"key", map[string]string{"A": "B"}, nil, "AddKeyConflict"},
		{"wildcard", map[string]string{"A*": "B"}, nil, "AddWildcardConflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 3, 0, 0)
			f.manage("1", "A")
			if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET observed_state='not_provided' WHERE model_name='A'`); err != nil {
				t.Fatal(err)
			}
			f.mapping("1", tc.mapping)
			if tc.edit != nil {
				f.change("1", tc.edit)
			}
			if tc.name == "unknown" {
				f.service.modelControlActions = modelControlUnreadableMapping{f.service.modelControlActions}
			}
			p := f.preview("1", "A", "add")
			if p["blockReasonKey"] != modelControlError(tc.reason) || len(p["groups"].([]any)) != 0 {
				t.Fatal(p)
			}
			item := f.item("1", "A")
			control := item["control"].(map[string]any)
			attempt := control["lastAttempt"].(map[string]any)
			if control["conflictReason"] != "" || attempt["operation"] != "add" || attempt["reasonKey"] != modelControlError(tc.reason) || f.eventCount("add_blocked") != 1 || f.writeCount() != 0 {
				t.Fatal(control)
			}
			if tc.name == "key" || tc.name == "wildcard" {
				entries := p["entries"].([]any)
				if len(entries) != 1 || entries[0].(map[string]any)["state"] != "blocking" || !reflect.DeepEqual(entries, attempt["entries"]) {
					t.Fatal("blocking entries lost", p, attempt)
				}
				code, result := f.execute("1", "A", "add", p)
				if code != 200 || result["outcome"] != "blocked" || !reflect.DeepEqual(result["entries"], entries) || f.writeCount() != 0 {
					t.Fatal(code, result)
				}
			}
		})
	}
}

func TestModelControlV2911AddReadbackAndUnknownReconciliation(t *testing.T) {
	for _, mode := range []string{"success", "not_added", "manually_changed", "unknown_applied", "unknown_not_applied"} {
		t.Run(mode, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.mapping("1", map[string]string{"b": "B"})
			f.round("1", "A", 3, 0, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "add")
			if mode == "not_added" {
				f.afterWrite = func() { f.mapping("1", map[string]string{"b": "B"}) }
			}
			if mode == "manually_changed" {
				f.afterWrite = func() { f.mapping("1", map[string]string{"b": "B", "A": "C"}) }
			}
			if strings.HasPrefix(mode, "unknown") {
				f.bulkCode = 502
			}
			code, result := f.execute("1", "A", "add", p)
			wantOutcome := "added"
			if mode == "not_added" || mode == "manually_changed" {
				wantOutcome = "failed"
			}
			if strings.HasPrefix(mode, "unknown") {
				wantOutcome = "unknown"
			}
			if code != 200 || result["outcome"] != wantOutcome || f.writeCount() != 1 {
				t.Fatal(code, result)
			}
			if mode == "not_added" || mode == "manually_changed" {
				entries := result["entries"].([]any)
				if len(entries) != 1 || entries[0].(map[string]any)["state"] != mode {
					t.Fatal(entries)
				}
			}
			if strings.HasPrefix(mode, "unknown") {
				control := f.item("1", "A")["control"].(map[string]any)
				if control["pending"] == nil {
					t.Fatal("unknown add did not freeze account")
				}
				f.expect("POST", "model-control/add", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "basis": f.item("1", "A")["basis"], "planFingerprint": p["planFingerprint"]}, 409)
				if mode == "unknown_applied" {
					f.mapping("1", map[string]string{"b": "B", "A": "A"})
				}
				f.agePending("1", "A", 31)
				f.verify("1")
				control = f.item("1", "A")["control"].(map[string]any)
				if control["pending"] != nil || len(control["closedEntries"].(map[string]any)) != 0 || f.eventCount("pending_resolved") != 1 || f.eventCount("add_unknown") != 1 || f.writeCount() != 1 {
					t.Fatal(control)
				}
				if mode == "unknown_not_applied" {
					attempt := control["lastAttempt"].(map[string]any)
					if attempt["operation"] != "add" || attempt["entries"].([]any)[0].(map[string]any)["state"] != "not_added" {
						t.Fatal(attempt)
					}
				}
			} else {
				if f.eventCount("add_"+map[string]string{"success": "succeeded", "not_added": "failed", "manually_changed": "failed"}[mode]) != 1 {
					t.Fatal("wrong add event")
				}
			}
		})
	}
}

type modelControlFuturePreparedClock struct{ modelControlRepository }

func (r modelControlFuturePreparedClock) mutateModelControlOwnership(ctx context.Context, user, workspace, target string, mutation bool, stage string, fn func([]*modelControlTarget, time.Time) ([]ModelControlEvent, error), guards ...modelControlTxGuard) error {
	if stage == "prepared" {
		wrapped := []modelControlTxGuard{}
		for _, guard := range guards {
			current := guard
			wrapped = append(wrapped, func(ctx context.Context, tx pgx.Tx, now time.Time) error {
				return current(ctx, tx, now.AddDate(0, 0, 1))
			})
		}
		guards = wrapped
	}
	return r.modelControlRepository.mutateModelControlOwnership(ctx, user, workspace, target, mutation, stage, fn, guards...)
}
func TestModelControlV2911PreparedClockDayChangeCreatesNoPending(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.service.modelControls = modelControlFuturePreparedClock{f.service.modelControls}
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "BasisChanged") || f.writeCount() != 0 || f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
		t.Fatal(code, result)
	}
}
func TestModelControlV2911BusyRemovalAndMissingPreviewDoNotWrite(t *testing.T) {
	for _, op := range []string{"remove", "missing-preview"} {
		t.Run(op, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			item := f.manage("1", "A")
			_, release, acquired, err := f.service.acquireActionTargetLease(t.Context(), c3REDTarget("1"), false)
			if err != nil || !acquired {
				t.Fatal(err)
			}
			defer release()
			var code int
			var result map[string]any
			if op == "remove" {
				code, result = f.request("DELETE", "model-control/managed", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "expectedVersion": item["version"]})
			} else {
				f.mu.Lock()
				delete(f.accounts, "1")
				f.mu.Unlock()
				code, result = f.request("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "operation": "close", "basis": item["basis"]})
			}
			if code != 409 || !strings.Contains(fmt.Sprint(result), "Busy") || f.writeCount() != 0 {
				t.Fatal(code, result)
			}
		})
	}
}

type modelControlUnreadableMapping struct{ ModelControlActioner }

func (r modelControlUnreadableMapping) ReadSub2APIModelControlAccountContext(ctx context.Context, session upstream.Session, account string) (upstream.Sub2APIModelControlAccount, error) {
	detail, err := r.ModelControlActioner.ReadSub2APIModelControlAccountContext(ctx, session, account)
	detail.ModelMappingKnown = false
	return detail, err
}

func TestModelControlV2911EntryRejectsPreUpgradeBasisAndSecondReadRejectsRunningRound(t *testing.T) {
	for _, mode := range []string{"missing-business-day", "round-started"} {
		t.Run(mode, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.round("1", "A", 1, 2, 0)
			f.manage("1", "A")
			p := f.preview("1", "A", "close")
			if mode == "missing-business-day" {
				delete(p["item"].(map[string]any)["basis"].(map[string]any), "businessDay")
			} else {
				f.beforeDetail = func() {
					f.mu.Lock()
					f.beforeDetail = nil
					f.mu.Unlock()
					if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_question_answer_records SET status='running',answer_judgment=NULL,completed_at=NULL WHERE target_id=$1 AND model_name='A'`, c3REDTarget("1")); err != nil {
						t.Error(err)
					}
				}
			}
			code, result := f.execute("1", "A", "close", p)
			if code != 409 || !strings.Contains(fmt.Sprint(result), "BasisChanged") || f.writeCount() != 0 || f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
				t.Fatal(code, result)
			}
		})
	}
}

type modelControlAddSendBarrier struct {
	ModelControlActioner
	entered chan struct{}
	release chan struct{}
}

func (r modelControlAddSendBarrier) UpdateSub2APIAdminAccountModelMappingContext(ctx context.Context, session upstream.Session, account string, mapping map[string]string) error {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		return r.ModelControlActioner.UpdateSub2APIAdminAccountModelMappingContext(ctx, session, account, mapping)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestModelControlV2911ConcurrentAddFreezesOtherModelAndSendsOnce(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("1", map[string]string{"b": "B"})
	for _, model := range []string{"A", "C"} {
		f.round("1", model, 3, 0, 0)
		f.manage("1", model)
	}
	first, second := f.preview("1", "A", "add"), f.preview("1", "C", "add")
	barrier := modelControlAddSendBarrier{ModelControlActioner: f.service.modelControlActions, entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.service.modelControlActions = barrier
	once := sync.Once{}
	unblock := func() { once.Do(func() { close(barrier.release) }) }
	defer unblock()
	type response struct {
		code   int
		result map[string]any
	}
	done := make(chan response, 1)
	go func() { code, result := f.execute("1", "A", "add", first); done <- response{code, result} }()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first add never reached send barrier")
	}
	code, result := f.execute("1", "C", "add", second)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "Pending") || f.writeCount() != 0 {
		t.Error("second model not frozen", code, result)
	}
	unblock()
	select {
	case response := <-done:
		if response.code != 200 || response.result["outcome"] != "added" {
			t.Fatal(response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first add never finished")
	}
	if f.writeCount() != 1 || f.eventCount("add_succeeded") != 1 || f.item("1", "C")["control"].(map[string]any)["accountPending"] != nil {
		t.Fatal("concurrent add duplicated or retained freeze")
	}
}
func TestModelControlV2911AddRequestHealthAndInventoryGuards(t *testing.T) {
	for _, mode := range []string{"suspended", "disabled", "obsolete-health", "incomplete-inventory"} {
		t.Run(mode, func(t *testing.T) {
			f := newC3REDFixture(t)
			f.mapping("1", map[string]string{"b": "B"})
			f.round("1", "A", 3, 0, 0)
			f.manage("1", "A")
			if mode == "incomplete-inventory" {
				f.mu.Lock()
				f.failInventory = true
				f.mu.Unlock()
			} else {
				state := mode
				if mode == "obsolete-health" {
					state = "suspended"
				}
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_states(connection_id,model_name,user_id,admin_account_id,upstream_site_id,upstream_group_name,state) VALUES($1,'A',$2,'ws1','fixture','fixture',$3)`, c3REDTarget("1"), c3REDUser, state); err != nil {
					t.Fatal(err)
				}
				if mode != "obsolete-health" {
					if _, err := f.pool.Exec(t.Context(), `WITH policy AS (INSERT INTO connection_health_policies(id,user_id,admin_account_id,name) VALUES('add-health',$1,'ws1','Add health') RETURNING id), model AS (INSERT INTO connection_health_model_targets(id,policy_id,user_id,admin_account_id,model_name,provider_family) SELECT 'add-health-model',id,$1,'ws1','A','openai' FROM policy) INSERT INTO connection_health_policy_assignments(id,user_id,admin_account_id,target_id,policy_id) SELECT 'add-health-assignment',$1,'ws1',$2,id FROM policy`, c3REDUser, c3REDTarget("1")); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := f.preview("1", "A", "add")
			if mode == "obsolete-health" {
				code, result := f.execute("1", "A", "add", p)
				if code != 200 || result["outcome"] != "added" || f.writeCount() != 1 {
					t.Fatal(code, result)
				}
			} else {
				want := "RequestHealthSuspended"
				if mode == "incomplete-inventory" {
					want = "InventoryIncomplete"
				}
				if p["blockReasonKey"] != modelControlError(want) {
					t.Fatal(p)
				}
				code, result := f.execute("1", "A", "add", p)
				if code != 200 || result["outcome"] != "blocked" || f.writeCount() != 0 {
					t.Fatal(code, result)
				}
			}
		})
	}
}

type modelControlSupplyReadCounter struct {
	PlatformGroupReader
	groups, accounts atomic.Int32
}

func (r *modelControlSupplyReadCounter) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.groups.Add(1)
	return r.PlatformGroupReader.FetchAdminAllGroups(session)
}
func (r *modelControlSupplyReadCounter) ListAdminGroupAccounts(session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.accounts.Add(1)
	return r.PlatformGroupReader.ListAdminGroupAccounts(session, group)
}
func TestModelControlV2911GroupSupplyUsesExistingInventoryAndHidesUnavailableGroups(t *testing.T) {
	for _, mode := range []string{"empty", "managed", "accounts-failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newC3REDFixture(t)
			if mode != "empty" {
				f.round("1", "A", 3, 0, 0)
				f.manage("1", "A")
			}
			counter := &modelControlSupplyReadCounter{PlatformGroupReader: f.service.platformGroups}
			f.service.platformGroups = counter
			if mode == "accounts-failed" {
				f.mu.Lock()
				f.failInventory = true
				f.mu.Unlock()
			}
			groups, err := f.service.AdminGroups(t.Context(), c3REDUser)
			if err != nil || len(groups) != 1 {
				t.Fatal(groups, err)
			}
			if counter.groups.Load() != 1 || counter.accounts.Load() != 1 {
				t.Fatal("supply issued extra upstream inventory requests", counter.groups.Load(), counter.accounts.Load())
			}
			if mode == "managed" {
				if groups[0].ModelSupply == nil || len(groups[0].ModelSupply.Items) != 1 || groups[0].ModelSupply.Items[0].Open != 2 {
					t.Fatal(groups[0].ModelSupply)
				}
			} else if groups[0].ModelSupply != nil {
				t.Fatal("unavailable/empty group has supply", groups[0].ModelSupply)
			}
			if mode == "accounts-failed" && groups[0].AccountsError == "" {
				t.Fatal("group read error hidden")
			}
		})
	}
}
