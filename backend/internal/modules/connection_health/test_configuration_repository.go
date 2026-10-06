package connection_health

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type configurationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listGroupTestConfigurations(ctx context.Context, query configurationQuerier, userID, adminAccountID string) ([]GroupTestConfig, error) {
	rows, err := query.Query(ctx, `SELECT user_id, admin_account_id, admin_group_id, protocol, probe_timeout_seconds FROM connection_health_group_test_configs WHERE user_id=$1 AND admin_account_id=$2 ORDER BY admin_group_id`, userID, adminAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := []GroupTestConfig{}
	for rows.Next() {
		var config GroupTestConfig
		if err := rows.Scan(&config.UserID, &config.AdminAccountID, &config.AdminGroupID, &config.Protocol, &config.ProbeTimeoutSeconds); err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}
	return configs, rows.Err()
}

func listGroupTestConfigurationsTx(ctx context.Context, tx pgx.Tx, userID, adminAccountID string) ([]GroupTestConfig, error) {
	return listGroupTestConfigurations(ctx, tx, userID, adminAccountID)
}

func (r *Repository) ListGroupTestConfigurations(ctx context.Context, userID, adminAccountID string) ([]GroupTestConfig, error) {
	return listGroupTestConfigurations(ctx, r.db, userID, adminAccountID)
}

func (r *Repository) SaveGroupTestConfiguration(ctx context.Context, userID, adminAccountID, groupID string, configuration *GroupTestConfiguration) error {
	if configuration != nil && !validGroupTestConfiguration(*configuration) {
		return requestError(ErrorRequest)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if configuration == nil {
		_, err = tx.Exec(ctx, `DELETE FROM connection_health_group_test_configs WHERE user_id=$1 AND admin_account_id=$2 AND admin_group_id=$3`, userID, adminAccountID, groupID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO connection_health_group_test_configs (user_id,admin_account_id,admin_group_id,protocol,probe_timeout_seconds) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (user_id,admin_account_id,admin_group_id) DO UPDATE SET protocol=EXCLUDED.protocol,probe_timeout_seconds=EXCLUDED.probe_timeout_seconds,updated_at=clock_timestamp()
		WHERE connection_health_group_test_configs.protocol IS DISTINCT FROM EXCLUDED.protocol OR connection_health_group_test_configs.probe_timeout_seconds IS DISTINCT FROM EXCLUDED.probe_timeout_seconds`, userID, adminAccountID, groupID, configuration.Protocol, configuration.ProbeTimeoutSeconds)
	}
	if err != nil {
		return err
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, userID, adminAccountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
