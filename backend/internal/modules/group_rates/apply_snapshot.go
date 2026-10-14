package group_rates

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SnapshotSaveError distinguishes a failed transaction from an unconfirmed commit.
type SnapshotSaveError struct {
	Cause     error
	Uncertain bool
}

func (e *SnapshotSaveError) Error() string {
	if e.Uncertain {
		return "倍率数据库提交结果尚未确认"
	}
	return "倍率保存失败：" + e.Cause.Error()
}
func (e *SnapshotSaveError) Unwrap() error                 { return e.Cause }
func (e *SnapshotSaveError) UncertainSnapshotResult() bool { return e.Uncertain }

func snapshotMode(group SnapshotGroup) string {
	if group.MultiplierMode == "" && group.Multiplier != nil {
		return "fixed"
	}
	return group.MultiplierMode
}
func validateSnapshotGroups(groups []SnapshotGroup) error {
	ids, names := map[string]bool{}, map[string]int{}
	for _, g := range groups {
		if g.Name == "" || strings.TrimSpace(g.Name) != g.Name || strings.TrimSpace(g.ID) != g.ID {
			return errors.New("分组名称或身份缺失、含首尾空格，本轮倍率未更新或清理，请核查上游分组列表")
		}
		mode := snapshotMode(g)
		if mode != "fixed" && mode != "auto" {
			return errors.New("分组倍率方式无法确认，本轮倍率未更新或清理，请核查上游分组列表")
		}
		if mode == "fixed" && (g.Multiplier == nil || math.IsNaN(*g.Multiplier) || math.IsInf(*g.Multiplier, 0) || *g.Multiplier < 0) || mode == "auto" && g.Multiplier != nil {
			return errors.New("分组固定倍率缺失、数值无效或与auto方式矛盾，本轮倍率未更新或清理，请核查上游分组列表")
		}
		if g.ID != "" {
			if ids[g.ID] {
				return errors.New("分组编号重复，本轮倍率未更新或清理，请核查上游分组列表")
			}
			ids[g.ID] = true
		}
		names[g.Name]++
	}
	for _, g := range groups {
		if g.ID == "" && names[g.Name] != 1 {
			return errors.New("缺少编号的同名分组无法唯一对应，本轮倍率未更新或清理，请核查上游分组列表")
		}
	}
	return nil
}

type snapshotIdentity struct {
	ID         string
	Name       string
	Mode       string
	Multiplier *float64
}

func snapshotContents(groups []SnapshotGroup) []snapshotIdentity {
	result := make([]snapshotIdentity, 0, len(groups))
	for _, g := range groups {
		result = append(result, snapshotIdentity{g.ID, g.Name, snapshotMode(g), g.Multiplier})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result
}
func staleSnapshotError() error {
	return errors.New("本站资料已变化，本轮倍率未更新或清理，请重新同步")
}
func snapshotKey(id, name string) string {
	if id != "" {
		return "id:" + id
	}
	return "name:" + name
}

func (r *Repository) ApplySiteSnapshot(ctx context.Context, userID, workspaceID, siteID, siteName, sitePlatform string, groups []SnapshotGroup, lastSyncedAt int64, sourceRowVersion string, allowCleanup bool) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return &SnapshotSaveError{Cause: err}
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rollbackCancel()
		rollbackErr := tx.Rollback(rollbackCtx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = &SnapshotSaveError{Cause: errors.Join(resultErr, errors.New("数据库事务回滚结果尚未确认")), Uncertain: true}
		}
		if resultErr != nil {
			var known *SnapshotSaveError
			if !errors.As(resultErr, &known) {
				resultErr = &SnapshotSaveError{Cause: resultErr}
			}
		}
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return err
	}
	var parent string
	if err = tx.QueryRow(ctx, `SELECT id FROM admin_accounts WHERE user_id=$1 AND id=$2 FOR KEY SHARE`, userID, workspaceID).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return staleSnapshotError()
	} else if err != nil {
		return err
	}
	var platform, version string
	var storedAt *int64
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT platform,xmin::text,last_synced_at,metrics FROM upstream_sites WHERE user_id=$1 AND admin_account_id=$2 AND id=$3 FOR UPDATE`, userID, workspaceID, siteID).Scan(&platform, &version, &storedAt, &raw); errors.Is(err, pgx.ErrNoRows) {
		return staleSnapshotError()
	} else if err != nil {
		return err
	}
	var metrics struct {
		Groups []SnapshotGroup `json:"groups"`
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil || len(shape["groups"]) == 0 || string(shape["groups"]) == "null" || json.Unmarshal(raw, &metrics) != nil {
		return errors.New("本站已保存的分组资料无法解析，倍率未更新或清理，请核查Hub站点保存记录")
	}
	if platform != sitePlatform || version != sourceRowVersion || storedAt == nil || *storedAt != lastSyncedAt || !reflect.DeepEqual(snapshotContents(metrics.Groups), snapshotContents(groups)) {
		return staleSnapshotError()
	}
	if err = validateSnapshotGroups(metrics.Groups); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id,group_id,group_name,type,multiplier,created_at FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 ORDER BY created_at DESC,id DESC FOR UPDATE`, userID, workspaceID, siteID)
	if err != nil {
		return err
	}
	var history []snapshotRecord
	for rows.Next() {
		var v snapshotRecord
		if err = rows.Scan(&v.ID, &v.GroupID, &v.GroupName, &v.Type, &v.Multiplier, &v.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		history = append(history, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	latest := map[string]snapshotRecord{}
	for _, v := range history {
		key := snapshotKey(v.GroupID, v.GroupName)
		if _, exists := latest[key]; !exists {
			latest[key] = v
		}
	}
	incomingNames := map[string]int{}
	for _, g := range groups {
		incomingNames[g.Name]++
	}
	// Legacy empty identities can join a current ID only when its name is unique.
	for _, g := range groups {
		if g.ID == "" {
			continue
		}
		key := snapshotKey("", g.Name)
		old, hasLegacy := latest[key]
		if !hasLegacy {
			continue
		}
		if incomingNames[g.Name] != 1 {
			return errors.New("旧记录同名分组无法唯一对应，本轮倍率未更新或清理，请核查分组编号")
		}
		for _, v := range latest {
			if v.GroupName == g.Name && v.GroupID != "" && v.GroupID != g.ID {
				return errors.New("旧记录包含不同编号的同名分组，本轮倍率未更新或清理，请核查分组编号")
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE group_rate_snapshots SET group_id=$4 WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND group_id='' AND group_name=$5`, userID, workspaceID, siteID, g.ID, g.Name); err != nil {
			return err
		}
		delete(latest, key)
		newKey := snapshotKey(g.ID, g.Name)
		old.GroupID = g.ID
		prev, exists := latest[newKey]
		if !exists || old.CreatedAt.After(prev.CreatedAt) || old.CreatedAt.Equal(prev.CreatedAt) && old.ID > prev.ID {
			latest[newKey] = old
		}
	}
	resolved := make([]SnapshotGroup, 0, len(groups))
	for _, g := range groups {
		if g.ID == "" {
			var candidates []snapshotRecord
			for _, v := range latest {
				if v.GroupName == g.Name {
					candidates = append(candidates, v)
				}
			}
			if len(candidates) > 1 {
				return errors.New("缺少编号的分组对应多条同名旧记录，本轮倍率未更新或清理，请核查分组编号")
			}
			if len(candidates) == 1 {
				g.ID = candidates[0].GroupID
			}
		}
		resolved = append(resolved, g)
	}
	if err = validateSnapshotGroups(resolved); err != nil {
		return err
	}
	now := time.UnixMilli(lastSyncedAt)
	for _, g := range resolved {
		key := snapshotKey(g.ID, g.Name)
		if snapshotMode(g) == "auto" {
			if _, err = tx.Exec(ctx, `DELETE FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND ((group_id<>'' AND $4<>'' AND group_id=$4) OR ((group_id='' OR $4='') AND group_name=$5))`, userID, workspaceID, siteID, g.ID, g.Name); err != nil {
				return err
			}
			continue
		}
		previous, exists := latest[key]
		if exists && previous.Multiplier == *g.Multiplier {
			if _, err = tx.Exec(ctx, `UPDATE group_rate_snapshots SET last_seen_at=$5,site_name=$6,group_name=$7,deleted=false WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND id=$4`, userID, workspaceID, siteID, previous.ID, now, siteName, g.Name); err != nil {
				return err
			}
			continue
		}
		category := ""
		if exists {
			category = previous.Type
		} else if g.Platform != nil {
			category = strings.TrimSpace(*g.Platform)
		}
		createdAt := time.Now()
		if exists && !createdAt.After(previous.CreatedAt) {
			createdAt = previous.CreatedAt.Add(time.Microsecond)
		}
		id, idErr := newSnapshotID()
		if idErr != nil {
			return idErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO group_rate_snapshots(id,user_id,admin_account_id,site_id,site_name,group_id,group_name,platform,type,multiplier,created_at,last_seen_at,deleted) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,false)`, id, userID, workspaceID, siteID, siteName, g.ID, g.Name, sitePlatform, category, *g.Multiplier, createdAt, now); err != nil {
			return err
		}
	}
	if allowCleanup {
		ids, names := []string{}, []string{}
		for _, g := range resolved {
			if snapshotMode(g) != "fixed" {
				continue
			}
			if g.ID != "" {
				ids = append(ids, g.ID)
			} else {
				names = append(names, g.Name)
			}
		}
		if _, err = tx.Exec(ctx, `DELETE FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3 AND NOT ((group_id<>'' AND group_id=ANY($4::text[])) OR (group_id='' AND group_name=ANY($5::text[])))`, userID, workspaceID, siteID, ids, names); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return &SnapshotSaveError{Cause: err, Uncertain: true}
	}
	return nil
}
