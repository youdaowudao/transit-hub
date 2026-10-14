package my_sites

import (
	"context"
	"time"
)

// The guarded JSON expansion never casts saved group fields. A damaged row
// becomes an unconfirmed source instead of failing the whole page. Ownership,
// count and page are read by one statement and therefore share one PG snapshot.
const listConnectionsNeedingAttentionSQL = `
WITH source AS (
 SELECT connection_row.id, connection_row.user_id, connection_row.workspace_admin_account_id,
        connection_row.upstream_site_id, connection_row.upstream_group_id, connection_row.upstream_group_name,
        connection_row.upstream_key_id, connection_row.admin_account_id, connection_row.admin_account_name,
        connection_row.own_group_ids, connection_row.own_group_names, connection_row.group_type,
        connection_row.provisioning_mode, connection_row.status, connection_row.upstream_platform,
        connection_row.admin_platform, connection_row.pricing_mapping_enabled, connection_row.created_at,
        site.id AS source_site_id, COALESCE(site.name,'') AS site_name,
        site.last_synced_at, site.metrics,
        CASE WHEN jsonb_typeof(site.metrics->'groups')='array'
             THEN site.metrics->'groups' ELSE '[]'::jsonb END AS groups
 FROM real_connections connection_row
 LEFT JOIN upstream_sites site ON site.id=connection_row.upstream_site_id
      AND site.user_id=connection_row.user_id
      AND site.admin_account_id=connection_row.workspace_admin_account_id
 WHERE connection_row.user_id=$1 AND connection_row.workspace_admin_account_id=$2
   AND ($3='' OR connection_row.upstream_site_id=$3)
   AND connection_row.status IN ('','active','missing')
), examined AS (
 SELECT source.*, CASE
   WHEN source_site_id IS NULL THEN '对应的网站已无法在当前工作区找到'
   WHEN last_synced_at IS NULL THEN '网站尚无已保存的同步时间'
   WHEN jsonb_typeof(metrics->'groups') IS DISTINCT FROM 'array' THEN '网站保存的分组资料不是分组列表'
   WHEN jsonb_array_length(groups)=0 THEN '网站保存的分组列表为空，无法确认完整分组资料'
   WHEN EXISTS (
     SELECT 1 FROM jsonb_array_elements(groups) AS entry(value)
     WHERE jsonb_typeof(value) IS DISTINCT FROM 'object'
       OR jsonb_typeof(value->'name') IS DISTINCT FROM 'string'
       OR COALESCE(value->>'name','')='' OR (value->>'name')~'(^[[:space:]]|[[:space:]]$)'
       OR COALESCE(jsonb_typeof(value->'id'),'null') NOT IN ('string','null')
       OR (jsonb_typeof(value->'id')='string' AND (value->>'id')~'(^[[:space:]]|[[:space:]]$)')
       OR COALESCE(jsonb_typeof(value->'multiplier'),'null') NOT IN ('number','null')
       OR CASE WHEN jsonb_typeof(value->'multiplier')='number' THEN value->'multiplier'<'0'::jsonb ELSE false END
       OR COALESCE(jsonb_typeof(value->'multiplierMode'),'null') NOT IN ('string','null')
       OR COALESCE(value->>'multiplierMode','') NOT IN ('','fixed','auto')
   ) THEN '网站保存的分组条目、名称、身份或倍率字段损坏'
   WHEN EXISTS (
     SELECT 1 FROM jsonb_array_elements(groups) AS entry(value)
     WHERE COALESCE(value->>'id','')<>'' GROUP BY value->>'id' HAVING count(*)>1
   ) THEN '网站保存的分组ID重复，无法确认对应关系'
   WHEN upstream_group_id='' AND upstream_group_name='' THEN '这条对接没有可对应的上游分组身份或名称'
   ELSE '' END AS source_issue
 FROM source
), compared AS (
 SELECT examined.*, matches.exact_count, matches.name_count, matches.compatible_count
 FROM examined
 CROSS JOIN LATERAL (
   SELECT count(*) FILTER (WHERE examined.upstream_group_id<>'' AND COALESCE(value->>'id','')<>'' AND value->>'id'=examined.upstream_group_id)::int AS exact_count,
          count(*) FILTER (WHERE value->>'name'=examined.upstream_group_name)::int AS name_count,
          count(*) FILTER (WHERE value->>'name'=examined.upstream_group_name AND (examined.upstream_group_id='' OR COALESCE(value->>'id','')=''))::int AS compatible_count
   FROM jsonb_array_elements(examined.groups) AS entry(value)
 ) matches
), pending AS (
 SELECT *, CASE WHEN source_issue<>'' OR name_count>1 THEN 'source_unconfirmed' ELSE 'group_not_found' END AS attention_reason,
        CASE WHEN source_issue<>'' THEN source_issue
             WHEN name_count>1 THEN '网站保存的同名分组不唯一，无法确认这个对接对应哪一个分组'
             ELSE 'Hub保存的上游分组资料里未找到这个分组，不能据此确认上游删除。已有对接仍保留，你可以核对账号或解除本地对接' END AS attention_detail
 FROM compared
 WHERE source_issue<>'' OR NOT (exact_count=1 OR (name_count=1 AND compatible_count=1))
), metadata AS (
 SELECT (SELECT count(*)::int FROM pending) AS total,
        ($3='' OR EXISTS(SELECT 1 FROM upstream_sites WHERE user_id=$1 AND admin_account_id=$2 AND id=$3)) AS site_owned
), page_rows AS (
 SELECT * FROM pending ORDER BY upstream_site_id,upstream_group_id,upstream_group_name,id LIMIT 30 OFFSET $4
)
SELECT COALESCE(page_rows.id,''), COALESCE(page_rows.user_id,''), COALESCE(page_rows.workspace_admin_account_id,''),
       COALESCE(page_rows.upstream_site_id,''), COALESCE(page_rows.upstream_group_id,''), COALESCE(page_rows.upstream_group_name,''),
       COALESCE(page_rows.upstream_key_id,''), ''::text, COALESCE(page_rows.admin_account_id,''), COALESCE(page_rows.admin_account_name,''),
       COALESCE(page_rows.own_group_ids,'[]'::jsonb), COALESCE(page_rows.own_group_names,'[]'::jsonb),
       COALESCE(page_rows.group_type,''), COALESCE(page_rows.provisioning_mode,''), COALESCE(page_rows.status,''),
       COALESCE(page_rows.upstream_platform,''), COALESCE(page_rows.admin_platform,''), COALESCE(page_rows.pricing_mapping_enabled,false),
       ''::text, COALESCE(page_rows.created_at,'epoch'::timestamptz), COALESCE(page_rows.site_name,''),
       metadata.total, metadata.site_owned, COALESCE(page_rows.attention_reason,''), COALESCE(page_rows.attention_detail,''), page_rows.last_synced_at
FROM metadata LEFT JOIN page_rows ON true
ORDER BY page_rows.upstream_site_id,page_rows.upstream_group_id,page_rows.upstream_group_name,page_rows.id
`

type attentionRowScanner struct {
	row       realConnectionScanner
	total     *int
	siteOwned *bool
	reason    *string
	detail    *string
	syncedAt  **int64
}

func (s attentionRowScanner) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.total, s.siteOwned, s.reason, s.detail, s.syncedAt)...)
}

func (r *Repository) ListConnectionsNeedingAttention(ctx context.Context, userID, workspaceID string, query NeedsAttentionQuery) (NeedsAttentionResponse, error) {
	query.WorkspaceAdminAccountID = workspaceID
	query, err := normalizeAttentionQuery(query)
	if err != nil {
		return NeedsAttentionResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := r.db.Query(ctx, listConnectionsNeedingAttentionSQL, userID, workspaceID, query.SiteID, int64(query.Page-1)*needsAttentionPageSize)
	if err != nil {
		return NeedsAttentionResponse{}, err
	}
	defer rows.Close()
	result := NeedsAttentionResponse{WorkspaceAdminAccountID: workspaceID, Page: query.Page, PageSize: needsAttentionPageSize, Items: []NeedsAttentionItem{}}
	for rows.Next() {
		var item NeedsAttentionItem
		var owned bool
		conn, err := scanListedRealConnection(attentionRowScanner{row: rows, total: &result.Total, siteOwned: &owned, reason: &item.Reason, detail: &item.Detail, syncedAt: &item.LastSyncedAt})
		if err != nil {
			return NeedsAttentionResponse{}, err
		}
		if !owned {
			return NeedsAttentionResponse{}, attentionSiteNotFound()
		}
		if conn.ID == "" {
			continue
		}
		item.Connection = publicRealConnection(*conn)
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return NeedsAttentionResponse{}, err
	}
	result.TotalPages = (result.Total + needsAttentionPageSize - 1) / needsAttentionPageSize
	return result, nil
}
