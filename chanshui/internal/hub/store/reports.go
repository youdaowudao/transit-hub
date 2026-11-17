package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

var reportPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func canonicalRequest(r CreateRequest) []byte {
	if len(r.Request) > 0 {
		var value any
		if json.Unmarshal(r.Request, &value) == nil {
			b, _ := json.Marshal(value)
			return b
		}
	}
	r.Target = Target{}
	r.AccountID = ""
	r.AccountName = ""
	r.GroupName = ""
	r.BaseURL = ""
	r.Protocol = ""
	b, _ := json.Marshal(r)
	return b
}
func (s *Store) CreateReport(ctx context.Context, o Owner, r CreateRequest) (Report, error) {
	if e := o.Validate(); e != nil {
		return Report{}, e
	}
	if !reportPattern.MatchString(r.ReportID) {
		return Report{}, Fail(422, "报告编号必须为32位小写随机十六进制字符", "create")
	}
	if strings.TrimSpace(r.Model) == "" || len(r.Model) > 256 || r.TargetID == "" {
		return Report{}, Fail(422, "请先选择账号并填写模型名（最多256字节）", "create")
	}
	req := canonicalRequest(r)
	exists, e := s.Exists(ctx, o, r.ReportID, req)
	if e != nil {
		return Report{}, e
	}
	if exists {
		return s.Report(ctx, o, r.ReportID)
	}
	if e = s.EnsureScope(ctx, o); e != nil {
		return Report{}, e
	}
	if e = s.writable(); e != nil {
		return Report{}, e
	}
	e = retryWrite(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		var raw string
		var settings Settings
		var rev int
		if e = tx.QueryRowContext(ctx, "SELECT settings_json,revision FROM hub_settings WHERE user_id=? AND workspace_id=?", o.UserID, o.WorkspaceID).Scan(&raw, &rev); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(raw), &settings); e != nil {
			return e
		}
		settings.Revision = rev
		sections, ids := map[string]bool{}, map[string]bool{}
		for _, s := range r.SelectedSectionIDs {
			sections[s] = true
		}
		for _, id := range r.SelectedQuestionIDs {
			ids[id] = true
		}
		rows, e := tx.QueryContext(ctx, `SELECT definition_json,revision FROM hub_questions WHERE user_id=? AND workspace_id=? AND json_extract(definition_json,'$.enabled')=1 ORDER BY json_extract(definition_json,'$.order'),question_id`, o.UserID, o.WorkspaceID)
		if e != nil {
			return e
		}
		defs := []questions.Definition{}
		for rows.Next() {
			var b string
			var revision int
			if e = rows.Scan(&b, &revision); e != nil {
				rows.Close()
				return e
			}
			var q questions.Definition
			if e = json.Unmarshal([]byte(b), &q); e != nil {
				rows.Close()
				return e
			}
			q.Revision = revision
			section := q.Kind
			if strings.HasPrefix(section, "fingerprint_") {
				section = "fingerprint"
			}
			if len(sections) > 0 && !sections[q.Kind] && !sections[section] {
				continue
			}
			if len(ids) > 0 && !ids[q.ID] {
				continue
			}
			defs = append(defs, q)
			if len(defs) > 128 {
				rows.Close()
				return Fail(422, "本次选择超过128项，请减少检测项", "create")
			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if len(defs) == 0 {
			return Fail(422, "没有可执行的题目，请在设置中新增或启用题目并重新选择", "create")
		}
		snap := Snapshot{Settings: settings, Questions: []questions.Generated{}, UpstreamVersion: "308315dc9c0dd83504f042bde2a96e82e243fe73"}
		counts := map[string]int{}
		for _, q := range defs {
			counts[q.Kind]++
			if (q.Kind == "iq" || q.Kind == "knowledge") && counts[q.Kind] > 32 {
				return Fail(422, "推理/知识每类最多启用32题", "create")
			}
			var seedBytes [8]byte
			if _, e = rand.Read(seedBytes[:]); e != nil {
				return e
			}
			seed := int64(binary.LittleEndian.Uint64(seedBytes[:]) & 0x1fffffffffffff)
			g, e := questions.Generate(q, seed)
			if e != nil {
				return Fail(422, fmt.Sprintf("题目%s无法生成：%s", q.Title, e), "generate")
			}
			snap.Questions = append(snap.Questions, g)
		}
		snapshotJSON, e := json.Marshal(snap)
		if e != nil {
			return e
		}
		if len(snapshotJSON) > 512<<10 {
			return Fail(422, "本次题目快照超过512KiB，请减少题目或缩短正文", "create")
		}
		target := r.Target
		if target.AccountID == "" {
			target.AccountID = r.AccountID
		}
		if target.AccountName == "" {
			target.AccountName = r.AccountName
		}
		if target.GroupName == "" {
			target.GroupName = r.GroupName
		}
		if target.BaseURL == "" {
			target.BaseURL = r.BaseURL
		}
		if target.Protocol == "" {
			target.Protocol = r.Protocol
		}
		groupID := ""
		if r.GroupID != nil {
			groupID = fmt.Sprint(r.GroupID)
		}
		now := Now().UnixMilli()
		_, e = tx.ExecContext(ctx, `INSERT INTO hub_reports(report_id,user_id,workspace_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,request_json,snapshot_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, r.ReportID, o.UserID, o.WorkspaceID, r.TargetID, target.AccountID, target.AccountName, groupID, target.GroupName, target.BaseURL, target.Protocol, r.Model, now, now, string(req), string(snapshotJSON))
		if e != nil {
			return e
		}
		for i, g := range snap.Questions {
			raw, _ := json.Marshal(g)
			var image any
			if len(g.Image) > 0 {
				image = g.Image
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO hub_items(report_id,item_id,section_id,order_index,status,question_json,image) VALUES(?,?,?,?,'queued',?,?)`, r.ReportID, g.Definition.ID, g.Definition.Kind, i, string(raw), image); e != nil {
				return e
			}
		}
		return tx.Commit()
	})
	if e != nil {
		return Report{}, errorSQL(e)
	}
	return s.Report(ctx, o, r.ReportID)
}
func (s *Store) LoadRun(ctx context.Context, o Owner, id string) (Report, Snapshot, error) {
	r, e := s.Report(ctx, o, id)
	if e != nil {
		return r, Snapshot{}, e
	}
	var raw string
	e = s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM hub_reports WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, id).Scan(&raw)
	if e != nil {
		return r, Snapshot{}, e
	}
	var snap Snapshot
	e = json.Unmarshal([]byte(raw), &snap)
	return r, snap, e
}
func (s *Store) BeginRun(ctx context.Context, o Owner, id string) error {
	return retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, `UPDATE hub_reports SET status='running',updated_at_ms=? WHERE user_id=? AND workspace_id=? AND report_id=? AND status='queued'`, Now().UnixMilli(), o.UserID, o.WorkspaceID, id)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Fail(409, "报告已开始或已结束，相同报告编号不会重复调用模型", "run")
		}
		return nil
	})
}
func (s *Store) BeginItem(ctx context.Context, o Owner, id, itemID string) error {
	return retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, `UPDATE hub_items SET status='running',attempts=0,started_at_ms=? WHERE report_id=? AND item_id=? AND status='queued' AND EXISTS(SELECT 1 FROM hub_reports r WHERE r.report_id=hub_items.report_id AND r.user_id=? AND r.workspace_id=? AND r.status='running')`, Now().UnixMilli(), id, itemID, o.UserID, o.WorkspaceID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Fail(409, "该检测项已执行或报告已结束，未重复调用上游", "item")
		}
		return nil
	})
}
func (s *Store) SaveItem(ctx context.Context, o Owner, id, itemID, status string, result any, attempts int) error {
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	if len(raw) > 256<<10 {
		return Fail(422, "本题结果超过256KiB，不能保存为完整答案", "item")
	}
	return retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, `UPDATE hub_items SET status=?,result_json=?,attempts=?,completed_at_ms=? WHERE report_id=? AND item_id=? AND status IN('queued','running') AND EXISTS(SELECT 1 FROM hub_reports r WHERE r.report_id=hub_items.report_id AND r.user_id=? AND r.workspace_id=? AND r.status='running')`, status, string(raw), attempts, Now().UnixMilli(), id, itemID, o.UserID, o.WorkspaceID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Fail(409, "报告已结束，未覆盖已保存结果", "item")
		}
		return nil
	})
}
func (s *Store) Finish(ctx context.Context, o Owner, id, status string, summary, detail any) error {
	if status != "done" && status != "partial" && status != "cancelled" && status != "interrupted" {
		return Fail(422, "报告结束状态无效", "finish")
	}
	b, _ := json.Marshal(summary)
	d, _ := json.Marshal(detail)
	return retryWrite(ctx, func() error {
		_, e := s.db.ExecContext(ctx, `UPDATE hub_reports SET status=?,summary_json=?,error_json=?,updated_at_ms=? WHERE user_id=? AND workspace_id=? AND report_id=? AND status IN('queued','running')`, status, string(b), string(d), Now().UnixMilli(), o.UserID, o.WorkspaceID, id)
		return e
	})
}
func (s *Store) FinishInterrupted(ctx context.Context, o Owner, id string, detail any) error {
	if _, e := s.Report(ctx, o, id); e != nil {
		return e
	}
	return s.Finish(ctx, o, id, "interrupted", map[string]any{"complete": false}, detail)
}

// CancelQueued never changes a running or completed report. The caller removes
// its FIFO entry only after this state transition has been committed.
func (s *Store) CancelQueued(ctx context.Context, o Owner, id string) error {
	r, err := s.Report(ctx, o, id)
	if err != nil {
		return err
	}
	if r.Status == "cancelled" || r.Status == "done" || r.Status == "partial" || r.Status == "interrupted" {
		return nil
	}
	if r.Status != "queued" {
		return Fail(409, "本报告已开始执行，请停止正在运行的检测；未取消其他任务", "cancel")
	}
	return retryWrite(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `UPDATE hub_reports SET status='cancelled',updated_at_ms=?,summary_json='{"complete":false}',error_json='{"message":"已取消等待，本报告没有发送检测请求","step":"cancel","operationResult":"not_started"}' WHERE user_id=? AND workspace_id=? AND report_id=? AND status='queued'`, Now().UnixMilli(), o.UserID, o.WorkspaceID, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			current, readErr := s.Report(ctx, o, id)
			if readErr != nil {
				return readErr
			}
			if current.Status == "cancelled" || current.Status == "done" || current.Status == "partial" || current.Status == "interrupted" {
				return nil
			}
			return Fail(409, "报告状态已变化，请重新读取后停止正在运行的检测；未覆盖已保存结果", "cancel")
		}
		return nil
	})
}
func (s *Store) Recover(ctx context.Context) error {
	return retryWrite(ctx, func() error {
		_, e := s.db.ExecContext(ctx, `UPDATE hub_reports SET status='interrupted',updated_at_ms=?,error_json='{"message":"检测服务已重启，已停止旧任务；已保存结果仍可查看，不会自动重新调用模型","step":"recover","operationResult":"unknown"}' WHERE status IN('queued','running')`, Now().UnixMilli())
		return e
	})
}
func (s *Store) Active(ctx context.Context) (string, error) {
	var id string
	e := s.db.QueryRowContext(ctx, `SELECT report_id FROM hub_reports WHERE status IN('queued','running') LIMIT 1`).Scan(&id)
	if e == sql.ErrNoRows {
		return "", nil
	}
	return id, e
}

func (s *Store) RecordAttempt(ctx context.Context, o Owner, id, itemID string) error {
	return retryWrite(ctx, func() error {
		res, e := s.db.ExecContext(ctx, `UPDATE hub_items SET attempts=attempts+1 WHERE report_id=? AND item_id=? AND status='running' AND EXISTS(SELECT 1 FROM hub_reports r WHERE r.report_id=hub_items.report_id AND r.user_id=? AND r.workspace_id=? AND r.status='running')`, id, itemID, o.UserID, o.WorkspaceID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Fail(409, "报告已结束，停止新的模型请求", "attempt")
		}
		return nil
	})
}
