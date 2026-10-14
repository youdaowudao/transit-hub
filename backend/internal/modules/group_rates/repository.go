package group_rates

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoEditableSnapshot = errors.New("该分组已没有可编辑倍率记录，请同步或重新选择")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) EnsureSchema(ctx context.Context) error {
	// group_rate_snapshots deliberately has no foreign key to upstream sites: those sites are
	// stored in memory, while rate history must survive process restarts independently.
	if _, err := r.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS group_rate_snapshots (
			id text PRIMARY KEY,
			user_id text NOT NULL DEFAULT '',
			site_id text NOT NULL,
			site_name text NOT NULL,
			group_name text NOT NULL,
			platform text NOT NULL,
			type text NOT NULL DEFAULT '',
			multiplier double precision NOT NULL,
			created_at timestamptz NOT NULL,
			last_seen_at timestamptz
		)
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		UPDATE group_rate_snapshots SET platform = '' WHERE platform IS NULL
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ALTER COLUMN platform SET NOT NULL
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS type text NOT NULL DEFAULT ''
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT ''
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS admin_account_id text NOT NULL DEFAULT ''
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS group_id text NOT NULL DEFAULT ''
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		UPDATE group_rate_snapshots AS snapshots
		SET user_id = sites.user_id
		FROM upstream_sites AS sites
		WHERE snapshots.user_id = '' AND snapshots.site_id = sites.id AND sites.user_id <> ''
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		UPDATE group_rate_snapshots SET type = '' WHERE type IN ('newapi', 'sub2api')
	`); err != nil {
		return err
	}
	// An empty category may be a user-saved choice. Historical nonempty values
	// cannot distinguish that choice from legacy absence, so never infer it here.
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_group_latest
		ON group_rate_snapshots (user_id, site_id, group_id, group_name, created_at DESC)
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_latest_lookup
		ON group_rate_snapshots (user_id, site_id, group_id, group_name, created_at DESC, id DESC)
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_history_lookup
		ON group_rate_snapshots (user_id, site_id, group_name, platform, created_at DESC, id DESC)
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_user_created
		ON group_rate_snapshots (user_id, created_at DESC)
	`); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_multiplier
		ON group_rate_snapshots (user_id, multiplier)
	`); err != nil {
		return err
	}
	// 已删除标记：上游同步时分组消失，将最新快照标记为 deleted，后续不再为其生成新快照。
	if _, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS deleted boolean NOT NULL DEFAULT false
	`); err != nil {
		return err
	}
	// created_at records a multiplier change; last_seen_at records the latest
	// successful synchronization. Existing rows remain valid through COALESCE.
	_, err := r.db.Exec(ctx, `
		ALTER TABLE group_rate_snapshots ADD COLUMN IF NOT EXISTS last_seen_at timestamptz
	`)
	return err
}

// LatestGroupKeysForSite 返回指定站点每个分组最新一行的 ID、group_id、group_name、倍率和 deleted 状态，
// 用于在同步时判断哪些分组已消失以及倍率是否发生变化。
func (r *Repository) LatestGroupKeysForSite(ctx context.Context, userID string, adminAccountID string, siteID string) ([]latestGroupKey, error) {
	rows, err := r.db.Query(ctx, `
		WITH ranked AS (
			SELECT
				id,
				group_id,
				group_name,
				deleted,
				multiplier,
				ROW_NUMBER() OVER (
					PARTITION BY COALESCE(NULLIF(group_id, ''), group_name)
					ORDER BY created_at DESC, id DESC
				) AS row_number
			FROM group_rate_snapshots
			WHERE user_id = $1 AND admin_account_id = $2 AND site_id = $3
		)
		SELECT id, group_id, group_name, deleted, multiplier
		FROM ranked
		WHERE row_number = 1
	`, userID, adminAccountID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []latestGroupKey
	for rows.Next() {
		var k latestGroupKey
		if err := rows.Scan(&k.ID, &k.GroupID, &k.GroupName, &k.Deleted, &k.Multiplier); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// MarkDeleted 将指定 ID 的快照行标记为已删除。
func (r *Repository) MarkDeleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		UPDATE group_rate_snapshots SET deleted = true, last_seen_at = NOW() WHERE id = ANY($1)
	`, ids)
	return err
}

// TouchSnapshots 刷新指定快照的同步时间和站点名称，用于倍率未变时更新"最后确认"时间戳，
// 避免插入重复行导致涨跌幅只展示一个同步周期。
func (r *Repository) TouchSnapshots(ctx context.Context, ids []string, siteName string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		UPDATE group_rate_snapshots SET last_seen_at = $2, site_name = $3 WHERE id = ANY($1)
	`, ids, now, siteName)
	return err
}

func (r *Repository) InsertSnapshots(ctx context.Context, records []snapshotRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, record := range records {
		if _, err := tx.Exec(ctx, `
			INSERT INTO group_rate_snapshots (id, user_id, admin_account_id, site_id, site_name, group_id, group_name, platform, type, multiplier, created_at, last_seen_at)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11
			WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = $2 AND id = $3)
		`, record.ID, record.UserID, record.AdminAccountID, record.SiteID, record.SiteName, record.GroupID, record.GroupName, record.Platform, record.Type, record.Multiplier, record.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) List(ctx context.Context, userID string, adminAccountID string, query ListQuery) (listRecords, error) {
	query = normalizeListQuery(query)
	var result listRecords
	var raw []byte
	err := r.db.QueryRow(ctx, `
		WITH enriched AS (
			SELECT
				snapshots.id,
				snapshots.user_id,
				snapshots.site_id,
				snapshots.site_name,
				snapshots.group_id,
				snapshots.group_name,
				snapshots.platform,
				snapshots.type,
				snapshots.multiplier,
				snapshots.deleted,
				COALESCE(sites.recharge_rate, 1) AS recharge_rate,
				snapshots.created_at,
				COALESCE(snapshots.last_seen_at, snapshots.created_at) AS last_seen_at,
				COALESCE(sites.base_url, snapshots.site_id) AS site_key,
				COALESCE(NULLIF(snapshots.group_id, ''), snapshots.group_name) AS group_key
			FROM group_rate_snapshots AS snapshots
			LEFT JOIN upstream_sites AS sites
				ON sites.user_id = snapshots.user_id
				AND sites.id = snapshots.site_id
			WHERE snapshots.user_id = $1 AND snapshots.admin_account_id = $2
		), ranked AS (
			SELECT
				id,
				user_id,
				site_id,
				site_name,
				group_id,
				group_name,
				platform,
				type,
				multiplier,
				deleted,
				recharge_rate,
				created_at,
				last_seen_at,
				ROW_NUMBER() OVER (
					PARTITION BY user_id, site_key, group_key
					ORDER BY created_at DESC, id DESC
				) AS row_number,
				LEAD(multiplier) OVER (
					PARTITION BY user_id, site_key, group_key
					ORDER BY created_at DESC, id DESC
				) AS previous_multiplier
			FROM enriched
		), latest AS (
			SELECT id, user_id, site_id, site_name, group_id, group_name, platform, type, multiplier, deleted, recharge_rate, created_at, last_seen_at, previous_multiplier
			FROM ranked
			WHERE row_number = 1 AND NOT deleted
		), mapped AS (
			SELECT latest.*, (
				EXISTS (
					SELECT 1
					FROM real_connections AS connections
					WHERE connections.user_id = latest.user_id
						AND connections.workspace_admin_account_id = $2
						AND (connections.status = '' OR connections.status = 'active')
						AND connections.upstream_site_id = latest.site_id
						AND (
							(connections.upstream_group_id <> '' AND latest.group_id <> '' AND connections.upstream_group_id = latest.group_id)
							OR ((connections.upstream_group_id = '' OR latest.group_id = '')
								AND connections.upstream_group_name = latest.group_name)
						)
				)
			) AS mapped, (
				EXISTS (
					SELECT 1
					FROM my_site_states AS states
					CROSS JOIN LATERAL jsonb_array_elements(
						CASE WHEN jsonb_typeof(states.mappings) = 'array' THEN states.mappings ELSE '[]'::jsonb END
					) AS mapping
					CROSS JOIN LATERAL jsonb_array_elements(
						CASE WHEN jsonb_typeof(mapping->'upstreamTargets') = 'array' THEN mapping->'upstreamTargets' ELSE '[]'::jsonb END
					) AS target
					WHERE states.user_id = latest.user_id
						AND states.admin_account_id = $2
						AND target->>'siteId' = latest.site_id
						AND target->>'groupName' = latest.group_name
				)
			) AS pricing_mapped
			FROM latest
		), base_filtered AS (
			SELECT *
			FROM mapped
			WHERE ($3 = '' OR lower(site_name) LIKE '%' || $3 || '%' OR lower(group_name) LIKE '%' || $3 || '%' OR lower(platform) LIKE '%' || $3 || '%' OR lower(type) LIKE '%' || $3 || '%')
				AND ($4 = '' OR type = $4)
				AND ($5 = '' OR platform = $5)
				AND ($8 = '' OR site_id = $8)
		), filtered AS (
			SELECT *
			FROM base_filtered
			WHERE ($6 = 'legacy')
				OR ($6 = 'all' AND NOT deleted)
				OR ($6 = 'mapped' AND mapped AND NOT deleted)
				OR ($6 = 'unmapped' AND NOT mapped AND NOT deleted)
				OR ($6 = 'deleted' AND deleted)
		), facets AS (
			SELECT
				COALESCE(array_agg(DISTINCT type ORDER BY type) FILTER (WHERE type <> ''), ARRAY[]::text[]) AS types,
				COALESCE(array_agg(DISTINCT platform ORDER BY platform) FILTER (WHERE platform <> ''), ARRAY[]::text[]) AS platforms
			FROM latest
		), counted AS (SELECT count(*)::int AS total FROM filtered),
		counts AS (
			SELECT count(*) FILTER (WHERE NOT deleted)::int AS all_count,
			count(*) FILTER (WHERE mapped AND NOT deleted)::int AS mapped_count,
			count(*) FILTER (WHERE NOT mapped AND NOT deleted)::int AS unmapped_count,
			count(*) FILTER (WHERE deleted)::int AS deleted_count FROM base_filtered
		), ordered AS (
			SELECT filtered.*, row_number() OVER (ORDER BY CASE WHEN $7 = 'multiplierAsc' THEN multiplier * recharge_rate END ASC NULLS LAST,
				CASE WHEN $7 = 'multiplierDesc' THEN multiplier * recharge_rate END DESC NULLS LAST,
				CASE WHEN $7 = 'siteNameAsc' THEN lower(site_name) END ASC,
				CASE WHEN $7 = 'groupNameAsc' THEN lower(group_name) END ASC,
				CASE WHEN $7 = 'default' THEN CASE WHEN mapped THEN 1 ELSE 0 END END DESC,
				CASE WHEN $7 = 'default' THEN multiplier * recharge_rate END ASC NULLS LAST,
				site_name ASC, group_name ASC, platform ASC, type ASC, site_id ASC, group_id ASC, id ASC) AS position FROM filtered
		), focused AS (
			SELECT count(*)::int AS matches, min(position) AS position FROM ordered
			WHERE ($11 <> '' AND group_id = $11) OR ($11 = '' AND $12 <> '' AND group_name = $12)
		), target AS (
			SELECT CASE WHEN $11 <> '' OR $12 <> '' THEN
				CASE WHEN focused.matches = 1 THEN ((focused.position - 1) / $9 + 1)::int ELSE 1 END
				ELSE least(greatest($10,1), greatest((counted.total + $9 - 1) / $9,1)) END AS page,
				CASE WHEN $11 <> '' OR $12 <> '' THEN focused.matches = 1 ELSE NULL END AS focus_found
			FROM focused CROSS JOIN counted
		), page_rows AS (
			SELECT ordered.* FROM ordered CROSS JOIN target
			WHERE position > (target.page::bigint - 1) * $9 AND position <= target.page::bigint * $9
		)
		SELECT counted.total, facets.types, facets.platforms, counts.all_count, counts.mapped_count,
			counts.unmapped_count, counts.deleted_count, target.page, target.focus_found,
			COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'ID', id, 'UserID', user_id, 'SiteID', site_id, 'SiteName', site_name, 'GroupID', group_id,
				'GroupName', group_name, 'Platform', platform, 'Type', type, 'Mapped', mapped,
				'PricingMapped', pricing_mapped, 'Deleted', deleted, 'Multiplier', multiplier,
				'RechargeRate', recharge_rate, 'CreatedAt', created_at, 'LastSeenAt', last_seen_at,
				'PreviousMultiplier', previous_multiplier) ORDER BY position) FROM page_rows), '[]'::jsonb)
		FROM counted CROSS JOIN facets CROSS JOIN counts CROSS JOIN target
	`, userID, adminAccountID, strings.ToLower(query.Search), query.Type, query.Platform,
		query.Status, query.Sort, query.SiteID, query.PageSize, query.Page, query.FocusGroupID, query.FocusGroup).Scan(
		&result.Total, &result.Types, &result.Platforms, &result.StatusCounts.All, &result.StatusCounts.Mapped,
		&result.StatusCounts.Unmapped, &result.StatusCounts.Deleted, &result.Page, &result.FocusFound, &raw)
	if err != nil {
		return listRecords{}, err
	}
	if err := json.Unmarshal(raw, &result.Items); err != nil {
		return listRecords{}, err
	}
	return result, nil
}

// ListDistinctGroupNames 返回按 search/type/platform 匹配的最新分组快照的去重分组名列表，
// 供 group_rate_campaigns 模块的 "按分组类型"/"当前筛选结果" 选择模式与 admin 自有分组名取交集。
// 只看每个站点+分组的最新一行（与 List 的 ranked/latest CTE 同一语义），已删除分组不参与匹配。
func (r *Repository) ListDistinctGroupNames(ctx context.Context, userID string, adminAccountID string, search string, groupType string, platform string) ([]string, error) {
	search = strings.ToLower(strings.TrimSpace(search))
	groupType = strings.TrimSpace(groupType)
	platform = strings.TrimSpace(platform)
	rows, err := r.db.Query(ctx, `
		WITH enriched AS (
			SELECT
				snapshots.group_name,
				snapshots.platform,
				snapshots.type,
				snapshots.deleted,
				snapshots.created_at,
				snapshots.id,
				COALESCE(sites.base_url, snapshots.site_id) AS site_key,
				COALESCE(NULLIF(snapshots.group_id, ''), snapshots.group_name) AS group_key
			FROM group_rate_snapshots AS snapshots
			LEFT JOIN upstream_sites AS sites
				ON sites.user_id = snapshots.user_id
				AND sites.id = snapshots.site_id
			WHERE snapshots.user_id = $1 AND snapshots.admin_account_id = $2
		), ranked AS (
			SELECT group_name, platform, type, deleted,
				ROW_NUMBER() OVER (PARTITION BY site_key, group_key ORDER BY created_at DESC, id DESC) AS row_number
			FROM enriched
		)
		SELECT DISTINCT group_name
		FROM ranked
		WHERE row_number = 1
			AND NOT deleted
			AND ($3 = '' OR lower(group_name) LIKE '%' || $3 || '%')
			AND ($4 = '' OR type = $4)
			AND ($5 = '' OR platform = $5)
	`, userID, adminAccountID, search, groupType, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (r *Repository) UpdateType(ctx context.Context, userID string, adminAccountID string, ref GroupRef, groupType string) error {
	if userID == "" || adminAccountID == "" || ref.SiteID == "" || ref.GroupName == "" {
		return ErrNoEditableSnapshot
	}
	result, err := r.db.Exec(ctx, `
		WITH ranked AS (
		 SELECT *,row_number() OVER(PARTITION BY CASE WHEN group_id<>'' THEN 'id:'||group_id ELSE 'name:'||group_name END ORDER BY created_at DESC,id DESC) AS rank
		 FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3
		), current AS (
		 SELECT * FROM ranked WHERE rank=1 AND NOT deleted AND (group_id=$4 OR group_name=$4)
		), chosen AS (SELECT * FROM current WHERE (SELECT count(*) FROM current)=1)
		UPDATE group_rate_snapshots AS snapshots SET type=$5 FROM chosen
		WHERE snapshots.user_id=$1 AND snapshots.admin_account_id=$2 AND snapshots.site_id=$3
		 AND ((snapshots.group_id<>'' AND chosen.group_id<>'' AND snapshots.group_id=chosen.group_id)
		 OR ((snapshots.group_id='' OR chosen.group_id='') AND snapshots.group_name=chosen.group_name))
	`, userID, adminAccountID, ref.SiteID, ref.GroupName, groupType)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNoEditableSnapshot
	}
	return nil
}

func (r *Repository) History(ctx context.Context, userID string, adminAccountID string, siteID string, groupName string, platform string) ([]snapshotRecord, error) {
	rows, err := r.db.Query(ctx, `
		WITH ranked AS (
		 SELECT *,row_number() OVER(PARTITION BY CASE WHEN group_id<>'' THEN 'id:'||group_id ELSE 'name:'||group_name END ORDER BY created_at DESC,id DESC) AS rank
		 FROM group_rate_snapshots WHERE user_id=$1 AND admin_account_id=$2 AND site_id=$3
		), current AS (
		 SELECT * FROM ranked WHERE rank=1 AND NOT deleted AND (group_id=$4 OR (group_id='' AND group_name=$4))
		), chosen AS (SELECT * FROM current WHERE (SELECT count(*) FROM current)=1)
		SELECT
			group_rate_snapshots.id,
			group_rate_snapshots.user_id,
			group_rate_snapshots.site_id,
			group_rate_snapshots.site_name,
			group_rate_snapshots.group_id,
			group_rate_snapshots.group_name,
			group_rate_snapshots.platform,
			group_rate_snapshots.type,
			group_rate_snapshots.multiplier,
			group_rate_snapshots.deleted,
			COALESCE(upstream_sites.recharge_rate, 1) AS recharge_rate,
			group_rate_snapshots.created_at,
			COALESCE(group_rate_snapshots.last_seen_at, group_rate_snapshots.created_at) AS last_seen_at,
			LEAD(group_rate_snapshots.multiplier) OVER (ORDER BY group_rate_snapshots.created_at DESC, group_rate_snapshots.id DESC) AS previous_multiplier
		FROM group_rate_snapshots
		LEFT JOIN upstream_sites
			ON upstream_sites.user_id = group_rate_snapshots.user_id
			AND upstream_sites.id = group_rate_snapshots.site_id
		WHERE group_rate_snapshots.user_id = $1 AND group_rate_snapshots.admin_account_id = $2 AND group_rate_snapshots.site_id = $3
		 AND EXISTS(SELECT 1 FROM chosen WHERE
		  (group_rate_snapshots.group_id<>'' AND chosen.group_id<>'' AND group_rate_snapshots.group_id=chosen.group_id)
		  OR ((group_rate_snapshots.group_id='' OR chosen.group_id='') AND group_rate_snapshots.group_name=chosen.group_name)) AND ($5 = '' OR group_rate_snapshots.platform = $5 OR group_rate_snapshots.type = $5)
		ORDER BY group_rate_snapshots.created_at DESC, group_rate_snapshots.id DESC
	`, userID, adminAccountID, siteID, groupName, platform)
	if err != nil {
		return nil, err
	}
	return scanSnapshots(rows)
}

func scanListSnapshots(rows pgxRows) (listRecords, error) {
	defer rows.Close()

	result := listRecords{Items: make([]snapshotRecord, 0)}
	for rows.Next() {
		var record snapshotRecord
		var previous sql.NullFloat64
		if err := rows.Scan(
			&record.ID,
			&record.UserID,
			&record.SiteID,
			&record.SiteName,
			&record.GroupID,
			&record.GroupName,
			&record.Platform,
			&record.Type,
			&record.Mapped,
			&record.PricingMapped,
			&record.Deleted,
			&record.Multiplier,
			&record.RechargeRate,
			&record.CreatedAt,
			&record.LastSeenAt,
			&previous,
			&result.Total,
			&result.Types,
			&result.Platforms,
		); err != nil {
			return listRecords{}, err
		}
		if previous.Valid {
			value := previous.Float64
			record.PreviousMultiplier = &value
		}
		result.Items = append(result.Items, record)
	}
	if err := rows.Err(); err != nil {
		return listRecords{}, err
	}
	return result, nil
}

func scanSnapshots(rows pgxRows) ([]snapshotRecord, error) {
	defer rows.Close()

	records := make([]snapshotRecord, 0)
	for rows.Next() {
		var record snapshotRecord
		var previous sql.NullFloat64
		if err := rows.Scan(
			&record.ID,
			&record.UserID,
			&record.SiteID,
			&record.SiteName,
			&record.GroupID,
			&record.GroupName,
			&record.Platform,
			&record.Type,
			&record.Multiplier,
			&record.Deleted,
			&record.RechargeRate,
			&record.CreatedAt,
			&record.LastSeenAt,
			&previous,
		); err != nil {
			return nil, err
		}
		if previous.Valid {
			value := previous.Float64
			record.PreviousMultiplier = &value
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *Repository) facets(ctx context.Context, userID string, adminAccountID string) (listRecords, error) {
	var result listRecords
	err := r.db.QueryRow(ctx, `
		WITH enriched AS (
			SELECT
				snapshots.user_id,
				snapshots.site_id,
				snapshots.group_name,
				COALESCE(NULLIF(snapshots.group_id, ''), snapshots.group_name) AS group_key,
				snapshots.platform,
				snapshots.type,
				snapshots.deleted,
				snapshots.created_at,
				snapshots.id,
				COALESCE(sites.base_url, snapshots.site_id) AS site_key
			FROM group_rate_snapshots AS snapshots
			LEFT JOIN upstream_sites AS sites
				ON sites.user_id = snapshots.user_id
				AND sites.id = snapshots.site_id
			WHERE snapshots.user_id = $1 AND snapshots.admin_account_id = $2
		), ranked AS (
			SELECT platform, type, deleted,
				ROW_NUMBER() OVER (PARTITION BY user_id, site_key, group_key ORDER BY created_at DESC, id DESC) AS row_number
			FROM enriched
		), latest AS (
			SELECT platform, type FROM ranked WHERE row_number = 1 AND NOT deleted
		)
		SELECT
			COALESCE(array_agg(DISTINCT type ORDER BY type) FILTER (WHERE type <> ''), ARRAY[]::text[]) AS types,
			COALESCE(array_agg(DISTINCT platform ORDER BY platform) FILTER (WHERE platform <> ''), ARRAY[]::text[]) AS platforms
		FROM latest
	`, userID, adminAccountID).Scan(&result.Types, &result.Platforms)
	return result, err
}

// statusCounts calculates status-tab totals using the same current-row and
// mapped semantics as List. Search/type/platform remain active so tab counts
// always describe the result set the operator is currently inspecting.
func (r *Repository) statusCounts(ctx context.Context, userID string, adminAccountID string, search string, filterType string, filterPlatform string, siteIDs ...string) (StatusCounts, error) {
	var counts StatusCounts
	siteID := ""
	if len(siteIDs) > 0 {
		siteID = strings.TrimSpace(siteIDs[0])
	}
	err := r.db.QueryRow(ctx, `
		WITH enriched AS (
			SELECT
				snapshots.id,
				snapshots.user_id,
				snapshots.site_id,
				snapshots.site_name,
				snapshots.group_id,
				snapshots.group_name,
				snapshots.platform,
				snapshots.type,
				snapshots.deleted,
				snapshots.created_at,
				COALESCE(sites.base_url, snapshots.site_id) AS site_key,
				COALESCE(NULLIF(snapshots.group_id, ''), snapshots.group_name) AS group_key
			FROM group_rate_snapshots AS snapshots
			LEFT JOIN upstream_sites AS sites
				ON sites.user_id = snapshots.user_id
				AND sites.id = snapshots.site_id
			WHERE snapshots.user_id = $1 AND snapshots.admin_account_id = $2
		), ranked AS (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY user_id, site_key, group_key
				ORDER BY created_at DESC, id DESC
			) AS row_number
			FROM enriched
		), latest AS (
			SELECT * FROM ranked WHERE row_number = 1 AND NOT deleted
		), mapped AS (
			SELECT latest.*, (
				EXISTS (
					SELECT 1
					FROM real_connections AS connections
					WHERE connections.user_id = latest.user_id
						AND connections.workspace_admin_account_id = $2
						AND (connections.status = '' OR connections.status = 'active')
						AND connections.upstream_site_id = latest.site_id
						AND (
							(connections.upstream_group_id <> '' AND latest.group_id <> '' AND connections.upstream_group_id = latest.group_id)
							OR ((connections.upstream_group_id = '' OR latest.group_id = '')
								AND connections.upstream_group_name = latest.group_name)
						)
				)
			) AS mapped
			FROM latest
		), base_filtered AS (
			SELECT * FROM mapped
			WHERE ($3 = '' OR lower(site_name) LIKE '%' || $3 || '%' OR lower(group_name) LIKE '%' || $3 || '%' OR lower(platform) LIKE '%' || $3 || '%' OR lower(type) LIKE '%' || $3 || '%')
				AND ($4 = '' OR type = $4)
				AND ($5 = '' OR platform = $5)
				AND ($6 = '' OR site_id = $6)
		)
		SELECT
			count(*) FILTER (WHERE NOT deleted)::int,
			count(*) FILTER (WHERE mapped AND NOT deleted)::int,
			count(*) FILTER (WHERE NOT mapped AND NOT deleted)::int,
			count(*) FILTER (WHERE deleted)::int
		FROM base_filtered
	`, userID, adminAccountID, search, filterType, filterPlatform, siteID).Scan(
		&counts.All,
		&counts.Mapped,
		&counts.Unmapped,
		&counts.Deleted,
	)
	return counts, err
}

func newSnapshotID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.New("generate group rate snapshot id")
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

type pgxRows interface {
	Close()
	Next() bool
	Scan(dest ...any) error
	Err() error
}
