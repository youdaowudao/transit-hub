package connection_health

import (
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

// Business expectations owned by the primary agent, from SPEC 4.7.
func TestManagedGroupRemovalCriticalRules(t *testing.T) {
	now := time.Now()
	target := AdminProbeTarget{TargetID: buildTargetID("sub2api", "ws1", "1"), AccountID: "1", Platform: "sub2api"}
	account := func(id, status string, sched *bool) upstream.AdminGroupAccountInfo {
		return upstream.AdminGroupAccountInfo{ID: id, Status: status, Schedulable: sched, TempUnschedulableKnown: true}
	}
	for _, tc := range []struct {
		name, status string
		peer         []upstream.AdminGroupAccountInfo
		monitored    []string
		freeze       bool
		frozen       bool
		pending      map[string]bool
		want         string
	}{
		{name: "workspace pending", status: "active", freeze: true, want: "未确认"},
		{name: "group pending", status: "active", frozen: true, want: "未确认"},
		{name: "active sole member", status: "active", want: "没有其他可用账号"},
		{name: "inactive candidate no exemption", status: "inactive", want: "没有其他可用账号"},
		{name: "unknown candidate no exemption", status: "mystery", want: "没有其他可用账号"},
		{name: "unknown peer", status: "active", peer: []upstream.AdminGroupAccountInfo{account("2", "mystery", boolPointer(true))}, want: "状态读不准"},
		{name: "active peer missing schedulable", status: "active", peer: []upstream.AdminGroupAccountInfo{account("2", "active", nil)}, want: "状态读不准"},
		{name: "outside monitoring counts all peers", status: "inactive", peer: []upstream.AdminGroupAccountInfo{account("2", "active", boolPointer(true))}},
		{name: "monitored group excludes nonmonitored peer", status: "active", peer: []upstream.AdminGroupAccountInfo{account("2", "active", boolPointer(true))}, monitored: []string{"1"}, want: "没有其他可用账号"},
		{name: "candidate not monitored still group scope", status: "active", peer: []upstream.AdminGroupAccountInfo{account("2", "inactive", boolPointer(true)), account("3", "active", boolPointer(true))}, monitored: []string{"2"}, want: "没有其他可用账号"},
		{name: "pending peer cannot survive", status: "active", peer: []upstream.AdminGroupAccountInfo{account("2", "active", boolPointer(true))}, pending: map[string]bool{"2": true}, want: "没有其他可用账号"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "10", Name: "旧分组"}, accounts: append([]upstream.AdminGroupAccountInfo{account("1", tc.status, boolPointer(true))}, tc.peer...)}}}
			scope := floorTestMonitoringScope("critical", map[string][]string{})
			if tc.monitored != nil {
				scope = floorTestMonitoringScope("critical", map[string][]string{"10": tc.monitored})
			}
			frozen := map[string]struct{}{}
			if tc.frozen {
				frozen["10"] = struct{}{}
			}
			err := checkGroupRemovalFloor(target, []string{"10"}, inv, scope, tc.freeze, frozen, tc.pending, nil, now)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q got %v", tc.want, err)
			}
		})
	}
	t.Run("missing group", func(t *testing.T) {
		err := checkGroupRemovalFloor(target, []string{"10"}, adminWorkspaceInventory{groupsComplete: true}, floorTestMonitoringScope("", nil), false, nil, nil, nil, now)
		if err == nil || !strings.Contains(err.Error(), "没有读全") {
			t.Fatalf("missing membership must fail B10: %v", err)
		}
	})
}
