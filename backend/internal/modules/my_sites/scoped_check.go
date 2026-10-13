package my_sites

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type ScopedConnectionStatusRepository interface {
	ReadScopedConnectionSnapshots(context.Context, string, string, ScopedConnectionScope) ([]ScopedConnectionSnapshot, error)
	ApplyScopedConnectionStatuses(context.Context, []ScopedConnectionSnapshot, map[string]string) (ScopedStatusApplyResult, error)
}

func scopedCheckLimits(mode string) (time.Duration, time.Duration) {
	if mode == "manual" {
		return 45 * time.Second, 10 * time.Second
	}
	return 20 * time.Second, 5 * time.Second
}
func validScopedAccountID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
func scopedApplicable(row ScopedConnectionSnapshot) bool {
	return strings.TrimSpace(row.AdminAccountID) != "" && (row.AdminPlatform == "" || row.AdminPlatform == string(upstream.PlatformSub2API))
}
func newScopedCheckResult(workspace string, total int) ScopedCheckResult {
	return ScopedCheckResult{WorkspaceAdminAccountID: workspace, Total: total, ConfirmedConnectionIDs: []string{}, UnconfirmedConnectionIDs: []string{}, SkippedConnectionIDs: []string{}, ChangedConnectionIDs: []string{}, ConfirmedStatuses: make(map[string]string), Issues: []ScopedCheckIssue{}}
}
func scopedRemoteCause(err error, session upstream.Session) (string, int) {
	detail := "原因无法安全确认，请查看主站响应及Hub后端检查记录"
	status := 0
	var remote *upstream.RequestError
	if errors.As(err, &remote) {
		status = remote.StatusCode
		if remote.UpstreamMessage != "" {
			detail = upstream.ImportDiagnosticHint(remote.UpstreamMessage)
		}
		if detail == "" || detail == "原因无法安全确认，请查看主站响应及Hub后端检查记录" {
			switch remote.Reason {
			case upstream.ErrorNetworkTimeout:
				detail = "读取主站资料超时"
			case upstream.ErrorNetworkUnreachable:
				detail = "Hub暂时无法连接主站"
			case upstream.ErrorTLSFailed:
				detail = "主站安全连接未能建立"
			default:
				switch remote.MessageKey {
				case upstream.ErrorInvalidResponse:
					detail = "主站返回的资料格式不正确，无法确认账号"
				case upstream.ErrorAuth:
					detail = "已有主站登录资料未通过管理员验证"
				default:
					detail = "主站未能完成本次读取，具体原因无法安全确认，请查看主站与Hub后端记录"
				}
			}
		}
	}
	if errors.Is(err, context.Canceled) {
		detail = "检查请求已取消，本次未继续保存结果"
	} else if errors.Is(err, context.DeadlineExceeded) {
		detail = "本次检查已到时限，尚未取得完整结果"
	}
	for _, secret := range []string{session.AccessToken, session.RefreshToken, session.Cookie, session.AdminAPIKey} {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[已遮盖]")
		}
	}
	return detail, status
}
func (s *Service) CheckScopedRealConnections(ctx context.Context, userID string, req ScopedCheckRequest) (ScopedCheckResult, error) {
	workspace, err := s.scopedWorkspace(ctx, userID, req.WorkspaceAdminAccountID)
	if err != nil {
		return ScopedCheckResult{}, err
	}
	req.SiteID = strings.TrimSpace(req.SiteID)
	if (req.Mode != "automatic" && req.Mode != "manual") || len(req.SiteID) > 128 || (req.SiteID != "" && req.GroupRefs != nil) || (req.SiteID == "" && req.GroupRefs == nil) {
		return ScopedCheckResult{}, scopedInvalid("检查方式必须是进入检查或手动刷新，网站与当前页分组必须且只能选择一种")
	}
	refs, err := normalizeScopedRefs(req.GroupRefs)
	if err != nil {
		return ScopedCheckResult{}, err
	}
	repo, ok := s.connRepository.(ScopedConnectionStatusRepository)
	if !ok {
		return ScopedCheckResult{}, scopedReadFailure("本批账号对接资料", nil)
	}
	snapshots, err := repo.ReadScopedConnectionSnapshots(ctx, userID, workspace, ScopedConnectionScope{WorkspaceAdminAccountID: workspace, SiteID: req.SiteID, GroupRefs: refs})
	if err != nil {
		var known *scopedRequestError
		if errors.As(err, &known) {
			return ScopedCheckResult{}, err
		}
		return ScopedCheckResult{}, scopedReadFailure("本批账号对接资料", err)
	}
	result := newScopedCheckResult(workspace, len(snapshots))
	categories := make(map[string]string, len(snapshots))
	accounts := make(map[string][]ScopedConnectionSnapshot)
	accountOrder := make([]string, 0)
	for _, row := range snapshots {
		categories[row.ID] = "unconfirmed"
		if !scopedApplicable(row) {
			categories[row.ID] = "skipped"
			result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: row.ID, AccountID: row.AdminAccountID, Step: "scope", Detail: "这条记录没有主站账号ID，或使用其他主站平台，本次保持原状态", Next: "请沿原对接入口处理；本次未修改主站账号", RetryAllowed: false})
			continue
		}
		if !validScopedAccountID(row.AdminAccountID) {
			result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: row.ID, AccountID: row.AdminAccountID, Step: "account_id", Detail: "这条记录的主站账号ID格式不正确，当前仍保留上次状态", Next: "请检查对接资料中的账号ID后再刷新", RetryAllowed: false})
			continue
		}
		if _, exists := accounts[row.AdminAccountID]; !exists {
			accountOrder = append(accountOrder, row.AdminAccountID)
		}
		accounts[row.AdminAccountID] = append(accounts[row.AdminAccountID], row)
	}
	finish := func() {
		result.ConfirmedConnectionIDs = []string{}
		result.UnconfirmedConnectionIDs = []string{}
		result.SkippedConnectionIDs = []string{}
		result.ChangedConnectionIDs = []string{}
		for _, row := range snapshots {
			switch categories[row.ID] {
			case "confirmed":
				result.ConfirmedConnectionIDs = append(result.ConfirmedConnectionIDs, row.ID)
			case "skipped":
				result.SkippedConnectionIDs = append(result.SkippedConnectionIDs, row.ID)
			case "changed":
				result.ChangedConnectionIDs = append(result.ChangedConnectionIDs, row.ID)
			default:
				result.UnconfirmedConnectionIDs = append(result.UnconfirmedConnectionIDs, row.ID)
			}
		}
	}
	if len(accountOrder) == 0 {
		finish()
		return result, nil
	}
	totalLimit, itemLimit := scopedCheckLimits(req.Mode)
	batchCtx, cancel := context.WithTimeout(ctx, totalLimit)
	defer cancel()
	session, available, readErr := s.StoredSession(batchCtx, userID, workspace)
	// StoredSession intentionally omits unauthenticated credentials. A narrow
	// fallback reads only the existing platform to preserve other-platform skips.
	if readErr == nil && !available {
		stored, err := s.repository.Get(batchCtx, userID, workspace)
		if err != nil {
			readErr = err
		} else if stored != nil && stored.Session.Platform != upstream.PlatformSub2API {
			session.Platform = stored.Session.Platform
		}
	}
	if readErr == nil && session.Platform != "" && session.Platform != upstream.PlatformSub2API {
		for _, row := range snapshots {
			categories[row.ID] = "skipped"
		}
		result.Issues = append(result.Issues, ScopedCheckIssue{Step: "session", Detail: "当前主站使用其他平台，本次没有检查或更新这些账号的本地状态", Next: "请沿原主站管理和对接入口处理，本次未修改主站账号", RetryAllowed: false})
		finish()
		return result, nil
	}
	if readErr != nil || !available || s.platformService == nil {
		detail := "已有主站登录资料不可用，本次未更新这些账号的本地状态"
		if readErr != nil {
			detail = "Hub未能读取已有主站登录资料，具体存储原因无法安全确认；本次未更新账号状态"
		}
		result.Issues = append(result.Issues, ScopedCheckIssue{Step: "session", Detail: detail, Next: "请到主站管理更新登录后再刷新；仍失败时请查看Hub后端读取记录", RetryAllowed: true})
		finish()
		return result, nil
	}
	if err := s.platformService.VerifyAdminContext(batchCtx, session); err != nil {
		detail, status := scopedRemoteCause(err, session)
		result.Issues = append(result.Issues, ScopedCheckIssue{Step: "verify", Detail: "已有主站登录资料未通过验证：" + detail + "；本次未更新这些账号的本地状态", Next: "请到主站管理更新登录后再刷新", RetryAllowed: true, HTTPStatus: status})
		finish()
		return result, nil
	}
	type observation struct {
		found bool
		err   error
	}
	observations := make(map[string]observation, len(accountOrder))
	var mu sync.Mutex
	next := 0
	var workers sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if batchCtx.Err() != nil || next >= len(accountOrder) {
					mu.Unlock()
					return
				}
				accountID := accountOrder[next]
				next++
				mu.Unlock()
				if batchCtx.Err() != nil {
					return
				}
				itemCtx, itemCancel := context.WithTimeout(batchCtx, itemLimit)
				found, err := s.platformService.ReadSub2APIAdminAccountExistenceContext(itemCtx, session, accountID)
				itemCancel()
				mu.Lock()
				observations[accountID] = observation{found: found, err: err}
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	determined := make(map[string]string)
	pendingMissing := make([]string, 0)
	for _, accountID := range accountOrder {
		observed, completed := observations[accountID]
		if completed && observed.err == nil && observed.found {
			for _, row := range accounts[accountID] {
				determined[row.ID] = ConnectionStatusActive
			}
			continue
		}
		var remote *upstream.RequestError
		if completed && errors.As(observed.err, &remote) && remote.StatusCode == http.StatusNotFound {
			if req.Mode == "manual" {
				pendingMissing = append(pendingMissing, accountID)
			} else {
				for _, row := range accounts[accountID] {
					result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: row.ID, AccountID: accountID, Step: "detail", Detail: fmt.Sprintf("主站暂时查不到账号%s，尚不能确认是否已删除，当前仍保留上次状态", accountID), Next: "请点击刷新进一步核对；本次未创建或删除账号", RetryAllowed: true, HTTPStatus: http.StatusNotFound})
				}
			}
			continue
		}
		cause := observed.err
		if !completed {
			cause = batchCtx.Err()
			if cause == nil {
				cause = context.DeadlineExceeded
			}
		}
		detail, status := scopedRemoteCause(cause, session)
		for _, row := range accounts[accountID] {
			result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: row.ID, AccountID: accountID, Step: "detail", Detail: fmt.Sprintf("读取主站账号%s时未能确认：%s；仍保留上次状态", accountID, detail), Next: "请稍后刷新，并检查主站账号详情是否能正常打开", RetryAllowed: true, HTTPStatus: status})
		}
	}
	if len(pendingMissing) > 0 {
		ids, listErr := s.platformService.ListSub2APIAdminAccountIDsStrictContext(batchCtx, session)
		for _, accountID := range pendingMissing {
			if listErr == nil {
				status := ConnectionStatusMissing
				if _, exists := ids[accountID]; exists {
					status = ConnectionStatusActive
				}
				for _, row := range accounts[accountID] {
					determined[row.ID] = status
				}
			} else {
				detail, status := scopedRemoteCause(listErr, session)
				for _, row := range accounts[accountID] {
					result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: row.ID, AccountID: accountID, Step: "inventory", Detail: fmt.Sprintf("进一步核对账号%s是否已删除时，完整账号列表未能读取完成：%s；删除状态未更新", accountID, detail), Next: "请稍后刷新，并检查主站的账号列表是否能正常打开", RetryAllowed: true, HTTPStatus: status})
				}
			}
		}
	}
	// The batch deadline can retain established results; a canceled client cannot.
	if ctx.Err() != nil || len(determined) == 0 {
		finish()
		return result, nil
	}
	saveCtx, saveCancel := context.WithTimeout(ctx, 3*time.Second)
	defer saveCancel()
	applied, saveErr := repo.ApplyScopedConnectionStatuses(saveCtx, snapshots, determined)
	for _, id := range applied.ChangedConnectionIDs {
		categories[id] = "changed"
		for _, row := range snapshots {
			if row.ID == id {
				result.Issues = append(result.Issues, ScopedCheckIssue{ConnectionID: id, AccountID: row.AdminAccountID, Step: "changed", Detail: fmt.Sprintf("账号%s的关联资料在检查期间发生变化，本次结果没有覆盖它", row.AdminAccountID), Next: "请刷新查看最新记录", RetryAllowed: true})
				break
			}
		}
	}
	if saveErr != nil {
		var storage *ScopedStorageError
		uncertain := errors.As(saveErr, &storage) && storage.Uncertain
		result.StorageUncertain = uncertain
		message := "主站检查已返回，但Hub保存本地账号状态失败并已回滚"
		detail := "具体数据库原因无法安全确认，旧状态保持；本次未修改主站账号"
		next := "请查看Hub后端保存记录，确认数据库恢复后再刷新重试"
		status := http.StatusInternalServerError
		if uncertain {
			message = "主站检查已返回，但Hub保存本地账号状态结果尚未确认"
			detail = "提交后的实际保存结果无法安全确认，本次未修改主站账号"
			next = "请先刷新读取本地记录，并查看Hub后端保存记录；不要自动重复检查"
			status = http.StatusServiceUnavailable
		}
		result.Issues = append(result.Issues, ScopedCheckIssue{Step: "save", Detail: message + "：" + detail, Next: next, RetryAllowed: !uncertain})
		finish()
		return result, &scopedRequestError{Status: status, Message: message, Detail: detail, Next: next, Cause: saveErr}
	}
	for _, id := range applied.ConfirmedConnectionIDs {
		categories[id] = "confirmed"
		result.ConfirmedStatuses[id] = determined[id]
	}
	finish()
	return result, nil
}
func (h *Handler) checkScopedRealConnections(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var req ScopedCheckRequest
	if err := decodeScopedBody(w, r, &req); err != nil {
		writeScopedError(w, err)
		return
	}
	result, err := h.service.CheckScopedRealConnections(r.Context(), userID, req)
	if err != nil {
		var known *scopedRequestError
		if errors.As(err, &known) && result.WorkspaceAdminAccountID != "" {
			httpjson.Write(w, known.Status, struct {
				ScopedCheckResult
				Message string `json:"message"`
				Detail  string `json:"detail"`
				Next    string `json:"next"`
			}{result, known.Message, known.Detail, known.Next})
			return
		}
		writeScopedError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
