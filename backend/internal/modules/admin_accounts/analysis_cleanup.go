package admin_accounts

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// New analysis modules are optional on older databases. Original deletion statements stay intact.
var analysisWorkspaceTables = []string{"user_activity_profiles", "user_activity_daily", "user_activity_recharge_daily", "user_activity_site_daily", "user_activity_sync_states", "vip_watch", "vip_settings", "vip_alert_states"}

func deleteAnalysisWorkspaceRows(ctx context.Context, tx pgx.Tx, user, workspace string) error {
	for _, table := range analysisWorkspaceTables {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); e != nil {
			return e
		}
		if exists {
			if _, e := tx.Exec(ctx, `DELETE
        FROM `+table+` WHERE user_id=$1 AND admin_account_id=$2`, user, workspace); e != nil {
				return e
			}
		}
	}
	return nil
}
