package my_sites

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type MainSiteGroup struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}
type MainSiteGroupAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}
type GroupChangeResult struct {
	Outcome    string
	Groups     []MainSiteGroup
	WriteError error
	ReadError  error
}
type GroupChangeBlockedError struct {
	Status                int
	Message, Detail, Next string
	Cause                 error
}

func (e *GroupChangeBlockedError) Error() string {
	return e.Message + "；" + e.Detail + "；" + e.Next
}
func (e *GroupChangeBlockedError) Unwrap() error { return e.Cause }

type ManagedAccountGroupChanger interface {
	ChangeManagedSub2APIAccountGroups(context.Context, string, string, upstream.Session, string, string, []string, []string) (GroupChangeResult, error)
}

func (s *Service) SetManagedAccountGroupChanger(changer ManagedAccountGroupChanger) {
	s.managedGroupChanger = changer
}

type MainSiteGroupsRequest struct {
	WorkspaceAdminAccountID  string              `json:"workspaceAdminAccountId"`
	ExpectedConnection       *ExpectedConnection `json:"expectedConnection"`
	ExpectedMainSiteGroupIDs []string            `json:"expectedMainSiteGroupIds"`
	TargetGroupIDs           []string            `json:"targetGroupIds"`
	RemoveOldPricingMapping  bool                `json:"removeOldPricingMapping"`
}
type MainSiteGroupsResult struct {
	WorkspaceAdminAccountID string          `json:"workspaceAdminAccountId"`
	Outcome                 string          `json:"outcome"`
	Message                 string          `json:"message"`
	Detail                  string          `json:"detail"`
	Next                    string          `json:"next"`
	MainSiteGroups          []MainSiteGroup `json:"mainSiteGroups"`
	Connection              *RealConnection `json:"connection,omitempty"`
}
type MainSiteGroupsReadResponse struct {
	WorkspaceAdminAccountID string               `json:"workspaceAdminAccountId"`
	Connection              RealConnection       `json:"connection"`
	Account                 MainSiteGroupAccount `json:"account"`
	CurrentGroups           []MainSiteGroup      `json:"currentGroups"`
	HubGroups               []MainSiteGroup      `json:"hubGroups"`
	Groups                  []MainSiteGroup      `json:"groups"`
	Consistent              bool                 `json:"consistent"`
}
type CheckedGroupReassignmentRepository interface {
	ReassignRealConnectionGroupsChecked(context.Context, RealConnection, []string, []string, bool) error
}

func mainGroupsError(status int, message, detail, next string) error {
	return &scopedRequestError{Status: status, Message: message, Detail: detail, Next: next}
}
func mainGroupsChanged() error {
	return mainGroupsError(409, "对接资料已变化", "这条对接已不存在，或主站分组、Hub 记录与窗口中显示的不同；本次未修改任何内容", "请按下面重新读取到的资料核对；对接已不存在时请关闭窗口并点\"刷新数据\"")
}
func mainGroupsInvalid(detail string) error {
	return mainGroupsError(400, "修改请求不正确", detail+"；本次未修改任何内容", "请关闭窗口重新打开后再试")
}
func mainGroupsUnsupported() error {
	return mainGroupsError(400, "不能在这里修改主站分组", "这条对接不是由 Hub 自动创建的，或主站不是 Sub2API；本次未修改任何内容", "如需更换，请取消对接后重新配置对接")
}
func mainGroupsEmpty() error {
	return mainGroupsError(409, "主站账号没有任何分组", "主站上这个账号不在任何分组，Hub 不能在这里处理；本次未修改任何内容", "请先到主站后台给这个账号选择分组，再点\"重新读取\"")
}
func mainGroupsPlatform(actual, expected string) error {
	return mainGroupsError(409, "主站账号平台与 Hub 记录不一致", fmt.Sprintf("主站账号平台是 %s，Hub 记录是 %s；本次未修改任何内容", actual, expected), "平台不能在这里改，请取消对接后重新配置对接")
}
func mainGroupsReadFailure(err error, session upstream.Session, account string) error {
	var remote *upstream.RequestError
	if errors.As(err, &remote) && remote.StatusCode == 404 && account != "" {
		return mainGroupsError(409, "主站账号已不存在", "主站查不到账号 "+account+"；本次未修改任何内容", "请关闭窗口，点\"刷新数据\"核对，确认已删除后用\"清理本地失效对接\"处理")
	}
	detail, _ := scopedRemoteCause(err, session)
	next := "请稍后重试，并检查主站账号详情能否正常打开"
	if errors.As(err, &remote) && (remote.StatusCode == 401 || remote.MessageKey == upstream.ErrorAuth) {
		next = "请到\"我的站点\"重新连接主站后点\"重新读取\""
	}
	return mainGroupsError(400, "读取主站资料失败", detail+"；本次未修改任何内容", next)
}
func sameMainSiteGroupIDs(a, b []string) bool {
	aa, bb := sortedIdentityValues(a), sortedIdentityValues(b)
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
func importMainSiteGroups(groups []upstream.Sub2APIImportGroup) []MainSiteGroup {
	result := make([]MainSiteGroup, 0, len(groups))
	for _, g := range groups {
		result = append(result, MainSiteGroup{ID: g.ID, Name: g.Name})
	}
	return result
}
func mainSiteGroupIDs(groups []MainSiteGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids
}
func normalizedMainPlatform(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func (s *Service) mainGroupsConnection(ctx context.Context, user, id, workspace string) (RealConnection, upstream.Session, error) {
	if s.connRepository == nil {
		return RealConnection{}, upstream.Session{}, mainGroupsChanged()
	}
	conn, err := s.connRepository.GetRealConnection(ctx, id, user, workspace)
	if err != nil {
		return RealConnection{}, upstream.Session{}, scopedReadFailure("这条对接", err)
	}
	if conn == nil {
		return RealConnection{}, upstream.Session{}, mainGroupsChanged()
	}
	if conn.ProvisioningMode != ProvisioningModeManaged || conn.AdminPlatform != string(upstream.PlatformSub2API) || strings.TrimSpace(conn.AdminAccountID) == "" || (conn.Status != "" && conn.Status != ConnectionStatusActive) {
		return RealConnection{}, upstream.Session{}, mainGroupsUnsupported()
	}
	all, err := s.ListRealConnectionsForWorkspace(ctx, user, workspace)
	if err != nil {
		return RealConnection{}, upstream.Session{}, scopedReadFailure("工作区对接", err)
	}
	shared := 0
	for _, other := range all {
		if other.ID != conn.ID && (other.Status == "" || other.Status == ConnectionStatusActive) && other.AdminAccountID == conn.AdminAccountID {
			shared++
		}
	}
	if shared > 0 {
		return RealConnection{}, upstream.Session{}, mainGroupsError(409, "主站账号还被其他对接使用", fmt.Sprintf("主站账号 %s 还被另外 %d 条对接使用，修改分组会同时影响它们；本次未修改任何内容", conn.AdminAccountName, shared), "请先核对这些对接，只保留一条后再修改")
	}
	session, available, err := s.StoredSession(ctx, user, workspace)
	if err != nil || !available {
		return RealConnection{}, upstream.Session{}, mainGroupsError(409, "没有可用的主站登录资料", "Hub 没有这个工作区可用的主站登录资料，或读取已保存的资料出错；本次未修改任何内容", "请到\"我的站点\"重新连接主站后点\"重新读取\"")
	}
	if session.Platform != upstream.PlatformSub2API {
		return RealConnection{}, upstream.Session{}, mainGroupsUnsupported()
	}
	return *conn, session, nil
}
func (s *Service) ReadConnectionMainSiteGroups(ctx context.Context, user, id, requested string) (MainSiteGroupsReadResponse, error) {
	workspace, err := s.scopedWorkspace(ctx, user, requested)
	if err != nil {
		return MainSiteGroupsReadResponse{}, err
	}
	conn, session, err := s.mainGroupsConnection(ctx, user, id, workspace)
	if err != nil {
		return MainSiteGroupsReadResponse{}, err
	}
	detail, err := s.platformService.ReadSub2APIImportConfigurationContext(ctx, session, conn.AdminAccountID)
	if err != nil {
		return MainSiteGroupsReadResponse{}, mainGroupsReadFailure(err, session, conn.AdminAccountID)
	}
	if normalizedMainPlatform(detail.Platform) != normalizedMainPlatform(conn.GroupType) {
		return MainSiteGroupsReadResponse{}, mainGroupsPlatform(detail.Platform, conn.GroupType)
	}
	if len(detail.Groups) == 0 {
		return MainSiteGroupsReadResponse{}, mainGroupsEmpty()
	}
	groups, err := s.platformService.FetchAdminAllGroupsContext(ctx, session)
	if err != nil {
		return MainSiteGroupsReadResponse{}, mainGroupsReadFailure(err, session, "")
	}
	result := MainSiteGroupsReadResponse{WorkspaceAdminAccountID: workspace, Connection: publicRealConnection(conn), Account: MainSiteGroupAccount{ID: detail.ID, Name: detail.Name, Platform: detail.Platform}, CurrentGroups: importMainSiteGroups(detail.Groups), HubGroups: []MainSiteGroup{}, Groups: []MainSiteGroup{}}
	byID := map[string]upstream.AdminGroupInfo{}
	for _, g := range groups {
		byID[g.ID] = g
		if normalizedMainPlatform(g.Platform) == normalizedMainPlatform(detail.Platform) {
			result.Groups = append(result.Groups, MainSiteGroup{ID: g.ID, Name: g.Name, Status: g.Status})
		}
	}
	for i, g := range result.CurrentGroups {
		if known, ok := byID[g.ID]; ok {
			result.CurrentGroups[i].Name = known.Name
		}
	}
	for i, id := range conn.OwnGroupIDs {
		name := ""
		if i < len(conn.OwnGroupNames) {
			name = conn.OwnGroupNames[i]
		}
		result.HubGroups = append(result.HubGroups, MainSiteGroup{ID: id, Name: name})
	}
	sort.SliceStable(result.Groups, func(i, j int) bool { return result.Groups[i].Name < result.Groups[j].Name })
	result.Consistent = sameMainSiteGroupIDs(mainSiteGroupIDs(result.CurrentGroups), conn.OwnGroupIDs)
	return result, nil
}
func validMainGroupIDs(ids []string, allowEmpty bool) bool {
	if len(ids) > 512 || (!allowEmpty && len(ids) == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (s *Service) prepareMainGroupRequest(ctx context.Context, user, id string, req MainSiteGroupsRequest, change bool) (RealConnection, upstream.Session, string, error) {
	workspace, err := s.scopedWorkspace(ctx, user, req.WorkspaceAdminAccountID)
	if err != nil {
		return RealConnection{}, upstream.Session{}, "", err
	}
	if req.ExpectedConnection == nil || req.ExpectedConnection.WorkspaceAdminAccountID != workspace || !validExpectedConnection(RealDisconnectRequest{ConnectionID: id, Mode: "unlink", ExpectedConnection: req.ExpectedConnection}) || !validMainGroupIDs(req.ExpectedMainSiteGroupIDs, true) || (change && !validMainGroupIDs(req.TargetGroupIDs, false)) {
		return RealConnection{}, upstream.Session{}, "", mainGroupsInvalid("必须带上窗口最近读取的完整对接资料、主站分组及所选分组；分组编号须为不重复的正整数，每次最多 512 个")
	}
	// Compare before checking eligibility so stale snapshots always require a reread.
	if s.connRepository == nil {
		return RealConnection{}, upstream.Session{}, "", mainGroupsChanged()
	}
	current, err := s.connRepository.GetRealConnection(ctx, id, user, workspace)
	if err != nil {
		return RealConnection{}, upstream.Session{}, "", scopedReadFailure("这条对接", err)
	}
	if current == nil || !sameCheckedConnection(*current, req.ExpectedConnection.connection(user)) {
		return RealConnection{}, upstream.Session{}, "", mainGroupsChanged()
	}
	conn, session, err := s.mainGroupsConnection(ctx, user, id, workspace)
	if err != nil {
		return RealConnection{}, upstream.Session{}, "", err
	}
	// The helper reads again; that record must retain the window's checked basis.
	if !sameCheckedConnection(conn, req.ExpectedConnection.connection(user)) {
		return RealConnection{}, upstream.Session{}, "", mainGroupsChanged()
	}
	if len(req.ExpectedMainSiteGroupIDs) == 0 {
		return RealConnection{}, upstream.Session{}, "", mainGroupsEmpty()
	}
	return conn, session, workspace, nil
}
func mainGroupReassignmentDetail(err error) string {
	var saved *GroupReassignmentError
	if errors.As(err, &saved) {
		switch saved.Kind {
		case "removed":
			return "这条对接已被解除"
		case "changed":
			return "Hub 记录已被改动"
		}
		cause := safeLocalStorageCause(saved.Cause)
		if errors.Is(saved.Cause, errGroupReassignmentStateMissing) {
			cause = "工作区的调价数据源记录已不存在，请重新连接主站后再重新读取"
		}
		return "本地保存失败：" + saved.Step + "：" + cause
	}
	return "本地保存失败：保存对接分组：" + safeLocalStorageCause(err)
}
func (s *Service) saveMainGroups(ctx context.Context, conn RealConnection, groups []MainSiteGroup, remove bool) error {
	repository, ok := s.connRepository.(CheckedGroupReassignmentRepository)
	if !ok {
		return &GroupReassignmentError{Kind: "storage", Step: "确认本地分组保存入口", Cause: errors.New("checked repository unavailable")}
	}
	ids, names := make([]string, 0, len(groups)), make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
		names = append(names, g.Name)
	}
	return repository.ReassignRealConnectionGroupsChecked(ctx, conn, ids, names, remove)
}
func completedMainGroups(workspace string, conn RealConnection, groups []MainSiteGroup) MainSiteGroupsResult {
	conn.OwnGroupIDs, conn.OwnGroupNames = []string{}, []string{}
	for _, g := range groups {
		conn.OwnGroupIDs = append(conn.OwnGroupIDs, g.ID)
		conn.OwnGroupNames = append(conn.OwnGroupNames, g.Name)
	}
	public := publicRealConnection(conn)
	return MainSiteGroupsResult{WorkspaceAdminAccountID: workspace, Outcome: "completed", MainSiteGroups: groups, Connection: &public}
}
func (s *Service) ChangeConnectionMainSiteGroups(ctx context.Context, user, id string, req MainSiteGroupsRequest) (MainSiteGroupsResult, error) {
	conn, session, workspace, err := s.prepareMainGroupRequest(ctx, user, id, req, true)
	if err != nil {
		return MainSiteGroupsResult{}, err
	}
	ids, names, err := s.resolveImportAdminGroups(ctx, &State{Session: session}, req.TargetGroupIDs, normalizedMainPlatform(conn.GroupType))
	if err != nil {
		cause := "所选分组无法确认"
		var safe interface{ SafeImportDetail() string }
		if errors.As(err, &safe) {
			cause = safe.SafeImportDetail()
		} else {
			cause, _ = scopedRemoteCause(err, session)
		}
		return MainSiteGroupsResult{}, mainGroupsError(400, "所选主站分组不能使用", cause+"；本次未修改任何内容", "请按下面重新读取到的分组重新选择")
	}
	if sameMainSiteGroupIDs(ids, req.ExpectedMainSiteGroupIDs) {
		return MainSiteGroupsResult{}, mainGroupsError(400, "没有需要保存的修改", "选择的分组与主站当前分组相同；本次未修改任何内容", "无需操作")
	}
	if s.managedGroupChanger == nil {
		return MainSiteGroupsResult{}, mainGroupsError(409, "Hub 未启用主站分组修改", "后端没有接入分组修改功能；本次未修改任何内容", "请联系维护者检查 Hub 后端启动配置")
	}
	changed, err := s.managedGroupChanger.ChangeManagedSub2APIAccountGroups(ctx, user, workspace, session, conn.AdminAccountID, normalizedMainPlatform(conn.GroupType), req.ExpectedMainSiteGroupIDs, ids)
	if err != nil {
		var blocked *GroupChangeBlockedError
		if errors.As(err, &blocked) {
			return MainSiteGroupsResult{}, mainGroupsError(blocked.Status, blocked.Message, blocked.Detail, blocked.Next)
		}
		return MainSiteGroupsResult{}, mainGroupsReadFailure(err, session, conn.AdminAccountID)
	}
	result := MainSiteGroupsResult{WorkspaceAdminAccountID: workspace, MainSiteGroups: []MainSiteGroup{}}
	cause, _ := scopedRemoteCause(changed.WriteError, session)
	switch changed.Outcome {
	case "unchanged":
		if changed.WriteError == nil {
			cause = "主站读回仍为修改前分组，未返回具体拒绝原因，请核查主站记录"
		}
		result.Outcome = "unchanged"
		result.MainSiteGroups = changed.Groups
		result.Message = "主站分组没有修改"
		result.Detail = cause + "。Hub 记录和调价数据源也没有改动"
		result.Next = "请核对下面重新读取到的分组后再决定是否重试"
		var remote *upstream.RequestError
		if errors.As(changed.WriteError, &remote) && strings.Contains(strings.ToLower(remote.UpstreamCode+" "+remote.RemoteReason), "mixed_channel_warning") {
			result.Detail = "主站拒绝了这次修改：目标分组里已有另一种渠道的账号，Anthropic 与 Antigravity 账号不能放在同一分组。请改选其他分组。主站原文：" + cause + "。Hub 记录和调价数据源也没有改动"
		}
		return result, nil
	case "changed":
		// Use the validated complete group table for names; the readback confirms IDs.
		target := make([]MainSiteGroup, 0, len(ids))
		for i, id := range ids {
			target = append(target, MainSiteGroup{ID: id, Name: names[i]})
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = s.saveMainGroups(saveCtx, conn, target, req.RemoveOldPricingMapping)
		if err == nil {
			return completedMainGroups(workspace, conn, target), nil
		}
		result.MainSiteGroups = target
		result.Outcome = "main_changed_hub_not_saved"
		result.Message = "主站已改为 " + strings.Join(names, "、") + "，但 Hub 记录没有更新"
		result.Detail = mainGroupReassignmentDetail(err)
		result.Next = "不要再次保存主站；请在下面核对，两边仍不一致时点\"按主站当前分组更新 Hub 记录\"补存。"
		var storage *GroupReassignmentError
		if !errors.As(err, &storage) || storage.Uncertain {
			result.Outcome = "hub_unknown"
			result.Message = "主站已改为 " + strings.Join(names, "、") + "；Hub 保存结果未能确认"
			result.Next = "请在下面核对；两边仍不一致时，点\"按主站当前分组更新 Hub 记录\"。"
		}
		return result, nil
	default:
		result.Outcome = "main_unknown"
		result.Message = "已向主站发出修改，但没能确认主站现在的分组"
		result.Detail = "Hub 记录没有改动"
		if changed.WriteError != nil {
			result.Detail += "；主站写入回复：" + cause
		}
		if changed.ReadError != nil {
			readCause, _ := scopedRemoteCause(changed.ReadError, session)
			result.Detail += "；读回主站分组失败：" + readCause
		} else {
			result.Detail += "；读回的主站分组既不是修改前的分组，也不是所选目标分组"
		}
		result.Next = "请核对下面的主站当前分组后再决定是否重新保存"
		return result, nil
	}
}
func (s *Service) SyncConnectionHubGroups(ctx context.Context, user, id string, req MainSiteGroupsRequest) (MainSiteGroupsResult, error) {
	conn, session, workspace, err := s.prepareMainGroupRequest(ctx, user, id, req, false)
	if err != nil {
		return MainSiteGroupsResult{}, err
	}
	detail, err := s.platformService.ReadSub2APIImportConfigurationContext(ctx, session, conn.AdminAccountID)
	if err != nil {
		return MainSiteGroupsResult{}, mainGroupsReadFailure(err, session, conn.AdminAccountID)
	}
	if len(detail.Groups) == 0 {
		return MainSiteGroupsResult{}, mainGroupsEmpty()
	}
	if normalizedMainPlatform(detail.Platform) != normalizedMainPlatform(conn.GroupType) {
		return MainSiteGroupsResult{}, mainGroupsPlatform(detail.Platform, conn.GroupType)
	}
	current := importMainSiteGroups(detail.Groups)
	if !sameMainSiteGroupIDs(mainSiteGroupIDs(current), req.ExpectedMainSiteGroupIDs) {
		return MainSiteGroupsResult{}, mainGroupsChanged()
	}
	groups, err := s.platformService.FetchAdminAllGroupsContext(ctx, session)
	if err != nil {
		return MainSiteGroupsResult{}, mainGroupsReadFailure(err, session, "")
	}
	byID := map[string]upstream.AdminGroupInfo{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	for i, g := range current {
		known, ok := byID[g.ID]
		if !ok {
			return MainSiteGroupsResult{}, mainGroupsError(409, "主站分组资料不一致", "账号所在的分组 "+g.ID+" 在主站分组列表里找不到；本次未更新 Hub 记录", "请稍后重试")
		}
		current[i].Name = known.Name
	}
	if sameMainSiteGroupIDs(mainSiteGroupIDs(current), conn.OwnGroupIDs) {
		return MainSiteGroupsResult{}, mainGroupsError(400, "没有需要保存的修改", "Hub 记录已与主站一致；本次未修改任何内容", "无需操作")
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = s.saveMainGroups(saveCtx, conn, current, req.RemoveOldPricingMapping)
	if err == nil {
		return completedMainGroups(workspace, conn, current), nil
	}
	var storage *GroupReassignmentError
	if errors.As(err, &storage) && !storage.Uncertain {
		return MainSiteGroupsResult{}, mainGroupsError(409, "Hub 记录没有更新", mainGroupReassignmentDetail(err)+"；主站没有改动", "请按下面重新读取到的资料核对后再试")
	}
	return MainSiteGroupsResult{WorkspaceAdminAccountID: workspace, Outcome: "hub_unknown", Message: "Hub 保存结果未能确认", Detail: mainGroupReassignmentDetail(err) + "；本次未修改主站", Next: "请核对下面的分组；两边仍不一致时，点\"按主站当前分组更新 Hub 记录\"", MainSiteGroups: current}, nil
}
func (h *Handler) readConnectionMainSiteGroups(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, 401, "auth.errors.unauthorized")
		return
	}
	result, err := h.service.ReadConnectionMainSiteGroups(r.Context(), user, r.PathValue("id"), r.URL.Query().Get("workspaceAdminAccountId"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, 200, result)
}
func (h *Handler) writeConnectionMainSiteGroups(w http.ResponseWriter, r *http.Request, sync bool) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, 401, "auth.errors.unauthorized")
		return
	}
	var req MainSiteGroupsRequest
	if err := httpjson.Decode(r, &req); err != nil {
		writeError(w, mainGroupsInvalid("请求内容不是完整的分组修改资料"))
		return
	}
	var result MainSiteGroupsResult
	var err error
	if sync {
		result, err = h.service.SyncConnectionHubGroups(r.Context(), user, r.PathValue("id"), req)
	} else {
		result, err = h.service.ChangeConnectionMainSiteGroups(r.Context(), user, r.PathValue("id"), req)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, 200, result)
}
