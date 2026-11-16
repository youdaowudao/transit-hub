package chanshui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"transithub/backend/internal/shared/authctx"
)

const WorkspaceHeader = "X-Chanshui-Workspace"
const ResultContentType = "application/vnd.transithub.chanshui-result+json"

func RegisterRoutes(mux *http.ServeMux, s *Service) {
	mux.Handle("/api/chanshui", s)
	mux.Handle("/api/chanshui/", s)
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	p := asProblem(err)
	if p.Status < 400 {
		p.Status = 503
	}
	if p.Status == 429 {
		w.Header().Set("Retry-After", "1")
	}
	reply(w, p.Status, p)
}
func decode(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 128*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return problem(422, "input", "输入格式不正确或超过128KiB，请检查填写内容后重试；原值没有被修改")
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return problem(422, "input", "输入只能包含一份JSON，请检查后重试；原值没有被修改")
	}
	return nil
}
func filters(values url.Values) (map[string]any, error) {
	out := map[string]any{}
	for key, items := range values {
		if len(items) != 1 {
			return nil, problem(422, "input", "筛选条件重复，请重新选择后重试")
		}
		value := items[0]
		switch key {
		case "limit":
			v, e := strconv.Atoi(value)
			if e != nil || v < 1 || v > 50 {
				return nil, problem(422, "input", "每页条数须为1～50，请重新选择")
			}
			out[key] = v
		case "enabled":
			v, e := strconv.ParseBool(value)
			if e != nil {
				return nil, problem(422, "input", "启用状态筛选无效，请重新选择")
			}
			out[key] = v
		default:
			out[key] = value
		}
	}
	return out, nil
}
func reportDates(args map[string]any) error {
	location, _ := time.LoadLocation("Asia/Singapore")
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	if args["scope"] == "sidebar" {
		args["fromMs"] = today.AddDate(0, 0, -1).UnixMilli()
		args["toMs"] = today.AddDate(0, 0, 1).UnixMilli()
		return nil
	}
	from, to := today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
	for key, target := range map[string]*time.Time{"from": &from, "to": &to} {
		value, _ := args[key].(string)
		if value == "" {
			continue
		}
		parsed, e := time.ParseInLocation("2006-01-02", value, location)
		if e != nil {
			return problem(422, "input", "历史日期格式应为年-月-日，请重新选择")
		}
		if key == "to" {
			parsed = parsed.AddDate(0, 0, 1)
		}
		*target = parsed
	}
	if !from.Before(to) {
		return problem(422, "input", "历史开始日期不能晚于结束日期，请重新选择")
	}
	args["fromMs"] = from.UnixMilli()
	args["toMs"] = to.UnixMilli()
	return nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, _ := authctx.UserID(r.Context())
	owner, err := s.owner(r.Context(), user)
	if err != nil {
		fail(w, err)
		return
	}
	if values := r.Header.Values(WorkspaceHeader); len(values) != 1 || strings.TrimSpace(values[0]) != owner.WorkspaceID {
		fail(w, problem(409, "workspace", "页面所属工作区已切换或尚未确认，请重新打开模型检测后重试；本次操作没有保存或发送检测请求"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/chanshui"), "/")
	parts := strings.Split(path, "/")
	if path == "runtime" && r.Method == http.MethodGet {
		reply(w, 200, s.runtime(r.Context(), owner))
		return
	}
	if path == "bank-info" && r.Method == http.MethodGet {
		s.bankInfo(w, r)
		return
	}
	if err = s.ensureInitialized(r.Context()); err != nil {
		fail(w, err)
		return
	}
	if path == "targets" && r.Method == http.MethodGet {
		items, e := s.targets.ListAuditTargets(r.Context(), owner.UserID)
		if e != nil {
			fail(w, problem(503, "account", e.Error()))
			return
		}
		reply(w, 200, map[string]any{"items": items})
		return
	}
	if len(parts) == 3 && parts[0] == "targets" && (parts[2] == "context" || parts[2] == "models") {
		if (parts[2] == "context" && r.Method != http.MethodGet) || (parts[2] == "models" && r.Method != http.MethodPost) {
			w.WriteHeader(405)
			return
		}
		groupID := r.URL.Query().Get("groupId")
		var input struct {
			GroupID         string          `json:"groupId"`
			ExpectedContext ExpectedContext `json:"expectedContext"`
		}
		if parts[2] == "models" {
			if e := decode(r, &input); e != nil {
				fail(w, e)
				return
			}
			groupID = input.GroupID
		}
		audit, e := s.targets.ResolveAuditContext(r.Context(), owner.UserID, parts[1], groupID, parts[2] == "models")
		if e != nil {
			fail(w, problem(409, "account", e.Error()))
			return
		}
		if audit.WorkspaceID != owner.WorkspaceID {
			fail(w, problem(409, "workspace", "工作区已切换，请重新打开账号后重试"))
			return
		}
		if parts[2] == "context" {
			data, _ := json.Marshal(audit)
			var result map[string]any
			_ = json.Unmarshal(data, &result)
			result["expectedContext"] = expected(audit)
			reply(w, 200, result)
			return
		}
		if !audit.CanRun {
			fail(w, problem(422, "account", audit.Reason))
			return
		}
		if expected(audit) != input.ExpectedContext {
			fail(w, problem(409, "context", "账号地址、分组或协议已变化，请刷新账号后重新读取模型"))
			return
		}
		cred, e := checkedCredential(audit)
		if e != nil {
			fail(w, e)
			return
		}
		data, e := s.queryJSON(r.Context(), "models", owner, map[string]any{}, &cred, false)
		if e != nil {
			p := asProblem(e)
			p.Message = strings.ReplaceAll(p.Message, cred.Key, "[已隐藏]")
			p.Detail = strings.ReplaceAll(p.Detail, cred.Key, "[已隐藏]")
			fail(w, p)
			return
		}
		var raw struct {
			Models []json.RawMessage `json:"models"`
		}
		if json.Unmarshal(data, &raw) != nil {
			fail(w, problem(503, "models", "上游模型列表格式无法读取，请检查该账号接口后重试"))
			return
		}
		models := []map[string]any{}
		for _, value := range raw.Models {
			var id string
			if json.Unmarshal(value, &id) != nil {
				var item struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(value, &item) != nil {
					continue
				}
				id = item.ID
			}
			if id != "" {
				models = append(models, map[string]any{"id": id})
			}
		}
		reply(w, 200, map[string]any{"models": models})
		return
	}
	if path == "reports" && r.Method == http.MethodPost {
		var input StartInput
		if e := decode(r, &input); e != nil {
			fail(w, e)
			return
		}
		result, e := s.Start(r.Context(), owner, input)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, result)
		return
	}
	if len(parts) == 3 && parts[0] == "reports" && parts[2] == "cancel" && r.Method == http.MethodPost {
		result, e := s.Cancel(r.Context(), owner, parts[1])
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, result)
		return
	}
	args, e := filters(r.URL.Query())
	if e != nil {
		fail(w, e)
		return
	}
	operation := ""
	switch {
	case path == "reports" && r.Method == http.MethodGet:
		operation = "list-reports"
		if e = reportDates(args); e != nil {
			fail(w, e)
			return
		}
	case len(parts) == 2 && parts[0] == "reports" && r.Method == http.MethodGet:
		data, e := s.queryJSON(r.Context(), "read-report", owner, map[string]any{"reportId": parts[1]}, nil, false)
		if e != nil {
			fail(w, e)
			return
		}
		var result map[string]any
		_ = json.Unmarshal(data, &result)
		result["processExited"] = s.processExited(owner, parts[1])
		s.mu.Lock()
		if s.active != nil && s.active.Owner == owner && s.active.ReportID == parts[1] {
			if s.active.Diagnostic != "" {
				result["diagnostic"] = s.active.Diagnostic
			}
			if s.active.Job != nil {
				info := s.active.Job.Info()
				result["taskMemoryMaxBytes"] = info.TaskMemoryMaxBytes
				result["sliceMemoryMaxBytes"] = info.SliceMemoryMaxBytes
				result["swapMaxBytes"] = info.SwapMaxBytes
			}
		}
		s.mu.Unlock()
		reply(w, 200, result)
		return
	case len(parts) == 3 && parts[0] == "reports" && parts[2] == "items" && r.Method == http.MethodGet:
		operation = "list-items"
		args["reportId"] = parts[1]
	case len(parts) == 5 && parts[0] == "reports" && parts[2] == "items" && parts[4] == "image" && r.Method == http.MethodGet:
		operation = "read-image"
		args["reportId"] = parts[1]
		args["itemId"] = parts[3]
	case path == "settings" && r.Method == http.MethodGet:
		operation = "read-settings"
	case path == "settings" && r.Method == http.MethodPut:
		operation = "save-settings"
		if e = decode(r, &args); e != nil {
			fail(w, e)
			return
		}
	case path == "questions" && r.Method == http.MethodGet:
		operation = "list-question-summaries"
	case path == "questions" && r.Method == http.MethodPost:
		operation = "save-question"
		if e = decode(r, &args); e != nil {
			fail(w, e)
			return
		}
	case path == "questions/preview" && r.Method == http.MethodPost:
		operation = "preview-question"
		if e = decode(r, &args); e != nil {
			fail(w, e)
			return
		}
	case len(parts) == 2 && parts[0] == "questions" && r.Method == http.MethodGet:
		operation = "read-question"
		args["id"] = parts[1]
	case len(parts) == 2 && parts[0] == "questions" && r.Method == http.MethodPut:
		operation = "save-question"
		if e = decode(r, &args); e != nil {
			fail(w, e)
			return
		}
		definition, ok := args["definition"].(map[string]any)
		if !ok || definition["id"] != parts[1] {
			fail(w, problem(422, "question", "题目编号与编辑目标不一致，请重新打开题目后重试"))
			return
		}
	case len(parts) == 2 && parts[0] == "questions" && r.Method == http.MethodDelete:
		operation = "delete-question"
		revision, e := strconv.Atoi(strings.Trim(r.Header.Get("If-Match"), "\""))
		if e != nil || revision < 1 {
			fail(w, problem(422, "revision", "删除题目前须提供当前版本，请重新打开题目后重试"))
			return
		}
		args = map[string]any{"id": parts[1], "revision": revision}
	default:
		reply(w, 404, map[string]any{"message": "此模型检测页面操作不存在，请刷新页面后重试", "step": "route", "operationResult": "not_started", "retryable": true})
		return
	}
	response, e := s.runner.Query(r.Context(), Envelope{V: 1, Operation: operation, Owner: owner, Args: args}, nil, false)
	if e != nil {
		fail(w, e)
		return
	}
	writeStreamResult(w, response, operation)
}

// The final metadata is written only after the helper has been closed. The
// business payload is encoded directly to the response, using a fixed buffer.
func writeStreamResult(w http.ResponseWriter, response *QueryResponse, operation string) {
	closed := false
	defer func() {
		if !closed {
			_ = response.Body.Close()
		}
	}()
	contentType, _ := json.Marshal(response.ContentType)
	w.Header().Set("Content-Type", ResultContentType)
	w.WriteHeader(response.Status)
	_, copyErr := fmt.Fprintf(w, `{"v":1,"contentType":%s,"length":%d,"payloadBase64":"`, contentType, response.Length)
	var copied int64
	var encodingErr error
	if copyErr == nil {
		encoder := base64.NewEncoder(base64.StdEncoding, w)
		copied, copyErr = io.CopyBuffer(encoder, io.LimitReader(response.Body, response.Length+1), make([]byte, 32*1024))
		encodingErr = encoder.Close()
	}
	closed = true
	closeErr := response.Body.Close()
	complete := copied == response.Length && copyErr == nil && encodingErr == nil
	var issue *Problem
	if !complete || closeErr != nil {
		action, writes := streamAction(operation)
		message := ""
		if complete {
			message = action + "；但结束清理未完成：" + closeErr.Error()
		} else {
			message = fmt.Sprintf("检测结果未完整读取，已返回%d/%d字节", copied, response.Length)
			if writes {
				message = fmt.Sprintf("%s；但最新返回值未完整收到，页面保留原显示，已返回%d/%d字节", action, copied, response.Length)
			}
			if copyErr != nil {
				message += "；读取或传送时中断：" + copyErr.Error()
			} else if copied != response.Length {
				message += "；实际结果长度与声明不符"
			}
			if encodingErr != nil {
				message += "；结果传送中断：" + encodingErr.Error()
			}
			if closeErr != nil {
				message += "；结束清理未完成：" + closeErr.Error()
			}
		}
		message += "。请核查检测程序及systemd启动、停止权限和配置后重新读取"
		if writes {
			message += "；写操作不要直接重复提交"
		}
		message += "。"
		issue = problem(503, "query-exit", message)
		issue.OperationResult = "unknown"
		issue.Retryable = !writes
	}
	tail, _ := json.Marshal(struct {
		Complete bool     `json:"complete"`
		Error    *Problem `json:"error,omitempty"`
	}{complete, issue})
	_, _ = io.WriteString(w, `",`)
	_, _ = w.Write(tail[1:])
}
func streamAction(operation string) (string, bool) {
	switch operation {
	case "save-settings":
		return "本次设置更改已保存", true
	case "save-question":
		return "本次题目更改已保存", true
	case "delete-question":
		return "本次题目已删除", true
	default:
		return "本次数据已读取", false
	}
}
func (s *Service) bankInfo(w http.ResponseWriter, r *http.Request) {
	if runner, ok := s.runner.(*Runner); ok && !within(runner.cfg.ProjectDir, s.webDir) {
		fail(w, problem(503, "package", "检测页面目录必须位于当前项目内，请修正部署路径"))
		return
	}
	file, e := os.Open(filepath.Join(s.webDir, "bank-manifest.json"))
	if e != nil {
		fail(w, problem(503, "package", "参考库说明文件缺失，请重新构建并安装完整检测包"))
		return
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || info.Size() > 128*1024 {
		fail(w, problem(503, "package", "参考库说明文件大小不符合要求，请重新安装完整检测包"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, file)
}
func (s *Service) StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(405)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/chanshui")
		if strings.Contains(path, "..") || strings.ContainsRune(path, '\x00') {
			http.NotFound(w, r)
			return
		}
		switch strings.TrimSuffix(path, "/") {
		case "":
			path = "/index.html"
		case "/settings":
			path = "/settings.html"
		case "/history":
			path = "/history.html"
		}
		clean := filepath.Join(s.webDir, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		resolved, e := filepath.EvalSymlinks(clean)
		if e != nil || !within(s.webDir, resolved) {
			http.NotFound(w, r)
			return
		}
		info, e := os.Stat(resolved)
		if e != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(path, "/_next/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		http.ServeFile(w, r, resolved)
	})
}
