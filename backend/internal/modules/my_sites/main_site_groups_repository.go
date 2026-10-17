package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

type GroupReassignmentError struct {
	Kind      string
	Step      string
	Cause     error
	Uncertain bool
}

var errGroupReassignmentStateMissing = errors.New("工作区的调价数据源记录已不存在")

func (e *GroupReassignmentError) Error() string {
	switch e.Kind {
	case "removed":
		return "这条对接已被解除"
	case "changed":
		return "Hub 记录已被改动"
	}
	return "本地保存失败：" + e.Step
}
func (e *GroupReassignmentError) Unwrap() error { return e.Cause }

func (r *Repository) ReassignRealConnectionGroupsChecked(ctx context.Context, expected RealConnection, ids, names []string, removeOldPricing bool) error {
	return reassignRealConnectionGroupsChecked(ctx, expected, ids, names, removeOldPricing, r.db.Begin)
}
func reassignRealConnectionGroupsChecked(ctx context.Context, expected RealConnection, ids, names []string, removeOldPricing bool, begin func(context.Context) (pgx.Tx, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fail := func(step string, cause error) error {
		return &GroupReassignmentError{Kind: "storage", Step: step, Cause: cause}
	}
	tx, err := begin(ctx)
	if err != nil {
		return fail("开始本地保存", err)
	}
	defer func() {
		rollbackCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return fail("设置本地锁等待时间", err)
	}
	var state *State
	if expected.PricingMappingEnabled {
		state, err = scanState(tx.QueryRow(ctx, `SELECT user_id,admin_account_id,base_url,email,session,mappings,own_groups FROM my_site_states WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, expected.UserID, expected.WorkspaceAdminAccountID))
		if err != nil {
			return fail("读取并锁定调价数据源", err)
		}
		if state == nil {
			return fail("读取并锁定调价数据源", errGroupReassignmentStateMissing)
		}
	}
	current, err := scanRealConnection(tx.QueryRow(ctx, `SELECT id,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,upstream_key_id,''::text,admin_account_id,admin_account_name,own_group_ids,own_group_names,group_type,provisioning_mode,status,upstream_platform,admin_platform,pricing_mapping_enabled,''::text,created_at FROM real_connections WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3 FOR UPDATE`, expected.ID, expected.UserID, expected.WorkspaceAdminAccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &GroupReassignmentError{Kind: "removed"}
	}
	if err != nil {
		return fail("读取并锁定这条对接", err)
	}
	if current == nil {
		return &GroupReassignmentError{Kind: "removed"}
	}
	if !sameCheckedConnection(*current, expected) {
		return &GroupReassignmentError{Kind: "changed"}
	}
	if state != nil {
		target := UpstreamGroupRef{SiteID: current.UpstreamSiteID, GroupName: current.UpstreamGroupName}
		if removeOldPricing {
			for i, oldID := range current.OwnGroupIDs {
				if containsString(ids, oldID) || i >= len(current.OwnGroupNames) {
					continue
				}
				oldName := current.OwnGroupNames[i]
				// A retained group may share its name with the removed one.
				if containsString(names, oldName) {
					continue
				}
				var shared bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM real_connections WHERE user_id=$1 AND workspace_admin_account_id=$2 AND id<>$3 AND pricing_mapping_enabled AND upstream_site_id=$4 AND upstream_group_name=$5 AND own_group_names ? $6)`, current.UserID, current.WorkspaceAdminAccountID, current.ID, current.UpstreamSiteID, current.UpstreamGroupName, oldName).Scan(&shared); err != nil {
					return fail("检查其他对接的调价引用", err)
				}
				if !shared {
					removeMappingTargetForOwnGroups(state, []string{oldName}, target)
				}
			}
		}
		addMappingTargetForOwnGroups(state, names, target)
		if err := updateStateInTx(ctx, tx, *state); err != nil {
			return fail("保存调价数据源", err)
		}
	}
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return fail("准备主站分组编号", err)
	}
	namesJSON, err := json.Marshal(names)
	if err != nil {
		return fail("准备主站分组名称", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE real_connections SET own_group_ids=$4::jsonb,own_group_names=$5::jsonb WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3`, current.ID, current.UserID, current.WorkspaceAdminAccountID, string(idsJSON), string(namesJSON))
	if err != nil {
		return fail("保存对接分组", err)
	}
	if tag.RowsAffected() != 1 {
		return fail("保存对接分组", fmt.Errorf("expected one updated connection"))
	}
	if err := tx.Commit(ctx); err != nil {
		return &GroupReassignmentError{Kind: "storage", Step: "提交对接分组与调价数据源", Cause: err, Uncertain: !errors.Is(err, pgx.ErrTxCommitRollback)}
	}
	return nil
}
