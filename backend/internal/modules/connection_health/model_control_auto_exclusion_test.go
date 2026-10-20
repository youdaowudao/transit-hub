package connection_health

import (
	"testing"
)

func c32PriorityInputs(t *testing.T, f *c32REDFixture) (map[string]*priorityTargetInventory, []ConnectionHealthState, []PrioritySyncState) {
	t.Helper()
	ctx := t.Context()
	policies, err := f.service.repo.ListPolicies(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	states, err := f.service.repo.ListStatesByWorkspace(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	syncs, err := f.service.repo.ListPrioritySyncStates(ctx, c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.loadAdminInventory(ctx, c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]*priorityTargetInventory{}
	for _, g := range inv.groups {
		for _, a := range g.accounts {
			if a.ID != "1" && a.ID != "2" {
				continue
			}
			id := c32REDTarget(a.ID)
			items[id] = &priorityTargetInventory{target: AdminProbeTarget{TargetID: id, Platform: "sub2api", AccountID: a.ID, AccountStatus: a.Status, Schedulable: a.Schedulable, InventoryComplete: true, Models: []string{"A", "B"}, TestMemberships: []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "c3-red-group"}}}, account: a, policies: policies, currentPriority: *a.Priority, priorityPresent: true, snapshotStartedAt: inv.snapshotStartedAt, upstreamMultiplier: upstreamMultiplierResolution{status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: float64Ptr(1)}}}
		}
	}
	return items, states, syncs
}
func c32Reconcile(t *testing.T, f *c32REDFixture, id string) (targetRemoteActionResult, error) {
	t.Helper()
	inventory, err := f.service.loadAdminInventory(t.Context(), c32REDUser, c32REDWorkspace, adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	target, found := findActionInventoryTarget(c32REDTarget(id), *inventory)
	if !found {
		t.Fatal("target missing")
	}
	target.InventoryComplete = true
	target.Models = []string{"A", "B"}
	target.TestMemberships = []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "c3-red-group"}}
	configs, err := f.service.repo.ListGroupTestConfigurations(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	target.TestConfiguration = ResolveGroupTestConfiguration("sub2api", target.TestMemberships, true, configs)
	policies, err := f.service.repo.ListPolicies(t.Context(), c32REDUser, c32REDWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.service.loadAdminMonitoringScope(t.Context(), c32REDUser, c32REDWorkspace, *inventory)
	if err != nil {
		t.Fatal(err)
	}
	return f.service.reconcileTargetRemoteActionWithFloor(t.Context(), c32REDUser, c32REDWorkspace, f.session, target, candidateModelSpecs(target.Models, policies, "", probeModelSource{}, true), f.service.sub2APIFloorGuardFor(c32REDUser, c32REDWorkspace), inventory, scope)
}
func TestModelControlAutoFullyClosedFilteringAndFailures(t *testing.T) {
	f := c32AutoFixture(t, true)
	r := f.service.modelControls.(*Repository)
	o := c32AutoObject(t, f, "1", "A")
	if o.Observation.State != "closed" {
		t.Fatal(o)
	}
	f.switches(false, false, true)
	for _, c := range []struct {
		name     string
		sql      string
		excluded bool
	}{
		{"closed", `UPDATE connection_health_model_control_targets SET observed_state='closed',conflict_reason='',pending=NULL`, true},
		{"partial", `UPDATE connection_health_model_control_targets SET observed_state='partially_closed',conflict_reason='',pending=NULL`, false},
		{"conflict", `UPDATE connection_health_model_control_targets SET observed_state='closed',conflict_reason='changed',pending=NULL`, false},
		{"pending", `UPDATE connection_health_model_control_targets SET observed_state='closed',conflict_reason='',pending='{"id":"test"}'`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := f.pool.Exec(t.Context(), c.sql); err != nil {
				t.Fatal(err)
			}
			excluded, err := f.service.modelControlExcluded(t.Context(), c32REDUser, c32REDWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			_, got := excluded[c32REDTarget("1")]["A"]
			if got != c.excluded {
				t.Fatal(excluded)
			}
		})
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=NULL,observed_state='closed',conflict_reason=''`); err != nil {
		t.Fatal(err)
	}
	f.monitor([]string{"1", "2"}, "A", "B")
	f.service.runSchedulerTickSafely(t.Context())
	waitSchedulerPathsIdle(t, f.service)
	// The tick starts automatic scans asynchronously; settle them before the next fixture phase.
	f.service.modelControlScanWG.Wait()
	for _, failure := range []string{"settings", "set"} {
		t.Run("fail closed/"+failure, func(t *testing.T) {
			hook := &c32AutoHookRepository{Repository: r, failSettings: failure == "settings", failClosed: failure == "set"}
			f.service.modelControls = hook
			before := f.inactiveCount()
			if _, err := c32Reconcile(t, f, "1"); err == nil || f.inactiveCount() != before {
				t.Fatal("health should do no action on failed read", err)
			}
			items, states, syncs := c32PriorityInputs(t, f)
			f.mu.Lock()
			f.priorityWrites = nil
			f.mu.Unlock()
			f.service.syncWorkspacePriorities(t.Context(), f.session, c32REDUser, c32REDWorkspace, items, true, states, syncs)
			if len(f.priorityWrites) > 0 {
				t.Fatal("priority wrote after failed read")
			}
			groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, g := range groups {
				if g.ModelControlError == modelControlError("Storage") {
					found = true
				}
				for _, account := range g.Accounts {
					for _, model := range account.ModelHealth {
						if model.ModelControlClosed {
							t.Fatal("list excluded during failed read")
						}
					}
				}
			}
			if !found {
				t.Fatal("list missed storage warning", groups)
			}
			f.service.modelControls = r
		})
	}
	t.Run("remove managed reintroduces model", func(t *testing.T) {
		object := c32AutoObject(t, f, "1", "A")
		if err := f.service.RemoveManagedModel(t.Context(), c32REDUser, c32REDTarget("1"), "A", object.Version, true); err != nil {
			t.Fatal(err)
		}
		excluded, err := f.service.modelControlExcluded(t.Context(), c32REDUser, c32REDWorkspace)
		if err != nil || len(excluded[c32REDTarget("1")]) != 0 {
			t.Fatal(excluded, err)
		}
	})
}
func TestModelControlAutoAllClosedPriorityAndRestoration(t *testing.T) {
	t.Run("all closed no health action or priority", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.probeFailAccount = "1"
		f.mapping("1", map[string]string{"a": "A", "a-alias": "A", "b": "B", "c": "C"})
		for _, model := range []string{"A", "B"} {
			f.manage("1", model)
			f.freshRound("1", model, 1, 2)
			f.close("1", model)
		}
		f.switches(false, false, true)
		f.monitor([]string{"1"}, "A", "B")
		f.service.runSchedulerTickSafely(t.Context())
		waitSchedulerPathsIdle(t, f.service)
		f.service.modelControlScanWG.Wait()
		if f.inactiveCount() > 0 {
			t.Fatal("all excluded still stopped")
		}
		items, states, syncs := c32PriorityInputs(t, f)
		delete(items, c32REDTarget("2"))
		f.service.syncWorkspacePriorities(t.Context(), f.session, c32REDUser, c32REDWorkspace, items, true, states, syncs)
		if len(f.priorityWrites) > 0 {
			t.Fatal("all excluded priority write")
		}
		groups, err := f.service.AdminGroups(t.Context(), c32REDUser)
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, g := range groups {
			for _, account := range g.Accounts {
				if account.ID == "1" {
					seen = true
					if !account.ModelControlNoActiveProbe {
						t.Fatal("no active probe not marked", account)
					}
					for _, model := range account.ModelHealth {
						if !model.ModelControlClosed {
							t.Fatal("closed model hidden/unmarked")
						}
					}
				}
			}
		}
		if !seen {
			t.Fatal("account missing")
		}
		// The existing multiplier-only path bypasses the health model set entirely.
		for id, item := range items {
			for n := range item.policies {
				item.policies[n].StrategyMode = StrategyModeMultiplierOnly
				item.policies[n].AutoDegradeEnabled = false
			}
			item.multipliers = []float64{1}
			items[id] = item
		}
		f.service.syncWorkspacePriorities(t.Context(), f.session, c32REDUser, c32REDWorkspace, items, true, states, syncs)
		if len(f.priorityWrites) != 1 {
			t.Fatal("multiplier-only was excluded")
		}
	})
	t.Run("closed failing model permits healthy remainder restore", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.probeFailAccount = "1"
		f.manage("1", "A")
		f.freshRound("1", "A", 1, 2)
		f.close("1", "A")
		f.monitor([]string{"1", "2"}, "A", "B")
		f.service.runSchedulerTickSafely(t.Context())
		waitSchedulerPathsIdle(t, f.service)
		f.service.modelControlScanWG.Wait()
		if f.inactiveCount() != 1 {
			t.Fatal("baseline initial stop missing")
		}
		f.switches(false, false, true)
		result, err := c32Reconcile(t, f, "1")
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		status := f.accounts["1"]["status"]
		f.mu.Unlock()
		if status != "active" {
			t.Fatal("healthy remainder did not restore", result, status)
		}
	})
	t.Run("empty-group safety restore unaffected", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.closedSources()
		f.switches(false, false, true)
		f.mu.Lock()
		for _, a := range f.accounts {
			a["status"] = "inactive"
		}
		f.mu.Unlock()
		stored := TargetActionState{UserID: c32REDUser, AdminAccountID: c32REDWorkspace, TargetID: c32REDTarget("3"), OriginalStatus: "active", LastAppliedStatus: "inactive"}
		if err := f.service.repo.UpsertTargetActionState(t.Context(), stored); err != nil {
			t.Fatal(err)
		}
		ctx, err := f.service.captureAllWorkspaceRules(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		f.service.restoreEmptySub2APIGroups(ctx, []TargetActionState{stored}, adminInventoryCache{})
		f.mu.Lock()
		status := f.accounts["3"]["status"]
		f.mu.Unlock()
		if status != "active" {
			t.Fatal("empty group restore changed")
		}
	})
}
