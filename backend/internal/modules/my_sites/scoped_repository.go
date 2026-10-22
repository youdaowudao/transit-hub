package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"time"
)

const scopedLatestGroupsSQL = `
 WITH enriched AS (
 SELECT snapshots.*, COALESCE(sites.base_url,snapshots.site_id) AS site_key,
 COALESCE(NULLIF(snapshots.group_id,''),snapshots.group_name) AS group_key
 FROM group_rate_snapshots snapshots
 LEFT JOIN upstream_sites sites ON sites.user_id=snapshots.user_id AND sites.id=snapshots.site_id
 WHERE snapshots.user_id=$1 AND snapshots.admin_account_id=$2
 ), ranked AS (
 SELECT *, row_number() OVER (PARTITION BY user_id,site_key,group_key ORDER BY created_at DESC,id DESC) AS row_number FROM enriched
 ), latest AS (SELECT * FROM ranked WHERE row_number=1),
 refs AS (SELECT * FROM jsonb_to_recordset($3::jsonb) AS ref("siteId" text,"groupId" text,"groupName" text))
`

func (r *Repository) validateScopedOwnership(ctx context.Context, userID, workspaceID string, scope ScopedConnectionScope) error {
	if scope.SiteID != "" {
		var owned bool
		err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_sites WHERE user_id=$1 AND admin_account_id=$2 AND id=$3) OR EXISTS(SELECT 1 FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3)`, userID, workspaceID, scope.SiteID).Scan(&owned)
		if err != nil {
			return err
		}
		if !owned {
			return scopedNotFound()
		}
		return nil
	}
	if len(scope.GroupRefs) == 0 {
		return nil
	}
	raw, err := json.Marshal(scope.GroupRefs)
	if err != nil {
		return err
	}
	var valid bool
	err = r.db.QueryRow(ctx, scopedLatestGroupsSQL+`
 SELECT NOT EXISTS(SELECT 1 FROM refs WHERE
 (SELECT count(*) FROM latest WHERE site_id=refs."siteId" AND
 ((latest.group_id<>'' AND refs."groupId"<>'' AND latest.group_id=refs."groupId") OR
 ((latest.group_id='' OR refs."groupId"='') AND latest.group_name=refs."groupName"))) <> 1)
 `, userID, workspaceID, raw).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return scopedNotFound()
	}
	return nil
}

// ListScopedRealConnections includes saved missing/legacy records, while the
// runtime ListRealConnectionsForWorkspace keeps its original active filter.
func (r *Repository) ListScopedRealConnections(ctx context.Context, userID, workspaceID string, refs []ScopedGroupRef) ([]RealConnection, error) {
	scope := ScopedConnectionScope{GroupRefs: refs}
	if err := r.validateScopedOwnership(ctx, userID, workspaceID, scope); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []RealConnection{}, nil
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	query := strings.Replace(listRealConnectionsSQL, "ORDER BY connection_row.created_at DESC", `
 AND EXISTS (SELECT 1 FROM jsonb_to_recordset($3::jsonb) AS ref("siteId" text,"groupId" text,"groupName" text)
 WHERE connection_row.upstream_site_id=ref."siteId" AND
 ((connection_row.upstream_group_id<>'' AND ref."groupId"<>'' AND connection_row.upstream_group_id=ref."groupId") OR
 ((connection_row.upstream_group_id='' OR ref."groupId"='') AND connection_row.upstream_group_name=ref."groupName")))
 ORDER BY connection_row.created_at DESC,connection_row.id`, 1)
	rows, err := r.db.Query(ctx, query, userID, workspaceID, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RealConnection, 0)
	for rows.Next() {
		conn, err := scanListedRealConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *conn)
	}
	return result, rows.Err()
}

func (r *Repository) ReadScopedConnectionSnapshots(ctx context.Context, userID, workspaceID string, scope ScopedConnectionScope) ([]ScopedConnectionSnapshot, error) {
	if err := r.validateScopedOwnership(ctx, userID, workspaceID, scope); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(scope.GroupRefs)
	if err != nil {
		return nil, err
	}
	if scope.GroupRefs == nil {
		raw = []byte("[]")
	}
	rows, err := r.db.Query(ctx, `
 SELECT id,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,
 admin_account_id,admin_account_name,admin_platform,status,xmin::text
 FROM real_connections AS connection_row
 WHERE user_id=$1 AND workspace_admin_account_id=$2 AND
 (($3<>'' AND upstream_site_id=$3) OR ($3='' AND EXISTS(
 SELECT 1 FROM jsonb_to_recordset($4::jsonb) AS ref("siteId" text,"groupId" text,"groupName" text)
 WHERE upstream_site_id=ref."siteId" AND
 ((upstream_group_id<>'' AND ref."groupId"<>'' AND upstream_group_id=ref."groupId") OR
 ((upstream_group_id='' OR ref."groupId"='') AND upstream_group_name=ref."groupName")))))
 ORDER BY id`, userID, workspaceID, scope.SiteID, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ScopedConnectionSnapshot, 0)
	for rows.Next() {
		var row ScopedConnectionSnapshot
		if err := rows.Scan(&row.ID, &row.UserID, &row.WorkspaceAdminAccountID, &row.UpstreamSiteID, &row.UpstreamGroupID, &row.UpstreamGroupName, &row.AdminAccountID, &row.AdminAccountName, &row.AdminPlatform, &row.Status, &row.Xmin); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *Repository) ApplyScopedConnectionStatuses(ctx context.Context, snapshots []ScopedConnectionSnapshot, determined map[string]string) (ScopedStatusApplyResult, error) {
	result := ScopedStatusApplyResult{ConfirmedConnectionIDs: make([]string, 0), ChangedConnectionIDs: make([]string, 0)}
	if len(determined) == 0 {
		return result, nil
	}
	ordered := append([]ScopedConnectionSnapshot{}, snapshots...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return result, &ScopedStorageError{Cause: err}
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	for _, snapshot := range ordered {
		status, known := determined[snapshot.ID]
		if !known {
			continue
		}
		if status != ConnectionStatusActive && status != ConnectionStatusMissing {
			return ScopedStatusApplyResult{}, &ScopedStorageError{Cause: fmt.Errorf("invalid scoped connection status")}
		}
		var current ScopedConnectionSnapshot
		err = tx.QueryRow(ctx, `SELECT id,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,admin_account_id,admin_account_name,admin_platform,status,xmin::text FROM real_connections WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3 FOR UPDATE`, snapshot.ID, snapshot.UserID, snapshot.WorkspaceAdminAccountID).Scan(&current.ID, &current.UserID, &current.WorkspaceAdminAccountID, &current.UpstreamSiteID, &current.UpstreamGroupID, &current.UpstreamGroupName, &current.AdminAccountID, &current.AdminAccountName, &current.AdminPlatform, &current.Status, &current.Xmin)
		if errors.Is(err, pgx.ErrNoRows) {
			result.ChangedConnectionIDs = append(result.ChangedConnectionIDs, snapshot.ID)
			continue
		}
		if err != nil {
			return ScopedStatusApplyResult{ChangedConnectionIDs: result.ChangedConnectionIDs}, &ScopedStorageError{Cause: err}
		}
		if current != snapshot {
			result.ChangedConnectionIDs = append(result.ChangedConnectionIDs, snapshot.ID)
			continue
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE real_connections SET status=$11 WHERE id=$1 AND user_id=$2 AND workspace_admin_account_id=$3 AND upstream_site_id=$4 AND upstream_group_id=$5 AND upstream_group_name=$6 AND admin_account_id=$7 AND admin_platform=$8 AND status=$9 AND xmin::text=$10`, snapshot.ID, snapshot.UserID, snapshot.WorkspaceAdminAccountID, snapshot.UpstreamSiteID, snapshot.UpstreamGroupID, snapshot.UpstreamGroupName, snapshot.AdminAccountID, snapshot.AdminPlatform, snapshot.Status, snapshot.Xmin, status)
		if updateErr != nil {
			return ScopedStatusApplyResult{ChangedConnectionIDs: result.ChangedConnectionIDs}, &ScopedStorageError{Cause: updateErr}
		}
		if tag.RowsAffected() != 1 {
			result.ChangedConnectionIDs = append(result.ChangedConnectionIDs, snapshot.ID)
			continue
		}
		result.ConfirmedConnectionIDs = append(result.ConfirmedConnectionIDs, snapshot.ID)
	}
	if err := ctx.Err(); err != nil {
		return ScopedStatusApplyResult{ChangedConnectionIDs: result.ChangedConnectionIDs}, &ScopedStorageError{Cause: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return ScopedStatusApplyResult{ChangedConnectionIDs: result.ChangedConnectionIDs}, &ScopedStorageError{Uncertain: !errors.Is(err, pgx.ErrTxCommitRollback), Cause: err}
	}
	return result, nil
}
