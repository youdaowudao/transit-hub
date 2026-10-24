package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func readSignupRiskPreferences(ctx context.Context, q queryer, user, workspace string) (SignupRiskPreferences, error) {
	result := SignupRiskPreferences{WorkspaceAdminAccountID: workspace}
	err := q.QueryRow(ctx, `SELECT enabled,initialized,updated_at FROM signup_risk_settings WHERE user_id=$1 AND admin_account_id=$2`, user, workspace).Scan(&result.Enabled, &result.Initialized, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	return result, err
}

func (r *Repository) ListSignupRiskTargets(ctx context.Context) ([]SignupRiskTarget, error) {
	rows, err := r.db.Query(ctx, `SELECT a.user_id,a.id,a.base_url,a.display_name,s.initialized,s.updated_at
FROM signup_risk_settings s JOIN admin_accounts a ON a.id=s.admin_account_id AND a.user_id=s.user_id
WHERE s.enabled=true AND a.platform='sub2api' ORDER BY a.id`)
	if err != nil {
		return nil, fmt.Errorf("读取已开启的注册提醒站点失败：%s；本轮未请求主站，下分钟重试，请维护人员核查通知设置存储", SafeStorageCause(err))
	}
	defer rows.Close()
	var targets []SignupRiskTarget
	for rows.Next() {
		var target SignupRiskTarget
		if err := rows.Scan(&target.UserID, &target.AdminAccountID, &target.BaseURL, &target.SiteName, &target.Initialized, &target.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取注册提醒站点资料失败：%s；本轮未请求主站，请维护人员核查管理员工作区，下分钟重试", SafeStorageCause(err))
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("注册提醒站点列表未完整读取：%s；本轮未请求主站，下分钟重试", SafeStorageCause(err))
	}
	return targets, nil
}

func (r *Repository) ProcessedSignupRiskIDs(ctx context.Context, target SignupRiskTarget, ids []string) (map[string]bool, error) {
	rows, err := r.db.Query(ctx, `SELECT remote_user_id FROM signup_risk_processed_users WHERE user_id=$1 AND admin_account_id=$2 AND site_key=$3 AND remote_user_id=ANY($4::text[])`, target.UserID, target.AdminAccountID, SignupRiskSiteKey(target.BaseURL), ids)
	if err != nil {
		return nil, fmt.Errorf("核查已处理注册用户失败：%s；本轮没有登记或生成消息，下分钟从第一页重试", SafeStorageCause(err))
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("核查已处理用户编号失败：%s；本轮没有登记或生成消息，下分钟重试", SafeStorageCause(err))
		}
		result[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("已处理用户编号未完整读取：%s；本轮没有登记或生成消息，下分钟重试", SafeStorageCause(err))
	}
	return result, nil
}

// CommitSignupRiskRound is all-or-nothing, including the empty first scan.
// No remote calls or implicit current-workspace lookup occur in this transaction.
func (r *Repository) CommitSignupRiskRound(ctx context.Context, target SignupRiskTarget, users []SignupRiskUser, discovered time.Time) (resultErr error) {
	inputs := make([]NoticeInput, 0, len(users))
	for _, user := range users {
		input, err := signupRiskNotice(target, user, discovered)
		if err != nil {
			return err
		}
		inputs = append(inputs, input)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始保存注册提醒失败：%s；本轮未登记，下分钟重试", SafeStorageCause(err))
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			resultErr = errors.New("注册提醒保存或回滚结果尚未确认；下分钟将核查同一用户编号后重试，请维护人员核查通知存储")
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return fmt.Errorf("准备保存注册提醒失败：%s；本轮未确认登记，下分钟重试", SafeStorageCause(err))
	}
	var address string
	err = tx.QueryRow(ctx, `SELECT base_url FROM admin_accounts WHERE user_id=$1 AND id=$2 AND platform='sub2api' FOR KEY SHARE`, target.UserID, target.AdminAccountID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // The workspace was removed; do not recreate its state.
	}
	if err != nil {
		return fmt.Errorf("核查注册提醒所属站点失败：%s；本轮未确认登记，下分钟重试", SafeStorageCause(err))
	}
	if SignupRiskSiteKey(address) != SignupRiskSiteKey(target.BaseURL) {
		return nil
	}
	var enabled, initialized bool
	var updated time.Time
	err = tx.QueryRow(ctx, `SELECT enabled,initialized,updated_at FROM signup_risk_settings WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, target.UserID, target.AdminAccountID).Scan(&enabled, &initialized, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("复核注册提醒开关失败：%s；本轮未确认登记，下分钟重试", SafeStorageCause(err))
	}
	if !enabled || initialized != target.Initialized || !updated.Equal(target.UpdatedAt) {
		return nil // A close/reopen or another completed bootstrap invalidates this read.
	}
	for i, user := range users {
		result, err := tx.Exec(ctx, `INSERT INTO signup_risk_processed_users(user_id,admin_account_id,site_key,remote_user_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, target.UserID, target.AdminAccountID, SignupRiskSiteKey(target.BaseURL), user.ID)
		if err != nil {
			return fmt.Errorf("登记注册用户编号失败：%s；本轮事务未完成，下分钟核查后重试", SafeStorageCause(err))
		}
		if result.RowsAffected() > 0 && initialized {
			if _, _, err := r.AppendTx(ctx, tx, inputs[i]); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE signup_risk_settings SET initialized=true WHERE user_id=$1 AND admin_account_id=$2`, target.UserID, target.AdminAccountID); err != nil {
		return fmt.Errorf("完成首次注册用户登记失败：%s；本轮事务未完成，下分钟重试", SafeStorageCause(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("注册提醒提交结果无法确认；下分钟将核查同一用户编号后重试，请维护人员核查通知存储")
	}
	return nil
}

func loadSignupRiskSummary(ctx context.Context, tx pgx.Tx, user, workspace string) (*SignupRiskSummary, error) {
	prefs, err := readSignupRiskPreferences(ctx, tx, user, workspace)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+noticeColumns+` FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2 AND n.kind=$3 AND n.level=3 AND n.reminder_completed_at IS NULL ORDER BY n.created_at,n.id LIMIT 20`, user, workspace, SignupRiskKind)
	if err != nil {
		return nil, err
	}
	items, err := collectNotices(rows)
	if err != nil {
		return nil, err
	}
	return &SignupRiskSummary{Enabled: prefs.Enabled, Items: items}, nil
}
