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

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

const (
	ErrorImportSettingsInvalid          = "admin.mySites.errors.importSettingsInvalid"
	ErrorImportGroupTypeMismatch        = "admin.mySites.errors.importGroupTypeMismatch"
	ErrorImportSettingsUnsupported      = "admin.mySites.errors.importSettingsUnsupported"
	ErrorImportUpstreamKeyFailed        = "admin.mySites.errors.importUpstreamKeyFailed"
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
	case "openai", "anthropic", "gemini", "antigravity":
	default:
		return ImportAccountSettings{}, requestError(ErrorImportGroupTypeMismatch)
	}
	settings := ImportAccountSettings{PriorityMode: "automatic", Priority: 100, Concurrency: 50, PoolMode: true, UpstreamBillingProbeEnabled: true}
	if req != nil {
		if req.invalidNull {
			return ImportAccountSettings{}, requestError(ErrorImportSettingsInvalid)
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
	if settings.PriorityMode != "automatic" && settings.PriorityMode != "manual" || settings.PriorityMode == "automatic" && (settings.Priority < 10 || settings.Priority > 2147483647) || settings.PriorityMode == "manual" && (settings.Priority < 1 || settings.Priority > 9) || settings.Concurrency < 1 || settings.Concurrency > 1000 || settings.Passthrough && platform != "openai" && platform != "anthropic" || strings.TrimSpace(platform) == "" {
		return ImportAccountSettings{}, requestError(ErrorImportSettingsInvalid)
	}
	return settings, nil
}

func importFailure(message, stage, cleanup string, status int, cause error) *ImportFailure {
	if cleanup == "retained" || cleanup == "pending" {
		status = http.StatusConflict
	}
	result := &ImportFailure{Message: message, Stage: stage, Cleanup: cleanup, RetryAllowed: cleanup == "not_needed" || cleanup == "confirmed", StatusCode: status, Cause: cause}
	result.Reason = safeManagedDeleteReason(cause)
	if result.Reason == "" {
		var remote *upstream.RequestError
		if errors.As(cause, &remote) {
			for _, key := range []string{upstream.ErrorAuth, upstream.ErrorForbidden, upstream.ErrorNotFound, upstream.ErrorRateLimited, upstream.ErrorUpstreamServer, upstream.ErrorNetworkTimeout, upstream.ErrorNetworkUnreachable, upstream.ErrorTLSFailed, upstream.ErrorInvalidResponse, upstream.ErrorBusinessRejected} {
				if remote.Reason == key || remote.Reason == "" && remote.MessageKey == key {
					result.Reason = key
					break
				}
			}
		}
	}
	var grouped interface{ SafeDeletionGroup() (string, string) }
	if errors.As(cause, &grouped) {
		result.GroupID, result.GroupName = grouped.SafeDeletionGroup()
	}
	return result
}

func importValidationFailure(err error) error {
	var local requestError
	message := ErrorRequest
	if errors.As(err, &local) {
		message = local.Error()
	}
	return importFailure(message, "validation", "not_needed", http.StatusBadRequest, err)
}

// The request resolves its workspace exactly once. Successful replays precede
// all mutable cached group and form validation.
func (s *Service) realConnectManaged(ctx context.Context, userID string, req RealConnectRequest) (RealConnectResponse, error) {
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return RealConnectResponse{}, err
	}
	state, err := s.authenticatedState(ctx, userID, workspace)
	if err != nil {
		return RealConnectResponse{}, err
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
	if req.UpstreamSiteID == "" || req.UpstreamGroupID == "" {
		return RealConnectResponse{}, importValidationFailure(requestError(ErrorRequest))
	}
	operationID, err := normalizeOperationID(req.OperationID)
	if err != nil {
		return RealConnectResponse{}, importValidationFailure(err)
	}
	existing, err := s.idempotentConnection(ctx, userID, workspace, operationID)
	if err != nil {
		return RealConnectResponse{}, importFailure(ErrorImportPersistencePending, "persistence", "pending", http.StatusConflict, err)
	}
	if existing != nil {
		if existing.UserID != userID || existing.WorkspaceAdminAccountID != workspace || existing.UpstreamSiteID != req.UpstreamSiteID || existing.UpstreamGroupID != req.UpstreamGroupID {
			failure := importFailure(ErrorRequest, "validation", "retained", http.StatusConflict, nil)
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
		return RealConnectResponse{}, importValidationFailure(requestError(ErrorUnknown))
	}
	clock := s.now
	if clock == nil {
		clock = time.Now
	}
	resourceName := fmt.Sprintf("%s%s-%s-%s", randomKeyPrefix(), clock().In(businesstime.Location()).Format("0102"), connectionCtx.upstreamSite.Name, connectionCtx.groupName)
	credential, err := s.platformService.CreateImportUpstreamCredentialContext(ctx, connectionCtx.upstreamSession, resourceName, req.UpstreamGroupID)
	if err != nil {
		failure := importFailure(ErrorImportUpstreamKeyFailed, "upstream_key", "pending", http.StatusBadGateway, err)
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
	models := []string{}
	if !settings.Passthrough {
		models, err = s.platformService.PreviewSub2APIImportModelsContext(ctx, state.Session, connectionCtx.groupType, connectionCtx.upstreamSite.BaseURL, credential.Key)
		if err != nil {
			failure := importFailure(ErrorImportModelSyncFailed, "model_sync", "pending", http.StatusBadGateway, err)
			failure.UpstreamKeyID, failure.UpstreamResourceName = credential.ID, resourceName
			s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
			return RealConnectResponse{}, failure
		}
	}
	rateLabel := connectionCtx.multiplierLabel
	if rateLabel == "" {
		rateLabel = connectionCtx.groupName
	}
	accountName := fmt.Sprintf("%s-【%s】-%s", groupTypePrefix(connectionCtx.groupType), connectionCtx.upstreamSite.Name, rateLabel)
	payload, err := buildAccountPayload(connectionCtx.groupType, connectionCtx.upstreamSite.BaseURL, credential.Key, numericIDs, accountName, settings, models)
	if err != nil {
		failure := importFailure(ErrorImportModelSyncFailed, "model_sync", "pending", http.StatusBadGateway, err)
		failure.UpstreamKeyID, failure.UpstreamResourceName = credential.ID, resourceName
		s.cleanupImportKey(ctx, connectionCtx, credential.ID, failure)
		return RealConnectResponse{}, failure
	}
	accountID, err := s.platformService.CreateSub2APIImportAccountContext(ctx, state.Session, payload)
	if err != nil {
		failure := importFailure(ErrorImportAccountCreateFailed, "account_create", "pending", http.StatusBadGateway, err)
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
		failure := importFailure(ErrorImportConfigurationUnavailable, "configuration_check", "retained", http.StatusBadGateway, readErr)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		return RealConnectResponse{}, failure
	}
	configuration, err := verifyImportedAccountConfiguration(observed, connectionCtx.groupType, settings, ids, models)
	if err != nil {
		failure := importFailure(ErrorImportConfigurationMismatch, "configuration_check", "retained", http.StatusBadGateway, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		s.compensateImport(ctx, userID, connectionCtx, accountID, credential.ID, failure)
		return RealConnectResponse{}, failure
	}
	conn := RealConnection{ID: connID, UserID: userID, WorkspaceAdminAccountID: workspace, UpstreamSiteID: req.UpstreamSiteID, UpstreamGroupID: req.UpstreamGroupID, UpstreamGroupName: connectionCtx.groupName, UpstreamKeyID: credential.ID, UpstreamKey: credential.Key, AdminAccountID: accountID, AdminAccountName: observed.Name, OwnGroupIDs: ids, OwnGroupNames: names, GroupType: connectionCtx.groupType, ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusActive, UpstreamPlatform: string(connectionCtx.upstreamSession.Platform), AdminPlatform: string(state.Session.Platform), PricingMappingEnabled: addToPricingMapping(req.AddToPricingMapping), OperationID: operationID, CanDeleteRemote: true, CreatedAt: time.Now().Format(time.RFC3339)}
	if err = s.persistImportedConnection(ctx, conn); err != nil {
		failure := importFailure(ErrorImportPersistencePending, "persistence", "retained", http.StatusInternalServerError, err)
		failure.AdminResourceID, failure.UpstreamKeyID, failure.UpstreamResourceName = accountID, credential.ID, resourceName
		if connectionCommitOutcome(err) == CommitConfirmedNotCommitted {
			failure.Message = ErrorImportPersistenceFailed
			s.compensateImport(ctx, userID, connectionCtx, accountID, credential.ID, failure)
		}
		return RealConnectResponse{}, failure
	}
	return RealConnectResponse{Connection: publicRealConnection(conn), WorkspaceAdminAccountID: workspace, ConfigurationStatus: "confirmed", Configuration: &configuration}, nil
}

func (s *Service) prepareImportConnectionContext(ctx context.Context, userID, workspace string, state *State, req RealConnectRequest) (connectionContext, error) {
	site, err := s.upstreamLookup.GetSite(ctx, req.UpstreamSiteID)
	if err != nil || site == nil || site.Session == nil || site.UserID != userID || site.AdminAccountID != workspace {
		return connectionContext{}, requestError(ErrorRequest)
	}
	platform, multiplier := resolveGroupInfo(site.Metrics.Groups, req.UpstreamGroupID)
	requested := strings.ToLower(strings.TrimSpace(req.GroupType))
	if platform != "" && requested != "" && platform != requested {
		return connectionContext{}, requestError(ErrorImportGroupTypeMismatch)
	}
	if platform == "" {
		platform = requested
	}
	if platform == "" {
		return connectionContext{}, requestError(ErrorImportGroupTypeMismatch)
	}
	name := strings.TrimSpace(req.UpstreamGroupName)
	if name == "" {
		name = req.UpstreamGroupID
	}
	return connectionContext{adminAccountID: workspace, state: state, upstreamSite: site, upstreamSession: *site.Session, groupType: platform, groupName: name, multiplierLabel: multiplier}, nil
}

func (s *Service) resolveImportAdminGroups(ctx context.Context, state *State, requestedIDs []string, platform string) ([]string, []string, error) {
	if len(requestedIDs) == 0 {
		return nil, nil, requestError(ErrorRequest)
	}
	groups, err := s.platformService.FetchAdminAllGroups(state.Session)
	if err != nil {
		return nil, nil, requestError(ErrorRequest)
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
			return nil, nil, requestError(ErrorRequest)
		}
		group, ok := byID[requestedID]
		if !ok {
			return nil, nil, requestError(ErrorRequest)
		}
		if group.Platform != platform {
			return nil, nil, requestError(ErrorImportGroupTypeMismatch)
		}
		if !seen[requestedID] {
			seen[requestedID] = true
			ids = append(ids, requestedID)
			names = append(names, group.Name)
		}
	}
	return ids, names, nil
}

func buildAccountPayload(platform, baseURL, key string, groups []int, name string, settings ImportAccountSettings, models []string) (map[string]any, error) {
	credentials := map[string]any{"base_url": baseURL, "api_key": key, "pool_mode": settings.PoolMode}
	if platform == "gemini" {
		credentials["tier_id"] = "aistudio_free"
	}
	if !settings.Passthrough {
		if len(models) == 0 {
			return nil, requestError(ErrorImportModelSyncFailed)
		}
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
			configuration.ModelState = "synced"
		} else if len(configuration.Models) > 0 {
			configuration.ModelState = "current_whitelist"
		} else {
			configuration.ModelState = "current_unrestricted"
		}
	}
	return configuration
}

func verifyImportedAccountConfiguration(observed upstream.Sub2APIImportConfiguration, platform string, settings ImportAccountSettings, groups, models []string) (ImportedAccountConfiguration, error) {
	actualGroups := make([]string, 0, len(observed.Groups))
	for _, group := range observed.Groups {
		actualGroups = append(actualGroups, group.ID)
	}
	if observed.Platform != platform || observed.Type != "apikey" || observed.Priority != settings.Priority || observed.Concurrency != settings.Concurrency || observed.Passthrough != settings.Passthrough || observed.PoolMode != settings.PoolMode || observed.UpstreamBillingProbeEnabled != settings.UpstreamBillingProbeEnabled || observed.UpstreamBillingRateSyncEnabled || !equalImportIDSets(actualGroups, groups) {
		return ImportedAccountConfiguration{}, requestError(ErrorImportConfigurationMismatch)
	}
	if !settings.Passthrough {
		if len(models) == 0 || len(observed.ModelMapping) != len(models) {
			return ImportedAccountConfiguration{}, requestError(ErrorImportConfigurationMismatch)
		}
		for _, model := range models {
			if observed.ModelMapping[model] != model {
				return ImportedAccountConfiguration{}, requestError(ErrorImportConfigurationMismatch)
			}
		}
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
		failure.Cause = errors.Join(failure.Cause, err)
	}
}

func (s *Service) compensateImport(ctx context.Context, userID string, connectionCtx connectionContext, accountID, keyID string, failure *ImportFailure) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := s.safeDeleteAdminResource(cleanupCtx, userID, connectionCtx.adminAccountID, connectionCtx.state.Session, accountID, "compensate_delete")
	if err != nil {
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
