package connection_health

import (
	"context"
	"time"
)

func (r *Repository) ListLatestProbeAttemptEventsByWorkspace(ctx context.Context, userID, workspace string, since time.Time) ([]ConnectionHealthEvent, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT ON (connection_id,model_name)
		id,connection_id,model_name,user_id,admin_account_id,policy_id,admin_group_id,own_group_name,
		upstream_site_id,upstream_group_name,result,from_state,to_state,
		latency_ms,error_key,error_detail,remote_action,action_source,source,request_protocol,request_timeout_seconds,probe_disposition,created_at
		FROM connection_health_events
		WHERE user_id=$1 AND admin_account_id=$2 AND created_at >= $3 AND probe_disposition IN ('applied','invalid')
		ORDER BY connection_id,model_name,created_at DESC,id DESC`, userID, workspace, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
