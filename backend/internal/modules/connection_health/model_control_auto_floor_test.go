package connection_health

import (
	"errors"
	"reflect"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlAutoFloorKeysAndReservations(t *testing.T) {
	now := time.Now()
	a := modelControlTestAccount(map[string]string{"a": "A", "b": "B"})
	a.ID = "1"
	target := modelControlTestAccount(map[string]string{"a": "A", "b": "B"})
	target.ID = "2"
	for _, c := range []struct {
		name string
		edit func(*upstream.AdminGroupAccountInfo)
		want bool
	}{
		{"exact", func(*upstream.AdminGroupAccountInfo) {}, true},
		{"passthrough", func(a *upstream.AdminGroupAccountInfo) { a.OpenAIPassthrough = true }, true},
		{"empty whitelist", func(a *upstream.AdminGroupAccountInfo) { a.ModelMapping = map[string]string{} }, true},
		{"unknown whitelist", func(a *upstream.AdminGroupAccountInfo) { a.ModelMappingKnown = false }, true},
		{"wildcard", func(a *upstream.AdminGroupAccountInfo) { a.ModelMapping = map[string]string{"a*": "A"} }, true},
		{"non apikey", func(a *upstream.AdminGroupAccountInfo) { a.Type = "oauth" }, false},
		{"other platform", func(a *upstream.AdminGroupAccountInfo) { a.Platform = "grok" }, false},
		{"inactive", func(a *upstream.AdminGroupAccountInfo) { a.Status = "inactive" }, false},
		{"unschedulable", func(a *upstream.AdminGroupAccountInfo) { a.Schedulable = boolPointer(false) }, false},
		{"unsupported", func(a *upstream.AdminGroupAccountInfo) { a.ModelMapping = map[string]string{"b": "B"} }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			subject := target
			c.edit(&subject)
			keys := modelFloorLastKeys([]upstream.AdminGroupAccountInfo{a, subject}, subject, nil, map[string]map[string]bool{"1": {"a": true, "*": true}}, now)
			if (len(keys) > 0) != c.want {
				t.Fatal(keys)
			}
			if len(keys) > 0 && !reflect.DeepEqual(keys, []string{"a"}) {
				t.Fatal("star should not be a model", keys)
			}
		})
	}
	inventory := adminWorkspaceInventory{groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g1", Name: "group1"}, accounts: []upstream.AdminGroupAccountInfo{a, target}}}}
	scope := adminMonitoringScope{complete: true, monitoredByGroup: map[string]map[string]struct{}{}}
	probe := AdminProbeTarget{TargetID: "sub2api:ws1:2", AccountID: "2", Platform: "sub2api"}
	reserved := map[string]map[string]bool{"1": {"a": true}}
	if out := newWorkspaceFloorGuard().reserveSub2APIMutation(probe, inventory, scope); out.remoteAction != "" {
		t.Fatal("legacy unmonitored should pass")
	}
	if out := newWorkspaceFloorGuard().reserveSub2APIMutationWithModels(probe, inventory, scope, reserved); out.remoteAction != RemoteActionSkippedSub2APILastModelSource || out.adminGroupID != "g1" {
		t.Fatal("unmonitored not guarded", out)
	}
	alternate := modelControlTestAccount(map[string]string{"a": "A"})
	alternate.ID = "3"
	inventory.groups[0].accounts = append(inventory.groups[0].accounts, alternate)
	guard := newWorkspaceFloorGuard()
	if out := guard.reserveSub2APIMutationWithModels(probe, inventory, scope, reserved); out.remoteAction != "" {
		t.Fatal("other source should allow", out)
	}
	second := AdminProbeTarget{TargetID: "sub2api:ws1:3", AccountID: "3", Platform: "sub2api"}
	if out := guard.reserveSub2APIMutationWithModels(second, inventory, scope, reserved); out.remoteAction != RemoteActionSkippedSub2APILastModelSource {
		t.Fatal("second destructive action missed reservation", out)
	}
	frozen := newWorkspaceFloorGuard()
	frozen.frozenGroups = map[string]struct{}{"g1": {}}
	if out := frozen.reserveSub2APIMutationWithModels(probe, inventory, scope, reserved); out.remoteAction != RemoteActionAwaitingConfirmation {
		t.Fatal("frozen group bypassed", out)
	}
	inventory.groups = append(inventory.groups, adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g2", Name: "group2"}, accounts: []upstream.AdminGroupAccountInfo{a, target}})
	if out := newWorkspaceFloorGuard().reserveSub2APIMutationWithModels(probe, inventory, scope, reserved); out.adminGroupID != "g2" || out.remoteAction != RemoteActionSkippedSub2APILastModelSource {
		t.Fatal("second group not guarded", out)
	}
}
func TestModelControlAutoFloorExtraEntrypointsAndFailure(t *testing.T) {
	for _, source := range []string{ActionSourceManualDelete, ActionSourceCompensateDelete} {
		t.Run(source, func(t *testing.T) {
			f := newC32REDFixture(t)
			f.closedSources()
			f.switches(false, false, true)
			err := f.service.DeleteManagedSub2APIAccount(t.Context(), c32REDUser, c32REDWorkspace, f.session, "3", source)
			if !errors.Is(err, requestError(ErrorSub2APIGroupLastModelSource)) || len(f.deleteWrites) > 0 {
				t.Fatal("delete source bypassed floor", err)
			}
		})
	}
	for _, failure := range []string{"settings", "records"} {
		t.Run("manual failure/"+failure, func(t *testing.T) {
			f := newC32REDFixture(t)
			f.closedSources()
			f.switches(false, false, true)
			hook := &c32AutoHookRepository{Repository: f.service.modelControls.(*Repository), failSettings: failure == "settings", failTargets: failure == "records"}
			f.service.modelControls = hook
			code, body := f.danger("status")
			if code != 409 || body["message"] != ErrorSub2APIInventoryIncomplete || f.inactiveCount() > 0 {
				t.Fatal("read failure did not block", code, body)
			}
		})
	}
	t.Run("no closed record allows", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.switches(false, false, true)
		code, result := f.danger("status")
		if code != 200 || f.inactiveCount() != 1 {
			t.Fatal(code, result)
		}
	})
	t.Run("other source allows", func(t *testing.T) {
		f := newC32REDFixture(t)
		f.manage("1", "A")
		f.freshRound("1", "A", 1, 2)
		f.close("1", "A")
		f.switches(false, false, true)
		code, result := f.danger("status")
		if code != 200 || f.inactiveCount() != 1 {
			t.Fatal(code, result)
		}
	})
}
