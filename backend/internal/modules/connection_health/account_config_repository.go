package connection_health

import "context"

func (r *Repository) ListAccountConfigs(
	ctx context.Context,
	userID string,
	adminAccountID string,
) ([]AccountConfig, error) {
	rows, err := r.db.Query(ctx, `
		SELECT user_id, admin_account_id, target_id, intelligence_weight, created_at, updated_at
		FROM connection_health_account_configs
		WHERE user_id = $1 AND admin_account_id = $2
	`, userID, adminAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := make([]AccountConfig, 0)
	for rows.Next() {
		var config AccountConfig
		if err := rows.Scan(
			&config.UserID,
			&config.AdminAccountID,
			&config.TargetID,
			&config.IntelligenceWeight,
			&config.CreatedAt,
			&config.UpdatedAt,
		); err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}
	return configs, rows.Err()
}

func (r *Repository) UpsertAccountIntelligenceWeight(
	ctx context.Context,
	userID string,
	adminAccountID string,
	targetID string,
	intelligenceWeight *int,
) (AccountConfig, error) {
	var config AccountConfig
	err := r.db.QueryRow(ctx, `
		INSERT INTO connection_health_account_configs (
			user_id, admin_account_id, target_id, intelligence_weight
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, admin_account_id, target_id) DO UPDATE SET
			intelligence_weight = EXCLUDED.intelligence_weight,
			updated_at = now()
		RETURNING user_id, admin_account_id, target_id, intelligence_weight, created_at, updated_at
	`, userID, adminAccountID, targetID, intelligenceWeight).Scan(
		&config.UserID,
		&config.AdminAccountID,
		&config.TargetID,
		&config.IntelligenceWeight,
		&config.CreatedAt,
		&config.UpdatedAt,
	)
	return config, err
}
