package notifications

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func readPreference(ctx context.Context, q queryer, user, workspace string) (PreferencesResponse, error) {
	result := PreferencesResponse{WorkspaceAdminAccountID: workspace, IntervalSeconds: 300}
	err := q.QueryRow(ctx, `SELECT poll_interval_seconds,updated_at FROM site_notification_preferences WHERE user_id=$1 AND admin_account_id=$2`, user, workspace).Scan(&result.IntervalSeconds, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreferencesResponse{WorkspaceAdminAccountID: workspace, IntervalSeconds: 300}, nil
	}
	if err != nil {
		return result, err
	}
	if result.IntervalSeconds < 60 || result.IntervalSeconds > 3600 || result.IntervalSeconds%60 != 0 {
		return result, errStoredPreference
	}
	return result, nil
}

func (r *Repository) loadPreference(ctx context.Context, user, workspace string) (PreferencesResponse, error) {
	return readPreference(ctx, r.db, user, workspace)
}

func (s *Service) Preferences(ctx context.Context, user, workspace string) (PreferencesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.repository.beginRead(ctx, user, workspace)
	if err != nil {
		return PreferencesResponse{}, err
	}
	defer tx.Rollback(ctx)
	result, err := readPreference(ctx, tx, user, workspace)
	if err != nil {
		return PreferencesResponse{}, storageFailure(workspace, "读取这个工作区的通知检查间隔", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PreferencesResponse{}, storageFailure(workspace, "完成通知检查间隔读取", err)
	}
	return result, nil
}

func (s *Service) SavePreferences(ctx context.Context, user, workspace string, minutes int) (PreferencesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if minutes < 1 || minutes > 60 {
		return PreferencesResponse{}, invalidRequest(workspace, "通知检查间隔必须是1到60的整数分钟；原有间隔和上游自动同步设置没有改变")
	}
	tx, err := s.repository.beginWrite(ctx, user, workspace)
	if err != nil {
		return PreferencesResponse{}, err
	}
	defer tx.Rollback(ctx)
	result := PreferencesResponse{WorkspaceAdminAccountID: workspace}
	err = tx.QueryRow(ctx, `INSERT INTO site_notification_preferences(user_id,admin_account_id,poll_interval_seconds,updated_at) VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT(user_id,admin_account_id) DO UPDATE SET poll_interval_seconds=EXCLUDED.poll_interval_seconds,updated_at=EXCLUDED.updated_at RETURNING poll_interval_seconds,updated_at`, user, workspace, minutes*60).Scan(&result.IntervalSeconds, &result.UpdatedAt)
	if err != nil {
		return PreferencesResponse{}, storageFailure(workspace, "保存这个工作区的通知检查间隔", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PreferencesResponse{}, commitFailure(workspace, "通知检查间隔", err)
	}
	return result, nil
}
