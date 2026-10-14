package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

const (
	ErrorImportSettingsInvalid          = "admin.mySites.errors.importSettingsInvalid"
	ErrorImportGroupTypeMismatch        = "admin.mySites.errors.importGroupTypeMismatch"
	ErrorImportSettingsUnsupported      = "admin.mySites.errors.importSettingsUnsupported"
	ErrorImportUpstreamKeyFailed        = "admin.mySites.errors.importUpstreamKeyFailed"
	ErrorImportModelSyncWarning         = "admin.mySites.errors.importModelSyncWarning"
	ErrorImportCancelled                = "admin.mySites.errors.importCancelled"
	ErrorImportModelListEmpty           = "admin.mySites.errors.importModelListEmpty"
	ErrorImportModelListInvalid         = "admin.mySites.errors.importModelListInvalid"
	ErrorImportSessionRefreshFailed     = "admin.mySites.errors.importSessionRefreshFailed"
	ErrorImportAdminVerificationFailed  = "admin.mySites.errors.importAdminVerificationFailed"
	ErrorImportSessionSaveFailed        = "admin.mySites.errors.importSessionSaveFailed"
	ErrorImportModelSyncFailed          = "admin.mySites.errors.importModelSyncFailed"
	ErrorImportAccountCreateFailed      = "admin.mySites.errors.importAccountCreateFailed"
	ErrorImportConfigurationUnavailable = "admin.mySites.errors.importConfigurationUnavailable"
	ErrorImportConfigurationMismatch    = "admin.mySites.errors.importConfigurationMismatch"
	ErrorImportPersistenceFailed        = "admin.mySites.errors.importPersistenceFailed"
	ErrorImportPersistencePending       = "admin.mySites.errors.importPersistencePending"
)

func (settings *ImportAccountSettingsRequest) UnmarshalJSON(data []byte) error {
	type wire ImportAccountSettingsRequest
	var decoded wire
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"priorityMode", "priority", "concurrency", "passthrough", "poolMode", "upstreamBillingProbeEnabled"} {
		if value, exists := fields[key]; exists && strings.TrimSpace(string(value)) == "null" {
			decoded.invalidNull = true
		}
	}
	*settings = ImportAccountSettingsRequest(decoded)
	return nil
}

func normalizeImportAccountSettings(req *ImportAccountSettingsRequest, platform string) (ImportAccountSettings, error) {
	switch platform {
	case "openai", "anthropic", "gemini", "antigravity", "grok", "kimi", "zhipu", "deepseek", "minimax":
	default:
		return ImportAccountSettings{}, importInvalid(ErrorImportGroupTypeMismatch, "所选平台不支持导入，请重新选择已有支持平台")
	}
	settings := ImportAccountSettings{PriorityMode: "automatic", Priority: 100, Concurrency: 50, PoolMode: true, UpstreamBillingProbeEnabled: true}
	if req != nil {
		if req.invalidNull {
			return ImportAccountSettings{}, importInvalid(ErrorImportSettingsInvalid, "账号设置字段不能为 null，请提供有效值或省略该字段")
		}
		if req.PriorityMode != nil {
			settings.PriorityMode = *req.PriorityMode
		}
		if req.Priority != nil {
			settings.Priority = *req.Priority
		} else if settings.PriorityMode == "manual" {
			settings.Priority = 1
		}
		if req.Concurrency != nil {
			settings.Concurrency = *req.Concurrency
		}
		if req.Passthrough != nil {
			settings.Passthrough = *req.Passthrough
		}
		if req.PoolMode != nil {
			settings.PoolMode = *req.PoolMode
		}
		if req.UpstreamBillingProbeEnabled != nil {
			settings.UpstreamBillingProbeEnabled = *req.UpstreamBillingProbeEnabled
		}
	}
	problems := []string{}
	if settings.PriorityMode != "automatic" && settings.PriorityMode != "manual" {
		problems = append(problems, "Priority 模式必须为自动或人工")
	}
	if settings.PriorityMode == "automatic" && (settings.Priority < 10 || settings.Priority > 2147483647) {
		problems = append(problems, "自动 Priority 必须为 10–2147483647 的整数")
	}
	if settings.PriorityMode == "manual" && (settings.Priority < 1 || settings.Priority > 9) {
		problems = append(problems, "人工 Priority 必须为 1–9 的整数")
	}
	if settings.Concurrency < 1 || settings.Concurrency > 1000 {
		problems = append(problems, "并发 concurrency 必须为 1–1000 的整数")
	}
	if settings.Passthrough && platform != "openai" && platform != "anthropic" {
		problems = append(problems, "透传 passthrough 仅支持 OpenAI 或 Anthropic 平台，请关闭透传或更改平台")
	}
	if len(problems) > 0 {
		return ImportAccountSettings{}, importInvalid(ErrorImportSettingsInvalid, strings.Join(problems, "；"))
	}
	return settings, nil
}

func importFailure(message, stage, target, cleanup string, status int, cause error) *ImportFailure {
	if cleanup == "retained" || cleanup == "pending" {
		status = http.StatusConflict
	}
	result := &ImportFailure{Message: message, Stage: stage, Cleanup: cleanup, RetryAllowed: cleanup == "not_needed" || cleanup == "confirmed", StatusCode: status, Cause: cause}
	result.Diagnostics = []ImportDiagnostic{newImportDiagnostic(stage, target, message, cause, "")}
	result.Reason = result.Diagnostics[0].Reason
	if status == http.StatusConflict && cleanup == "not_needed" {
		result.RetryAllowed = false
	}
	var grouped interface{ SafeDeletionGroup() (string, string) }
	if errors.As(cause, &grouped) {
		result.GroupID, result.GroupName = grouped.SafeDeletionGroup()
	}
	return result
}

func importValidationFailure(err error) error {
	var failure *ImportFailure
	if errors.As(err, &failure) {
		return failure
	}
	var local requestError
	message := ErrorRequest
	if errors.As(err, &local) {
		message = local.Error()
	}
	return importFailure(message, "validation", "local", "not_needed", http.StatusBadRequest, err)
}

// The request resolves its workspace exactly once. Successful replays precede
// all mutable cached group and form validation.
func (s *Service) realConnectManaged(ctx context.Context, userID string, req RealConnectRequest) (response RealConnectResponse, resultErr error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return RealConnectResponse{}, importPreflightFailure(err)
	}
	if strings.TrimSpace(workspace) == "" {
		return RealConnectResponse{}, importPreflightFailure(requestError("admin.adminAccounts.errors.noCurrentAccount"))
	}
	var sessionDiagnostic *ImportDiagnostic
	state, err := s.authenticatedStateObserved(ctx, userID, workspace, func(step, target string, cause error) {
		message, detail := ErrorUnknown, "Hub 读取登录资料失败，具体原因无法安全确认；请查看 Hub 后端记录"
		switch step {
		case "refresh_session":
			message, detail = ErrorImportSessionRefreshFailed, ""
		case "verify_admin":
			message, detail = ErrorImportAdminVerificationFailed, ""
		case "save_session":
			message, detail = ErrorImportSessionSaveFailed, "Hub 保存刷新后的登录资料失败，具体数据库原因无法安全确认；请查看 Hub 后端记录"
		}
		d := newImportDiagnostic("validation", target, message, cause, detail)
		sessionDiagnostic = &d
	})
	if err != nil {
		// Identified legacy callers keep their original authentication output.
		// The Sub2API import form supplies settings; its observer preserves causes.
		if req.AccountSettings == nil && sessionDiagnostic == nil {
			return RealConnectResponse{}, err
		}
		failure := importPreflightFailure(err)
		if sessionDiagnostic != nil {
			failure.Diagnostics = []ImportDiagnostic{*sessionDiagnostic}
		}
		return RealConnectResponse{}, failure
	}
	if state.Session.Platform != upstream.PlatformSub2API {
		if req.AccountSettings != nil {
			return RealConnectResponse{}, importValidationFailure(requestError(ErrorImportSettingsUnsupported))
		}
		response, err := s.realConnectLegacy(ctx, userID, req, workspace, state)
		if err == nil {
			response.ConfigurationStatus = "not_applicable"
		}
		return response, err
	}
	req.UpstreamSiteID = strings.TrimSpace(req.UpstreamSiteID)
	req.UpstreamGroupID = strings.TrimSpace(req.UpstreamGroupID)
	if req.UpstreamSiteID == "" {
		return RealConnectResponse{}, importValidationFailure(importInvalid(ErrorRequest, "尚未选择上游站点，请重新选择要导入的上游分组"))
	}
	if req.UpstreamGroupID == "" {
		return RealConnectResponse{}, importValidationFailure(importInvalid(ErrorRequest, "所选上游分组缺少编号，请刷新上游分组并重新选择"))
	}
	operationID, err := normalizeOperationID(req.OperationID)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(importInvalid(ErrorRequest, "本次操作编号长度超过 128 字节，请关闭并重新打开导入窗口"))
	}
	release, err := s.tryBeginConnectionCreation(userID, workspace, req.UpstreamSiteID, req.UpstreamGroupID, operationID)
	if err != nil {
		return RealConnectResponse{}, err
	}
	defer release()
	existing, err := s.idempotentConnection(ctx, userID, workspace, operationID)
	if err != nil {
		return RealConnectResponse{}, importFailure(ErrorImportPersistencePending, "persistence", "local", "pending", http.StatusConflict, err)
	}
	if existing != nil {
		if existing.UserID != userID || existing.WorkspaceAdminAccountID != workspace || existing.UpstreamSiteID != req.UpstreamSiteID || existing.UpstreamGroupID != req.UpstreamGroupID {
			failure := importFailure(ErrorRequest, "validation", "local", "retained", http.StatusConflict, nil)
			return RealConnectResponse{}, failure
		}
		return s.replayImportedConnection(ctx, state, *existing), nil
	}
	connectionCtx, err := s.prepareImportConnectionContext(ctx, userID, workspace, state, req)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(err)
	}
	settings, err := normalizeImportAccountSettings(req.AccountSettings, connectionCtx.groupType)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(err)
	}
	sourceBaseURL, sourcePlatform := connectionCtx.upstreamSite.BaseURL, connectionCtx.upstreamSite.Platform
	ids, names, err := s.resolveImportAdminGroups(ctx, state, req.OwnGroupIDs, connectionCtx.groupType)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(err)
	}
	numericIDs, err := stringsToInts(ids)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(requestError(ErrorRequest))
	}
	if err = s.rejectDuplicateTarget(ctx, userID, workspace, req.UpstreamSiteID, req.UpstreamGroupID, connectionCtx.groupName); err != nil {
		return RealConnectResponse{}, importValidationFailure(err)
	}
	connID, err := randomConnID()
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(importInvalid(ErrorUnknown, "Hub 生成本次关联编号失败，本次未创建账号或 Key；请查看 Hub 后端记录并重试"))
	}
	clock := s.now
	if clock == nil {
		clock = time.Now
	}
	resourceName := fmt.Sprintf("%s%s-%s-%s", randomKeyPrefix(), clock().In(businesstime.Location()).Format("0102"), connectionCtx.upstreamSite.Name, connectionCtx.groupName)
	resourceName = buildImportCredentialName(connectionCtx.upstreamSession.Platform, resourceName, connID)
	credential, err := s.platformService.CreateImportUpstreamCredentialContext(ctx, connectionCtx.upstreamSession, resourceName, req.UpstreamGroupID)
	if err != nil {
		failure := importFailure(ErrorImportUpstreamKeyFailed, "upstream_key", "upstream", "pending", http.StatusBadGateway, err)
		failure.UpstreamKeyID, failure.UpstreamResourceName = credential.ID, resourceName
		switch credential.Outcome {
		case upstream.ImportCredentialNotSent, upstream.ImportCredentialRejected:
			if credential.ID == "" {
				failure.Cleanup, failure.RetryAllowed, failure.StatusCode = "not_needed", true, http.StatusBadGateway
			}
		case upstream.ImportCredentialKnownID:
			s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
		case upstream.ImportCredentialUnknownID:
			failure.Cleanup = "retained"
		}
		return RealConnectResponse{}, failure
	}
	modelResult := importModelResult{}
	defer func() {
		var failure *ImportFailure
		if errors.As(resultErr, &failure) {
			appendImportWarning(failure, modelResult.Warning)
		}
	}()
	stopBeforeCreate := func(cause error) *ImportFailure {
		failure := importFailure(ErrorImportModelSyncFailed, "model_sync", "main", "pending", http.StatusBadGateway, cause)
		failure.UpstreamKeyID, failure.UpstreamResourceName = credential.ID, resourceName
		s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
		return failure
	}
	if err = ctx.Err(); err != nil {
		return RealConnectResponse{}, stopBeforeCreate(err)
	}
	if !settings.Passthrough {
		modelResult.Models, err = s.platformService.PreviewSub2APIImportModelsContext(ctx, state.Session, connectionCtx.groupType, connectionCtx.upstreamSite.BaseURL, credential.Key)
		if parentErr := ctx.Err(); parentErr != nil {
			return RealConnectResponse{}, stopBeforeCreate(parentErr)
		}
		if err != nil {
			d := newImportDiagnostic("model_sync", "main", ErrorImportModelSyncWarning, err, "")
			modelResult = importModelResult{Warning: &d}
		}
	}
	rateLabel := connectionCtx.multiplierLabel
	if rateLabel == "" {
		rateLabel = connectionCtx.groupName
	}
	accountName := fmt.Sprintf("%s-【%s】-%s", groupTypePrefix(connectionCtx.groupType), connectionCtx.upstreamSite.Name, rateLabel)
	payload, err := buildAccountPayload(connectionCtx.groupType, connectionCtx.upstreamSite.BaseURL, credential.Key, numericIDs, accountName, settings, modelResult)
	if err != nil {
		failure := importFailure(ErrorImportModelSyncFailed, "model_sync", "main", "pending", http.StatusBadGateway, err)
		failure.UpstreamKeyID, failure.UpstreamResourceName = credential.ID, resourceName
		s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
		return RealConnectResponse{}, failure
	}
	if err = ctx.Err(); err != nil {
		return RealConnectResponse{}, stopBeforeCreate(err)
	}
	accountID, err := s.platformService.CreateSub2APIImportAccountContext(ctx, state.Session, payload)
	if err != nil {
		failure := importFailure(ErrorImportAccountCreateFailed, "account_create", "main", "pending", http.StatusBadGateway, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		outcome := upstream.RemoteMutationOutcome(err)
		if accountID == "" && (outcome == upstream.MutationNotSent || outcome == upstream.MutationConfirmedRejected) {
			s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
		} else {
			failure.Message = "admin.mySites.errors.accountCreationPendingVerification"
			failure.Cause = &ManagedResourcePendingError{MessageKey: failure.Message, AdminResourceID: accountID, UpstreamKeyID: credential.ID, Cause: err}
		}
		return RealConnectResponse{}, failure
	}
	observed, readErr := s.platformService.ReadSub2APIImportConfigurationContext(ctx, state.Session, accountID)
	if readErr != nil {
		failure := importFailure(ErrorImportConfigurationUnavailable, "configuration_check", "main", "retained", http.StatusBadGateway, readErr)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		return RealConnectResponse{}, failure
	}
	if err = ctx.Err(); err != nil {
		failure := importFailure(ErrorImportConfigurationUnavailable, "configuration_check", "main", "retained", http.StatusConflict, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		return RealConnectResponse{}, failure
	}
	configuration, err := verifyImportedAccountConfiguration(observed, connectionCtx.groupType, settings, ids, modelResult)
	if err != nil {
		failure := importFailure(ErrorImportConfigurationMismatch, "configuration_check", "main", "retained", http.StatusBadGateway, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		s.compensateImport(ctx, userID, connectionCtx, accountID, credential.ID, failure)
		return RealConnectResponse{}, failure
	}
	conn := RealConnection{ID: connID, UserID: userID, WorkspaceAdminAccountID: workspace, UpstreamSiteID: req.UpstreamSiteID, UpstreamGroupID: req.UpstreamGroupID, UpstreamGroupName: connectionCtx.groupName, UpstreamKeyID: credential.ID, UpstreamKey: credential.Key, AdminAccountID: accountID, AdminAccountName: observed.Name, OwnGroupIDs: ids, OwnGroupNames: names, GroupType: connectionCtx.groupType, ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusActive, UpstreamPlatform: string(connectionCtx.upstreamSession.Platform), AdminPlatform: string(state.Session.Platform), PricingMappingEnabled: addToPricingMapping(req.AddToPricingMapping), OperationID: operationID, CanDeleteRemote: true, CreatedAt: time.Now().Format(time.RFC3339)}
	currentSite, sourceErr := s.upstreamLookup.GetSite(ctx, req.UpstreamSiteID)
	sourceDetail := ""
	if sourceErr != nil {
		sourceDetail = "Hub 无法读取当前上游站点资料，具体原因无法确认；账号和 Key 保留，关联未保存，请查看 Hub 后端记录"
	}
	if sourceErr == nil && (currentSite == nil || currentSite.Session == nil || currentSite.UserID != userID || currentSite.AdminAccountID != workspace || currentSite.BaseURL != sourceBaseURL || currentSite.Platform != sourcePlatform || currentSite.Session.Platform != connectionCtx.upstreamSession.Platform || currentSite.Session.BaseURL != connectionCtx.upstreamSession.BaseURL) {
		sourceErr = importInvalid(ErrorRequest, "上游站点、登录来源或所属工作区已变化，账号和 Key 保留，关联未保存；请刷新上游站点并核对已有资源")
	}
	var currentGroup upstream.GroupInfo
	if sourceErr == nil {
		currentGroup, sourceErr = resolveImportSourceGroup(currentSite, req.UpstreamGroupID)
	}
	if sourceErr != nil {
		failure := importFailure(ErrorImportSourceGroupUnavailable, "persistence", "local", "retained", http.StatusConflict, sourceErr)
		if sourceDetail != "" {
			failure.Diagnostics[0] = newImportDiagnostic("persistence", "local", ErrorImportSourceGroupUnavailable, sourceErr, sourceDetail)
		}
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		return RealConnectResponse{}, failure
	}
	conn.UpstreamGroupName = currentGroup.Name
	if err = ctx.Err(); err != nil {
		failure := importFailure(ErrorImportPersistencePending, "persistence", "local", "retained", http.StatusConflict, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		return RealConnectResponse{}, failure
	}
	if err = s.persistImportedConnection(ctx, conn); err != nil {
		failure := importFailure(ErrorImportPersistencePending, "persistence", "local", "retained", http.StatusInternalServerError, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		if connectionCommitOutcome(err) == CommitConfirmedNotCommitted {
			failure.Message = ErrorImportPersistenceFailed
			failure.Diagnostics[0] = newImportDiagnostic("persistence", "local", ErrorImportPersistenceFailed, err, "Hub 已确认本次关联未保存；请按下方清理结果处理账号和 Key，并查看 Hub 后端保存记录")
			s.compensateImport(ctx, userID, connectionCtx, accountID, credential.ID, failure)
		}
		return RealConnectResponse{}, failure
	}
	response = RealConnectResponse{Connection: publicRealConnection(conn), WorkspaceAdminAccountID: workspace, ConfigurationStatus: "confirmed", Configuration: &configuration}
	if modelResult.Warning != nil {
		response.Diagnostics = []ImportDiagnostic{*modelResult.Warning}
	}
	return response, nil
}

func (s *Service) prepareImportConnectionContext(ctx context.Context, userID, workspace string, state *State, req RealConnectRequest) (connectionContext, error) {
	site, err := s.upstreamLookup.GetSite(ctx, req.UpstreamSiteID)
	if err != nil {
		return connectionContext{}, importInvalid(ErrorRequest, "Hub 读取上游站点资料失败，具体原因无法确认；本次未创建资源，请查看 Hub 后端记录")
	}
	if site == nil {
		return connectionContext{}, importInvalid(ErrorRequest, "所选上游站点不存在，请刷新站点列表并重新选择")
	}
	if site.Session == nil {
		return connectionContext{}, importInvalid(ErrorRequest, "所选上游站点缺少登录资料，请重新连接上游站点")
	}
	if site.UserID != userID || site.AdminAccountID != workspace {
		return connectionContext{}, importInvalid(ErrorRequest, "所选上游站点不属于当前用户或工作区，请切换到对应工作区并重新选择")
	}
	group, err := resolveImportSourceGroup(site, req.UpstreamGroupID)
	if err != nil {
		return connectionContext{}, err
	}
	upstreamPlatform, multiplier := resolveGroupInfo([]upstream.GroupInfo{group}, req.UpstreamGroupID)
	platform := strings.ToLower(strings.TrimSpace(req.GroupType))
	if platform == "" {
		platform = upstreamPlatform
	}
	if platform == "" {
		return connectionContext{}, importInvalid(ErrorImportGroupTypeMismatch, "上游分组尚未分类，请手动选择要创建的主站账号平台")
	}
	return connectionContext{adminAccountID: workspace, state: state, upstreamSite: site, upstreamSession: *site.Session, groupType: platform, groupName: group.Name, multiplierLabel: multiplier}, nil
}

func resolveImportSourceGroup(site *upstream.Site, id string) (upstream.GroupInfo, error) {
	var selected upstream.GroupInfo
	matches := 0
	for _, group := range site.Metrics.Groups {
		if group.ID == id {
			selected = group
			matches++
		}
	}
	if matches == 0 {
		return upstream.GroupInfo{}, importInvalid(ErrorRequest, "所选上游分组编号已不存在，请刷新上游分组并重新选择")
	}
	if matches != 1 {
		return upstream.GroupInfo{}, importInvalid(ErrorRequest, "所选上游分组编号对应多条记录，无法确定唯一来源，请刷新上游分组并核对记录")
	}
	if strings.TrimSpace(selected.Name) == "" {
		return upstream.GroupInfo{}, importInvalid(ErrorRequest, "所选上游分组名称为空，请刷新上游分组并重新选择")
	}
	return selected, nil
}

func buildImportCredentialName(platform upstream.Platform, readableName, connID string) string {
	if platform != upstream.PlatformNewAPI {
		return readableName
	}
	if len(readableName) > 17 {
		end := 17
		for end > 0 && !utf8.RuneStart(readableName[end]) {
			end--
		}
		readableName = readableName[:end]
	}
	return readableName + "-" + connID
}

func (s *Service) resolveImportAdminGroups(ctx context.Context, state *State, requestedIDs []string, platform string) ([]string, []string, error) {
	if len(requestedIDs) == 0 {
		return nil, nil, importInvalid(ErrorRequest, "尚未选择主站目标分组，请选择至少一个同平台分组")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	groups, err := s.platformService.FetchAdminAllGroupsContext(ctx, state.Session)
	if err != nil {
		return nil, nil, importFailure(ErrorImportGroupsLoadFailed, "validation", "main", "not_needed", http.StatusBadGateway, err)
	}
	byID := map[string]upstream.AdminGroupInfo{}
	for _, group := range groups {
		byID[group.ID] = group
	}
	ids, names := []string{}, []string{}
	seen := map[string]bool{}
	for _, requestedID := range requestedIDs {
		requestedID = strings.TrimSpace(requestedID)
		id, err := strconv.ParseInt(requestedID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != requestedID {
			return nil, nil, importInvalid(ErrorRequest, "所选主站分组编号格式无效，请刷新主站分组并重新选择")
		}
		group, ok := byID[requestedID]
		if !ok {
			return nil, nil, importInvalid(ErrorRequest, "主站目标分组编号 "+requestedID+" 已不存在，请刷新主站分组并重新选择")
		}
		if strings.ToLower(strings.TrimSpace(group.Platform)) != platform {
			return nil, nil, importInvalid(ErrorImportGroupTypeMismatch, "主站目标分组编号 "+requestedID+" 的平台与所选平台不符，请重新选择同平台分组")
		}
		if !seen[requestedID] {
			seen[requestedID] = true
			ids = append(ids, requestedID)
			names = append(names, group.Name)
		}
	}
	return ids, names, nil
}

func buildAccountPayload(platform, baseURL, key string, groups []int, name string, settings ImportAccountSettings, result importModelResult) (map[string]any, error) {
	if !result.valid(settings.Passthrough) {
		return nil, importInvalid(ErrorImportModelSyncFailed, "本次模型同步结果不一致，未创建主站账号")
	}
	models := result.Models
	credentials := map[string]any{"base_url": baseURL, "api_key": key, "pool_mode": settings.PoolMode}
	if platform == "gemini" {
		credentials["tier_id"] = "aistudio_free"
	}
	if !settings.Passthrough {
		mapping := map[string]string{}
		for _, model := range models {
			if model == "" || model != strings.TrimSpace(model) || strings.Contains(model, "*") {
				return nil, requestError(ErrorImportModelSyncFailed)
			}
			mapping[model] = model
		}
		credentials["model_mapping"] = mapping
	}
	payload := map[string]any{"name": name, "platform": platform, "type": "apikey", "credentials": credentials, "priority": settings.Priority, "concurrency": settings.Concurrency, "group_ids": groups, "upstream_billing_probe_enabled": settings.UpstreamBillingProbeEnabled}
	if platform == "openai" || platform == "anthropic" {
		payload["extra"] = map[string]any{platform + "_passthrough": settings.Passthrough}
	}
	return payload, nil
}

func configurationSummary(observed upstream.Sub2APIImportConfiguration, observation string) ImportedAccountConfiguration {
	configuration := ImportedAccountConfiguration{Observation: observation, AdminAccountID: observed.ID, Name: observed.Name, Platform: observed.Platform, Priority: observed.Priority, Concurrency: observed.Concurrency, Passthrough: observed.Passthrough, PoolMode: observed.PoolMode, UpstreamBillingProbeEnabled: observed.UpstreamBillingProbeEnabled, OwnGroups: []ImportOwnGroup{}, Models: []string{}}
	for _, group := range observed.Groups {
		configuration.OwnGroups = append(configuration.OwnGroups, ImportOwnGroup{ID: group.ID, Name: group.Name})
	}
	if observed.Passthrough {
		configuration.ModelState = "not_required"
	} else {
		for model := range observed.ModelMapping {
			configuration.Models = append(configuration.Models, model)
		}
		sort.Strings(configuration.Models)
		if observation == "creation" {
			if len(configuration.Models) > 0 {
				configuration.ModelState = "synced"
			} else {
				configuration.ModelState = "not_synced"
			}
		} else if len(configuration.Models) > 0 {
			configuration.ModelState = "current_whitelist"
		} else {
			configuration.ModelState = "current_unrestricted"
		}
	}
	return configuration
}

func verifyImportedAccountConfiguration(observed upstream.Sub2APIImportConfiguration, platform string, settings ImportAccountSettings, groups []string, result importModelResult) (ImportedAccountConfiguration, error) {
	if !result.valid(settings.Passthrough) {
		return ImportedAccountConfiguration{}, importInvalid(ErrorImportConfigurationMismatch, "本次模型同步结果不一致，未保存关联")
	}
	models := result.Models
	actualGroups := make([]string, 0, len(observed.Groups))
	for _, group := range observed.Groups {
		actualGroups = append(actualGroups, group.ID)
	}
	differences := []string{}
	if observed.Platform != platform {
		differences = append(differences, "platform 平台要求 "+platform+"，实际 "+safeImportPlatform(observed.Platform))
	}
	if observed.Type != "apikey" {
		actualType := "无效或不支持的类型"
		if observed.Type == "oauth" || observed.Type == "setup-token" {
			actualType = observed.Type
		}
		differences = append(differences, "type 账号类型要求 apikey，实际 "+actualType)
	}
	for _, field := range []struct {
		name             string
		expected, actual any
	}{
		{"Priority", settings.Priority, observed.Priority}, {"concurrency", settings.Concurrency, observed.Concurrency},
		{"passthrough", settings.Passthrough, observed.Passthrough}, {"poolMode", settings.PoolMode, observed.PoolMode},
		{"upstreamBillingProbeEnabled", settings.UpstreamBillingProbeEnabled, observed.UpstreamBillingProbeEnabled},
		{"upstreamBillingRateSyncEnabled", false, observed.UpstreamBillingRateSyncEnabled},
	} {
		if field.expected != field.actual {
			differences = append(differences, fmt.Sprintf("%s 要求 %v，实际 %v", field.name, field.expected, field.actual))
		}
	}
	if !equalImportIDSets(actualGroups, groups) {
		differences = append(differences, fmt.Sprintf("目标分组要求编号 %s，实际编号 %s", strings.Join(groups, ","), strings.Join(actualGroups, ",")))
	}
	if !settings.Passthrough {
		matches := len(observed.ModelMapping) == len(models)
		for _, model := range models {
			if observed.ModelMapping[model] != model {
				matches = false
			}
		}
		if !matches {
			if result.Warning != nil {
				differences = append(differences, "模型映射要求为空，实际并非空映射")
			} else {
				differences = append(differences, "模型映射与同步后的逐项同名白名单不符，存在遗漏、增加或改名")
			}
		}
	}
	if len(differences) > 0 {
		return ImportedAccountConfiguration{}, importInvalid(ErrorImportConfigurationMismatch, strings.Join(differences, "；")+"；请到主站检查账号设置和保留资源")
	}
	return configurationSummary(observed, "creation"), nil
}

func equalImportIDSets(a, b []string) bool {
	left, right := map[string]bool{}, map[string]bool{}
	for _, id := range a {
		left[id] = true
	}
	for _, id := range b {
		right[id] = true
	}
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if !right[id] {
			return false
		}
	}
	return true
}

func (s *Service) replayImportedConnection(ctx context.Context, state *State, conn RealConnection) RealConnectResponse {
	response := RealConnectResponse{Connection: publicRealConnection(conn), WorkspaceAdminAccountID: conn.WorkspaceAdminAccountID, ConfigurationStatus: "unavailable", Message: ErrorImportConfigurationUnavailable}
	observed, err := s.platformService.ReadSub2APIImportConfigurationContext(ctx, state.Session, conn.AdminAccountID)
	if err != nil {
		response.Diagnostics = []ImportDiagnostic{newImportDiagnostic("configuration_check", "main", ErrorImportConfigurationUnavailable, err, "")}
		return response
	}
	configuration := configurationSummary(observed, "current")
	response.ConfigurationStatus, response.Configuration, response.Message = "confirmed", &configuration, ""
	return response
}

func (s *Service) cleanupImportKey(ctx context.Context, connectionCtx connectionContext, keyID string, failure *ImportFailure) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	cleanup, err := s.platformService.DeleteImportUpstreamCredentialContext(cleanupCtx, connectionCtx.upstreamSession, keyID)
	failure.Cleanup = cleanup
	failure.RetryAllowed = cleanup == "confirmed"
	if cleanup != "confirmed" {
		failure.StatusCode = http.StatusConflict
	} else {
		switch failure.Stage {
		case "persistence":
			failure.StatusCode = http.StatusInternalServerError
		default:
			failure.StatusCode = http.StatusBadGateway
		}
	}
	if err != nil {
		failure.Diagnostics = append(failure.Diagnostics, newImportDiagnostic("key_cleanup", "upstream", "admin.mySites.errors.upstreamKeyCleanupPendingVerification", err, ""))
		failure.Cause = errors.Join(failure.Cause, err)
	}
}

func (s *Service) compensateImport(ctx context.Context, userID string, connectionCtx connectionContext, accountID, keyID string, failure *ImportFailure) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := s.safeDeleteAdminResource(cleanupCtx, userID, connectionCtx.adminAccountID, connectionCtx.state.Session, accountID, "compensate_delete")
	if err != nil {
		failure.Diagnostics = append(failure.Diagnostics, newImportDiagnostic("account_cleanup", "main", "admin.mySites.errors.compensationPendingVerification", err, ""))
		failure.Message = "admin.mySites.errors.compensationPendingVerification"
		failure.Cleanup, failure.RetryAllowed, failure.StatusCode = "retained", false, http.StatusConflict
		if upstream.RemoteMutationOutcome(err) == upstream.MutationUncertain {
			failure.Cleanup = "pending"
		}
		failure.Cause = &ManagedResourcePendingError{MessageKey: failure.Message, AdminResourceID: accountID, UpstreamKeyID: keyID, Cause: errors.Join(failure.Cause, err)}
		failure.Reason = safeManagedDeleteReason(err)
		var grouped interface{ SafeDeletionGroup() (string, string) }
		if errors.As(err, &grouped) {
			failure.GroupID, failure.GroupName = grouped.SafeDeletionGroup()
		}
		return
	}
	s.cleanupImportKey(ctx, connectionCtx, keyID, failure)
	if failure.Cleanup != "confirmed" {
		failure.Message = "admin.mySites.errors.upstreamKeyCleanupPendingVerification"
	}
}

func (s *Service) persistImportedConnection(ctx context.Context, conn RealConnection) error {
	err := s.persistConnection(ctx, conn)
	if err == nil || connectionCommitOutcome(err) == CommitConfirmedNotCommitted {
		return err
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if s.connRepository == nil {
		return err
	}
	stored, checkErr := s.connRepository.GetRealConnection(checkCtx, conn.ID, conn.UserID, conn.WorkspaceAdminAccountID)
	if checkErr == nil && stored != nil && stored.UserID == conn.UserID && stored.WorkspaceAdminAccountID == conn.WorkspaceAdminAccountID && stored.ID == conn.ID && (conn.OperationID == "" || stored.OperationID == conn.OperationID) && stored.UpstreamSiteID == conn.UpstreamSiteID && stored.UpstreamGroupID == conn.UpstreamGroupID && stored.AdminAccountID == conn.AdminAccountID && stored.UpstreamKeyID == conn.UpstreamKeyID && equalImportIDSets(stored.OwnGroupIDs, conn.OwnGroupIDs) && stored.PricingMappingEnabled == conn.PricingMappingEnabled && stored.Status == ConnectionStatusActive {
		return nil
	}
	return err
}

type importModelResult struct {
	Models  []string
	Warning *ImportDiagnostic
}

func (r importModelResult) valid(passthrough bool) bool {
	if passthrough {
		return len(r.Models) == 0 && r.Warning == nil
	}
	if r.Warning != nil {
		return len(r.Models) == 0 && r.Warning.Stage == "model_sync" && r.Warning.Target == "main" && r.Warning.Message == ErrorImportModelSyncWarning
	}
	if len(r.Models) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, model := range r.Models {
		if model == "" || model != strings.TrimSpace(model) || strings.Contains(model, "*") || seen[model] {
			return false
		}
		seen[model] = true
	}
	return true
}

func importPreflightFailure(err error) *ImportFailure {
	var failure *ImportFailure
	if errors.As(err, &failure) {
		return failure
	}
	message, status := ErrorUnknown, http.StatusInternalServerError
	var local requestError
	if errors.As(err, &local) {
		message, status = local.Error(), http.StatusBadRequest
		switch message {
		case ErrorAuthRequired:
			status = http.StatusUnauthorized
		case ErrorAdminOnly:
			status = http.StatusForbidden
		case "admin.adminAccounts.errors.noCurrentAccount", ErrorConnectionExists, ErrorManagedDeleteOnly:
			status = http.StatusConflict
		}
	}
	return importFailure(message, "validation", "local", "not_needed", status, err)
}

func safeImportPlatform(platform string) string {
	switch platform {
	case "openai", "anthropic", "gemini", "antigravity", "grok", "kimi", "zhipu", "deepseek", "minimax":
		return platform
	}
	return "无效或不支持的平台"
}
