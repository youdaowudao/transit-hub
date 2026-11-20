package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Filters struct {
	FromMs   int64  `json:"fromMs"`
	ToMs     int64  `json:"toMs"`
	Scope    string `json:"scope"`
	From     string `json:"from"`
	To       string `json:"to"`
	TargetID string `json:"targetId"`
	ReportID string `json:"reportId"`
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
	Kind     string `json:"kind"`
	Enabled  *bool  `json:"enabled"`
	Q        string `json:"q"`
}
type cursor struct {
	N  int64  `json:"n"`
	ID string `json:"id"`
}

func encodeCursor(n int64, id string) string {
	b, _ := json.Marshal(cursor{n, id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	if len(s) > 1024 {
		return cursor{}, Fail(422, "分页位置过长，请从第一页重新读取", "pagination")
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return cursor{}, Fail(422, "分页位置无效，请从第一页重新读取", "pagination")
	}
	var c cursor
	e = json.Unmarshal(b, &c)
	if e != nil || c.ID == "" || len(c.ID) > 200 {
		return c, Fail(422, "分页位置无效，请从第一页重新读取", "pagination")
	}
	return c, nil
}
func (s *Store) ListQuestions(ctx context.Context, o Owner, f Filters) (Page[QuestionSummary], error) {
	out := Page[QuestionSummary]{Items: []QuestionSummary{}}
	if e := s.EnsureScope(ctx, o); e != nil {
		return out, e
	}
	c, e := decodeCursor(f.Cursor)
	if e != nil {
		return out, e
	}
	if utf8.RuneCountInString(f.Q) > 80 {
		return out, Fail(422, "题名查找最多80字", "question")
	}
	limit := f.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return out, Fail(422, "每页题数必须为1～50", "pagination")
	}
	q := `SELECT question_id,json_extract(definition_json,'$.title'),json_extract(definition_json,'$.kind'),json_extract(definition_json,'$.enabled'),json_extract(definition_json,'$.order'),revision FROM hub_questions WHERE user_id=? AND workspace_id=?`
	args := []any{o.UserID, o.WorkspaceID}
	if f.Kind != "" {
		q += ` AND json_extract(definition_json,'$.kind')=?`
		args = append(args, f.Kind)
	}
	if f.Enabled != nil {
		q += ` AND json_extract(definition_json,'$.enabled')=?`
		args = append(args, *f.Enabled)
	}
	if f.Q != "" {
		q += ` AND instr(json_extract(definition_json,'$.title'),?)>0`
		args = append(args, f.Q)
	}
	if f.Cursor != "" {
		q += ` AND (json_extract(definition_json,'$.order')>? OR (json_extract(definition_json,'$.order')=? AND question_id>?))`
		args = append(args, c.N, c.N, c.ID)
	}
	q += ` ORDER BY json_extract(definition_json,'$.order'),question_id LIMIT ?`
	args = append(args, limit+1)
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v QuestionSummary
		if e = rows.Scan(&v.ID, &v.Title, &v.Kind, &v.Enabled, &v.Order, &v.Revision); e != nil {
			return out, e
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1]
			out.NextCursor = encodeCursor(int64(last.Order), last.ID)
			break
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}

const summaryCols = `report_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,summary_json,error_json`

const historySummaryCols = `report_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,json_object('iqScore',json_extract(summary_json,'$.iqScore'),'totalScore',json_extract(summary_json,'$.totalScore'),'complete',json_extract(summary_json,'$.complete'),'verdict',json_object('scoring_version',json_extract(summary_json,'$.verdict.scoring_version'))),json_object('message',substr(json_extract(error_json,'$.message'),1,512),'step',json_extract(error_json,'$.step'))`

func scanSummary(row interface{ Scan(...any) error }) (Summary, error) {
	v := Summary{}
	var summary, detail string
	e := row.Scan(&v.ReportID, &v.TargetID, &v.AccountID, &v.AccountName, &v.GroupID, &v.GroupName, &v.BaseURL, &v.Protocol, &v.Model, &v.CreatedAtMs, &v.UpdatedAtMs, &v.Status, &summary, &detail)
	v.Summary = json.RawMessage(summary)
	v.Error = json.RawMessage(detail)
	return v, e
}
func dateBoundary(s string, end bool, loc *time.Location) (int64, error) {
	if n, e := strconv.ParseInt(s, 10, 64); e == nil {
		return n, nil
	}
	if t, e := time.ParseInLocation("2006-01-02", s, loc); e == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t.UnixMilli(), nil
	}
	if t, e := time.Parse(time.RFC3339, s); e == nil {
		return t.UnixMilli(), nil
	}
	return 0, Fail(422, "日期格式无效，请使用年-月-日", "history")
}
func (s *Store) ListReports(ctx context.Context, o Owner, f Filters) (Page[Summary], error) {
	out := Page[Summary]{Items: []Summary{}}
	if e := o.Validate(); e != nil {
		return out, e
	}
	c, e := decodeCursor(f.Cursor)
	if e != nil {
		return out, e
	}
	limit := f.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return out, Fail(422, "每页报告数必须为1～50", "pagination")
	}
	loc, e := time.LoadLocation("Asia/Singapore")
	if e != nil {
		return out, e
	}
	now := Now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from, to := midnight.AddDate(0, 0, -30).UnixMilli(), midnight.AddDate(0, 0, 1).UnixMilli()
	if f.Scope == "sidebar" {
		from = midnight.AddDate(0, 0, -1).UnixMilli()
	} else {
		if f.From != "" {
			from, e = dateBoundary(f.From, false, loc)
			if e != nil {
				return out, e
			}
		}
		if f.To != "" {
			to, e = dateBoundary(f.To, true, loc)
			if e != nil {
				return out, e
			}
		}
	}
	if f.Scope != "sidebar" {
		if f.FromMs != 0 {
			from = f.FromMs
		}
		if f.ToMs != 0 {
			to = f.ToMs
		}
	}
	if from >= to {
		return out, Fail(422, "开始日期必须早于结束日期", "history")
	}
	q := `SELECT ` + historySummaryCols + ` FROM hub_reports WHERE user_id=? AND workspace_id=? AND created_at_ms>=? AND created_at_ms<?`
	args := []any{o.UserID, o.WorkspaceID, from, to}
	if f.TargetID != "" {
		q += ` AND target_id=?`
		args = append(args, f.TargetID)
	}
	if f.ReportID != "" {
		q += ` AND report_id=?`
		args = append(args, f.ReportID)
	}
	if f.Cursor != "" {
		q += ` AND (created_at_ms<? OR (created_at_ms=? AND report_id<?))`
		args = append(args, c.N, c.N, c.ID)
	}
	q += ` ORDER BY created_at_ms DESC,report_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	bytesUsed := len(`{"items":[],"nextCursor":""}`)
	for rows.Next() {
		v, e := scanSummary(rows)
		if e != nil {
			return out, e
		}
		raw, _ := json.Marshal(v)
		if len(out.Items) == limit || (len(out.Items) > 0 && bytesUsed+len(raw)+2048 > 64<<10) {
			last := out.Items[len(out.Items)-1]
			out.NextCursor = encodeCursor(last.CreatedAtMs, last.ReportID)
			break
		}
		if len(raw)+2048 > 64<<10 {
			return out, Fail(503, "这条报告的账号摘要超过读取上限，请按报告编号打开；已有结果未删除", "history")
		}
		out.Items = append(out.Items, v)
		bytesUsed += len(raw) + 1
	}
	return out, rows.Err()
}
func (s *Store) Report(ctx context.Context, o Owner, id string) (Report, error) {
	v, e := scanSummary(s.db.QueryRowContext(ctx, `SELECT `+summaryCols+` FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, id))
	if e == sql.ErrNoRows {
		return Report{}, Fail(404, "报告不存在或不属于当前工作区", "report")
	}
	if e != nil {
		return Report{}, e
	}
	out := Report{Summary: v, ItemCounts: map[string]int{}}
	var requestRaw string
	if e = s.db.QueryRowContext(ctx, `SELECT request_json FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, id).Scan(&requestRaw); e != nil {
		return out, e
	}
	out.Request = json.RawMessage(requestRaw)
	var raw string
	if e = s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, id).Scan(&raw); e != nil {
		return out, e
	}
	var snapshot Snapshot
	if e = json.Unmarshal([]byte(raw), &snapshot); e != nil {
		return out, e
	}
	out.Settings = snapshot.Settings
	out.Retry = snapshot.Retry
	out.MemoryLimitMiB = out.Settings.MemoryLimitMiB
	out.TotalTimeoutSeconds = out.Settings.TotalTimeoutSeconds
	var attempts int
	if e = s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(attempts),0) FROM hub_items WHERE report_id=?`, id).Scan(&attempts); e != nil {
		return out, e
	}
	summary := map[string]any{}
	_ = json.Unmarshal(out.Summary.Summary, &summary)
	if summary == nil {
		summary = map[string]any{}
	}
	summary["attempts"] = attempts
	out.Summary.Summary, _ = json.Marshal(summary)
	if snapshot.Retry != nil {
		ids, _ := json.Marshal(snapshot.Retry.ItemIDs)
		e = s.db.QueryRowContext(ctx, `SELECT COALESCE(min(NULLIF(started_at_ms,0)),0) FROM hub_items WHERE report_id=? AND item_id IN(SELECT value FROM json_each(?))`, id, string(ids)).Scan(&out.StartedAtMs)
	} else {
		e = s.db.QueryRowContext(ctx, `SELECT COALESCE(min(NULLIF(started_at_ms,0)),0) FROM hub_items WHERE report_id=?`, id).Scan(&out.StartedAtMs)
	}
	if e != nil {
		return out, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT status,count(*) FROM hub_items WHERE report_id=? GROUP BY status`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if e = rows.Scan(&key, &n); e != nil {
			return out, e
		}
		out.ItemCounts[key] = n
	}
	return out, rows.Err()
}
func (s *Store) ListItems(ctx context.Context, o Owner, id string, f Filters) (Page[Item], error) {
	out := Page[Item]{Items: []Item{}}
	if _, e := s.Report(ctx, o, id); e != nil {
		return out, e
	}
	c, e := decodeCursor(f.Cursor)
	if e != nil {
		return out, e
	}
	limit := f.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 5 {
		return out, Fail(422, "每页答案最多5项", "pagination")
	}
	q := `SELECT item_id,section_id,order_index,status,attempts,started_at_ms,completed_at_ms,result_json,question_json,image IS NOT NULL FROM hub_items WHERE report_id=?`
	args := []any{id}
	if f.Cursor != "" {
		q += ` AND (order_index>? OR (order_index=? AND item_id>?))`
		args = append(args, c.N, c.N, c.ID)
	}
	q += ` ORDER BY order_index,item_id LIMIT ?`
	args = append(args, limit+1)
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	bytes, lastOrder := 0, int64(0)
	lastID := ""
	for rows.Next() {
		var v Item
		var order int64
		var result, raw string
		if e = rows.Scan(&v.ItemID, &v.SectionID, &order, &v.Status, &v.Attempts, &v.StartedAtMs, &v.CompletedAtMs, &result, &raw, &v.ImageAvailable); e != nil {
			return out, e
		}
		if len(out.Items) == limit || (len(out.Items) > 0 && bytes+len(result)+len(raw) > 2<<20) {
			out.NextCursor = encodeCursor(lastOrder, lastID)
			break
		}
		g, decodeErr := decodeListedQuestion(raw, v.SectionID)
		if decodeErr != nil {
			return out, decodeErr
		}
		v.Definition = g.Definition
		v.OrderIndex = int(order)
		v.Seed = g.Seed
		v.Prompt = g.Prompt
		v.Expected = g.Expected
		v.Tags = g.Tags
		v.Result = json.RawMessage(result)
		out.Items = append(out.Items, v)
		bytes += len(result) + len(raw)
		lastOrder, lastID = order, v.ItemID
	}
	return out, rows.Err()
}

func decodeListedQuestion(raw, section string) (SnapshotQuestion, error) {
	var question SnapshotQuestion
	err := json.Unmarshal([]byte(raw), &question)
	if err == nil || section != "injection" {
		return question, err
	}
	// Older stored injection templates may be non-text. Use the default template
	// for that value and still validate every other field through the same DTO.
	var saved, definition map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &saved) != nil || json.Unmarshal(saved["definition"], &definition) != nil {
		return question, err
	}
	template := definition["template"]
	if len(template) == 0 || template[0] == '"' || string(template) == "null" {
		return question, err
	}
	definition["template"] = json.RawMessage(`""`)
	saved["definition"], err = json.Marshal(definition)
	if err != nil {
		return question, err
	}
	normalized, err := json.Marshal(saved)
	if err != nil {
		return question, err
	}
	question = SnapshotQuestion{}
	err = json.Unmarshal(normalized, &question)
	return question, err
}

type SnapshotQuestion = struct {
	Definition questions.Definition `json:"definition"`
	Seed       int64                `json:"seed"`
	Prompt     string               `json:"prompt"`
	Expected   string               `json:"expected"`
	Tags       []string             `json:"tags"`
}

func (s *Store) Image(ctx context.Context, o Owner, id, itemID string) ([]byte, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT i.image FROM hub_items i JOIN hub_reports r ON r.report_id=i.report_id WHERE r.user_id=? AND r.workspace_id=? AND i.report_id=? AND i.item_id=?`, o.UserID, o.WorkspaceID, id, itemID).Scan(&b)
	if e == sql.ErrNoRows || e == nil && len(b) == 0 {
		return nil, Fail(404, "该报告没有这张图片", "image")
	}
	if len(b) > 2<<20 {
		return nil, fmt.Errorf("图片超过读取上限")
	}
	return b, e
}
func (s *Store) Exists(ctx context.Context, o Owner, id string, request []byte) (bool, error) {
	var u, w, raw string
	e := s.db.QueryRowContext(ctx, `SELECT user_id,workspace_id,request_json FROM hub_reports WHERE report_id=?`, id).Scan(&u, &w, &raw)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if u != o.UserID || w != o.WorkspaceID {
		return true, Fail(404, "报告不存在或不属于当前工作区", "report")
	}
	if request != nil && strings.TrimSpace(raw) != string(request) {
		return true, Fail(409, "相同报告编号的检测选择已改变，请保留原任务或明确新建检测", "create")
	}
	return true, nil
}

// CheckRequest compares inside the short-lived helper. Hub never needs to read
// the potentially large original request to establish idempotency.
func (s *Store) CheckRequest(ctx context.Context, o Owner, id string, request json.RawMessage) error {
	var value any
	if e := json.Unmarshal(request, &value); e != nil {
		return Fail(422, "原检测选择不是有效JSON，请重新打开新建页面", "create")
	}
	canonical, e := json.Marshal(value)
	if e != nil {
		return e
	}
	exists, e := s.Exists(ctx, o, id, canonical)
	if e != nil {
		return e
	}
	if !exists {
		return Fail(404, "报告不存在或不属于当前工作区", "report")
	}
	return nil
}
func ReportView(r Report) map[string]any {
	raw, _ := json.Marshal(r)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	delete(v, "request")
	if summary, ok := v["summary"].(map[string]any); ok {
		delete(summary, "prefix")
	}
	return v
}
