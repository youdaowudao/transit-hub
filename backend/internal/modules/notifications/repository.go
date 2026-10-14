package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// SafeStorageCause explains storage failures without connection strings, row values or SQL.
func SafeStorageCause(err error) string {
	return safeStorageCause(err)
}

func safeStorageCause(err error) string {
	if errors.Is(err, errStoredContentFormat) {
		return "已保存的通知内容不是可读取的完整格式，请维护人员核查这条通知的保存内容"
	}
	if errors.Is(err, errStoredContentUnsafe) {
		return "已保存的通知内容未通过安全显示检查，请维护人员核查必要字段、长度和敏感资料；本次没有展示这条内容"
	}
	if errors.Is(err, errStoredPreference) {
		return "已保存的通知检查间隔不在1到60整数分钟范围内，请维护人员核查这个工作区的通知设置"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "数据库操作等待超时"
	}
	if errors.Is(err, context.Canceled) {
		return "数据库操作已取消"
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		reason := "原因尚未确认，请维护人员核查数据库连接和通知保存步骤"
		switch state.SQLState() {
		case "42P01", "42703":
			reason = "数据库存储表缺少必要结构，请维护人员核查启动迁移"
		case "42501":
			reason = "数据库未允许本次存储操作，请维护人员核查服务的数据库权限"
		case "23502", "23503", "23505", "23514":
			reason = "数据库拒绝了不符合存储约束的内容，请发布方和维护人员核查这条事件"
		case "55P03", "40P01":
			reason = "数据库当前正在处理冲突的操作，可稍后使用原事件编号重试"
		}
		return reason + "（SQLSTATE " + state.SQLState() + "）"
	}
	return "原因尚未确认，请维护人员核查数据库连接和通知保存步骤"
}

func (r *Repository) AppendTx(ctx context.Context, tx pgx.Tx, input NoticeInput) (string, bool, error) {
	raw, err := validate(input)
	if err != nil {
		return "", false, err
	}
	var parent string
	err = tx.QueryRow(ctx, `SELECT id FROM admin_accounts WHERE user_id=$1 AND id=$2 FOR KEY SHARE`, input.UserID, input.AdminAccountID).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrWorkspace
	}
	if err != nil {
		return "", false, fmt.Errorf("核查这条通知的工作区失败：%s；本次没有保存，处理原因后可使用原事件编号重试", safeStorageCause(err))
	}
	id, err := NewID()
	if err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO site_notifications(id,user_id,admin_account_id,kind,event_key,subject_type,subject_id,level,discovered_at,created_at,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,clock_timestamp(),$10::jsonb) ON CONFLICT(user_id,admin_account_id,kind,event_key) DO NOTHING RETURNING id`, id, input.UserID, input.AdminAccountID, input.Kind, input.EventKey, input.SubjectType, input.SubjectID, input.Level, input.DiscoveredAt, string(raw)).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("写入这条通知失败：%s；本次数据库保存已中止，处理原因后可使用原事件编号重试", safeStorageCause(err))
	}
	var consistent bool
	err = tx.QueryRow(ctx, `SELECT id,subject_type=$5 AND subject_id=$6 AND level=$7 AND discovered_at=$8 AND content=$9::jsonb FROM site_notifications WHERE user_id=$1 AND admin_account_id=$2 AND kind=$3 AND event_key=$4`, input.UserID, input.AdminAccountID, input.Kind, input.EventKey, input.SubjectType, input.SubjectID, input.Level, input.DiscoveredAt, string(raw)).Scan(&id, &consistent)
	if err != nil {
		return "", false, fmt.Errorf("核查这条已保存通知失败：%s；本次数据库保存已中止，请核查原事件后使用同一编号重试", safeStorageCause(err))
	}
	if !consistent {
		return "", false, ErrEventConflict
	}
	return id, false, nil
}
