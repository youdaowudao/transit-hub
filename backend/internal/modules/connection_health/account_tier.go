package connection_health

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/admin_accounts"
	"transithub/backend/internal/modules/upstream"
)

type AccountTierResult struct {
	TargetID    string `json:"targetId"`
	AccountTier int    `json:"accountTier"`
}

type accountTierWorkspaceReader interface {
	Current(context.Context, string) (*admin_accounts.Account, error)
}

func effectiveAccountTier(tier int) int {
	if tier == 1 {
		return 1
	}
	return 2
}

func (s *Service) accountTierWorkspace(ctx context.Context, userID, targetID string) (string, error) {
	parsed, ok := parseTargetID(targetID)
	if !ok || parsed.platform != string(upstream.PlatformSub2API) {
		return "", requestError(ErrorProbeTargetNotFound)
	}
	reader, ok := s.accounts.(accountTierWorkspaceReader)
	if !ok {
		return "", requestError(ErrorNoCurrentAccount)
	}
	account, err := reader.Current(ctx, userID)
	if err != nil {
		return "", err
	}
	if account == nil {
		return "", requestError(ErrorNoCurrentAccount)
	}
	if account.Platform != string(upstream.PlatformSub2API) || parsed.adminAccountID != account.ID {
		return "", requestError(ErrorProbeTargetNotFound)
	}
	return account.ID, nil
}

func (s *Service) GetAccountTier(ctx context.Context, userID, targetID string) (AccountTierResult, error) {
	targetID = strings.TrimSpace(targetID)
	workspaceID, err := s.accountTierWorkspace(ctx, userID, targetID)
	if err != nil {
		return AccountTierResult{}, err
	}
	tier, err := s.repo.GetAccountTier(ctx, userID, workspaceID, targetID)
	if err != nil {
		return AccountTierResult{}, err
	}
	return AccountTierResult{TargetID: targetID, AccountTier: effectiveAccountTier(tier)}, nil
}

func (s *Service) SaveAccountTier(ctx context.Context, userID, targetID string, tier int) (AccountTierResult, error) {
	if tier != 1 && tier != 2 {
		return AccountTierResult{}, requestError(ErrorRequest)
	}
	targetID = strings.TrimSpace(targetID)
	workspaceID, err := s.accountTierWorkspace(ctx, userID, targetID)
	if err != nil {
		return AccountTierResult{}, err
	}
	if err := s.repo.SaveAccountTier(ctx, userID, workspaceID, targetID, tier); err != nil {
		return AccountTierResult{}, err
	}
	return AccountTierResult{TargetID: targetID, AccountTier: tier}, nil
}

func (r *Repository) GetAccountTier(ctx context.Context, userID, workspaceID, targetID string) (int, error) {
	var tier int
	err := r.db.QueryRow(ctx, `SELECT COALESCE(account_tier, 2)
		FROM connection_health_account_configs
		WHERE user_id = $1 AND admin_account_id = $2 AND target_id = $3`, userID, workspaceID, targetID).Scan(&tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return 2, nil
	}
	return effectiveAccountTier(tier), err
}

func (r *Repository) SaveAccountTier(ctx context.Context, userID, workspaceID, targetID string, tier int) error {
	if tier != 1 && tier != 2 {
		return requestError(ErrorRequest)
	}
	_, err := r.db.Exec(ctx, `INSERT INTO connection_health_account_configs (user_id, admin_account_id, target_id, account_tier)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, admin_account_id, target_id) DO UPDATE
		SET account_tier = EXCLUDED.account_tier, updated_at = now()`, userID, workspaceID, targetID, tier)
	return err
}

func (r *Repository) ListAccountTiers(ctx context.Context, userID, workspaceID string) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `SELECT target_id, COALESCE(account_tier, 2)
		FROM connection_health_account_configs WHERE user_id = $1 AND admin_account_id = $2`, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiers := make(map[string]int)
	for rows.Next() {
		var targetID string
		var tier int
		if err := rows.Scan(&targetID, &tier); err != nil {
			return nil, err
		}
		tiers[targetID] = effectiveAccountTier(tier)
	}
	return tiers, rows.Err()
}
