package connection_health

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestManagedGroupChangeReadFailureKeepsSpecificCause(t *testing.T) {
	session := upstream.Session{AccessToken: "test-secret"}
	for _, tc := range []struct{ reason, hint, want string }{
		{upstream.ErrorAuth, "主站要求先确认公告 test-secret", "主站要求先确认公告"},
		{upstream.ErrorNetworkTimeout, "", "读取主站资料超时"},
		{upstream.ErrorNetworkUnreachable, "", "Hub 暂时无法连接主站"},
		{upstream.ErrorTLSFailed, "", "主站安全连接未能建立"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			err := groupChangeReadError(&upstream.RequestError{MessageKey: tc.reason, Reason: tc.reason, UpstreamMessage: tc.hint}, session, "1")
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("specific safe cause missing: %v", err)
			}
		})
	}
}

func TestManagedGroupRemovalTemporaryModelAndOrder(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	target := AdminProbeTarget{TargetID: "sub2api:ws1:2", AccountID: "2", Platform: "sub2api"}
	candidate := modelControlTestAccount(map[string]string{"a": "A"})
	candidate.ID = "2"
	peer := modelControlTestAccount(map[string]string{"a": "A"})
	peer.ID = "1"
	inventory := func(a, b upstream.AdminGroupAccountInfo) adminWorkspaceInventory {
		return adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "10", Name: "旧分组"}, accounts: []upstream.AdminGroupAccountInfo{a, b}}}}
	}
	scope := floorTestMonitoringScope("none", nil)
	for _, mode := range []string{"peer temporarily paused", "peer temporary unknown", "candidate paused no exemption"} {
		t.Run(mode, func(t *testing.T) {
			a, b := candidate, peer
			if mode == "peer temporarily paused" {
				b.TempUnschedulableUntil = &future
				b.TempUnschedulableKnown = true
			}
			if mode == "peer temporary unknown" {
				b.TempUnschedulableUntil = nil
				b.TempUnschedulableKnown = false
			}
			inv := inventory(a, b)
			if mode == "candidate paused no exemption" {
				inv.groups[0].accounts = inv.groups[0].accounts[:1]
				inv.groups[0].accounts[0].TempUnschedulableUntil = &future
			}
			err := checkGroupRemovalFloor(target, []string{"10"}, inv, scope, false, nil, nil, nil, now)
			if err == nil || !strings.Contains(err.Error(), "没有其他可用账号") {
				t.Fatalf("temporary account counted: %v", err)
			}
		})
	}
	t.Run("model protected", func(t *testing.T) {
		inv := inventory(candidate, peer)
		reserved := map[string]map[string]bool{"1": {"a": true}}
		err := checkGroupRemovalFloor(target, []string{"10"}, inv, scope, false, nil, nil, reserved, now)
		if err == nil || !strings.Contains(err.Error(), "模型 a") || !strings.Contains(err.Error(), "没有其他来源") {
			t.Fatalf("model floor bypassed: %v", err)
		}
		other := modelControlTestAccount(map[string]string{"a": "A"})
		other.ID = "3"
		inv.groups[0].accounts = append(inv.groups[0].accounts, other)
		if err := checkGroupRemovalFloor(target, []string{"10"}, inv, scope, false, nil, nil, reserved, now); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("first group sorted", func(t *testing.T) {
		inv := inventory(candidate, peer)
		inv.groups[0].group.Name = "先检查"
		inv.groups[0].accounts = inv.groups[0].accounts[:1]
		inv.groups = append(inv.groups, adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "20", Name: "后检查"}, accounts: []upstream.AdminGroupAccountInfo{candidate}})
		err := checkGroupRemovalFloor(target, []string{"20", "10"}, inv, scope, false, nil, nil, nil, now)
		if err == nil || !strings.Contains(err.Error(), "先检查") || strings.Contains(err.Error(), "后检查") {
			t.Fatal(err)
		}
	})
	t.Run("read only reservation", func(t *testing.T) {
		guard := newWorkspaceFloorGuard()
		guard.reservedUnavailable["sub2api:ws1:99"] = 7
		before := map[string]uint64{"sub2api:ws1:99": 7}
		if err := guard.checkGroupRemoval(target, []string{"10"}, inventory(candidate, peer), scope, nil, now); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, guard.reservedUnavailable) {
			t.Fatal("group-only check reserved the whole account")
		}
	})
}

func TestManagedGroupChangeRefreshesStaleReservationsThenProtectsPersistentPending(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale released", true: "persistent protected"}[persistent], func(t *testing.T) {
			f := &managedGroupFlowPlatform{after: []upstream.Sub2APIImportGroup{{ID: "g2"}}}
			s, repo := managedGroupFlowService(f)
			guard := s.sub2APIFloorGuardFor("user1", "ws1")
			old := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true}
			guard.rememberInventory(old)
			guard.mu.Lock()
			guard.reserveUnavailableLocked("sub2api:ws1:1616")
			guard.mu.Unlock()
			if persistent {
				value := 30
				repo.priorityStates["user1|ws1|sub2api:ws1:1616"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1616", PendingPriority: &value, PendingDispatchID: "peer-pending", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
			}
			result, err := s.ChangeManagedSub2APIAccountGroups(t.Context(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, "1515", "openai", []string{"g1"}, []string{"g2"})
			if persistent {
				if err == nil || !strings.Contains(err.Error(), "未确认") || f.writes != 0 {
					t.Fatalf("persistent protection lost: %v", err)
				}
			} else {
				if err != nil || result.Outcome != "changed" || f.writes != 1 {
					t.Fatalf("stale reservation blocked: %v", err)
				}
			}
		})
	}
}

func TestManagedGroupChangePreflightNeverWrites(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"404", "已不存在"}, {"empty", "没有任何分组"}, {"platform", "平台与 Hub 记录不一致"}, {"expected", "资料已变化"}, {"incomplete", "没有读全"}, {"missing account", "没有读全"}, {"pending", "未确认"}, {"last usable", "没有其他可用账号"}, {"lease", "正在执行其他主站操作"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &managedGroupFlowPlatform{}
			s, repo := managedGroupFlowService(f)
			expected := []string{"g1"}
			switch tc.name {
			case "404":
				f.firstErr = &upstream.RequestError{StatusCode: 404}
			case "empty":
				f.before = nil
			case "platform":
				f.firstPlatform = "anthropic"
			case "expected":
				expected = []string{"other"}
			case "incomplete":
				f.errByGrp = map[string]error{"g1": errors.New("missing page")}
			case "missing account":
				f.accountsByGrp["g1"] = f.accountsByGrp["g1"][1:]
			case "pending":
				value := 30
				repo.priorityStates["user1|ws1|sub2api:ws1:1515"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1515", PendingPriority: &value, PendingDispatchID: "pending", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
			case "last usable":
				f.accountsByGrp["g1"] = f.accountsByGrp["g1"][:1]
			case "lease":
				s.repo = &groupChangeLeaseFailureRepo{fakeRepository: repo}
			}
			_, err := s.ChangeManagedSub2APIAccountGroups(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, "1515", "openai", expected, []string{"g2"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.writes != 0 {
				t.Fatalf("preflight error=%v writes=%d want=%s", err, f.writes, tc.want)
			}
		})
	}
}

type groupChangeLeaseFailureRepo struct{ *fakeRepository }

func (r *groupChangeLeaseFailureRepo) AcquireActionLease(context.Context, string, bool) (*RuntimeLeaseHandle, bool, error) {
	return nil, false, errors.New("lease unavailable")
}
