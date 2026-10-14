package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExpectedConnection contains only the public identity the user opened. It is
// request-local and does not create another persistent connection version.
type ExpectedConnection struct {
	WorkspaceAdminAccountID string   `json:"workspaceAdminAccountId"`
	ID                      string   `json:"id"`
	UpstreamSiteID          string   `json:"upstreamSiteId"`
	UpstreamGroupID         string   `json:"upstreamGroupId"`
	UpstreamGroupName       string   `json:"upstreamGroupName"`
	UpstreamKeyID           string   `json:"upstreamKeyId"`
	AdminAccountID          string   `json:"adminAccountId"`
	AdminPlatform           string   `json:"adminPlatform"`
	UpstreamPlatform        string   `json:"upstreamPlatform"`
	ProvisioningMode        string   `json:"provisioningMode"`
	Status                  string   `json:"status"`
	OwnGroupIDs             []string `json:"ownGroupIds"`
	OwnGroupNames           []string `json:"ownGroupNames"`
	PricingMappingEnabled   bool     `json:"pricingMappingEnabled"`
}

type CheckedRealDisconnectRepository interface {
	DeleteRealConnectionWithPricingMappingChecked(context.Context, RealConnection, bool, func(context.Context, RealConnection) error) error
}

func expectedConnectionIdentity(conn RealConnection) ExpectedConnection {
	return ExpectedConnection{WorkspaceAdminAccountID: conn.WorkspaceAdminAccountID, ID: conn.ID,
		UpstreamSiteID: conn.UpstreamSiteID, UpstreamGroupID: conn.UpstreamGroupID, UpstreamGroupName: conn.UpstreamGroupName,
		UpstreamKeyID: conn.UpstreamKeyID, AdminAccountID: conn.AdminAccountID, AdminPlatform: conn.AdminPlatform,
		UpstreamPlatform: conn.UpstreamPlatform, ProvisioningMode: conn.ProvisioningMode, Status: conn.Status,
		OwnGroupIDs: sortedIdentityValues(conn.OwnGroupIDs), OwnGroupNames: sortedIdentityValues(conn.OwnGroupNames), PricingMappingEnabled: conn.PricingMappingEnabled}
}

func sortedIdentityValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func sameCheckedConnection(current, expected RealConnection) bool {
	return current.UserID == expected.UserID && reflect.DeepEqual(expectedConnectionIdentity(current), expectedConnectionIdentity(expected))
}

func (expected ExpectedConnection) connection(userID string) RealConnection {
	return RealConnection{UserID: userID, WorkspaceAdminAccountID: expected.WorkspaceAdminAccountID, ID: expected.ID,
		UpstreamSiteID: expected.UpstreamSiteID, UpstreamGroupID: expected.UpstreamGroupID, UpstreamGroupName: expected.UpstreamGroupName,
		UpstreamKeyID: expected.UpstreamKeyID, AdminAccountID: expected.AdminAccountID, AdminPlatform: expected.AdminPlatform,
		UpstreamPlatform: expected.UpstreamPlatform, ProvisioningMode: expected.ProvisioningMode, Status: expected.Status,
		OwnGroupIDs: expected.OwnGroupIDs, OwnGroupNames: expected.OwnGroupNames, PricingMappingEnabled: expected.PricingMappingEnabled}
}

func checkedConnectionChanged() error {
	return &scopedRequestError{Status: http.StatusConflict, Message: "已有对接资料已变化，本次未解除，请重新打开记录", Detail: "当前工作区、对接对象、分组、账号、状态或调价关联与窗口中确认的资料不同；本次未清理运行记录、移除映射或解除对接，也未修改主站账号或上游Key", Next: "请关闭窗口，重新读取并打开这条对接后再决定是否解除"}
}

func checkedDisconnectInvalid(detail string) error {
	return &scopedRequestError{Status: http.StatusBadRequest, Message: "已有对接未解除：本地处理请求不正确", Detail: detail + "；本次未修改主站、上游Key或本地记录", Next: "请重新打开已有对接，选择仅解除本地对接后重试"}
}

func validExpectedConnection(req RealDisconnectRequest) bool {
	expected := req.ExpectedConnection
	if expected == nil || req.Mode != "unlink" || expected.ID == "" || expected.ID != req.ConnectionID || expected.WorkspaceAdminAccountID == "" {
		return false
	}
	for _, value := range []string{expected.ID, expected.WorkspaceAdminAccountID, expected.UpstreamSiteID, expected.UpstreamGroupID, expected.UpstreamKeyID, expected.AdminAccountID, expected.AdminPlatform, expected.UpstreamPlatform, expected.ProvisioningMode, expected.Status} {
		if len(value) > 128 {
			return false
		}
	}
	if len(expected.UpstreamGroupName) > 512 || len(expected.OwnGroupIDs) > 512 || len(expected.OwnGroupNames) > 512 {
		return false
	}
	for _, value := range expected.OwnGroupIDs {
		if len(value) > 128 {
			return false
		}
	}
	for _, value := range expected.OwnGroupNames {
		if len(value) > 512 {
			return false
		}
	}
	return true
}

func (s *Service) disconnectExpectedConnection(ctx context.Context, userID string, req RealDisconnectRequest) error {
	if !validExpectedConnection(req) {
		return checkedDisconnectInvalid("必须携带窗口中确认的完整对接身份；这个入口只提供本地解除")
	}
	workspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return &scopedRequestError{Status: http.StatusConflict, Message: "当前工作区无法确认，已有对接未解除", Detail: "Hub未取得当前工作区；本次未清理运行记录、移除映射或解除对接，也未修改主站账号或上游Key", Next: "请选择当前工作区后重新打开已有对接", Cause: err}
	}
	if workspace != req.ExpectedConnection.WorkspaceAdminAccountID {
		return checkedConnectionChanged()
	}
	repository, ok := s.connRepository.(CheckedRealDisconnectRepository)
	if !ok {
		return checkedDisconnectFailure(&checkedDisconnectStorageError{Step: "确认本地解除入口", Cause: errors.New("checked local repository unavailable")})
	}
	removePricing := false
	if req.RemovePricingMapping != nil {
		removePricing = *req.RemovePricingMapping
	}
	beforeDelete := func(ctx context.Context, current RealConnection) error {
		if current.Status != ConnectionStatusMissing {
			return nil
		}
		if s.runtimeCleaner == nil {
			return errors.New("local runtime cleanup unavailable")
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return s.runtimeCleaner.CleanupRealConnectionRuntime(cleanupCtx, userID, workspace, current.ID)
	}
	err = repository.DeleteRealConnectionWithPricingMappingChecked(ctx, req.ExpectedConnection.connection(userID), removePricing, beforeDelete)
	if err != nil {
		var known *scopedRequestError
		if errors.As(err, &known) {
			return err
		}
		var storage *checkedDisconnectStorageError
		if !errors.As(err, &storage) {
			storage = &checkedDisconnectStorageError{Step: "保存本地解除结果", Cause: err, Uncertain: true}
		}
		return checkedDisconnectFailure(storage)
	}
	return nil
}

type checkedDisconnectStorageError struct {
	Step             string
	Cause            error
	RuntimeCleared   bool
	CleanupUncertain bool
	Uncertain        bool
}

func (e *checkedDisconnectStorageError) Error() string { return "本地对接解除未能完成确认" }
func (e *checkedDisconnectStorageError) Unwrap() error { return e.Cause }

func checkedDisconnectFailure(storage *checkedDisconnectStorageError) error {
	log.Printf("checked local connection unlink failed step=%s reason=%s runtime_cleared=%t cleanup_unconfirmed=%t save_unconfirmed=%t", storage.Step, safeLocalStorageCause(storage.Cause), storage.RuntimeCleared, storage.CleanupUncertain, storage.Uncertain)
	status := http.StatusInternalServerError
	message := "已有对接未解除：" + storage.Step + "失败"
	detail := safeLocalStorageCause(storage.Cause) + "；本次对接和调价映射保存已回滚，原记录保持"
	if storage.Step == "开始本地保存" || storage.Step == "确认本地解除入口" {
		detail = safeLocalStorageCause(storage.Cause) + "；本次未开始保存本地解除，对接和调价映射原记录保持"
	}
	next := "请检查Hub后端本地对接保存记录，恢复数据库连接后重新打开这条对接再重试"
	if storage.CleanupUncertain {
		detail = safeLocalStorageCause(storage.Cause) + "；对接和调价映射仍保留，旧运行记录是否已清理尚未确认"
		next = "请先核查Hub后端旧运行记录清理结果，再重新打开这条对接决定是否重试"
	}
	if storage.RuntimeCleared {
		detail = safeLocalStorageCause(storage.Cause) + "；旧运行记录已清理，对接和调价映射保存已回滚，原对接和映射仍保留；健康历史事件未删除"
	}
	if storage.Uncertain {
		status = http.StatusServiceUnavailable
		message = "已有对接的本地解除保存结果尚未确认"
		detail = storage.Step + "：" + safeLocalStorageCause(storage.Cause) + "；当前不能确认对接是否已解除或调价映射是否已更新"
		if storage.RuntimeCleared {
			detail += "；旧运行记录已清理，健康历史事件未删除"
		}
		next = "请先重新读取这条对接并由维护人员核查数据库提交结果，再决定是否重试；不要自动重复解除"
	}
	detail += "；本次未修改主站账号或上游Key"
	return &scopedRequestError{Status: status, Message: message, Detail: detail, Next: next, Cause: storage}
}

// Database and network error strings can include connection strings or query
// values. Describe known causes without returning those strings to the client.
func safeLocalStorageCause(err error) string {
	if err == nil {
		return "本地对接读取或保存接口不可用，具体原因尚未确认"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "本地数据库操作超过等待时间"
	}
	if errors.Is(err, context.Canceled) {
		return "本次本地操作已取消"
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		reason := map[string]string{"55P03": "其他操作正在使用这条本地记录，数据库锁等待超时", "57014": "数据库取消了本次查询或保存", "40P01": "本地记录被并发操作占用，数据库取消了冲突事务", "40001": "并发资料发生变化，数据库拒绝本次事务", "23503": "数据库关联记录检查未通过", "23505": "数据库中已有冲突的唯一记录", "23514": "数据库字段检查未通过", "42501": "当前数据库账号没有执行这一步的权限", "42P01": "当前数据库缺少本地查询所需的数据表", "53300": "数据库连接数量已满", "57P01": "数据库正在关闭，连接无法继续"}[database.Code]
		if reason == "" {
			reason = "数据库拒绝本次本地操作，详细原因请核查Hub后端记录"
		}
		return reason + "（数据库代码 " + database.Code + "）"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "连接本地存储时超过等待时间"
		}
		return "本地存储连接中断或无法建立"
	}
	return "本地读取或保存未能完成，具体原因无法安全确认，请核查Hub后端记录"
}

func (r *Repository) DeleteRealConnectionWithPricingMappingChecked(ctx context.Context, expected RealConnection, removePricing bool, beforeDelete func(context.Context, RealConnection) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return &checkedDisconnectStorageError{Step: "开始本地保存", Cause: err}
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer rollbackCancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return &checkedDisconnectStorageError{Step: "设置本地锁等待时间", Cause: err}
	}
	var state *State
	if removePricing && expected.PricingMappingEnabled {
		state, err = scanState(tx.QueryRow(ctx, `SELECT user_id,admin_account_id,base_url,email,session,mappings,own_groups FROM my_site_states WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, expected.UserID, expected.WorkspaceAdminAccountID))
		if err != nil {
			return &checkedDisconnectStorageError{Step: "读取并锁定调价映射", Cause: err}
		}
	}
	current, err := scanRealConnection(tx.QueryRow(ctx, `SELECT id,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,
 upstream_key_id,''::text,admin_account_id,admin_account_name,own_group_ids,own_group_names,group_type,provisioning_mode,status,
 upstream_platform,admin_platform,pricing_mapping_enabled,''::text,created_at FROM real_connections WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3 FOR UPDATE`, expected.ID, expected.UserID, expected.WorkspaceAdminAccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return checkedConnectionChanged()
	}
	if err != nil {
		return &checkedDisconnectStorageError{Step: "读取并锁定这条对接", Cause: err}
	}
	if !sameCheckedConnection(*current, expected) {
		return checkedConnectionChanged()
	}
	runtimeCleared := false
	if current.Status == ConnectionStatusMissing {
		if beforeDelete == nil {
			return &checkedDisconnectStorageError{Step: "清理旧运行记录", Cause: errors.New("runtime cleanup callback unavailable")}
		}
		if err := beforeDelete(ctx, *current); err != nil {
			return &checkedDisconnectStorageError{Step: "清理旧运行记录", Cause: err, CleanupUncertain: true}
		}
		runtimeCleared = true
	}
	failure := func(step string, cause error) error {
		return &checkedDisconnectStorageError{Step: step, Cause: cause, RuntimeCleared: runtimeCleared}
	}
	if state != nil {
		target := UpstreamGroupRef{SiteID: current.UpstreamSiteID, GroupName: current.UpstreamGroupName}
		for _, oldName := range normalizedConnectionGroupNames(current.OwnGroupNames) {
			var shared bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM real_connections WHERE user_id=$1 AND workspace_admin_account_id=$2 AND id<>$3 AND pricing_mapping_enabled AND upstream_site_id=$4 AND upstream_group_name=$5 AND own_group_names ? $6)`, current.UserID, current.WorkspaceAdminAccountID, current.ID, current.UpstreamSiteID, current.UpstreamGroupName, oldName).Scan(&shared); err != nil {
				return failure("检查其他对接的调价引用", err)
			}
			if !shared {
				removeMappingTargetForOwnGroups(state, []string{oldName}, target)
			}
		}
		if err := updateStateInTx(ctx, tx, *state); err != nil {
			return failure("保存调价映射", err)
		}
	}
	ids, err := json.Marshal(current.OwnGroupIDs)
	if err != nil {
		return failure("读取这条对接的主站分组", err)
	}
	names, err := json.Marshal(current.OwnGroupNames)
	if err != nil {
		return failure("读取这条对接的主站分组名称", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM real_connections WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3
 AND upstream_site_id=$4 AND upstream_group_id=$5 AND upstream_group_name=$6 AND upstream_key_id=$7
 AND admin_account_id=$8 AND admin_platform=$9 AND upstream_platform=$10 AND provisioning_mode=$11 AND status=$12
 AND pricing_mapping_enabled=$13 AND own_group_ids=$14::jsonb AND own_group_names=$15::jsonb`, current.ID, current.UserID, current.WorkspaceAdminAccountID,
		current.UpstreamSiteID, current.UpstreamGroupID, current.UpstreamGroupName, current.UpstreamKeyID, current.AdminAccountID, current.AdminPlatform,
		current.UpstreamPlatform, current.ProvisioningMode, current.Status, current.PricingMappingEnabled, string(ids), string(names))
	if err != nil {
		return failure("删除本地对接记录", err)
	}
	if tag.RowsAffected() != 1 {
		return failure("删除本地对接记录", fmt.Errorf("checked delete affected %d rows", tag.RowsAffected()))
	}
	if err := ctx.Err(); err != nil {
		return failure("提交前检查本地保存", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return &checkedDisconnectStorageError{Step: "提交本地解除和调价结果", Cause: err, RuntimeCleared: runtimeCleared, Uncertain: !errors.Is(err, pgx.ErrTxCommitRollback)}
	}
	return nil
}
