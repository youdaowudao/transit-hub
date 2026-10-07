package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// These tests exercise the public route through preparation, receipt and
// readback. The fixture never opens a port or reads real credentials.
type taskBAccountSettingsPlatform struct {
	mu                               sync.Mutex
	priority, concurrency            int
	load                             *int
	readErr                          error
	writeErr                         error
	readFailAfterWrite, mismatchLoad bool
	priorityWrites                   []int
	concurrencyWrites                int
	credentials                      int
}

func (*taskBAccountSettingsPlatform) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{{ID: "g1", Name: "Task B API fixture"}}, nil
}
func (f *taskBAccountSettingsPlatform) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	return []upstream.AdminGroupAccountInfo{{ID: "a", Name: "Task B API fixture", Status: "active", Schedulable: boolPointer(true), Priority: intPointer(f.priority), Concurrency: intPointer(f.concurrency), LoadFactor: cloneIntPointer(f.load), Models: "gpt-4o", TempUnschedulableKnown: true}}, nil
}
func (f *taskBAccountSettingsPlatform) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credentials++
	return upstream.ProbeCredential{}, errors.New("credentials must never be requested")
}
func (f *taskBAccountSettingsPlatform) UpdateAdminTargetPriority(s upstream.Session, id string, p int) error {
	return f.UpdateAdminTargetPriorityContext(context.Background(), s, id, p)
}
func (f *taskBAccountSettingsPlatform) UpdateAdminTargetPriorityContext(_ context.Context, _ upstream.Session, _ string, p int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.priorityWrites = append(f.priorityWrites, p)
	if f.writeErr == nil {
		f.priority = p
	}
	if f.readFailAfterWrite {
		f.readErr = errors.New("fixture delayed inventory")
	}
	return f.writeErr
}
func (f *taskBAccountSettingsPlatform) UpdateSub2APIAdminAccountConcurrencyContext(_ context.Context, _ upstream.Session, _ string, n int, load *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.concurrencyWrites++
	if f.writeErr == nil {
		f.concurrency = n
		if load != nil && !f.mismatchLoad {
			f.load = cloneIntPointer(load)
		}
	}
	if f.readFailAfterWrite {
		f.readErr = errors.New("fixture delayed inventory")
	}
	return f.writeErr
}

func taskBAccountAPIFixture(priority, tier int) (*Service, *fakeRepository, *taskBAccountSettingsPlatform) {
	r := newFakeRepository()
	r.accountTiers = map[string]int{"user1|ws1|sub2api:ws1:a": tier}
	p := sub2APIProbePolicy(false)
	p.RuleVersion = RuleVersionV2
	p.PriorityMode = PriorityModeMultiplier
	p.AutoRemoteActionEnabled = false
	p.AutoDegradeEnabled = true
	r.policies = []Policy{p}
	assignPolicyToTarget(r, p, "sub2api:ws1:a")
	f := &taskBAccountSettingsPlatform{priority: priority, concurrency: 30, load: intPointer(30)}
	s := newAdminGroupsService(f, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, r)
	s.priorityActions = f
	// The named optional capability is absent on the old baseline. Reflection
	// keeps this same public-route regression compilable there (where it is RED).
	taskASet(s, "concurrencyActions", f)
	return s, r, f
}
func taskBAccountRequest(s *Service, user, target, kind, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	req := httptest.NewRequest(http.MethodPut, "/api/connection-health/targets/"+target+"/"+kind, strings.NewReader(body))
	if user != "" {
		req = req.WithContext(authctx.WithUserID(req.Context(), user))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
func taskBRequireAccountResult(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["result"] != want || body["targetId"] != "sub2api:ws1:a" {
		t.Fatalf("result=%d/%s want %s", rec.Code, rec.Body.String(), want)
	}
}

func TestPriorityTaskBPageSameValueManualEndsOldRunAndFreshAutoBaseline(t *testing.T) {
	for _, delay := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "delayed"}[delay], func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(1, 1)
			r.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalPriority: 50, LastAppliedPriority: 1}
			f.readFailAfterWrite = delay
			rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":1}`)
			if delay {
				taskBRequireAccountResult(t, rec, "pending")
				st, ok := r.priorityStates["user1|ws1|sub2api:ws1:a"]
				if !ok || !strings.HasPrefix(st.PendingDispatchID, "priority-release:") || st.LastAppliedPriority != 1 || st.OriginalPriority != 50 {
					t.Fatalf("lost durable release evidence: %+v", st)
				}
				f.mu.Lock()
				f.readErr = nil
				f.readFailAfterWrite = false
				f.mu.Unlock()
				fresh, err := s.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
				if err != nil {
					t.Fatal(err)
				}
				observed, _ := findActionInventoryTarget("sub2api:ws1:a", *fresh)
				if pair, err := s.reconcileActionObservation(t.Context(), targetObservation("user1", "ws1", observed, fresh)); err != nil || pair.Priority != nil {
					t.Fatalf("later full readback did not end run: %+v %v", pair, err)
				}
			} else {
				taskBRequireAccountResult(t, rec, "success")
				waitForPriorityAsyncIdle(t)
			}
			if _, ok := r.priorityStates["user1|ws1|sub2api:ws1:a"]; ok {
				t.Fatal("explicit manual 1 retained old 50/1 restoration baseline")
			}
			if len(f.priorityWrites) != 1 || f.priorityWrites[0] != 1 {
				t.Fatalf("same-value manual must actually send: %v", f.priorityWrites)
			}
			rec = taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`)
			taskBRequireAccountResult(t, rec, "success")
			waitForPriorityAsyncIdle(t)
			st, ok := r.priorityStates["user1|ws1|sub2api:ws1:a"]
			if !ok || st.OriginalPriority != 1 || st.LastAppliedPriority != 10 || st.PendingPriority != nil {
				t.Fatalf("new run inherited old baseline: %+v", st)
			}
			if f.credentials != 0 || len(r.states) != 0 {
				t.Fatal("page auto performed a probe")
			}
		})
	}
}

func TestPriorityTaskBManualOneAutoTenWithdrawalRestoresOne(t *testing.T) {
	s, r, f := taskBAccountAPIFixture(1, 1)
	s.mySites = fakeAdminGroupKeyReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	s.sites = fakeSiteLookup{}
	r.groupSortSettings["user1|ws1|g1"] = GroupProbeSortSetting{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", FallbackMultiplier: float64Ptr(.1)}
	taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`), "success")
	waitForPriorityAsyncIdle(t)
	st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
	if f.priority != 10 || st.OriginalPriority != 1 || st.LastAppliedPriority != 10 || priorityActionPending(&st) {
		t.Fatalf("manual 1 did not become the confirmed new baseline: actual=%d state=%+v", f.priority, st)
	}
	r.assignments = nil
	r.bumpFakeConfigGeneration("user1", "ws1")
	for pass := 0; pass < 2; pass++ {
		states, err := r.ListPrioritySyncStates(t.Context(), "user1", "ws1")
		if err != nil {
			t.Fatal(err)
		}
		s.syncMultiplierPriorities(t.Context(), r.policies, r.assignments, r.groupAssignments, r.groupExclusions, states)
		if pass == 0 {
			st = r.priorityStates["user1|ws1|sub2api:ws1:a"]
			if f.priority != 1 || !priorityActionPending(&st) || !strings.HasPrefix(st.PendingDispatchID, "priority-release:") || st.LastAppliedPriority != 10 {
				t.Fatalf("withdrawal failed to restore exact manual origin once: actual=%d state=%+v writes=%v", f.priority, st, f.priorityWrites)
			}
		}
	}
	if _, exists := r.priorityStates["user1|ws1|sub2api:ws1:a"]; exists || len(f.priorityWrites) != 2 || f.priorityWrites[0] != 10 || f.priorityWrites[1] != 1 || f.credentials != 0 || len(r.states) != 0 || len(r.events) != 1 {
		t.Fatalf("full withdrawal retained, replayed, or probed: writes=%v checkpoint=%v credentials=%d states=%v events=%v", f.priorityWrites, r.priorityStates, f.credentials, r.states, r.events)
	}
}

func TestPriorityTaskBPageAutoWritesOnlyTemporaryTierValue(t *testing.T) {
	for _, tier := range []int{1, 2} {
		t.Run(map[int]string{1: "primary", 2: "backup"}[tier], func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(5, tier)
			taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`), "success")
			waitForPriorityAsyncIdle(t)
			want := 99
			if tier == 1 {
				want = 10
			}
			st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
			if len(f.priorityWrites) != 1 || f.priorityWrites[0] != want || st.OriginalPriority != 5 || st.LastAppliedPriority != want || f.credentials != 0 || len(r.states) != 0 {
				t.Fatalf("auto=%v state=%+v credentials=%d health=%v", f.priorityWrites, st, f.credentials, r.states)
			}
		})
	}
}

func TestPriorityTaskBPageThreeResultsKeepActualConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, result string
		old                   bool
	}{
		{"first-not-sent", upstream.MutationNotSent, "not_sent", false},
		{"first-rejected", upstream.MutationConfirmedRejected, "not_sent", false},
		{"old-rejected", upstream.MutationConfirmedRejected, "not_sent", true},
		{"first-uncertain", upstream.MutationUncertain, "pending", false},
		{"old-uncertain", upstream.MutationUncertain, "pending", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(20, 2)
			if tc.old {
				r.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalPriority: 50, LastAppliedPriority: 20, Conflict: true}
			}
			f.writeErr = &upstream.RequestError{MessageKey: "fixture.denied", MutationOutcome: tc.outcome}
			taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), tc.result)
			st, exists := r.priorityStates["user1|ws1|sub2api:ws1:a"]
			if tc.result == "not_sent" {
				if exists != tc.old {
					t.Fatalf("incorrect no-effect baseline exists=%v state=%+v", exists, st)
				}
				if tc.old && (st.OriginalPriority != 50 || st.LastAppliedPriority != 20 || st.PendingPriority != nil || st.Conflict) {
					t.Fatalf("rejection changed confirmed baseline: %+v", st)
				}
			} else {
				if !exists || st.PendingPriority == nil || *st.PendingPriority != 5 || !strings.HasPrefix(st.PendingDispatchID, "priority-release:") {
					t.Fatalf("uncertain lost pending evidence: %+v", st)
				}
				before := len(f.priorityWrites)
				rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`)
				if rec.Code != 409 || len(f.priorityWrites) != before {
					t.Fatalf("uncertain retried: %d writes=%v", rec.Code, f.priorityWrites)
				}
			}
			if f.priority != 20 || f.credentials != 0 {
				t.Fatal("failed page action changed current or probed")
			}
		})
	}
}

func TestPriorityTaskBPageNoopStillChecksPendingAndEligibility(t *testing.T) {
	for _, tc := range []string{"noop", "priority-pending", "target-pending", "multiplier-only", "sort-off", "auto-degrade-off", "no-model", "incomplete"} {
		t.Run(tc, func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(20, 1)
			want := 400
			switch tc {
			case "noop":
				want = 200
			case "priority-pending":
				want = 409
				r.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", PendingPriority: intPointer(10)}
			case "target-pending":
				want = 409
				r.targetActionStates["user1|ws1|sub2api:ws1:a"] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", PendingStatus: "inactive"}
			case "multiplier-only":
				r.policies[0].StrategyMode = StrategyModeMultiplierOnly
			case "sort-off":
				r.policies[0].PriorityMode = PriorityModeNone
			case "auto-degrade-off":
				r.policies[0].AutoDegradeEnabled = false
			case "no-model":
				r.policies[0].ModelTargets[0].ModelName = "other-model"
			case "incomplete":
				f.readErr = errors.New("inventory failed")
			}
			rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`)
			if rec.Code != want {
				t.Fatalf("%s status=%d body=%s", tc, rec.Code, rec.Body.String())
			}
			if tc == "noop" {
				taskBRequireAccountResult(t, rec, "noop")
			}
			if len(f.priorityWrites) != 0 || f.credentials != 0 {
				t.Fatalf("ineligible/noop request wrote/probed: %+v", f)
			}
		})
	}
}

func TestPriorityTaskBPageIdentityAndInputNeverWrite(t *testing.T) {
	for _, kind := range []string{"priority-owner", "concurrency"} {
		for _, tc := range []struct {
			name, user, target, body string
			status                   int
		}{
			{"unauthenticated", "", "sub2api:ws1:a", `{}`, 401},
			{"wrong-workspace", "user1", "sub2api:other:a", `{"mode":"auto","concurrency":10}`, 400},
			{"NewAPI", "user1", "newapi:ws1:a", `{"mode":"auto","concurrency":10}`, 400},
			{"forged-target", "user1", "sub2api:ws1:missing", `{"mode":"auto","concurrency":10}`, 400},
			{"invalid-input", "user1", "sub2api:ws1:a", `{"mode":"manual","priority":10,"concurrency":1001}`, 400},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				s, _, f := taskBAccountAPIFixture(20, 1)
				body := tc.body
				if tc.name == "wrong-workspace" || tc.name == "NewAPI" || tc.name == "forged-target" {
					if kind == "priority-owner" {
						body = `{"mode":"auto"}`
					} else {
						body = `{"concurrency":10}`
					}
				}
				rec := taskBAccountRequest(s, tc.user, tc.target, kind, body)
				if rec.Code != tc.status {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if len(f.priorityWrites) != 0 || f.concurrencyWrites != 0 || f.credentials != 0 {
					t.Fatal("rejected request had side effects")
				}
			})
		}
	}
}

func TestPriorityTaskBPageConcurrencyRequiresBothReadbackFields(t *testing.T) {
	for _, tc := range []struct {
		name            string
		load            *int
		mismatch, delay bool
		err             error
		result          string
	}{
		{"both-match", intPointer(30), false, false, nil, "success"},
		{"unset-load", nil, false, false, nil, "success"},
		{"load-mismatch", intPointer(30), true, false, nil, "pending"},
		{"read-failed", intPointer(30), false, true, nil, "pending"},
		{"confirmed-rejected", intPointer(30), false, false, &upstream.RequestError{MessageKey: "fixture.rejected", MutationOutcome: upstream.MutationConfirmedRejected}, "not_sent"},
		{"unknown", intPointer(30), false, false, errors.New("fixture connection reset"), "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(20, 1)
			f.load = tc.load
			f.mismatchLoad, f.readFailAfterWrite, f.writeErr = tc.mismatch, tc.delay, tc.err
			taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "concurrency", `{"concurrency":12}`), tc.result)
			if f.concurrencyWrites != 1 || len(f.priorityWrites) != 0 || f.priority != 20 || len(r.states) != 0 || len(r.priorityStates) != 0 || f.credentials != 0 {
				t.Fatalf("concurrency altered unrelated state: %+v", f)
			}
			if tc.err != nil && f.concurrency != 30 {
				t.Fatal("rejection changed concurrency")
			}
			if tc.load == nil && f.load != nil {
				t.Fatal("unset load factor was manufactured")
			}
		})
	}
}
