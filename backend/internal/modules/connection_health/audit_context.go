package connection_health

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

// AuditTarget is a public, read-only view. It never carries a credential.
type AuditTarget struct {
	TargetID       string       `json:"targetId"`
	AccountID      string       `json:"accountId"`
	AccountName    string       `json:"accountName"`
	ProviderFamily string       `json:"providerFamily"`
	Groups         []AuditGroup `json:"groups"`
}
type AuditGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type AuditContext struct {
	WorkspaceID    string                   `json:"workspaceId"`
	TargetID       string                   `json:"targetId"`
	AccountID      string                   `json:"accountId"`
	AccountName    string                   `json:"accountName"`
	ProviderFamily string                   `json:"providerFamily"`
	GroupID        string                   `json:"groupId"`
	GroupName      string                   `json:"groupName"`
	BaseURL        string                   `json:"baseURL"`
	Protocol       string                   `json:"protocol"`
	CanRun         bool                     `json:"canRun"`
	Reason         string                   `json:"reason,omitempty"`
	Credential     upstream.ProbeCredential `json:"-"`
}

// ListAuditTargets intentionally uses the same inventory source as group health,
// without health transitions, bookkeeping, model synchronization or remote writes.
func (s *Service) ListAuditTargets(ctx context.Context, userID string) ([]AuditTarget, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("请先选择自己的工作区，再打开模型检测")
	}
	session, err := s.mySites.RequireSession(ctx, userID, workspace)
	if err != nil {
		return nil, fmt.Errorf("读取主站登录状态失败，请到工作区重新登录后再读取账号")
	}
	if session.Platform != upstream.PlatformSub2API {
		return nil, fmt.Errorf("模型检测仅支持当前Sub2API工作区，请切换工作区后重试")
	}
	inventory, err := s.readAdminWorkspaceInventory(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("读取主站分组失败，请在分组健康检查主站连接后重试")
	}
	byID := map[string]*AuditTarget{}
	for _, group := range inventory.groups {
		if group.err != nil {
			return nil, fmt.Errorf("读取分组“%s”的账号失败，当前清单不完整；请在分组健康重新加载后重试", group.group.Name)
		}
		for _, account := range group.accounts {
			target := byID[account.ID]
			if target == nil {
				target = &AuditTarget{TargetID: buildTargetID(string(session.Platform), workspace, account.ID), AccountID: account.ID, AccountName: account.Name, ProviderFamily: account.Platform, Groups: []AuditGroup{}}
				byID[account.ID] = target
			}
			target.Groups = append(target.Groups, AuditGroup{group.group.ID, group.group.Name})
		}
	}
	items := make([]AuditTarget, 0, len(byID))
	for _, item := range byID {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TargetID < items[j].TargetID })
	return items, nil
}

// ResolveAuditContext exports no credential to JSON. The caller uses Credential
// only inside the server after the restricted process has verified its limits.
func (s *Service) ResolveAuditContext(ctx context.Context, userID, targetID, clickedGroupID string, needCredential bool) (AuditContext, error) {
	session, workspace, accountID, err := s.resolveManualSession(ctx, userID, targetID)
	if err != nil {
		return AuditContext{}, fmt.Errorf("无法核实此账号属于当前工作区；请重新打开分组健康并选择账号")
	}
	if session.Platform != upstream.PlatformSub2API {
		return AuditContext{}, fmt.Errorf("此检测只支持Sub2API账号，尚未向上游发出检测请求")
	}
	refreshed, err := s.refreshAdminTarget(ctx, session, workspace, accountID)
	if err != nil {
		return AuditContext{}, fmt.Errorf("读取账号当前分组信息失败，请检查主站连接后重试；尚未开始检测")
	}
	if !refreshed.found || refreshed.accountsReadError {
		return AuditContext{}, fmt.Errorf("账号已不存在或分组账号清单不完整；请重新加载分组健康后重试，尚未开始检测")
	}
	result := AuditContext{WorkspaceID: workspace, TargetID: targetID, AccountID: accountID, AccountName: refreshed.target.AccountName, ProviderFamily: refreshed.target.ProviderFamily, GroupID: refreshed.target.AdminGroupID, GroupName: refreshed.target.AdminGroupName}
	if clickedGroupID != "" {
		found := false
		for _, m := range refreshed.memberships {
			if m.groupID == clickedGroupID {
				result.GroupID = m.groupID
				result.GroupName = m.groupName
				found = true
				break
			}
		}
		if !found {
			return AuditContext{}, fmt.Errorf("该账号已不属于点击的分组，请重新打开账号后重试；尚未开始检测")
		}
	}
	if err = s.configureTestTarget(ctx, userID, workspace, &refreshed.target, refreshed.memberships, true); err != nil {
		return AuditContext{}, fmt.Errorf("账号所属分组的测试协议配置冲突或无法读取，请在分组健康的测试设置中修正后重试；尚未开始检测")
	}
	switch refreshed.target.TestConfiguration.Protocol {
	case TestProtocolChatCompletions:
		result.Protocol = "chat"
	case TestProtocolResponses:
		result.Protocol = "openai"
	case TestProtocolMessages:
		result.Protocol = "anthropic"
	default:
		return AuditContext{}, fmt.Errorf("此账号没有可确认的测试协议，请在分组健康设置协议后重试")
	}
	if result.Protocol == "anthropic" && strings.TrimSpace(refreshed.account.Type) != "apikey" {
		result.Reason = "Messages检测仅支持API Key账号；当前账号不是API Key类型，尚未发出请求"
		return result, nil
	}
	cred, err := s.resolveProbeCredential(ctx, session, refreshed.account)
	if err != nil {
		reasons := map[string]string{
			upstream.ReasonExportUnavailable:          "主站账号凭据导出接口不可用，请检查管理员权限及主站版本后重试",
			upstream.ReasonCredentialsRedacted:        "主站返回的账号密钥仍被遮盖，请检查凭据导出权限后重试",
			upstream.ReasonBaseURLUnavailable:         "此账号缺少上游API地址，请在主站补全后重试",
			upstream.ReasonSecureVerificationRequired: "主站要求管理员验证，请先在主站完成验证后重试",
		}
		result.Reason = reasons[upstream.ProbeCredentialReason(err)]
		if result.Reason == "" {
			result.Reason = "无法读取此账号配置的API密钥，请检查主站账号凭据后重试"
		}
		return result, nil
	}
	parsedURL, parseErr := url.Parse(cred.BaseURL)
	if parseErr != nil || parsedURL.Host == "" || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		result.Reason = "此账号的上游地址无效或含认证信息、查询参数，请在主站改为完整API基础地址后重试"
		return result, nil
	}
	result.BaseURL = cred.BaseURL
	result.CanRun = true
	if needCredential {
		result.Credential = cred
	}
	return result, nil
}
