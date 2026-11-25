package connection_health

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// AuditCatalog is only a selection list. Execution still resolves current context.
type AuditCatalog struct {
	WorkspaceID string              `json:"workspaceId"`
	Groups      []AuditCatalogGroup `json:"groups"`
}

type AuditCatalogGroup struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Accounts []AuditCatalogAccount `json:"accounts"`
}

type AuditCatalogAccount struct {
	TargetID    string `json:"targetId"`
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	Priority    *int   `json:"priority,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
}

// AuditCatalogError contains only public, readable diagnostics.
type AuditCatalogError struct {
	Status  int
	Message string
}

func (e *AuditCatalogError) Error() string { return e.Message }

// ListAuditCatalog uses the handler's captured owner, never another current-workspace
// lookup. It reuses inventory and protocol resolution without health aggregation.
func (s *Service) ListAuditCatalog(ctx context.Context, userID, workspaceID string) (AuditCatalog, error) {
	result := AuditCatalog{WorkspaceID: workspaceID, Groups: []AuditCatalogGroup{}}
	if strings.TrimSpace(workspaceID) == "" || s.mySites == nil || s.platformGroups == nil || s.repo == nil {
		return result, &AuditCatalogError{422, "无法读取所选工作区的分组账号，请重新选择工作区并检查后端配置；没有开始检测"}
	}
	session, err := s.mySites.RequireSession(ctx, userID, workspaceID)
	if err != nil {
		switch err.Error() {
		case my_sites.ErrorAuthRequired:
			return result, &AuditCatalogError{422, "当前工作区的主站登录尚未就绪，请到工作区重新登录后重试读取名单；调度站登录仍保留，没有开始检测"}
		case my_sites.ErrorAdminOnly:
			return result, &AuditCatalogError{422, "当前工作区的主站登录刷新或管理员核验未通过，原会话管理没有返回更具体原因。请在工作区重新登录，并检查主站管理员身份及后端日志后重试；调度站登录仍保留，没有开始检测"}
		default:
			return result, auditCatalogReadError("主站登录状态", err)
		}
	}
	if session.Platform != upstream.PlatformSub2API {
		return result, &AuditCatalogError{422, "模型检测仅支持Sub2API工作区，请重新选择工作区后读取名单；没有开始检测"}
	}
	inventory, err := s.readAdminWorkspaceInventory(ctx, session)
	if err != nil {
		return result, auditCatalogReadError("主站分组", err)
	}
	memberships := make(map[string][]TestConfigurationSource)
	for _, group := range inventory.groups {
		if group.err != nil {
			return result, auditCatalogReadError(fmt.Sprintf("分组“%s”的账号", group.group.Name), group.err)
		}
		for _, account := range group.accounts {
			memberships[account.ID] = append(memberships[account.ID], testConfigurationMembership(group.group, account))
		}
	}
	configs, err := s.repo.ListGroupTestConfigurations(ctx, userID, workspaceID)
	if err != nil {
		return result, auditCatalogReadError("本站保存的分组测试设置（请检查数据库连接）", err)
	}
	hidden, err := s.hiddenAccountTargetSet(ctx, userID, workspaceID)
	if err != nil {
		return result, auditCatalogReadError("本站保存的账号屏蔽清单（请检查数据库连接）", err)
	}
	protocols := make(map[string]string, len(memberships))
	for id, groups := range memberships {
		configuration := ResolveGroupTestConfiguration(string(session.Platform), groups, true, configs)
		protocols[id] = map[TestProtocol]string{TestProtocolChatCompletions: "chat", TestProtocolResponses: "openai", TestProtocolMessages: "anthropic"}[configuration.Protocol]
	}
	for _, group := range inventory.groups {
		item := AuditCatalogGroup{ID: group.group.ID, Name: group.group.Name, Accounts: []AuditCatalogAccount{}}
		seen := make(map[string]bool)
		for _, account := range group.accounts {
			targetID := buildTargetID(string(session.Platform), workspaceID, account.ID)
			if hidden[targetID] || seen[targetID] {
				continue
			}
			seen[targetID] = true
			name := account.Name
			if name == "" {
				name = account.ID
			}
			item.Accounts = append(item.Accounts, AuditCatalogAccount{TargetID: targetID, AccountID: account.ID, AccountName: name, Priority: cloneIntPointer(account.Priority), Protocol: protocols[account.ID]})
		}
		sort.Slice(item.Accounts, func(i, j int) bool {
			a, b := item.Accounts[i], item.Accounts[j]
			if a.Priority == nil || b.Priority == nil {
				if (a.Priority == nil) != (b.Priority == nil) {
					return a.Priority != nil
				}
			} else if *a.Priority != *b.Priority {
				return *a.Priority < *b.Priority
			}
			return a.TargetID < b.TargetID
		})
		result.Groups = append(result.Groups, item)
	}
	return result, nil
}

func auditCatalogReadError(step string, err error) error {
	reason := "原因尚未确认，请检查后端日志中本次名单读取"
	var upstreamError *upstream.RequestError
	if errors.As(err, &upstreamError) {
		key := upstreamError.Reason
		if key == "" {
			key = upstreamError.MessageKey
		}
		if known := map[string]string{
			upstream.ErrorNetworkTimeout:              "连接主站超时，请检查主站响应和网络",
			upstream.ErrorNetworkUnreachable:          "无法连接主站，请检查主站地址、网络和服务是否可访问",
			upstream.ErrorTLSFailed:                   "与主站建立安全连接失败，请检查主站证书及本机时间",
			upstream.ErrorInvalidURL:                  "主站地址格式不正确，请在工作区核对主站地址",
			upstream.ErrorAuth:                        "主站没有接受当前登录，请在该工作区重新登录",
			upstream.ErrorAccessTokenRejected:         "主站没有接受当前登录，请在该工作区重新登录",
			upstream.ErrorRefreshTokenRejected:        "主站没有接受登录刷新，请在该工作区重新登录",
			upstream.ErrorLoginRejected:               "主站没有接受当前登录，请在该工作区重新登录",
			upstream.ErrorLoginIncomplete:             "主站登录资料不完整，请在该工作区重新登录",
			upstream.ErrorForbidden:                   "主站拒绝本次分组或账号读取，请核查主站访问日志中的具体限制",
			upstream.ErrorNotFound:                    "主站没有找到这次读取的接口或资源，请核对主站版本、地址及后端日志",
			upstream.ErrorRateLimited:                 "主站要求降低读取频率，请稍候再试",
			upstream.ErrorUpstreamServer:              "主站处理本次读取时出错，请检查主站服务和对应时间的日志",
			upstream.ErrorInvalidResponse:             "主站返回的分组或账号内容格式不正确、分页不完整或不一致，无法确认完整名单；请检查主站版本及后端日志",
			upstream.ErrorBusinessRejected:            "主站返回了业务拒绝，请按具体说明处理；没有附具体原因时请检查主站日志",
			upstream.ErrorRequest:                     "主站读取请求未完成，未返回更具体原因，请检查本次读取的服务日志",
			upstream.ErrorNetwork:                     "主站读取连接未完成，未返回更具体原因，请检查本次读取的服务日志",
			upstream.ErrorAnnouncementAckRequired:     "主站明确要求先确认公告，请先打开这个工作区的主站网页确认公告",
			upstream.ErrorUpstreamInsufficientBalance: "主站明确报告余额不足，请在这个工作区的主站网页核对余额并按提示处理",
			upstream.ErrorUpstreamKeyQuotaExhausted:   "主站明确报告本次读取所用Key的额度已用完，请在这个工作区的主站网页核对额度并处理",
			upstream.ErrorUpstreamKeyExpired:          "主站明确报告本次读取所用Key已过期，请在这个工作区检查并更新有效登录凭据",
		}[key]; known != "" {
			reason = known
		}
		if message := strings.TrimSpace(upstreamError.UpstreamMessage); message != "" {
			reason += "；" + message
		}
		if upstreamError.StatusCode > 0 {
			reason += fmt.Sprintf("（HTTP %d）", upstreamError.StatusCode)
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "读取超过等待时间，请检查主站响应和网络"
	} else if errors.Is(err, context.Canceled) {
		reason = "本次读取已取消"
	}
	return &AuditCatalogError{503, fmt.Sprintf("读取%s失败：%s。没有修改主站或开始检测；处理后请点击“更新分组和账号”重试", step, reason)}
}
