package notifications

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) beginWrite(ctx context.Context, user, workspace string) (pgx.Tx, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, storageFailure(workspace, "开始保存通知进度或检查间隔", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageFailure(workspace, "准备保存通知进度或检查间隔", err)
	}
	if err := verifyWorkspace(ctx, tx, user, workspace, true); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Service) Progress(ctx context.Context, user, workspace string, ids []string, action string) (ProgressResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := ProgressResponse{WorkspaceAdminAccountID: workspace, Items: []Progress{}}
	if len(ids) == 0 || len(ids) > 50 {
		return result, invalidRequest(workspace, "每次操作必须提供1到50个实际展示的消息编号")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] || checkText("消息编号", id, 512, true, true) != nil {
			return result, invalidRequest(workspace, "消息编号不能重复、为空、包含非纯文字内容或超过512个字符；本组全部没有保存")
		}
		seen[id] = true
	}
	if action != "read" && action != "present" && action != "dismiss" && action != "ack" {
		return result, invalidRequest(workspace, "消息操作只允许已读、关注已展示、重要收起或紧急知悉（read、present、dismiss、ack）")
	}
	tx, err := s.repository.beginWrite(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,level FROM site_notifications WHERE user_id=$1 AND admin_account_id=$2 AND id=ANY($3::text[]) ORDER BY id FOR UPDATE`, user, workspace, ids)
	if err != nil {
		return result, storageFailure(workspace, "核查这组通知的归属和等级", err)
	}
	levels := make(map[string]Level, len(ids))
	for rows.Next() {
		var id string
		var level Level
		if err := rows.Scan(&id, &level); err != nil {
			rows.Close()
			return result, storageFailure(workspace, "核查这组通知的安全编号和等级", err)
		}
		levels[id] = level
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, storageFailure(workspace, "完成这组通知的归属核查", err)
	}
	if len(levels) != len(ids) {
		return result, unavailableNotice(workspace)
	}
	for _, level := range levels {
		if level < LevelNormal || level > LevelUrgent || action == "present" && level != LevelAttention || action == "dismiss" && level != LevelImportant || action == "ack" && level != LevelUrgent {
			return result, invalidRequest(workspace, "操作与本组消息等级不一致，整组没有保存：关注只能记录已展示，重要只能记录收起，紧急必须明确知悉；已读适用于所有等级")
		}
	}
	rows, err = tx.Query(ctx, `UPDATE site_notifications SET read_at=CASE WHEN $4 IN ('read','ack') THEN COALESCE(read_at,transaction_timestamp()) ELSE read_at END,reminder_completed_at=CASE WHEN $4 IN ('present','dismiss','ack') OR ($4='read' AND level=3) THEN COALESCE(reminder_completed_at,transaction_timestamp()) ELSE reminder_completed_at END WHERE user_id=$1 AND admin_account_id=$2 AND id=ANY($3::text[]) RETURNING id,read_at,reminder_completed_at`, user, workspace, ids, action)
	if err != nil {
		return result, storageFailure(workspace, "保存这组通知的阅读或提醒进度", err)
	}
	progress := make(map[string]Progress, len(ids))
	for rows.Next() {
		var item Progress
		if err := rows.Scan(&item.ID, &item.ReadAt, &item.ReminderCompletedAt); err != nil {
			rows.Close()
			return result, storageFailure(workspace, "核查这组通知保存后的进度", err)
		}
		progress[item.ID] = item
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, storageFailure(workspace, "取得这组通知保存后的进度", err)
	}
	if len(progress) != len(ids) {
		return result, storageFailure(workspace, "核对这组通知完整的保存结果", pgx.ErrNoRows)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, commitFailure(workspace, "这组通知进度", err)
	}
	for _, id := range ids {
		result.Items = append(result.Items, progress[id])
	}
	return result, nil
}
