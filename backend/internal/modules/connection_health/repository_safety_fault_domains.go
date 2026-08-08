package connection_health

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetTargetFaultEndpoint(ctx context.Context, userID, workspaceID, targetID string) (string, error) {
	var endpoint string
	err := r.db.QueryRow(ctx, `
		SELECT endpoint FROM connection_health_target_fault_domains
		WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3
	`, userID, workspaceID, targetID).Scan(&endpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return endpoint, err
}

func (r *Repository) UpsertTargetFaultEndpoint(ctx context.Context, userID, workspaceID, targetID, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return errors.New("target fault endpoint is empty")
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO connection_health_target_fault_domains
			(user_id,admin_account_id,target_id,endpoint,updated_at)
		VALUES($1,$2,$3,$4,now())
		ON CONFLICT(user_id,admin_account_id,target_id) DO UPDATE SET
			endpoint=EXCLUDED.endpoint,updated_at=now()
		WHERE connection_health_target_fault_domains.endpoint IS DISTINCT FROM EXCLUDED.endpoint
	`, userID, workspaceID, targetID, endpoint)
	return err
}

func (r *Repository) AnyIncidentCircuitOpen(ctx context.Context, userID, workspaceID string) (bool, error) {
	var open bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM connection_health_incidents
			WHERE user_id=$1 AND admin_account_id=$2 AND state IN ('open','half_open')
		)
	`, userID, workspaceID).Scan(&open)
	return open, err
}
