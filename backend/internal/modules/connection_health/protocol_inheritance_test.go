package connection_health

import "testing"

func TestProtocolInheritanceCompleteSnapshotAndConflicts(t *testing.T) {
	rule := func(group string, protocol TestProtocol, timeout int) GroupTestConfig {
		return GroupTestConfig{AdminGroupID: group, Protocol: protocol, ProbeTimeoutSeconds: timeout}
	}
	members := []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "first"}, {AdminGroupID: "2", AdminGroupName: "second"}}
	for _, tc := range []struct {
		name     string
		members  []TestConfigurationSource
		complete bool
		rules    []GroupTestConfig
		status   string
		protocol TestProtocol
		timeout  int
		sources  int
	}{
		{"main_site_default", members, true, nil, "default", TestProtocolChatCompletions, 20, 0},
		{"verified_ungrouped", nil, true, nil, "default", TestProtocolChatCompletions, 20, 0},
		{"one_explicit_other_unset", members, true, []GroupTestConfig{rule("2", TestProtocolResponses, 30)}, "inherited", TestProtocolResponses, 30, 1},
		{"equal_rules", members, true, []GroupTestConfig{rule("1", TestProtocolResponses, 30), rule("2", TestProtocolResponses, 30)}, "inherited", TestProtocolResponses, 30, 2},
		{"different_protocols", members, true, []GroupTestConfig{rule("1", TestProtocolResponses, 30), rule("2", TestProtocolChatCompletions, 30)}, "conflict", "", 0, 2},
		{"different_timeouts", members, true, []GroupTestConfig{rule("1", TestProtocolResponses, 30), rule("2", TestProtocolResponses, 20)}, "conflict", "", 0, 2},
		{"no_membership_proof", members, false, []GroupTestConfig{rule("2", TestProtocolResponses, 30)}, "unavailable", "", 0, 0},
		{"unknown_empty_inventory", nil, false, nil, "unavailable", "", 0, 0},
		{"invalid_stored_protocol", members, true, []GroupTestConfig{rule("2", "messages", 30)}, "unavailable", "", 0, 0},
		{"invalid_stored_timeout", members, true, []GroupTestConfig{rule("2", TestProtocolResponses, 121)}, "unavailable", "", 0, 0},
		{"left_configured_group", members[:1], true, []GroupTestConfig{rule("2", TestProtocolResponses, 30)}, "default", TestProtocolChatCompletions, 20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveGroupTestConfiguration("sub2api", tc.members, tc.complete, tc.rules)
			if got.Status != tc.status || got.Protocol != tc.protocol || got.ProbeTimeoutSeconds != tc.timeout || len(got.SourceGroups) != tc.sources {
				t.Fatalf("configuration=%+v, want %s/%s/%d sources=%d", got, tc.status, tc.protocol, tc.timeout, tc.sources)
			}
			if tc.status == "conflict" || tc.status == "unavailable" {
				if got.usable() || got.BlockedReason == "" {
					t.Error("blocked config must carry reason and reject dispatch")
				}
			}
		})
	}
}

func TestProtocolInheritanceNewMemberRenameAndClear(t *testing.T) {
	configs := []GroupTestConfig{{AdminGroupID: "1", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}}
	old := ResolveGroupTestConfiguration("sub2api", []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "old"}}, true, configs)
	fresh := ResolveGroupTestConfiguration("sub2api", []TestConfigurationSource{{AdminGroupID: "1", AdminGroupName: "renamed"}}, true, configs)
	if !sameEffectiveTestConfiguration(old, fresh) || fresh.SourceGroups[0].AdminGroupName != "renamed" {
		t.Error("new member or rename changed stable-ID inheritance")
	}
	// No target IDs or target configuration copies are inputs: next refresh uses
	// exactly the same rules for a new account.
	newMember := ResolveGroupTestConfiguration("sub2api", []TestConfigurationSource{{AdminGroupID: "1"}}, true, configs)
	if !sameEffectiveTestConfiguration(newMember, old) {
		t.Error("new account requires an impermissible per-account copy")
	}
	other := []GroupTestConfig{{AdminGroupID: "2", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 60}}
	afterClear := ResolveGroupTestConfiguration("sub2api", []TestConfigurationSource{{AdminGroupID: "1"}, {AdminGroupID: "2"}}, true, other)
	if afterClear.Protocol != TestProtocolResponses || afterClear.ProbeTimeoutSeconds != 60 || len(afterClear.SourceGroups) != 1 || afterClear.SourceGroups[0].AdminGroupID != "2" {
		t.Errorf("clear did not inherit remaining group: %+v", afterClear)
	}
	if sameEffectiveTestConfiguration(old, afterClear) {
		t.Error("timeout change must invalidate an in-flight captured configuration")
	}
}
