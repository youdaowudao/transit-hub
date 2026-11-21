package connection_health

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type actionConfirmReason string

func (r actionConfirmReason) Error() string { return string(r) }

const (
	actionConfirmChanged         actionConfirmReason = "changed"
	actionConfirmOriginalRunning actionConfirmReason = "original_running"
	actionConfirmMultiple        actionConfirmReason = "multiple_pending"
	actionConfirmReconcileFailed actionConfirmReason = "reconcile_failed"
)

type remoteActionCurrent struct {
	DispatchID    string     `json:"dispatchId,omitempty"`
	PendingValue  string     `json:"pendingValue,omitempty"`
	CurrentValue  string     `json:"currentValue"`
	ObservedAt    *time.Time `json:"observedAt,omitempty"`
	RecordedAt    *time.Time `json:"recordedAt,omitempty"`
	WillMarkAs    string     `json:"willMarkAs,omitempty"`
	Source        string     `json:"source,omitempty"`
	CanConfirm    bool       `json:"canConfirm"`
	BlockedReason string     `json:"blockedReason,omitempty"`
	Detail        string     `json:"detail,omitempty"`
	observation   RemoteActionObservation
}

type remoteActionConfirmInput struct {
	DispatchID   string `json:"dispatchId"`
	CurrentValue string `json:"currentValue"`
}

type remoteActionConfirmProblem struct {
	Message       string `json:"message"`
	BlockedReason string `json:"blockedReason"`
	CurrentValue  string `json:"currentValue,omitempty"`
	Detail        string `json:"detail,omitempty"`
	status        int
}

func (p *remoteActionConfirmProblem) Error() string { return p.BlockedReason }

func actionConfirmProblem(status int, reason, detail string) *remoteActionConfirmProblem {
	return &remoteActionConfirmProblem{status: status, Message: reason, BlockedReason: reason, Detail: detail}
}

type remoteActionStoredSessionReader interface {
	StoredSession(context.Context, string, string) (upstream.Session, bool, error)
}

type remoteActionStatusAccountReader interface {
	ListSub2APIAdminAccountsForStatusCheck(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error)
}

type remoteActionConfirmRepository interface {
	RemoteActionOwnerRunning(context.Context, string) (bool, error)
	ResolveUncertainAction(context.Context, RemoteActionClaim, RemoteActionObservation) (RemoteActionCheckpoints, error)
}

func normalizedRemoteActionStatus(value string) string {
	switch value {
	case "active", "1":
		return "active"
	case "inactive", "0", "disabled":
		return "inactive"
	default:
		return ""
	}
}

func remoteActionConfirmationPhase(pending, current string) RemoteDispatchPhase {
	pending, current = normalizedRemoteActionStatus(pending), normalizedRemoteActionStatus(current)
	if pending == "" || current == "" {
		return ""
	}
	if pending == current {
		return DispatchConfirmedApplied
	}
	return DispatchConfirmedRejected
}

func (s *Service) actionConfirmationWorkspace(ctx context.Context, userID string) (string, error) {
	if reader, ok := s.accounts.(accountTierWorkspaceReader); ok {
		account, err := reader.Current(ctx, userID)
		if err != nil {
			return "", err
		}
		if account == nil || account.ID == "" {
			return "", requestError(ErrorNoCurrentAccount)
		}
		if account.Platform != string(upstream.PlatformSub2API) {
			return "", actionConfirmProblem(http.StatusBadRequest, "unsupported_platform", "当前工作区的主站平台不支持此操作，请切换到 Sub2API 工作区后再试。")
		}
		return account.ID, nil
	}
	return s.currentAdminAccountID(ctx, userID)
}

func (s *Service) readRemoteActionCurrent(ctx context.Context, userID, accountID string) (remoteActionCurrent, error) {
	var view remoteActionCurrent
	workspace, err := s.actionConfirmationWorkspace(ctx, userID)
	if err != nil {
		return view, err
	}
	sessions, ok := s.mySites.(remoteActionStoredSessionReader)
	if !ok {
		view.BlockedReason = "session_unavailable"
		return view, nil
	}
	session, available, err := sessions.StoredSession(ctx, userID, workspace)
	if err != nil || !available || !session.IsAuthenticated() {
		view.BlockedReason = "session_unavailable"
		return view, nil
	}
	if session.Platform != upstream.PlatformSub2API {
		return view, actionConfirmProblem(http.StatusBadRequest, "unsupported_platform", "当前工作区保存的主站登录资料不属于 Sub2API，请在主站管理核对登录后再试。")
	}

	priorities, err := s.repo.ListPrioritySyncStates(ctx, userID, workspace)
	if err != nil {
		return view, actionConfirmProblem(http.StatusInternalServerError, "read_failed", "未能读取这个账号的 Priority 待确认记录，本次没有做任何修改。请稍后重试，仍失败时请查看 TransitHub 后端日志。")
	}
	targets, err := s.repo.ListTargetActionStates(ctx, userID, workspace)
	if err != nil {
		return view, actionConfirmProblem(http.StatusInternalServerError, "read_failed", "未能读取这个账号的待确认修改，本次没有做任何修改。请稍后重试，仍失败时请查看 TransitHub 后端日志。")
	}
	checkpoints := s.actionCheckpointsByAccount(userID, workspace, priorities, targets)[accountID]
	var target *TargetActionState
	pending := 0
	for _, state := range checkpoints.priorities {
		if priorityActionPending(state) {
			pending++
		}
	}
	for _, state := range checkpoints.targets {
		if targetActionPending(state) {
			pending++
			target = state
		}
	}
	switch {
	case pending == 0:
		view.BlockedReason = "none"
	case pending > 1:
		view.BlockedReason = string(actionConfirmMultiple)
	case target == nil || target.PendingActionKind != TargetMutationStatus:
		view.BlockedReason = "unsupported_action"
	case target.TargetID != buildTargetID(string(upstream.PlatformSub2API), workspace, accountID):
		view.BlockedReason = "legacy_alias"
	case target.PendingDispatchPhase != DispatchUncertain:
		view.BlockedReason = "not_uncertain"
	}
	if view.BlockedReason != "" {
		return view, nil
	}

	view.DispatchID, view.RecordedAt, view.Source = target.PendingDispatchID, &target.UpdatedAt, target.PendingSource
	if !manualTargetAction(target) {
		view.Source = StrategyModeHealthProbe
	}
	view.PendingValue = normalizedRemoteActionStatus(target.PendingStatus)
	repository, ok := s.repo.(remoteActionConfirmRepository)
	const ownerReadFailure = "未能核查这个账号的原操作是否仍在进行，本次没有做任何修改。请稍后重试，仍失败时请查看 TransitHub 后端日志。"
	if !ok {
		return view, actionConfirmProblem(http.StatusInternalServerError, "read_failed", ownerReadFailure)
	}
	running, err := repository.RemoteActionOwnerRunning(ctx, target.PendingOwnerID)
	if err != nil {
		return view, actionConfirmProblem(http.StatusInternalServerError, "read_failed", ownerReadFailure)
	}
	if running {
		view.BlockedReason = string(actionConfirmOriginalRunning)
		return view, nil
	}
	reader, ok := s.platformGroups.(remoteActionStatusAccountReader)
	if !ok {
		view.BlockedReason, view.Detail = "main_site_unavailable", "当前无法读取 Sub2API 账号清单，请查看 TransitHub 后端日志"
		return view, nil
	}
	readStartedAt := s.actionTime()
	accounts, err := reader.ListSub2APIAdminAccountsForStatusCheck(ctx, session)
	if err != nil {
		view.BlockedReason = "main_site_unavailable"
		view.Detail = modelControlSafeFailureCause(err, session)
		return view, nil
	}
	view.BlockedReason = "account_not_found"
	for _, account := range accounts {
		if strings.TrimSpace(account.ID) != accountID {
			continue
		}
		view.ObservedAt = &readStartedAt
		view.CurrentValue = normalizedRemoteActionStatus(account.Status)
		if view.CurrentValue == "" {
			view.BlockedReason = "main_site_status_unknown"
			view.CurrentValue = upstream.SafeSub2APIAccountStatusForDisplay(account.Status, session)
			return view, nil
		}
		phase := remoteActionConfirmationPhase(view.PendingValue, view.CurrentValue)
		if phase == "" || view.DispatchID == "" {
			view.BlockedReason = string(actionConfirmReconcileFailed)
			return view, nil
		}
		view.WillMarkAs = "rejected"
		if phase == DispatchConfirmedApplied {
			view.WillMarkAs = "applied"
		}
		view.CanConfirm = true
		view.BlockedReason = ""
		view.observation = RemoteActionObservation{
			RemoteActionScope: RemoteActionScope{userID, workspace, target.TargetID},
			InventoryComplete: true, Visible: true, SnapshotStartedAt: readStartedAt, Status: view.CurrentValue,
		}
		return view, nil
	}
	return view, nil
}

func (s *Service) confirmRemoteActionCurrent(ctx context.Context, userID, accountID string, input remoteActionConfirmInput) (resultErr error) {
	view, err := s.readRemoteActionCurrent(ctx, userID, accountID)
	defer func() {
		result := "resolved"
		var problem *remoteActionConfirmProblem
		if errors.As(resultErr, &problem) {
			result = problem.BlockedReason
		} else if resultErr != nil {
			result = "read_failed"
		}
		phase := remoteActionConfirmationPhase(view.PendingValue, view.observation.Status)
		log.Printf("[connection-health] confirm remote action account_id=%q pending=%q current=%q phase=%q result=%s", accountID, view.PendingValue, view.observation.Status, phase, result)
	}()
	if err != nil {
		return err
	}
	if !view.CanConfirm {
		problem := actionConfirmProblem(http.StatusConflict, view.BlockedReason, view.Detail)
		problem.CurrentValue = view.CurrentValue
		return problem
	}
	if view.DispatchID != input.DispatchID {
		return actionConfirmProblem(http.StatusConflict, string(actionConfirmChanged), "")
	}
	if view.CurrentValue != normalizedRemoteActionStatus(input.CurrentValue) {
		return actionConfirmProblem(http.StatusConflict, "current_value_changed", "")
	}
	observation := view.observation
	ctx, release, err := s.prepareManualAction(ctx, observation.UserID, observation.AdminAccountID, observation.TargetID)
	if err != nil {
		if errors.Is(err, ErrRemoteActionLeaseLost) {
			return actionConfirmProblem(http.StatusConflict, string(actionConfirmChanged), "")
		}
		return actionConfirmProblem(http.StatusInternalServerError, "prepare_failed", "未能取得处理这个账号的权限，本次没有做任何修改。请重新打开“核对并结束”再试，仍失败时请查看 TransitHub 后端日志。")
	}
	defer release()
	target := actionLeaseFromContext(ctx)
	mutation := mutationLeaseFromContext(ctx)
	claim := RemoteActionClaim{
		RemoteActionScope: observation.RemoteActionScope, Kind: ActionKindTarget, DispatchID: view.DispatchID,
		LeaseKey: target.Key, OwnerID: target.OwnerID,
		MutationLeaseKey: mutation.Key, MutationOwnerID: mutation.OwnerID,
	}
	if workspace := workspacePriorityLeaseFromContext(ctx); workspace != nil {
		claim.WorkspaceLeaseKey, claim.WorkspaceOwnerID = workspace.Key, workspace.OwnerID
	}
	guard := s.sub2APIFloorGuardFor(observation.UserID, observation.AdminAccountID)
	version, reserved := guard.reservationVersion(observation.TargetID)
	repository := s.repo.(remoteActionConfirmRepository)
	pair, err := repository.ResolveUncertainAction(ctx, claim, observation)
	if err != nil {
		var reason actionConfirmReason
		if errors.As(err, &reason) {
			return actionConfirmProblem(http.StatusConflict, string(reason), "")
		}
		if errors.Is(err, ErrRemoteActionLeaseLost) {
			return actionConfirmProblem(http.StatusConflict, string(actionConfirmChanged), "")
		}
		return actionConfirmProblem(http.StatusInternalServerError, "result_unknown", "未能确认这条记录的保存结果，请刷新页面查看它是否已经结束，再决定是否重试。")
	}
	if reserved && pair.reconciledWithoutRemoteEffect && pair.pendingCount() == 0 {
		guard.releaseUnchangedReservation(observation.TargetID, version)
	}
	return nil
}

func (h *Handler) getRemoteActionCurrent(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if accountID == "" {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	view, err := h.service.readRemoteActionCurrent(r.Context(), userID, accountID)
	if err != nil {
		writeRemoteActionConfirmError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, view)
}

func (h *Handler) postRemoteActionConfirmCurrent(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	var input remoteActionConfirmInput
	if err := httpjson.Decode(r, &input); err != nil || accountID == "" {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	if err := h.service.confirmRemoteActionCurrent(r.Context(), userID, accountID, input); err != nil {
		writeRemoteActionConfirmError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, struct{}{})
}

func writeRemoteActionConfirmError(w http.ResponseWriter, err error) {
	var problem *remoteActionConfirmProblem
	if errors.As(err, &problem) {
		httpjson.Write(w, problem.status, problem)
		return
	}
	writeError(w, err)
}
