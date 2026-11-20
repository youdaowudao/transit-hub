package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	path := flag.String("db", "", "隔离资源测试库绝对路径")
	id := flag.String("report", "", "隔离检测报告编号")
	user := flag.String("user", "", "资源测试用户编号")
	workspace := flag.String("workspace", "", "资源测试工作区编号")
	flag.Parse()
	if !filepath.IsAbs(*path) || *id == "" || *user == "" || *workspace == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "请提供隔离资源测试库和报告、用户、工作区编号")
		os.Exit(2)
	}
	if err := seed(*path, *id, *user, *workspace); err != nil {
		fmt.Fprintln(os.Stderr, "写入隔离资源数据失败，未生成压力样本：", err)
		os.Exit(1)
	}
}

func seed(path, id, user, workspace string) error {
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var model string
	var created int64
	if err := tx.QueryRowContext(ctx, `SELECT model,created_at_ms FROM hub_reports WHERE report_id=? AND user_id=? AND workspace_id=?`,
		id, user, workspace).Scan(&model, &created); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hub_items SET result_json=json_set(result_json,'$.padding',?) WHERE report_id=? AND section_id='injection'`, strings.Repeat("x", 4<<20), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("隔离报告应有一道注入题，实际 %d 道（%v）", n, err)
	}
	knowledge := make([]map[string]any, 32)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	answer := strings.Repeat("x", 16<<10)
	for i := range knowledge {
		knowledge[i] = map[string]any{"id": fmt.Sprintf("pressure-event-%d", i), "kind": "know", "event": "隔离知识题",
			"happened_at": day.AddDate(0, 0, i).Format(time.DateOnly), "answer": answer}
	}
	summary, err := json.Marshal(map[string]any{
		"knowledge": map[string]any{"rows": knowledge},
		"parts":     []any{map[string]any{"key": "vision", "applicable": true, "known": true, "points": 20, "max_points": 20}},
	})
	if err != nil {
		return err
	}
	for i := 1; i <= 20; i++ {
		report, account := fmt.Sprintf("%032x", 3000+i), fmt.Sprintf("pressure-%d", i)
		at := created + int64(i)*time.Minute.Milliseconds()
		_, err := tx.ExecContext(ctx, `INSERT INTO hub_reports(
report_id,user_id,workspace_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,request_json,snapshot_json,summary_json
) VALUES (?,?,?,?,?,?,'','','https://pressure.invalid','chat',?,?,?,'done','{}','{}',?)`, report, user, workspace, account, account, account, model, at, at, string(summary))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
