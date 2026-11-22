package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

type JudgeRequest struct {
	ReportID string `json:"reportId"`
	ItemID   string `json:"itemId"`
	Verdict  string `json:"verdict"`
}

type RescoreFunc func([]questions.Generated, map[string]Item, string, string) (map[string]any, error)

type manualJudgment struct {
	questions.Judgment
	Source     string `json:"source"`
	AutoReason string `json:"autoReason"`
}

func (s *Store) JudgeItem(ctx context.Context, o Owner, req JudgeRequest, rescore RescoreFunc) (Report, error) {
	if err := o.Validate(); err != nil {
		return Report{}, err
	}
	notFound := Fail(404, "报告不存在或不属于当前工作区", "report")
	if !reportPattern.MatchString(req.ReportID) {
		return Report{}, notFound
	}
	if err := s.writable(); err != nil {
		return Report{}, err
	}
	var report Report
	err := retryWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var model, status, snapshotRaw, summaryRaw string
		err = tx.QueryRowContext(ctx, `SELECT model,status,snapshot_json,summary_json FROM hub_reports
			WHERE user_id=? AND workspace_id=? AND report_id=?`, o.UserID, o.WorkspaceID, req.ReportID).Scan(&model, &status, &snapshotRaw, &summaryRaw)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return err
		}
		if status == "queued" || status == "running" {
			return Fail(409, "这份报告正在检测或补测，结束后再人工判定。本题分数没有改动。", "judge-item")
		}
		if req.Verdict != "pass" && req.Verdict != "fail" {
			return Fail(422, "请选择人工判为通过或不通过后重试。本题分数没有改动。", "judge-item")
		}
		if err := writeManualJudgment(ctx, tx, req, model); err != nil {
			return err
		}
		var snapshot Snapshot
		if err := json.Unmarshal([]byte(snapshotRaw), &snapshot); err != nil {
			return fmt.Errorf("读取已保存的报告题目：%w", err)
		}
		summary, err := rescoreJudgedReport(ctx, tx, req.ReportID, model, summaryRaw, snapshot, rescore, status == "cancelled")
		if err != nil {
			return err
		}
		if status != "cancelled" {
			status = "partial"
			if string(summary["complete"]) == "true" {
				status = "done"
			}
		}
		updated, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hub_reports SET status=?,summary_json=?,updated_at_ms=MAX(?,updated_at_ms+1)
			WHERE user_id=? AND workspace_id=? AND report_id=?`, status, string(updated), Now().UnixMilli(), o.UserID, o.WorkspaceID, req.ReportID); err != nil {
			return fmt.Errorf("写入重算后的报告分数：%w", err)
		}
		report, err = readReport(ctx, tx, o, req.ReportID)
		if err != nil {
			return fmt.Errorf("读取重算后的报告：%w", err)
		}
		return tx.Commit()
	})
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return Report{}, typed
		}
		return Report{}, Fail(500, "人工判定未写入检测数据库，已撤销本次更改。请检查磁盘、权限或数据库占用后重试。详情："+err.Error(), "judge-item")
	}
	return report, nil
}

func writeManualJudgment(ctx context.Context, tx *sql.Tx, req JudgeRequest, model string) error {
	ineligible := Fail(422, "只有已保存回答、且仍待核对或已人工判定的模型自报题可以人工判定。本题分数没有改动。", "judge-item")
	var judge, status, answer, requestError, stored string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(question_json,'$.definition.judge.type'),''), status,
		COALESCE(json_extract(result_json,'$.answer'),''), COALESCE(json_extract(result_json,'$.error'),''),
		COALESCE(result_json -> '$.judgment','null') FROM hub_items WHERE report_id=? AND item_id=?`,
		req.ReportID, req.ItemID).Scan(&judge, &status, &answer, &requestError, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return ineligible
	}
	if err != nil {
		return err
	}
	if judge != questions.ModelIdentityJudge || status != "done" || requestError != "" || strings.TrimSpace(answer) == "" || !questions.ModelIdentityApplies(model) {
		return ineligible
	}
	var old manualJudgment
	if json.Unmarshal([]byte(stored), &old) != nil || (old.Status != "unknown" && old.Source != "manual") {
		return ineligible
	}
	if old.Source != "manual" {
		old.AutoReason = old.Reason
	}
	points := 0
	reason := "人工判为不通过，本题0分；请结合完整回答和其他检测核查上游型号。"
	if req.Verdict == "pass" {
		points = old.Max
		reason = "人工判为通过，按本题满分计分；自报不证明实际型号。"
	}
	judgment := manualJudgment{
		Judgment: questions.Judgment{Status: "known", Earned: &points, Max: old.Max, Reason: reason, Tags: []string{"人工判定"}},
		Source:   "manual", AutoReason: old.AutoReason,
	}
	raw, err := json.Marshal(judgment)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hub_items SET result_json=json_set(result_json,'$.judgment',json(?)) WHERE report_id=? AND item_id=?`, string(raw), req.ReportID, req.ItemID)
	return err
}

func rescoreJudgedReport(ctx context.Context, tx *sql.Tx, reportID, model, raw string, snapshot Snapshot, rescore RescoreFunc, cancelled bool) (map[string]json.RawMessage, error) {
	var summary map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		return nil, fmt.Errorf("读取已保存的报告分数：%w", err)
	}
	if summary == nil {
		summary = map[string]json.RawMessage{}
	}
	verdict := map[string]json.RawMessage{}
	if value := summary["verdict"]; len(value) > 0 {
		if err := json.Unmarshal(value, &verdict); err != nil {
			return nil, fmt.Errorf("读取已保存的计分规则：%w", err)
		}
	}
	version := ""
	_ = json.Unmarshal(verdict["scoring_version"], &version)
	items, err := judgmentScoringItems(ctx, tx, reportID)
	if err != nil {
		return nil, fmt.Errorf("读取待重算的逐题分数：%w", err)
	}
	calculated, err := rescore(snapshot.Questions, items, model, version)
	if err != nil {
		return nil, fmt.Errorf("重算推理分和总分：%w", err)
	}
	newVerdict, _ := calculated["verdict"].(map[string]any)
	if verdict == nil {
		verdict = map[string]json.RawMessage{}
	}
	for _, key := range []string{"scoring_version", "total", "integrity", "iq"} {
		if value, ok := newVerdict[key]; ok {
			verdict[key], err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
	}
	summary["verdict"], err = json.Marshal(verdict)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"complete", "total", "totalScore", "parts", "iqMax", "iqScore"} {
		if cancelled && key == "complete" {
			continue
		}
		if value, ok := calculated[key]; ok {
			summary[key], err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
	}
	return summary, nil
}

func judgmentScoringItems(ctx context.Context, tx *sql.Tx, reportID string) (map[string]Item, error) {
	// Leave the original answer and its copy in the original IQ evidence on disk.
	rows, err := tx.QueryContext(ctx, `SELECT item_id,status,
		json_remove(result_json,'$.answer','$.thinkingText','$.metrics','$.evidence.answer','$.evidence.thinking_text','$.evidence.image')
		FROM hub_items WHERE report_id=?`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := map[string]Item{}
	for rows.Next() {
		var item Item
		var result string
		if err := rows.Scan(&item.ItemID, &item.Status, &result); err != nil {
			return nil, err
		}
		item.Result = json.RawMessage(result)
		items[item.ItemID] = item
	}
	return items, rows.Err()
}
