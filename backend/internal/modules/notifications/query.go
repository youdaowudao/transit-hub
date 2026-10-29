package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// All notification queries use the same current subject eligibility. This is a
// read projection: lifecycle changes never rewrite the stored fact or progress.
const subjectActiveExpression = `(n.subject_type <> 'upstream_site' OR EXISTS (
SELECT 1 FROM upstream_sites s WHERE s.id=n.subject_id AND s.user_id=n.user_id
AND s.admin_account_id=n.admin_account_id AND s.enabled=true))`

const subjectInactiveMessageExpression = `CASE WHEN ` + subjectActiveExpression + ` THEN ''
WHEN EXISTS (SELECT 1 FROM upstream_sites s WHERE s.id=n.subject_id AND s.user_id=n.user_id AND s.admin_account_id=n.admin_account_id)
THEN '这个站点已停止管理，这条消息保留为历史，不再主动提醒。'
ELSE '相关网站已删除，这条消息保留为历史，不再主动提醒。' END`

const noticeColumns = `n.id,n.user_id,n.admin_account_id,n.kind,n.subject_type,n.subject_id,n.level,n.discovered_at,n.created_at,n.content,n.read_at,n.reminder_completed_at,` + subjectActiveExpression + `,` + subjectInactiveMessageExpression

var (
	errStoredContentFormat = errors.New("已保存的通知内容格式无法读取")
	errStoredContentUnsafe = errors.New("已保存的通知内容未通过安全显示检查")
	errStoredPreference    = errors.New("已保存的通知检查间隔不符合整数分钟范围")
)

type noticeScanner interface{ Scan(...any) error }

func scanNotice(row noticeScanner) (Notice, error) {
	var item Notice
	var content []byte
	if err := row.Scan(&item.ID, &item.UserID, &item.AdminAccountID, &item.Kind, &item.SubjectType, &item.SubjectID, &item.Level, &item.DiscoveredAt, &item.CreatedAt, &content, &item.ReadAt, &item.ReminderCompletedAt, &item.SubjectActive, &item.SubjectInactiveMessage); err != nil {
		return item, err
	}
	if err := json.Unmarshal(content, &item.Content); err != nil {
		return Notice{}, errStoredContentFormat
	}
	// Recheck known display fields; never forward raw JSON or unknown stored fields.
	if _, err := validate(NoticeInput{UserID: item.UserID, AdminAccountID: item.AdminAccountID, Kind: item.Kind, EventKey: "stored-notice", SubjectType: item.SubjectType, SubjectID: item.SubjectID, Level: item.Level, DiscoveredAt: item.DiscoveredAt, Content: item.Content}); err != nil {
		return Notice{}, errStoredContentUnsafe
	}
	if item.Content.Details == nil {
		item.Content.Details = []Detail{}
	}
	return item, nil
}

func collectNotices(rows pgx.Rows) ([]Notice, error) {
	defer rows.Close()
	items := []Notice{}
	for rows.Next() {
		item, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) beginRead(ctx context.Context, user, workspace string) (pgx.Tx, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, storageFailure(workspace, "开始读取已保存的通知", err)
	}
	if err := verifyWorkspace(ctx, tx, user, workspace, false); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Service) Summary(ctx context.Context, user, workspace string, query SummaryQuery) (SummaryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := SummaryResponse{WorkspaceAdminAccountID: workspace, DisplayedStates: []DisplayedState{}}
	if len(query.DisplayedIDs) > 60 {
		return result, invalidRequest(workspace, "每次读取最多核查60个已显示的消息编号，请减少编号后重试；本次没有改变任何消息")
	}
	seen := make(map[string]bool, len(query.DisplayedIDs))
	for _, id := range query.DisplayedIDs {
		if seen[id] || checkText("消息编号", id, 512, true, true) != nil {
			return result, invalidRequest(workspace, "已显示的消息编号不能重复、为空、包含非纯文字内容或超过512个字符；请重新读取当前消息后重试，本次没有改变任何消息")
		}
		seen[id] = true
	}
	cursors := make([]*noticeCursor, 3)
	for i, raw := range []string{query.AttentionCursor, query.ImportantCursor, query.CriticalCursor} {
		cursor, err := decodeCursor(raw, user, workspace, Level(i+2), "")
		if err != nil {
			return result, err
		}
		cursors[i] = cursor
	}
	tx, err := s.repository.beginRead(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	prefs, err := readSignupRiskPreferences(ctx, tx, user, workspace)
	if err != nil {
		return result, storageFailure(workspace, "读取注册提醒工作时间", err)
	}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&result.ServerTime); err != nil {
		return result, storageFailure(workspace, "读取通知检查时间", err)
	}
	if err = prefs.at(result.ServerTime); err != nil {
		return result, storageFailure(workspace, "核查注册提醒工作时间", err)
	}
	riskFilter := ""
	if prefs.ActiveUntil == nil {
		riskFilter = " AND n.kind <> 'sub2api.signup_risk'"
	}
	var attentionCount, importantCount int64
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE read_at IS NULL),count(*) FILTER(WHERE level=4 AND reminder_completed_at IS NULL),count(*) FILTER(WHERE level=2 AND reminder_completed_at IS NULL),count(*) FILTER(WHERE level=3 AND reminder_completed_at IS NULL`+riskFilter+`) FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2 AND `+subjectActiveExpression, user, workspace).Scan(&result.UnreadCount, &result.PendingCriticalCount, &attentionCount, &importantCount)
	if err != nil {
		return result, storageFailure(workspace, "读取通知数量", err)
	}
	buckets := []*ActiveBucket{&result.Active.Attention, &result.Active.Important, &result.Active.Critical}
	counts := []int64{attentionCount, importantCount, result.PendingCriticalCount}
	for i, bucket := range buckets {
		level := Level(i + 2)
		sql := `SELECT ` + noticeColumns + ` FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2 AND level=$3 AND reminder_completed_at IS NULL AND ` + subjectActiveExpression
		if level == LevelImportant {
			sql += riskFilter
		}
		args := []any{user, workspace, level}
		if cursor := cursors[i]; cursor != nil {
			sql += ` AND (created_at,id)>($4,$5)`
			args = append(args, cursor.CreatedAt, cursor.ID)
		}
		rows, err := tx.Query(ctx, sql+` ORDER BY created_at,id LIMIT 21`, args...)
		if err != nil {
			return result, storageFailure(workspace, "读取待提醒的通知摘要", err)
		}
		items, err := collectNotices(rows)
		if err != nil {
			return result, storageFailure(workspace, "读取待提醒通知的安全内容", err)
		}
		bucket.TotalCount = counts[i]
		if len(items) > 20 {
			items = items[:20]
			bucket.NextCursor = encodeCursor(items[19], user, workspace, level, "")
		}
		bucket.Items = items
	}
	result.DisplayedStates, err = loadDisplayedStates(ctx, tx, user, workspace, query.DisplayedIDs)
	if err != nil {
		return result, storageFailure(workspace, "核查当前已显示通知的状态", err)
	}
	result.SignupRisk, err = loadSignupRiskSummary(ctx, tx, user, workspace, prefs, result.ServerTime)
	if err != nil {
		return result, storageFailure(workspace, "读取高风险注册提醒和开关", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, storageFailure(workspace, "完成通知摘要读取", err)
	}
	// The message transaction is closed before these serial, independent queries.
	preference, err := s.repository.loadPreference(ctx, user, workspace)
	if err != nil {
		result.PreferenceWarning = &Warning{Message: "消息已读取，但检查间隔暂时无法确认", Detail: SafeStorageCause(err) + "；已取得的消息和数量仍然有效", Next: "请沿用这个工作区最近确认的间隔；没有已确认值时临时每5分钟检查一次，稍后重试读取设置"}
	} else {
		result.IntervalSeconds = &preference.IntervalSeconds
	}
	result.SamplingState = s.repository.loadSamplingState(ctx, user, workspace)
	return result, nil
}

func (s *Service) History(ctx context.Context, user, workspace string, query HistoryQuery) (HistoryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := HistoryResponse{WorkspaceAdminAccountID: workspace, Items: []Notice{}}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 {
		return result, invalidRequest(workspace, "每页消息数量必须是正整数，默认20条，最多50条")
	}
	if query.Limit > 50 {
		return result, invalidRequest(workspace, "每页消息数量超过50条上限；请使用1到50的整数")
	}
	if query.Filter == "" {
		query.Filter = "all"
	}
	if query.Filter != "all" && query.Filter != "unread" && query.Filter != "pending" {
		return result, invalidRequest(workspace, "消息筛选只允许全部、未读或紧急待确认（all、unread、pending）")
	}
	cursor, err := decodeCursor(query.Cursor, user, workspace, 0, query.Filter)
	if err != nil {
		return result, err
	}
	tx, err := s.repository.beginRead(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	sql := `SELECT ` + noticeColumns + ` FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2`
	if query.Filter == "unread" {
		sql += ` AND read_at IS NULL AND ` + subjectActiveExpression
	} else if query.Filter == "pending" {
		sql += ` AND level=4 AND reminder_completed_at IS NULL AND ` + subjectActiveExpression
	}
	args := []any{user, workspace, query.Limit + 1}
	if cursor != nil {
		sql += ` AND (created_at,id)<($4,$5)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	rows, err := tx.Query(ctx, sql+` ORDER BY created_at DESC,id DESC LIMIT $3`, args...)
	if err != nil {
		return result, storageFailure(workspace, "读取通知历史", err)
	}
	items, err := collectNotices(rows)
	if err != nil {
		return result, storageFailure(workspace, "读取通知历史的安全内容", err)
	}
	if len(items) > query.Limit {
		items = items[:query.Limit]
		result.NextCursor = encodeCursor(items[len(items)-1], user, workspace, 0, query.Filter)
	}
	result.Items = items
	if err := tx.Commit(ctx); err != nil {
		return result, storageFailure(workspace, "完成通知历史读取", err)
	}
	return result, nil
}

func (s *Service) Detail(ctx context.Context, user, workspace, id string) (DetailResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := DetailResponse{WorkspaceAdminAccountID: workspace}
	if err := checkText("消息编号", id, 512, true, true); err != nil {
		return result, invalidRequest(workspace, "消息编号为空、不符合纯文字格式或超过512个字符")
	}
	tx, err := s.repository.beginRead(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	item, err := scanNotice(tx.QueryRow(ctx, `SELECT `+noticeColumns+` FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2 AND n.id=$3`, user, workspace, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return result, unavailableNotice(workspace)
	}
	if err != nil {
		return result, storageFailure(workspace, "读取这条通知详情", err)
	}
	result.Item = item
	if action := item.Content.Action; action != nil {
		switch action.Kind {
		case "group_rates":
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_sites WHERE user_id=$1 AND admin_account_id=$2 AND id=$3)`, user, workspace, action.SiteID).Scan(&exists); err != nil {
				return result, storageFailure(workspace, "核查这条通知的网站检查入口", err)
			}
			result.CheckAction = &CheckAction{Available: exists, Message: "查看相关网站当前保存的上游倍率，不会修改主站账号"}
			if exists && !item.SubjectActive {
				result.CheckAction.Message = "这个站点已停止管理，不会自动刷新；这里只查看已保存的上游倍率，不会修改主站账号"
			}
			if !exists {
				result.CheckAction.Message = "相关网站已删除，这条提醒保留当时信息"
			}
		case "connection_health":
			result.CheckAction = &CheckAction{Available: true, Message: "打开分组健康页面核查当前状态"}
		default:
			result.CheckAction = &CheckAction{Message: "当前版本暂时不能打开这个检查入口，请从菜单进入对应功能"}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return result, storageFailure(workspace, "完成这条通知详情读取", err)
	}
	return result, nil
}

func unavailableNotice(workspace string) *RequestError {
	return requestFailure(http.StatusNotFound, workspace, "这次请求的消息无法访问，本次没有改变任何消息", "一个或多个消息编号已不存在或不属于当前登录用户和工作区", "请重新读取当前工作区的消息，再对仍可查看的具体消息重试", false)
}

// One scoped batch read, in the same snapshot as counts and bucket pages.
func loadDisplayedStates(ctx context.Context, tx pgx.Tx, user, workspace string, ids []string) ([]DisplayedState, error) {
	result := make([]DisplayedState, 0, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT n.id,`+subjectActiveExpression+`,`+subjectInactiveMessageExpression+`,n.read_at,n.reminder_completed_at FROM site_notifications n WHERE n.user_id=$1 AND n.admin_account_id=$2 AND n.id=ANY($3::text[])`, user, workspace, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]DisplayedState, len(ids))
	for rows.Next() {
		var state DisplayedState
		if err := rows.Scan(&state.ID, &state.SubjectActive, &state.SubjectInactiveMessage, &state.ReadAt, &state.ReminderCompletedAt); err != nil {
			return nil, err
		}
		found[state.ID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		state, ok := found[id]
		if !ok {
			state = DisplayedState{ID: id, SubjectInactiveMessage: "这条消息已无法访问，请重新读取当前工作区的消息列表"}
		}
		result = append(result, state)
	}
	return result, nil
}
