package connection_health

import (
	"context"
	"sort"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

type TestProtocol string

const (
	TestProtocolChatCompletions       TestProtocol = "chat_completions"
	TestProtocolResponses             TestProtocol = "responses"
	ErrorTestConfigurationConflict                 = "admin.connectionHealth.errors.testConfigurationConflict"
	ErrorTestConfigurationUnavailable              = "admin.connectionHealth.errors.testConfigurationUnavailable"
)

type GroupTestConfiguration struct {
	Protocol            TestProtocol `json:"protocol"`
	ProbeTimeoutSeconds int          `json:"probeTimeoutSeconds"`
}

type GroupTestConfig struct {
	UserID              string       `json:"-"`
	AdminAccountID      string       `json:"-"`
	AdminGroupID        string       `json:"adminGroupId"`
	Protocol            TestProtocol `json:"protocol"`
	ProbeTimeoutSeconds int          `json:"probeTimeoutSeconds"`
}

type TestConfigurationSource struct {
	AdminGroupID        string       `json:"adminGroupId"`
	AdminGroupName      string       `json:"adminGroupName"`
	Protocol            TestProtocol `json:"protocol,omitempty"`
	ProbeTimeoutSeconds int          `json:"probeTimeoutSeconds,omitempty"`
}

type EffectiveTestConfiguration struct {
	Protocol            TestProtocol              `json:"protocol,omitempty"`
	ProbeTimeoutSeconds int                       `json:"probeTimeoutSeconds,omitempty"`
	SourceGroups        []TestConfigurationSource `json:"sourceGroups"`
	Status              string                    `json:"status"`
	BlockedReason       string                    `json:"blockedReason,omitempty"`
}

func validTestProtocol(protocol TestProtocol) bool {
	return protocol == TestProtocolChatCompletions || protocol == TestProtocolResponses
}

func validGroupTestConfiguration(configuration GroupTestConfiguration) bool {
	return validTestProtocol(configuration.Protocol) && configuration.ProbeTimeoutSeconds >= 5 && configuration.ProbeTimeoutSeconds <= 120
}

func defaultTestConfiguration() EffectiveTestConfiguration {
	return EffectiveTestConfiguration{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, SourceGroups: []TestConfigurationSource{}, Status: "default"}
}

func unavailableTestConfiguration() EffectiveTestConfiguration {
	return EffectiveTestConfiguration{Status: "unavailable", SourceGroups: []TestConfigurationSource{}, BlockedReason: ErrorTestConfigurationUnavailable}
}

// ResolveGroupTestConfiguration consumes a complete membership snapshot. Names
// are presentation only; policy exclusions and model families never enter here.
func ResolveGroupTestConfiguration(memberships []TestConfigurationSource, inventoryComplete bool, configurations []GroupTestConfig) EffectiveTestConfiguration {
	if !inventoryComplete {
		return unavailableTestConfiguration()
	}
	byGroup := make(map[string]GroupTestConfig, len(configurations))
	for _, config := range configurations {
		if strings.TrimSpace(config.AdminGroupID) == "" || !validGroupTestConfiguration(GroupTestConfiguration{config.Protocol, config.ProbeTimeoutSeconds}) {
			return unavailableTestConfiguration()
		}
		if _, exists := byGroup[config.AdminGroupID]; exists {
			return unavailableTestConfiguration()
		}
		byGroup[config.AdminGroupID] = config
	}
	result := defaultTestConfiguration()
	seen := make(map[string]bool)
	for _, membership := range memberships {
		if strings.TrimSpace(membership.AdminGroupID) == "" {
			return unavailableTestConfiguration()
		}
		if seen[membership.AdminGroupID] {
			continue
		}
		seen[membership.AdminGroupID] = true
		config, exists := byGroup[membership.AdminGroupID]
		if !exists {
			continue
		}
		result.SourceGroups = append(result.SourceGroups, TestConfigurationSource{membership.AdminGroupID, membership.AdminGroupName, config.Protocol, config.ProbeTimeoutSeconds})
		if result.Status == "default" {
			result.Protocol, result.ProbeTimeoutSeconds, result.Status = config.Protocol, config.ProbeTimeoutSeconds, "inherited"
		} else if result.Protocol != config.Protocol || result.ProbeTimeoutSeconds != config.ProbeTimeoutSeconds {
			result.Status, result.BlockedReason = "conflict", ErrorTestConfigurationConflict
		}
	}
	sort.Slice(result.SourceGroups, func(i, j int) bool { return result.SourceGroups[i].AdminGroupID < result.SourceGroups[j].AdminGroupID })
	if result.Status == "conflict" {
		result.Protocol, result.ProbeTimeoutSeconds = "", 0
	}
	return result
}

func (configuration EffectiveTestConfiguration) usable() bool {
	return (configuration.Status == "default" || configuration.Status == "inherited") && validGroupTestConfiguration(GroupTestConfiguration{configuration.Protocol, configuration.ProbeTimeoutSeconds})
}

func sameEffectiveTestConfiguration(a, b EffectiveTestConfiguration) bool {
	return a.usable() && b.usable() && a.Protocol == b.Protocol && a.ProbeTimeoutSeconds == b.ProbeTimeoutSeconds
}

func testConfigurationMemberships(memberships []adminTargetMembership) []TestConfigurationSource {
	result := make([]TestConfigurationSource, 0, len(memberships))
	for _, membership := range memberships {
		result = append(result, TestConfigurationSource{AdminGroupID: membership.groupID, AdminGroupName: membership.groupName})
	}
	return result
}

func inventoryTestMemberships(inventory adminWorkspaceInventory, accountID string) []TestConfigurationSource {
	result := []TestConfigurationSource{}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == accountID {
				result = append(result, TestConfigurationSource{AdminGroupID: group.group.ID, AdminGroupName: group.group.Name})
				break
			}
		}
	}
	return result
}

func (s *Service) configureTestTarget(ctx context.Context, userID, adminAccountID string, target *AdminProbeTarget, memberships []adminTargetMembership, complete bool) error {
	target.InventoryComplete = complete
	target.TestMemberships = testConfigurationMemberships(memberships)
	if target.Platform != string(upstream.PlatformSub2API) {
		target.TestConfiguration = defaultTestConfiguration()
		return nil
	}
	configs, err := s.repo.ListGroupTestConfigurations(ctx, userID, adminAccountID)
	if err != nil {
		target.TestConfiguration = unavailableTestConfiguration()
		return requestError(ErrorTestConfigurationUnavailable)
	}
	target.TestConfiguration = ResolveGroupTestConfiguration(target.TestMemberships, complete, configs)
	if !target.TestConfiguration.usable() {
		return requestError(target.TestConfiguration.BlockedReason)
	}
	return nil
}

type AdminGroupTestImpact struct {
	TargetID          string                     `json:"targetId"`
	AccountName       string                     `json:"accountName"`
	TestConfiguration EffectiveTestConfiguration `json:"testConfiguration"`
}

type AdminGroupTestConfiguration struct {
	AdminGroupID         string                  `json:"adminGroupId"`
	AdminGroupName       string                  `json:"adminGroupName"`
	Configuration        *GroupTestConfiguration `json:"configuration"`
	InventoryComplete    bool                    `json:"inventoryComplete"`
	AffectedAccountCount int                     `json:"affectedAccountCount"`
	ConflictAccountCount int                     `json:"conflictAccountCount"`
	Accounts             []AdminGroupTestImpact  `json:"accounts"`
}

func (s *Service) adminGroupTestContext(ctx context.Context, userID, groupID string) (string, *adminWorkspaceInventory, upstream.AdminGroupInfo, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return "", nil, upstream.AdminGroupInfo{}, err
	}
	if s.platformGroups == nil {
		return "", nil, upstream.AdminGroupInfo{}, requestError(ErrorUnknown)
	}
	inventory, err := s.loadAdminInventory(ctx, userID, workspace, make(adminInventoryCache))
	if err != nil {
		return "", nil, upstream.AdminGroupInfo{}, err
	}
	if inventory.session.Platform != upstream.PlatformSub2API {
		return "", nil, upstream.AdminGroupInfo{}, requestError(ErrorRequest)
	}
	for _, group := range inventory.groups {
		if group.group.ID == groupID {
			return workspace, inventory, group.group, nil
		}
	}
	return "", nil, upstream.AdminGroupInfo{}, requestError(ErrorNotFound)
}

func buildAdminGroupTestConfiguration(userID, workspace string, group upstream.AdminGroupInfo, inventory adminWorkspaceInventory, configs []GroupTestConfig) AdminGroupTestConfiguration {
	result := AdminGroupTestConfiguration{AdminGroupID: group.ID, AdminGroupName: group.Name, InventoryComplete: adminInventoryComplete(inventory), Accounts: []AdminGroupTestImpact{}}
	for _, config := range configs {
		if config.AdminGroupID == group.ID {
			result.Configuration = &GroupTestConfiguration{config.Protocol, config.ProbeTimeoutSeconds}
		}
	}
	if !result.InventoryComplete {
		return result
	}
	for _, members := range inventory.groups {
		if members.group.ID != group.ID {
			continue
		}
		for _, account := range members.accounts {
			configuration := ResolveGroupTestConfiguration(inventoryTestMemberships(inventory, account.ID), true, configs)
			result.Accounts = append(result.Accounts, AdminGroupTestImpact{buildTargetID(string(inventory.session.Platform), workspace, account.ID), account.Name, configuration})
			if configuration.Status == "conflict" {
				result.ConflictAccountCount++
			}
		}
	}
	result.AffectedAccountCount = len(result.Accounts)
	return result
}

func (s *Service) GetAdminGroupTestConfiguration(ctx context.Context, userID, groupID string) (AdminGroupTestConfiguration, error) {
	workspace, inventory, group, err := s.adminGroupTestContext(ctx, userID, groupID)
	if err != nil {
		return AdminGroupTestConfiguration{}, err
	}
	configs, err := s.repo.ListGroupTestConfigurations(ctx, userID, workspace)
	if err != nil {
		return AdminGroupTestConfiguration{}, err
	}
	return buildAdminGroupTestConfiguration(userID, workspace, group, *inventory, configs), nil
}

func (s *Service) SetAdminGroupTestConfiguration(ctx context.Context, userID, groupID string, configuration *GroupTestConfiguration) (AdminGroupTestConfiguration, error) {
	if configuration != nil && !validGroupTestConfiguration(*configuration) {
		return AdminGroupTestConfiguration{}, requestError(ErrorRequest)
	}
	workspace, inventory, group, err := s.adminGroupTestContext(ctx, userID, groupID)
	if err != nil {
		return AdminGroupTestConfiguration{}, err
	}
	if !adminInventoryComplete(*inventory) {
		return AdminGroupTestConfiguration{}, requestError(ErrorTestConfigurationUnavailable)
	}
	if err := s.repo.SaveGroupTestConfiguration(ctx, userID, workspace, groupID, configuration); err != nil {
		return AdminGroupTestConfiguration{}, err
	}
	configs, err := s.repo.ListGroupTestConfigurations(ctx, userID, workspace)
	if err != nil {
		return AdminGroupTestConfiguration{}, err
	}
	return buildAdminGroupTestConfiguration(userID, workspace, group, *inventory, configs), nil
}
