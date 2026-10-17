package connection_health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type ManagedGroupChangePlatform interface {
	ReadSub2APIImportConfigurationContext(context.Context, upstream.Session, string) (upstream.Sub2APIImportConfiguration, error)
	UpdateSub2APIAdminAccountGroupsContext(context.Context, upstream.Session, string, []string) error
}

func groupChangeBlock(message, detail, next string) error {
	return &my_sites.GroupChangeBlockedError{Status: http.StatusConflict, Message: message, Detail: detail, Next: next}
}
func groupChangeInventory() error {
	return groupChangeBlock("主站分组资料没有读全", "无法确认移出后是否还有可用账号；本次未修改任何内容", "请稍后重试")
}
func groupChangePending(name string) error {
	return groupChangeBlock("有未确认完成的主站操作", "这个账号或分组 "+name+" 有上次未确认完成的主站操作；本次未修改任何内容", "请到分组健康页查看，等待确认后再修改")
}
func groupChangeReadError(err error, session upstream.Session, account string) error {
	var remote *upstream.RequestError
	if errors.As(err, &remote) && remote.StatusCode == http.StatusNotFound {
		return groupChangeBlock("主站账号已不存在", "主站查不到账号 "+account+"；本次未修改任何内容", "请关闭窗口，点\"刷新数据\"核对，确认已删除后用\"清理本地失效对接\"处理")
	}
	detail := "原因无法安全确认，请查看主站响应及 Hub 后端检查记录"
	if errors.As(err, &remote) {
		hint := upstream.ImportDiagnosticHint(remote.UpstreamMessage)
		if hint != "" {
			detail = hint
		} else {
			switch remote.Reason {
			case upstream.ErrorNetworkTimeout:
				detail = "读取主站资料超时"
			case upstream.ErrorNetworkUnreachable:
				detail = "Hub 暂时无法连接主站"
			case upstream.ErrorTLSFailed:
				detail = "主站安全连接未能建立"
			default:
				switch remote.MessageKey {
				case upstream.ErrorAuth:
					detail = "已有主站登录资料未通过管理员验证"
				case upstream.ErrorInvalidResponse:
					detail = "主站返回的资料格式不正确，无法确认账号"
				}
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		detail = "读取主站资料超时"
	}
	if errors.Is(err, context.Canceled) {
		detail = "本次读取已取消"
	}
	for _, secret := range []string{session.AccessToken, session.RefreshToken, session.Cookie, session.AdminAPIKey} {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[已遮盖]")
		}
	}
	next := "请稍后重试，并检查主站账号详情能否正常打开"
	if errors.As(err, &remote) && (remote.StatusCode == 401 || remote.MessageKey == upstream.ErrorAuth) {
		next = "请到\"我的站点\"重新连接主站后点\"重新读取\""
	}
	return groupChangeBlock("读取主站资料失败", detail+"；本次未修改任何内容", next)
}
func groupChangeIDs(groups []upstream.Sub2APIImportGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids
}
func sameGroupChangeIDs(a, b []string) bool {
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
func publicGroupChangeGroups(groups []upstream.Sub2APIImportGroup) []my_sites.MainSiteGroup {
	result := make([]my_sites.MainSiteGroup, 0, len(groups))
	for _, g := range groups {
		result = append(result, my_sites.MainSiteGroup{ID: g.ID, Name: g.Name})
	}
	return result
}

// checkGroupRemovalFloor is read-only; moving a disabled account never exempts
// the old group from retaining another usable account.
func checkGroupRemovalFloor(target AdminProbeTarget, removedGroupIDs []string, inventory adminWorkspaceInventory, scope adminMonitoringScope, freezeWorkspace bool, frozenGroups map[string]struct{}, pendingAccountIDs map[string]bool, reservedModels map[string]map[string]bool, now time.Time) error {
	ids := append([]string(nil), removedGroupIDs...)
	sort.Strings(ids)
	parsed, validTarget := parseTargetID(target.TargetID)
	if !validTarget {
		return groupChangeInventory()
	}
	for _, id := range ids {
		var group *adminInventoryGroup
		for i := range inventory.groups {
			if inventory.groups[i].group.ID == id {
				group = &inventory.groups[i]
				break
			}
		}
		name := id
		if group != nil {
			name = group.group.Name
		}
		if _, frozen := frozenGroups[id]; freezeWorkspace || frozen {
			return groupChangePending(name)
		}
		if !adminInventoryComplete(inventory) || !scope.complete || group == nil {
			return groupChangeInventory()
		}
		var subject upstream.AdminGroupAccountInfo
		found := false
		usable, unknown := 0, false
		monitored, inScope := scope.monitoredByGroup[id]
		seen := map[string]bool{}
		for _, a := range group.accounts {
			if a.ID == target.AccountID {
				subject = a
				found = true
				continue
			}
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			if inScope {
				if _, included := monitored[buildTargetID(target.Platform, parsed.adminAccountID, a.ID)]; !included {
					continue
				}
			}
			status, known := normalizeFloorTargetStatus(target.Platform, a.Status)
			if !known || (status == "active" && a.Schedulable == nil) {
				unknown = true
				continue
			}
			if status == "active" && a.Schedulable != nil && *a.Schedulable && !pendingAccountIDs[a.ID] && upstream.Sub2APIDeadlineExpired(a.TempUnschedulableUntil, a.TempUnschedulableKnown, now) {
				usable++
			}
		}
		if !found {
			return groupChangeInventory()
		}
		if usable == 0 {
			if unknown {
				return groupChangeBlock("无法确认移出后是否还有可用账号", "分组 "+name+" 里有账号的状态读不准；本次未修改任何内容", "请稍后重试，或到分组健康页查看该分组")
			}
			return groupChangeBlock("移出后分组将没有可用账号", "分组 "+name+" 除这个账号外没有其他可用账号；本次未修改任何内容", "请先给该分组补充可用账号，或保留这个分组")
		}
		if reservedModels != nil {
			if keys := modelFloorLastKeys(group.accounts, subject, pendingAccountIDs, reservedModels, now); len(keys) > 0 {
				return groupChangeBlock("移出后模型将没有其他来源", "分组 "+name+" 中模型 "+strings.Join(keys, "、")+" 除这个账号外没有其他来源；本次未修改任何内容", "请先在该分组补充提供这些模型的账号，或保留这个分组")
			}
		}
	}
	return nil
}

// ChangeManagedSub2APIAccountGroups holds the existing target and workspace
// leases through the detached confirmation read; no Target checkpoint is added.
func (s *Service) ChangeManagedSub2APIAccountGroups(ctx context.Context, user, workspace string, session upstream.Session, accountID, platform string, expected, targetGroups []string) (my_sites.GroupChangeResult, error) {
	result := my_sites.GroupChangeResult{}
	actioner, ok := s.platformGroups.(ManagedGroupChangePlatform)
	if !ok || session.Platform != upstream.PlatformSub2API {
		return result, groupChangeBlock("Hub 未启用主站分组修改", "后端没有接入分组修改功能；本次未修改任何内容", "请联系维护者检查 Hub 后端启动配置")
	}
	targetID := buildTargetID(string(upstream.PlatformSub2API), workspace, accountID)
	leased, release, err := s.prepareManualAction(ctx, user, workspace, targetID)
	if err != nil {
		return result, groupChangeBlock("正在执行其他主站操作", "这个账号或本工作区正在执行其他主站操作；本次未修改任何内容", "请稍后再试")
	}
	defer release()
	before, err := actioner.ReadSub2APIImportConfigurationContext(leased, session, accountID)
	if err != nil {
		return result, groupChangeReadError(err, session, accountID)
	}
	if len(before.Groups) == 0 {
		return result, groupChangeBlock("主站账号没有任何分组", "主站上这个账号不在任何分组，Hub 不能在这里处理；本次未修改任何内容", "请先到主站后台给这个账号选择分组，再点\"重新读取\"")
	}
	if strings.ToLower(strings.TrimSpace(before.Platform)) != strings.ToLower(strings.TrimSpace(platform)) {
		return result, groupChangeBlock("主站账号平台与 Hub 记录不一致", fmt.Sprintf("主站账号平台是 %s，Hub 记录是 %s；本次未修改任何内容", before.Platform, platform), "平台不能在这里改，请取消对接后重新配置对接")
	}
	if !sameGroupChangeIDs(groupChangeIDs(before.Groups), expected) {
		return result, groupChangeBlock("对接资料已变化", "这条对接已不存在，或主站分组、Hub 记录与窗口中显示的不同；本次未修改任何内容", "请按下面重新读取到的资料核对；对接已不存在时请关闭窗口并点\"刷新数据\"")
	}
	inventory, err := s.loadAdminInventory(leased, user, workspace, adminInventoryCache{})
	if err != nil || inventory == nil || !adminInventoryComplete(*inventory) {
		return result, groupChangeInventory()
	}
	target, found := findActionInventoryTarget(targetID, *inventory)
	if !found {
		return result, groupChangeInventory()
	}
	pair, err := s.reconcileActionObservation(leased, targetObservation(user, workspace, target, inventory))
	if err != nil {
		return result, groupChangeInventory()
	}
	if pair.pendingCount() != 0 {
		return result, groupChangePending(target.AdminGroupName)
	}
	wanted := map[string]bool{}
	for _, id := range targetGroups {
		wanted[id] = true
	}
	removed := []string{}
	for _, id := range groupChangeIDs(before.Groups) {
		if !wanted[id] {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		guard := s.sub2APIFloorGuardFor(user, workspace)
		guard.rememberInventory(*inventory)
		if err := s.overlayPersistentPending(leased, user, workspace, guard, *inventory); err != nil {
			return result, groupChangeInventory()
		}
		scope, err := s.loadAdminMonitoringScope(leased, user, workspace, *inventory)
		if err != nil || !scope.complete {
			return result, groupChangeInventory()
		}
		reserved, err := s.modelFloorReserved(leased, user, workspace)
		if err != nil {
			return result, groupChangeInventory()
		}
		if err := guard.checkGroupRemoval(target, removed, *inventory, scope, reserved, time.Now()); err != nil {
			return result, err
		}
	}
	done, err := s.registerActionDispatch()
	if err != nil {
		return result, groupChangeBlock("正在执行其他主站操作", "Hub 后端正在停止，尚未发出本次修改；本次未修改任何内容", "请等待 Hub 后端恢复后重新读取")
	}
	defer done()
	result.WriteError = actioner.UpdateSub2APIAdminAccountGroupsContext(leased, session, accountID, targetGroups)
	if outcome := upstream.RemoteMutationOutcome(result.WriteError); outcome == upstream.MutationNotSent || outcome == upstream.MutationConfirmedRejected {
		result.Outcome = "unchanged"
		result.Groups = publicGroupChangeGroups(before.Groups)
		return result, nil
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(leased), 10*time.Second)
	defer cancel()
	after, readErr := actioner.ReadSub2APIImportConfigurationContext(readCtx, session, accountID)
	result.ReadError = readErr
	if readErr != nil {
		result.Outcome = "unknown"
		return result, nil
	}
	switch {
	case sameGroupChangeIDs(groupChangeIDs(after.Groups), targetGroups):
		result.Outcome = "changed"
		result.Groups = publicGroupChangeGroups(after.Groups)
	case sameGroupChangeIDs(groupChangeIDs(after.Groups), groupChangeIDs(before.Groups)):
		result.Outcome = "unchanged"
		result.Groups = publicGroupChangeGroups(after.Groups)
	default:
		result.Outcome = "unknown"
	}
	return result, nil
}
