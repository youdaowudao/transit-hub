package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/upstream"
)

func TestMessagesC45MembershipTruthTableAndSerialization(t *testing.T) {
	for _, tc := range []struct {
		name, group, platform, accountType string
		want                               bool
	}{
		{"eligible", "anthropic", "anthropic", "apikey", true},
		{"normalized_group", " Anthropic ", "anthropic", "apikey", true},
		{"normalized_account", "anthropic", " AnThRoPiC ", "apikey", true},
		{"trimmed_key_type", "anthropic", "anthropic", " apikey ", true},
		{"type_case_is_significant", "anthropic", "anthropic", "APIKEY", false},
		{"oauth", "anthropic", "anthropic", "oauth", false},
		{"empty_type", "anthropic", "anthropic", "", false},
		{"other_group", "openai", "anthropic", "apikey", false},
		{"empty_group", "", "anthropic", "apikey", false},
		{"other_account", "anthropic", "openai", "apikey", false},
		{"empty_account", "anthropic", "", "apikey", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := upstream.AdminGroupInfo{ID: "g", Name: "group name", Platform: tc.group}
			account := upstream.AdminGroupAccountInfo{Platform: tc.platform, Type: tc.accountType}
			got := testConfigurationMembership(group, account)
			if got.AdminGroupID != group.ID || got.AdminGroupName != group.Name || got.MessagesByDefault != tc.want || got.Protocol != "" || got.ProbeTimeoutSeconds != 0 {
				t.Fatalf("membership=%+v want eligible=%v", got, tc.want)
			}
			oldStyle := adminTargetMembershipFor(group, account)
			if oldStyle.groupID != group.ID || oldStyle.groupName != group.Name || oldStyle.messagesByDefault != tc.want {
				t.Fatalf("readback membership=%+v", oldStyle)
			}
			converted := testConfigurationMemberships([]adminTargetMembership{oldStyle})
			if len(converted) != 1 || converted[0] != got {
				t.Fatalf("conversion lost eligibility: %+v", converted)
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"messagesByDefault"`) != tc.want {
				t.Fatalf("omitempty contract lost: %s", body)
			}
			var roundtrip TestConfigurationSource
			if err = json.Unmarshal(body, &roundtrip); err != nil || roundtrip != got {
				t.Fatalf("snapshot lost eligibility: %+v %v", roundtrip, err)
			}
		})
	}
}
func TestMessagesC45DefaultMembershipCompletenessAndDuplicates(t *testing.T) {
	eligible := TestConfigurationSource{AdminGroupID: "g", AdminGroupName: "name", MessagesByDefault: true}
	for _, tc := range []struct {
		name     string
		members  []TestConfigurationSource
		complete bool
		protocol TestProtocol
		status   string
	}{
		{"duplicate_eligible", []TestConfigurationSource{eligible, eligible}, true, TestProtocolMessages, "default"},
		{"duplicate_false_last", []TestConfigurationSource{eligible, {AdminGroupID: "g"}}, true, TestProtocolChatCompletions, "default"},
		{"duplicate_false_first", []TestConfigurationSource{{AdminGroupID: "g"}, eligible}, true, TestProtocolChatCompletions, "default"},
		{"empty_membership", nil, true, TestProtocolChatCompletions, "default"},
		{"empty_identifier", []TestConfigurationSource{eligible, {AdminGroupID: " ", MessagesByDefault: false}}, true, "", "unavailable"},
		{"empty_identifier_with_true", []TestConfigurationSource{{AdminGroupID: " ", MessagesByDefault: true}}, true, "", "unavailable"},
		{"incomplete", []TestConfigurationSource{eligible}, false, "", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveGroupTestConfiguration("sub2api", tc.members, tc.complete, nil)
			if got.Protocol != tc.protocol || got.Status != tc.status {
				t.Fatalf("configuration=%+v", got)
			}
			if tc.status == "default" && got.ProbeTimeoutSeconds != 20 {
				t.Fatalf("default seconds changed: %+v", got)
			}
			if tc.status == "unavailable" && got.BlockedReason != ErrorTestConfigurationUnavailable {
				t.Fatalf("unavailable reason lost: %+v", got)
			}
		})
	}
	explicit := ResolveGroupTestConfiguration("sub2api", []TestConfigurationSource{eligible}, true, []GroupTestConfig{{AdminGroupID: "g", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}})
	if len(explicit.SourceGroups) != 1 || explicit.SourceGroups[0].MessagesByDefault {
		t.Fatalf("explicit source must not claim Messages default: %+v", explicit)
	}
	c45AssertConfiguration(t, explicit, TestProtocolResponses, 30, "inherited")
}
func TestMessagesC45AllMembershipGenerationEntries(t *testing.T) {
	for _, entry := range []string{"inventory", "single-refresh", "priority", "schedule-preparation", "legacy-conversion", "manual-readback-helper", "group-health"} {
		t.Run(entry, func(t *testing.T) {
			s, repo, reader := c45Fixture(t)
			snapshot, err := s.loadAdminInventory(t.Context(), "user1", "ws1", make(adminInventoryCache))
			if err != nil {
				t.Fatal(err)
			}
			var got []TestConfigurationSource
			switch entry {
			case "inventory":
				got = inventoryTestMemberships(*snapshot, "normal")
			case "single-refresh":
				refresh, e := s.refreshAdminTarget(t.Context(), snapshot.session, "ws1", "normal")
				if e != nil {
					t.Fatal(e)
				}
				got = testConfigurationMemberships(refresh.memberships)
			case "priority":
				items, complete, e := s.priorityInventoryForSnapshot(snapshot, "ws1", map[string][]Policy{c45Target: repo.policies}, nil, nil, nil, upstreamMultiplierLookup{})
				if e != nil || !complete || items[c45Target] == nil {
					t.Fatalf("priority fixture invalid: %v", e)
				}
				got = items[c45Target].target.TestMemberships
			case "schedule-preparation":
				execution := c2ScheduleExecutionFixture("groups")
				execution.ConfigSnapshot.Requested.Schedule.SelectedGroupIDs = []string{"g1"}
				inventory, e := s.resolveQuestionAnswerScheduleTargets(t.Context(), "user1", "ws1", execution.ConfigSnapshot.Requested.Schedule)
				if e != nil || inventory.blockedReason != "" {
					t.Fatalf("schedule fixture invalid: %+v %v", inventory, e)
				}
				got = inventory.memberships[c45Target]
			case "legacy-conversion":
				got = testConfigurationMemberships([]adminTargetMembership{{groupID: "g1", groupName: "C45 group", messagesByDefault: true}})
			case "manual-readback-helper":
				got = testConfigurationMemberships([]adminTargetMembership{adminTargetMembershipFor(reader.groups[0], reader.accountsByGrp["g1"][0])})
			case "group-health":
				groups, e := s.AdminGroups(t.Context(), "user1")
				if e != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
					t.Fatalf("group fixture invalid: %v", e)
				}
				c45AssertConfiguration(t, groups[0].Accounts[0].TestConfiguration, TestProtocolMessages, 20, "default")
				return
			}
			if len(got) != 1 || !got[0].MessagesByDefault || got[0].AdminGroupID != "g1" || got[0].AdminGroupName != "C45 group" {
				t.Fatalf("%s did not carry the eligible membership: %+v", entry, got)
			}
			c45AssertConfiguration(t, ResolveGroupTestConfiguration("sub2api", got, true, nil), TestProtocolMessages, 20, "default")
		})
	}
}
func TestMessagesC45ManualAndCommittedProbeRechecksAgree(t *testing.T) {
	for _, entry := range []string{"once", "formal", "formal-progress", "automatic"} {
		t.Run(entry, func(t *testing.T) {
			s, repo, _ := c45Fixture(t)
			calls := 0
			s.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" {
					t.Errorf("probe request=%s", req.URL.RequestURI())
				}
				return c45ProbeResponse(req), nil
			})
			switch entry {
			case "once":
				out, err := s.ManualProbeTarget(t.Context(), "user1", c45Target, []string{"gpt-4o"})
				if err != nil || len(out) != 1 || out[0].ConfigurationChanged || out[0].Protocol != TestProtocolMessages || !out[0].Healthy {
					t.Fatalf("once configuration recheck failed: %+v %v", out, err)
				}
				if len(repo.events) != 0 {
					t.Fatal("one-time probe wrote health event")
				}
			case "formal", "formal-progress":
				var out []ModelHealth
				var err error
				if entry == "formal" {
					out, err = s.ProbeTarget(t.Context(), "user1", c45Target, []string{"gpt-4o"})
				} else {
					out, err = s.ProbeTargetWithProgress(t.Context(), "user1", c45Target, []string{"gpt-4o"}, func(ProbeTargetPhase) {})
				}
				if err != nil || len(out) != 1 {
					t.Fatalf("formal probe result=%+v err=%v", out, err)
				}
			case "automatic":
				runProtocolServiceJob(s, c45Job(t, s, repo))
			}
			if calls != 1 {
				t.Fatalf("sends=%d want=1", calls)
			}
			if entry != "once" {
				if len(repo.events) != 1 || repo.events[0].ProbeDisposition != "applied" {
					t.Fatalf("commit rejected the default protocol: %+v", repo.events)
				}
				state := repo.states[c45Target]["gpt-4o"]
				if state.LastAppliedProbeProtocol == nil || *state.LastAppliedProbeProtocol != TestProtocolMessages {
					t.Fatalf("wrong protocol committed: %+v", state)
				}
			}
		})
	}
}

// Only the read methods used by the existing pre-action guard are implemented.
// An unexpected transaction operation panics through the embedded interface.
type c45GuardTx struct {
	pgx.Tx
	t          *testing.T
	stateReads int
}
type c45NoConfigurations struct{ pgx.Rows }

func (c45NoConfigurations) Next() bool { return false }
func (c45NoConfigurations) Close()     {}
func (c45NoConfigurations) Err() error { return nil }
func (tx *c45GuardTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !strings.Contains(sql, "connection_health_group_test_configs") || len(args) != 2 || args[0] != "user1" || args[1] != "ws1" {
		tx.t.Fatal("unexpected guard configuration query")
	}
	return c45NoConfigurations{}, nil
}

type c45StateRow struct{ t *testing.T }

func (row c45StateRow) Scan(dest ...any) error {
	if len(dest) != 41 {
		row.t.Fatalf("state fixture expects 41 columns, got %d", len(dest))
	}
	*dest[0].(*string) = c45Target
	*dest[1].(*string) = "gpt-4o"
	*dest[2].(*string) = "user1"
	*dest[3].(*string) = "ws1"
	*dest[9].(*string) = string(StateHealthy)
	*dest[30].(*string) = "valid"
	*dest[31].(**TestProtocol) = protocolPointer(TestProtocolMessages)
	return nil
}
func (tx *c45GuardTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "connection_health_states") || len(args) != 2 || args[0] != c45Target || args[1] != "gpt-4o" {
		tx.t.Fatal("unexpected guard state query")
	}
	tx.stateReads++
	return c45StateRow{tx.t}
}
func TestMessagesC45RealPreActionConfigurationGuardAcceptsDefaultMessages(t *testing.T) {
	s, repo, _ := c45Fixture(t)
	target := c45Job(t, s, repo).target
	if len(target.TestMemberships) != 1 || !target.TestMemberships[0].MessagesByDefault {
		t.Fatal("scheduled target lost membership fact")
	}
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{UserID: "user1", AdminAccountID: "ws1", TargetID: c45Target}, Guard: RemoteActionHealthGuard{Required: true, Memberships: target.TestMemberships, InventoryComplete: true, Configuration: target.TestConfiguration, Models: []string{"gpt-4o"}}}
	tx := &c45GuardTx{t: t}
	if err := validateRemoteActionHealthTx(t.Context(), tx, claim); err != nil || tx.stateReads != 1 {
		t.Fatalf("default Messages action guard failed: reads=%d err=%v", tx.stateReads, err)
	}
	claim.Guard.Configuration = defaultTestConfiguration()
	if err := validateRemoteActionHealthTx(t.Context(), tx, claim); !errors.Is(err, ErrRemoteActionEvidenceChanged) {
		t.Fatalf("contradictory Chat evidence must remain blocked: %v", err)
	}
	claim.Guard.Configuration = target.TestConfiguration
	claim.Guard.Memberships[0].MessagesByDefault = false
	if err := validateRemoteActionHealthTx(t.Context(), tx, claim); !errors.Is(err, ErrRemoteActionEvidenceChanged) {
		t.Fatalf("missing default fact must remain blocked: %v", err)
	}
}
