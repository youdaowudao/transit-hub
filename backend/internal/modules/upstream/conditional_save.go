package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Configuration and login are the guard. Runtime metrics/status are deliberately
// excluded because a sync may already have published its transient status.
func unchangedSiteConfiguration(current, expected Site) bool {
	return current.ID == expected.ID && current.UserID == expected.UserID && current.AdminAccountID == expected.AdminAccountID &&
		reflect.DeepEqual(current.Session, expected.Session) && current.Name == expected.Name && current.BaseURL == expected.BaseURL &&
		current.Platform == expected.Platform && current.RequestedPlatform == expected.RequestedPlatform && current.Account == expected.Account &&
		current.Remark == expected.Remark && current.RechargeRate == expected.RechargeRate && current.IsEnabled() == expected.IsEnabled() && reflect.DeepEqual(current.Settings, expected.Settings)
}

// Apply only fields actually changed by this request. The locked PG record keeps
// newer runtime fields when a setting-only request started from an older cache.
func mergeExistingSiteChanges(current, expected, candidate Site) Site {
	next := current
	if candidate.Name != expected.Name {
		next.Name = candidate.Name
	}
	if candidate.BaseURL != expected.BaseURL {
		next.BaseURL = candidate.BaseURL
	}
	if candidate.Platform != expected.Platform {
		next.Platform = candidate.Platform
	}
	if candidate.RequestedPlatform != expected.RequestedPlatform {
		next.RequestedPlatform = candidate.RequestedPlatform
	}
	if candidate.Account != expected.Account {
		next.Account = candidate.Account
	}
	if candidate.Remark != expected.Remark {
		next.Remark = candidate.Remark
	}
	if candidate.RechargeRate != expected.RechargeRate {
		next.RechargeRate = candidate.RechargeRate
	}
	if candidate.IsEnabled() != expected.IsEnabled() {
		next.Enabled = boolPointer(candidate.IsEnabled())
	}
	if candidate.Status != expected.Status {
		next.Status = candidate.Status
	}
	if !reflect.DeepEqual(candidate.ErrorKey, expected.ErrorKey) {
		next.ErrorKey = candidate.ErrorKey
	}
	if !reflect.DeepEqual(candidate.Metrics, expected.Metrics) {
		next.Metrics = candidate.Metrics
	}
	if !reflect.DeepEqual(candidate.Session, expected.Session) {
		next.Session = candidate.Session
	}
	if !reflect.DeepEqual(candidate.Settings, expected.Settings) {
		next.Settings = candidate.Settings
	}
	if !reflect.DeepEqual(candidate.LastSyncedAt, expected.LastSyncedAt) {
		next.LastSyncedAt = candidate.LastSyncedAt
	}
	return next
}
func (r *Repository) SaveExistingSiteIfUnchanged(ctx context.Context, expected, candidate Site) (bool, error) {
	if candidate.ID != expected.ID || candidate.UserID != expected.UserID || candidate.AdminAccountID != expected.AdminAccountID {
		return false, nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var current Site
	var metricsJSON, sessionJSON, settingsJSON []byte
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id,user_id,admin_account_id,name,base_url,platform,requested_platform,account,remark,recharge_rate,status,error_key,metrics,session,settings,last_synced_at,enabled FROM upstream_sites WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 FOR UPDATE`, expected.ID, expected.UserID, expected.AdminAccountID).Scan(&current.ID, &current.UserID, &current.AdminAccountID, &current.Name, &current.BaseURL, &current.Platform, &current.RequestedPlatform, &current.Account, &current.Remark, &current.RechargeRate, &current.Status, &current.ErrorKey, &metricsJSON, &sessionJSON, &settingsJSON, &current.LastSyncedAt, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current.Enabled = boolPointer(enabled)
	if err = json.Unmarshal(metricsJSON, &current.Metrics); err != nil {
		return false, err
	}
	if len(sessionJSON) > 0 {
		if err = json.Unmarshal(sessionJSON, &current.Session); err != nil {
			return false, err
		}
	}
	if err = json.Unmarshal(settingsJSON, &current.Settings); err != nil {
		return false, err
	}
	if !unchangedSiteConfiguration(current, expected) {
		return false, nil
	}
	next := mergeExistingSiteChanges(current, expected, candidate)
	metricsJSON, err = json.Marshal(next.Metrics)
	if err != nil {
		return false, err
	}
	sessionJSON = nil
	if next.Session != nil {
		sessionJSON, err = json.Marshal(next.Session)
		if err != nil {
			return false, err
		}
	}
	settingsJSON, err = json.Marshal(next.Settings)
	if err != nil {
		return false, err
	}
	result, err := tx.Exec(ctx, `UPDATE upstream_sites SET name=$4,base_url=$5,platform=$6,requested_platform=$7,account=$8,remark=$9,recharge_rate=$10,status=$11,error_key=$12,metrics=$13::jsonb,session=$14::jsonb,settings=$15::jsonb,last_synced_at=$16,enabled=$17,updated_at=$18 WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 AND EXISTS(SELECT 1 FROM admin_accounts WHERE user_id=$2 AND id=$3)`, next.ID, next.UserID, next.AdminAccountID, next.Name, next.BaseURL, next.Platform, next.RequestedPlatform, next.Account, next.Remark, next.RechargeRate, next.Status, next.ErrorKey, string(metricsJSON), nullableJSONString(sessionJSON), string(settingsJSON), next.LastSyncedAt, next.IsEnabled(), time.Now())
	if err != nil {
		return false, err
	}
	if result.RowsAffected() != 1 {
		return false, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return false, &siteCommitError{err: err}
	}
	return true, nil
}

type siteCommitError struct{ err error }

func (e *siteCommitError) Error() string { return "本站数据库提交结果尚未确认" }
func (e *siteCommitError) Unwrap() error { return e.err }

func cloneSite(site *Site) Site {
	// JSON round-trip preserves all optional pointers and detached slice elements.
	// This object never leaves memory or enters diagnostics.
	raw, err := json.Marshal(site)
	if err == nil {
		var copy Site
		if json.Unmarshal(raw, &copy) == nil {
			// Site identity and Session have intentionally private JSON tags.
			copy.UserID = site.UserID
			copy.AdminAccountID = site.AdminAccountID
			if site.Session != nil {
				sessionRaw, _ := json.Marshal(site.Session)
				_ = json.Unmarshal(sessionRaw, &copy.Session)
			}
			return copy
		}
	}
	copy := *site
	if site.Session != nil {
		session := *site.Session
		copy.Session = &session
	}
	copy.Metrics.Groups = append([]GroupInfo(nil), site.Metrics.Groups...)
	return copy
}
func saveFailureError(reason string, cause error, status int) *RequestError {
	return &RequestError{MessageKey: ErrorRequest, Stage: "save", StatusCode: status, UpstreamMessage: reason, Cause: cause}
}
func safePersistenceCause(cause error) string {
	if cause == nil {
		return "原因尚未确认"
	}
	var commit *siteCommitError
	if errors.As(cause, &commit) {
		return "数据库提交结果尚未确认"
	}
	// Never expose DSNs, credentials, or unfiltered SQL diagnostics.
	text := safeCostReason(cause.Error())
	if strings.Contains(strings.ToLower(text), "postgres") || strings.Contains(strings.ToLower(text), "redis://") {
		return "存储连接或写入失败，请维护人员检查存储日志"
	}
	if text == "" {
		return "存储连接或写入失败"
	}
	return text
}

// Caller holds s.mu; each store uses its own bounded persistence context.
func (s *Service) publishExistingSiteLocked(expected, candidate Site, preserveRotatedSession bool, syncStatusBefore *Status) error {
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	saved := true
	var durableErr error
	if s.repository != nil {
		saved, durableErr = s.repository.SaveExistingSiteIfUnchanged(ctx, expected, candidate)
	}
	if durableErr != nil {
		outcome := "本站资料未保存，原数据库资料保留"
		var commitErr *siteCommitError
		if errors.As(durableErr, &commitErr) {
			outcome = "数据库保存结果尚未确认，请维护人员核查实际提交结果"
		}
		message := "保存本站资料失败：" + safePersistenceCause(durableErr) + "；" + outcome + "；本轮未发布新费用，连接恢复并核查后可重试；不会修改主站。"
		if preserveRotatedSession && !reflect.DeepEqual(expected.Session, candidate.Session) {
			tokenOnly := cloneSite(&expected)
			// This sync still owns the transient status while its input Session is
			// current. Revoke only that status before preserving the rotated login.
			if syncStatusBefore != nil && tokenOnly.Status == StatusSyncing {
				tokenOnly.Status = *syncStatusBefore
			}
			tokenOnly.Session = candidate.Session
			cacheCtx, cacheCancel := context.WithTimeout(context.Background(), persistenceTimeout)
			defer cacheCancel()
			if cacheErr := s.cache.Set(cacheCtx, &tokenOnly); cacheErr != nil {
				message += " 新登录资料也未写入当前运行数据：" + safePersistenceCause(cacheErr) + "。"
			} else {
				message += " 新登录资料只保留在当前运行数据中，数据库尚未确认；请联系维护人员核查，不能保证重启后登录仍有效。"
			}
		}
		return saveFailureError(message, durableErr, 503)
	}
	if !saved {
		return saveFailureError("已保存的站点资料与本次请求不同，本次没有覆盖资料或更新费用；请联系维护人员核对已保存资料，或新增站点，不会修改主站。", nil, 409)
	}
	cacheCtx, cacheCancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cacheCancel()
	if err := s.cache.Set(cacheCtx, &candidate); err != nil {
		return saveFailureError("本站资料已保存，当前运行数据未更新："+safePersistenceCause(err)+"；本轮新费用未发布，刷新网页无法恢复，请联系维护人员检查存储连接后恢复服务，不会修改主站。", err, 503)
	}
	return nil
}
