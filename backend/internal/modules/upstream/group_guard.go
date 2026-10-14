package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type groupContent struct {
	ID, Name, Mode, Platform string
	Multiplier               *float64
}

func canonicalGroups(groups []GroupInfo) []groupContent {
	result := make([]groupContent, 0, len(groups))
	for _, g := range groups {
		mode := g.MultiplierMode
		if mode == "" && g.Multiplier != nil {
			mode = "fixed"
		}
		platform := ""
		if g.Platform != nil {
			platform = *g.Platform
		}
		result = append(result, groupContent{g.ID, g.Name, mode, platform, g.Multiplier})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Mode != b.Mode {
			return a.Mode < b.Mode
		}
		return a.Platform < b.Platform
	})
	return result
}
func unchangedGroupBaseline(current, baseline Site) bool {
	return reflect.DeepEqual(current.LastSyncedAt, baseline.LastSyncedAt) && reflect.DeepEqual(canonicalGroups(current.Metrics.Groups), canonicalGroups(baseline.Metrics.Groups)) && reflect.DeepEqual(canonicalGroups([]GroupInfo{current.Metrics.Group}), canonicalGroups([]GroupInfo{baseline.Metrics.Group}))
}
func keepCurrentGroupResult(next *Site, current Site) {
	next.Metrics.Groups = current.Metrics.Groups
	next.Metrics.Group = current.Metrics.Group
	next.LastSyncedAt = current.LastSyncedAt
	next.Status = current.Status
	next.ErrorKey = current.ErrorKey
}

type groupPublication struct {
	Baseline       Site
	Actual         Site
	RowVersion     string
	GroupsAccepted bool
}
type groupGuardRepository interface {
	SaveExistingSiteWithGroupGuard(context.Context, Site, Site, Site) (Site, string, bool, bool, error)
}
type snapshotVersionRepository interface {
	SaveSiteWithSnapshotVersion(context.Context, Site) (string, error)
}

func (r *Repository) SaveExistingSiteWithGroupGuard(ctx context.Context, expected, candidate, baseline Site) (actualSite Site, sourceVersion string, saved bool, groupsAccepted bool, resultErr error) {
	if candidate.ID != expected.ID || candidate.UserID != expected.UserID || candidate.AdminAccountID != expected.AdminAccountID {
		return Site{}, "", false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Site{}, "", false, false, err
	}
	defer rollbackSnapshotSiteTransaction(tx, &resultErr)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return Site{}, "", false, false, err
	}
	var parent string
	if err = tx.QueryRow(ctx, `SELECT id FROM admin_accounts WHERE user_id=$1 AND id=$2 FOR KEY SHARE`, expected.UserID, expected.AdminAccountID).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return Site{}, "", false, false, nil
	} else if err != nil {
		return Site{}, "", false, false, err
	}
	var current Site
	var rawMetrics, rawSession, rawSettings []byte
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id,user_id,admin_account_id,name,base_url,platform,requested_platform,account,remark,recharge_rate,status,error_key,metrics,session,settings,last_synced_at,enabled FROM upstream_sites WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 FOR UPDATE`, expected.ID, expected.UserID, expected.AdminAccountID).Scan(&current.ID, &current.UserID, &current.AdminAccountID, &current.Name, &current.BaseURL, &current.Platform, &current.RequestedPlatform, &current.Account, &current.Remark, &current.RechargeRate, &current.Status, &current.ErrorKey, &rawMetrics, &rawSession, &rawSettings, &current.LastSyncedAt, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, "", false, false, nil
	}
	if err != nil {
		return Site{}, "", false, false, err
	}
	current.Enabled = boolPointer(enabled)
	if err = json.Unmarshal(rawMetrics, &current.Metrics); err != nil {
		return Site{}, "", false, false, err
	}
	if len(rawSession) > 0 {
		if err = json.Unmarshal(rawSession, &current.Session); err != nil {
			return Site{}, "", false, false, err
		}
	}
	if err = json.Unmarshal(rawSettings, &current.Settings); err != nil {
		return Site{}, "", false, false, err
	}
	if !unchangedSiteConfiguration(current, expected) {
		return Site{}, "", false, false, nil
	}
	accepted := unchangedGroupBaseline(current, baseline)
	next := mergeExistingSiteChanges(current, expected, candidate)
	if !accepted {
		keepCurrentGroupResult(&next, current)
	}
	rawMetrics, err = json.Marshal(next.Metrics)
	if err != nil {
		return Site{}, "", false, false, err
	}
	rawSession = nil
	if next.Session != nil {
		rawSession, err = json.Marshal(next.Session)
		if err != nil {
			return Site{}, "", false, false, err
		}
	}
	rawSettings, err = json.Marshal(next.Settings)
	if err != nil {
		return Site{}, "", false, false, err
	}
	var version string
	err = tx.QueryRow(ctx, `UPDATE upstream_sites SET name=$4,base_url=$5,platform=$6,requested_platform=$7,account=$8,remark=$9,recharge_rate=$10,status=$11,error_key=$12,metrics=$13::jsonb,session=$14::jsonb,settings=$15::jsonb,last_synced_at=$16,enabled=$17,updated_at=$18 WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 RETURNING xmin::text`, next.ID, next.UserID, next.AdminAccountID, next.Name, next.BaseURL, next.Platform, next.RequestedPlatform, next.Account, next.Remark, next.RechargeRate, next.Status, next.ErrorKey, string(rawMetrics), nullableJSONString(rawSession), string(rawSettings), next.LastSyncedAt, next.IsEnabled(), time.Now()).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, "", false, false, nil
	}
	if err != nil {
		return Site{}, "", false, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Site{}, "", false, false, &siteCommitError{err: err}
	}
	return next, version, true, accepted, nil
}

func staleGroupsWarning() *GroupRatesWarning {
	return &GroupRatesWarning{Message: "分组资料已由另一次同步更新", Detail: "本次旧分组结果未写入或清理，已保留先保存的分组资料；独立费用结果见本站费用说明", Next: "请查看本站当前分组，仍需更新时重新同步网站"}
}
func (s *Service) saveSiteWithSnapshotVersion(ctx context.Context, site *Site) (string, error) {
	if site == nil {
		return "", nil
	}
	if s.isSiteDeleted(site.ID) {
		return "", newRequestError(ErrorNotFound, "")
	}
	if s.repository == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()
	if repository, ok := s.repository.(snapshotVersionRepository); ok {
		return repository.SaveSiteWithSnapshotVersion(ctx, *site)
	}
	return "", s.repository.SaveSite(ctx, *site)
}
func auditWarning(siteName string, audit *GroupSnapshotAudit) *GroupRatesWarning {
	if audit == nil {
		return &GroupRatesWarning{Message: siteName + "的分组倍率未更新", Detail: "本次没有取得可核验的分组读取结果；旧倍率和历史仍保留，本轮未清理", Next: "请同步重试并核查上游分组列表"}
	}
	if !audit.AllowUpdate {
		return &GroupRatesWarning{Message: siteName + "的分组倍率未更新", Detail: audit.Issue + "；旧倍率和历史仍保留，本轮未清理", Next: "请同步重试并核查上游分组列表"}
	}
	if !audit.AllowCleanup {
		return &GroupRatesWarning{Message: siteName + "的价格返回尚不能确认完整分组目录", Detail: "本次只处理能确认的倍率，不清理未返回组的历史", Next: "请到上游管理核查价格目录，保存结果见每行更新时间"}
	}
	return nil
}
func safeSnapshotCause(err error) string {
	if err == nil {
		return "原因尚未确认"
	}
	// SQL diagnostics may contain connection strings or raw row values.
	var pgError interface{ SQLState() string }
	if errors.As(err, &pgError) {
		return "数据库执行失败（SQLSTATE " + pgError.SQLState() + "），请维护人员核查倍率保存日志"
	}
	cause := safePersistenceCause(err)
	if strings.Contains(cause, "password") || strings.Contains(cause, "token") || strings.Contains(cause, "://") {
		return "存储连接或写入失败，请维护人员检查倍率保存日志"
	}
	return cause
}

func rollbackSnapshotSiteTransaction(tx pgx.Tx, resultErr *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*resultErr = &siteCommitError{err: errors.Join(*resultErr, errors.New("数据库事务回滚结果尚未确认"))}
	}
}
