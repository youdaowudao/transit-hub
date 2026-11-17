package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Only the latest explicit retry is retained. Original request and questions
// stay immutable; this is a version and execution subset of the same report.
type Retry struct {
	ID              string   `json:"id"`
	ExpectedRetryID string   `json:"expectedRetryId"`
	ItemIDs         []string `json:"itemIds"`
	AttemptsBefore  int      `json:"attemptsBefore"`
	AcceptedAtMs    int64    `json:"acceptedAtMs"`
}
type RetryRequest struct {
	ReportID        string   `json:"reportId"`
	RetryID         string   `json:"retryId"`
	ExpectedRetryID string   `json:"expectedRetryId"`
	ItemIDs         []string `json:"itemIds"`
	Target          *Target  `json:"target,omitempty"`
}

func NormalizeRetryIDs(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 128 {
		return nil, Fail(422, "请明确选择1～128个失败或未完成项目，未重新检测任何题目", "retry")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > 200 {
			return nil, Fail(422, "补测题目编号无效，请重新读取原报告", "retry")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (s *Store) RetryReport(ctx context.Context, o Owner, r RetryRequest) (Report, error) {
	if e := o.Validate(); e != nil {
		return Report{}, e
	}
	if !reportPattern.MatchString(r.ReportID) || !reportPattern.MatchString(r.RetryID) {
		return Report{}, Fail(422, "补测操作编号无效，请重新读取原报告后重试", "retry")
	}
	ids, e := NormalizeRetryIDs(r.ItemIDs)
	if e != nil {
		return Report{}, e
	}
	r.ItemIDs = ids
	if e = s.writable(); e != nil {
		return Report{}, e
	}
	e = retryWrite(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		var snapshotRaw, status, summaryRaw string
		var target Target
		e = tx.QueryRowContext(ctx, `SELECT snapshot_json,status,summary_json,account_id,account_name,group_name,base_url,protocol FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, r.ReportID).Scan(&snapshotRaw, &status, &summaryRaw, &target.AccountID, &target.AccountName, &target.GroupName, &target.BaseURL, &target.Protocol)
		if e == sql.ErrNoRows {
			return Fail(404, "报告不存在或不属于当前工作区", "report")
		}
		if e != nil {
			return e
		}
		var snap Snapshot
		if e = json.Unmarshal([]byte(snapshotRaw), &snap); e != nil {
			return e
		}
		current := ""
		if snap.Retry != nil {
			current = snap.Retry.ID
		}
		if current == r.RetryID {
			if snap.Retry.ExpectedRetryID != r.ExpectedRetryID || !reflect.DeepEqual(snap.Retry.ItemIDs, r.ItemIDs) {
				return Fail(409, "同一补测编号的题目已变化，请重新读取报告；没有重复检测", "retry")
			}
			return nil
		}
		if r.ExpectedRetryID != current {
			return Fail(409, "报告已有更新一轮补测，请重新读取后再选择；没有再次发送请求", "retry")
		}
		if status != "done" && status != "partial" && status != "cancelled" && status != "interrupted" {
			return Fail(409, "本报告仍在运行或等待，请等待结束或先停止；没有重复补测", "retry")
		}
		if r.Target != nil && (target.AccountID != r.Target.AccountID || target.BaseURL != r.Target.BaseURL || target.Protocol != r.Target.Protocol) {
			return Fail(409, "原报告的账号、地址或协议已变化，请重新检测；没有修改原报告", "retry")
		}
		// The report's original detection protocol survives later settings edits.
		originalProtocol := snap.Settings.Protocol
		if originalProtocol == "" {
			originalProtocol = target.Protocol
		}
		var settingsRaw string
		var revision int
		if e = tx.QueryRowContext(ctx, `SELECT settings_json,revision FROM hub_settings WHERE user_id=? AND workspace_id=?`, o.UserID, o.WorkspaceID).Scan(&settingsRaw, &revision); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(settingsRaw), &snap.Settings); e != nil {
			return e
		}
		snap.Settings.Revision = revision
		snap.Settings.Protocol = originalProtocol
		if e = snap.Settings.Validate(); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT item_id,status,result_json,attempts FROM hub_items WHERE report_id=?`, r.ReportID)
		if e != nil {
			return e
		}
		allowed := map[string]bool{}
		attempts := 0
		for rows.Next() {
			var id, state, raw string
			var n int
			if e = rows.Scan(&id, &state, &raw, &n); e != nil {
				rows.Close()
				return e
			}
			attempts += n
			var result struct {
				Judgment json.RawMessage `json:"judgment"`
			}
			_ = json.Unmarshal([]byte(raw), &result)
			var judgmentStatus string
			if json.Unmarshal(result.Judgment, &judgmentStatus) != nil {
				var j struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(result.Judgment, &j)
				judgmentStatus = j.Status
			}
			allowed[id] = state == "error" || judgmentStatus == "unknown" || state == "queued" || state == "running"
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		kinds := map[string]bool{}
		for _, id := range ids {
			if !allowed[id] {
				return Fail(422, "所选项目不是可补测的失败或未完成项；成功、未抽中和不适用项目保持原样，请重新读取报告", "retry")
			}
			for _, q := range snap.Questions {
				if q.Definition.ID == id {
					kind := q.Definition.Kind
					if kind == "fingerprint_numeric" {
						kind = "fingerprint"
					}
					kinds[kind] = true
				}
			}
		}
		now := Now().UnixMilli()
		snap.Retry = &Retry{r.RetryID, r.ExpectedRetryID, ids, attempts, now}
		snapshot, e := json.Marshal(snap)
		if e != nil {
			return e
		}
		summary := map[string]any{}
		_ = json.Unmarshal([]byte(summaryRaw), &summary)
		if summary == nil {
			summary = map[string]any{}
		}
		summary["complete"] = false
		summary["attempts"] = attempts
		if total, ok := summary["total"].(map[string]any); ok {
			affected := false
			if parts, ok := total["parts"].([]any); ok {
				for _, raw := range parts {
					if p, ok := raw.(map[string]any); ok && kinds[stringValue(p["key"])] {
						p["known"] = false
						affected = true
					}
				}
			}
			if affected {
				total["complete"] = false
				total["score"] = nil
				summary["totalScore"] = nil
			}
		}
		if kinds["iq"] {
			summary["iqScore"] = nil
		}
		summaryJSON, e := json.Marshal(summary)
		if e != nil {
			return e
		}
		for _, id := range ids {
			if _, e = tx.ExecContext(ctx, `UPDATE hub_items SET status='queued',started_at_ms=0,completed_at_ms=0 WHERE report_id=? AND item_id=?`, r.ReportID, id); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE hub_reports SET status='queued',snapshot_json=?,summary_json=?,error_json='{}',updated_at_ms=? WHERE user_id=? AND workspace_id=? AND report_id=?`, string(snapshot), string(summaryJSON), now, o.UserID, o.WorkspaceID, r.ReportID); e != nil {
			return e
		}
		return tx.Commit()
	})
	if e != nil {
		return Report{}, e
	}
	return s.Report(ctx, o, r.ReportID)
}
func stringValue(v any) string { s, _ := v.(string); return s }
