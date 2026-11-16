// TransitHub runs this entry point once per query or report. No HTTP server,
// anonymous history, cleanup ticker, cache or background bank sync is started.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/engine"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/hub/wire"
)

func main() {
	if e := mainRun(); e != nil {
		message := e.Error()
		if len(message) > 8192 {
			message = message[:8192]
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}
func mainRun() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("请指定query或run角色")
	}
	role := os.Args[1]
	if role != "query" && role != "run" {
		return fmt.Errorf("仅允许query或run角色")
	}
	flags := flag.NewFlagSet("transit-hub", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite绝对路径")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("包含多余启动参数")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	env, control, e := wire.Handshake(os.Stdin, os.Stdout, role)
	if e != nil {
		return e
	}
	if role == "query" {
		var c context.CancelFunc
		ctx, c = context.WithTimeout(ctx, 15*time.Second)
		defer c()
	}
	s, e := store.Open(*dbPath)
	if e != nil {
		if role == "query" {
			return wire.ResultError(os.Stdout, e)
		}
		return e
	}
	defer s.Close()
	if role == "run" {
		if control.Credential == nil {
			return fmt.Errorf("未收到账号密钥，未发送检测请求")
		}
		if e = env.Owner.Validate(); e != nil {
			return e
		}
		var args struct {
			ReportID string `json:"reportId"`
		}
		if e = wire.Decode(env.Args, &args); e != nil {
			return e
		}
		notify := func(event string, data map[string]any) {
			data["v"] = 1
			data["event"] = event
			_ = json.NewEncoder(os.Stdout).Encode(data)
		}
		e = engine.RunOne(ctx, s, env.Owner, args.ReportID, *control.Credential, engine.Config{Deployment: env.Deployment, Notify: notify})
		if e != nil {
			return fmt.Errorf("检测未完成：%s", replaceSecret(e.Error(), control.Credential.Key))
		}
		return nil
	}
	value, contentType, e := query(ctx, s, env, control)
	if e != nil {
		return wire.ResultError(os.Stdout, e)
	}
	return wire.Result(os.Stdout, 200, value, contentType)
}
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	return wire.Decode(raw, v)
}
func query(ctx context.Context, s *store.Store, env wire.Envelope, c wire.Continue) (any, string, error) {
	o := env.Owner
	operation := env.Operation
	if operation != "init" && operation != "recover" {
		if e := o.Validate(); e != nil {
			return nil, "", e
		}
	}
	switch operation {
	case "init":
		if o.UserID != "" || o.WorkspaceID != "" {
			if e := s.EnsureScope(ctx, o); e != nil {
				return nil, "", e
			}
		}
		return map[string]any{"initialized": true}, "", nil
	case "recover":
		return map[string]any{"recovered": true}, "", s.Recover(ctx)
	case "models":
		if c.Credential == nil {
			return nil, "", store.Fail(422, "未提供账号凭据，未读取模型列表", "models")
		}
		ids, e := engine.Models(ctx, *c.Credential, env.Deployment, nil)
		return map[string]any{"models": ids}, "", e
	case "read-settings":
		v, e := s.Settings(ctx, o)
		return v, "", e
	case "save-settings":
		var v store.Settings
		if e := decode(env.Args, &v); e != nil {
			return nil, "", e
		}
		saved, e := s.SaveSettings(ctx, o, v)
		return saved, "", e
	case "list-question-summaries":
		var f store.Filters
		if e := decode(env.Args, &f); e != nil {
			return nil, "", e
		}
		v, e := s.ListQuestions(ctx, o, f)
		return v, "", e
	case "read-question":
		var a struct {
			ID string `json:"id"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		v, e := s.Question(ctx, o, a.ID)
		return v, "", e
	case "save-question":
		var a struct {
			Definition     questions.Definition `json:"definition"`
			Revision       int                  `json:"revision"`
			RestoreDefault bool                 `json:"restoreDefault"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		if a.Revision == 0 {
			a.Revision = a.Definition.Revision
		}
		v, e := s.SaveQuestion(ctx, o, a.Definition, a.Revision, a.RestoreDefault)
		return v, "", e
	case "delete-question":
		var a struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		e := s.DeleteQuestion(ctx, o, a.ID, a.Revision)
		return map[string]any{"deleted": e == nil}, "", e
	case "preview-question":
		var a struct {
			Definition questions.Definition `json:"definition"`
			Seed       int64                `json:"seed"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		if a.Seed == 0 {
			a.Seed = time.Now().UnixNano() & 0x1fffffffffffff
		}
		v, e := questions.Generate(a.Definition, a.Seed)
		if e != nil {
			return nil, "", store.Fail(422, e.Error(), "preview")
		}
		return v.Preview(), "", nil
	case "create-report":
		var a store.CreateRequest
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		v, e := s.CreateReport(ctx, o, a)
		return store.ReportView(v), "", e
	case "list-reports":
		var a store.Filters
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		v, e := s.ListReports(ctx, o, a)
		return v, "", e
	case "read-report":
		var a struct {
			ReportID string          `json:"reportId"`
			Request  json.RawMessage `json:"request,omitempty"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		if len(a.Request) > 0 {
			if e := s.CheckRequest(ctx, o, a.ReportID, a.Request); e != nil {
				return nil, "", e
			}
		}
		v, e := s.Report(ctx, o, a.ReportID)
		return store.ReportView(v), "", e
	case "list-items":
		var a struct {
			ReportID string `json:"reportId"`
			Cursor   string `json:"cursor"`
			Limit    int    `json:"limit"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		v, e := s.ListItems(ctx, o, a.ReportID, store.Filters{Cursor: a.Cursor, Limit: a.Limit})
		return v, "", e
	case "read-image":
		var a struct {
			ReportID string `json:"reportId"`
			ItemID   string `json:"itemId"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		v, e := s.Image(ctx, o, a.ReportID, a.ItemID)
		return v, "image/png", e
	case "finish-interrupted":
		var a struct {
			ReportID string         `json:"reportId"`
			Error    map[string]any `json:"error"`
		}
		if e := decode(env.Args, &a); e != nil {
			return nil, "", e
		}
		if e := s.FinishInterrupted(ctx, o, a.ReportID, a.Error); e != nil {
			return nil, "", e
		}
		v, e := s.Report(ctx, o, a.ReportID)
		return store.ReportView(v), "", e
	}
	return nil, "", store.Fail(422, "不支持此检测操作，未执行任何模型请求", "operation")
}
func replaceSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	var out []byte
	for len(s) > 0 {
		n := indexString(s, secret)
		if n < 0 {
			out = append(out, s...)
			break
		}
		out = append(out, s[:n]...)
		out = append(out, "[密钥已隐藏]"...)
		s = s[n+len(secret):]
	}
	return string(out)
}
func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
