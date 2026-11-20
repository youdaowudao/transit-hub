package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

const oldLimit = 64 << 10

type Target struct {
	ID    string
	Owner store.Owner
}

type Summary struct {
	Reports, Measured, Failed, Over, Stopped int
	Lines                                    []string
}

func (s Summary) safe() bool {
	return s.Stopped == 0 && s.Reports == s.Measured && s.Failed == 0 && s.Over == 0
}

func main() {
	db := flag.String("db", "", "本地 SQLite 副本绝对路径")
	since := flag.Int64("since", -1, "停止接收检测的毫秒时间")
	flag.Parse()
	if !filepath.IsAbs(*db) || *since < 0 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "请提供已备份的 SQLite 副本绝对路径和停止接收检测的毫秒时间")
		os.Exit(2)
	}
	code, err := run(context.Background(), *db, *since, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取回滚检查副本失败，请根据错误核对副本和停机时间：", err)
		os.Exit(2)
	}
	if code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, path string, since int64, out io.Writer) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 2, err
	}
	s, err := store.Open(path)
	if err != nil {
		return 2, err
	}
	defer s.Close()
	rows, err := s.RuntimeDB().QueryContext(ctx, `SELECT report_id,status FROM hub_reports
WHERE status IN('queued','running') OR (status IN('cancelled','interrupted') AND updated_at_ms>=?) ORDER BY report_id`, since)
	if err != nil {
		return 2, err
	}
	stopped := 0
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return 2, err
		}
		fmt.Fprintf(out, "ROLLBACK_CHECK_STOPPED report=%s status=%s\n", id, status)
		stopped++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 2, err
	}
	rows, err = s.RuntimeDB().QueryContext(ctx, `SELECT report_id,user_id,workspace_id FROM hub_reports
WHERE json_type(summary_json,'$.prefix') IS NOT NULL ORDER BY report_id`)
	if err != nil {
		return 2, err
	}
	var targets []Target
	for rows.Next() {
		var target Target
		if err := rows.Scan(&target.ID, &target.Owner.UserID, &target.Owner.WorkspaceID); err != nil {
			rows.Close()
			return 2, err
		}
		targets = append(targets, target)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 2, err
	}
	summary := check(ctx, s, targets)
	summary.Stopped = stopped
	for _, line := range summary.Lines {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "ROLLBACK_CHECK_SUMMARY stopped=%d reports=%d measured=%d failed=%d over=%d\n", stopped, summary.Reports, summary.Measured, summary.Failed, summary.Over)
	if !summary.safe() {
		return 1, nil
	}
	return 0, nil
}

func check(ctx context.Context, s *store.Store, targets []Target) Summary {
	out := Summary{Reports: len(targets)}
	for _, target := range targets {
		report, err := s.Report(ctx, target.Owner, target.ID)
		var data []byte
		if err == nil {
			data, err = oldReportJSON(report)
		}
		if err != nil {
			out.Failed++
			out.Lines = append(out.Lines, fmt.Sprintf("ROLLBACK_CHECK report=%s error=%v", target.ID, err))
			continue
		}
		out.Measured++
		over := len(data) > oldLimit
		if over {
			out.Over++
		}
		out.Lines = append(out.Lines, fmt.Sprintf("ROLLBACK_CHECK report=%s oldBytes=%d limit=%d over=%t", target.ID, len(data), oldLimit, over))
	}
	return out
}

func oldReportJSON(report store.Report) ([]byte, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	delete(body, "request")
	return json.Marshal(body)
}
