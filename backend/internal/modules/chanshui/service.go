package chanshui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/connection_health"
)

type WorkspaceResolver interface {
	RequireCurrentID(context.Context, string) (string, error)
}
type TargetReader interface {
	ListAuditTargets(context.Context, string) ([]connection_health.AuditTarget, error)
	ResolveAuditContext(context.Context, string, string, string, bool) (connection_health.AuditContext, error)
}
type activeTask struct {
	Owner                 Owner
	ReportID, AccountName string
	Job                   *Job
	Diagnostic            string
	OperationResult       string
}
type Service struct {
	cfg         config.Config
	workspace   WorkspaceResolver
	targets     TargetReader
	runner      Executor
	webDir      string
	initMu      sync.Mutex
	initialized bool
	admission   sync.Mutex
	mu          sync.Mutex
	active      *activeTask
}

func projectDir() string {
	cwd, _ := os.Getwd()
	if filepath.Base(cwd) == "backend" {
		cwd = filepath.Dir(cwd)
	}
	return cwd
}
func NewService(cfg config.Config, workspace WorkspaceResolver, targets TargetReader) *Service {
	if cfg.APIOnly {
		cfg.ChanshuiEnabled = false
	}
	if cfg.ChanshuiSystemdManager == "" {
		if os.Geteuid() == 0 {
			cfg.ChanshuiSystemdManager = "system"
		} else {
			cfg.ChanshuiSystemdManager = "user"
		}
	}
	root := projectDir()
	binary := cfg.ChanshuiBinaryPath
	if binary == "" {
		binary = filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	}
	data := cfg.ChanshuiDataDir
	if data == "" {
		data = filepath.Join(root, "data/chanshui")
	}
	web := cfg.ChanshuiWebDir
	if web == "" {
		web = filepath.Join(root, "chanshui/web/out-build")
	}
	binary, _ = filepath.Abs(binary)
	data, _ = filepath.Abs(data)
	web, _ = filepath.Abs(web)
	return &Service{cfg: cfg, workspace: workspace, targets: targets, webDir: web, runner: NewRunner(RunnerConfig{binary, data, root, cfg.ChanshuiSystemdManager, cfg.ChanshuiTestAllowPrivateTargets})}
}
func (s *Service) ensureInitialized(ctx context.Context) error {
	if !s.cfg.ChanshuiEnabled {
		return problem(503, "configuration", "当前实例仅提供接口，不能运行检测；请打开正常调度站实例。原调度功能不受影响")
	}
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.initialized {
		return nil
	}
	if s.webDir != "" {
		if info, err := os.Stat(filepath.Join(s.webDir, "index.html")); err != nil || !info.Mode().IsRegular() {
			return problem(503, "package", "检测页面文件缺失，请重新执行整站升级并查看检测构建步骤后重试")
		}
	}
	if r, ok := s.runner.(*Runner); ok {
		if !within(r.cfg.ProjectDir, s.webDir) {
			return problem(503, "package", "检测页面目录必须位于当前项目内，请修正部署路径")
		}
		if err := r.prepareDataDir(); err != nil {
			return problem(503, "storage", err.Error())
		}
	}
	if err := s.runner.Available(ctx); err != nil {
		return problem(503, "configuration", err.Error())
	}
	if err := s.runner.Recover(ctx); err != nil {
		return problem(503, "recovery", "无法确认旧检测程序已结束："+err.Error()+"；新检测已暂停，请核查运行状态")
	}
	if _, err := s.queryJSON(ctx, "init", Owner{}, map[string]any{}, nil, true); err != nil {
		return err
	}
	if _, err := s.queryJSON(ctx, "recover", Owner{}, map[string]any{}, nil, true); err != nil {
		return err
	}
	s.initialized = true
	return nil
}
func (s *Service) owner(ctx context.Context, user string) (Owner, error) {
	if user == "" {
		return Owner{}, problem(401, "login", "登录已失效，请重新登录后打开模型检测")
	}
	workspace, err := s.workspace.RequireCurrentID(ctx, user)
	if err != nil || workspace == "" {
		return Owner{}, problem(409, "workspace", "请先选择自己的工作区，再打开模型检测")
	}
	return Owner{user, workspace}, nil
}
func (s *Service) queryJSON(ctx context.Context, op string, owner Owner, args any, cred *Credential, internal bool) (json.RawMessage, error) {
	response, err := s.runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, cred, internal)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Length > 64*1024 {
		return nil, problem(503, "query", "检测程序返回的摘要超过64KiB，请检查程序版本后重试")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || int64(len(data)) != response.Length {
		return nil, problem(503, "query", "检测摘要读取中断，请重新读取；本操作不会重新调用模型")
	}
	if err := response.Body.Close(); err != nil {
		return nil, problem(503, "query-exit", "检测数据已返回，但未能确认辅助程序退出；请核查运行状态后重试："+err.Error())
	}
	return json.RawMessage(data), nil
}

type ExpectedContext struct {
	WorkspaceID string `json:"workspaceId"`
	TargetID    string `json:"targetId"`
	GroupID     string `json:"groupId"`
	BaseURL     string `json:"baseURL"`
	Protocol    string `json:"protocol"`
}

func expected(c connection_health.AuditContext) ExpectedContext {
	return ExpectedContext{c.WorkspaceID, c.TargetID, c.GroupID, c.BaseURL, c.Protocol}
}
func credential(c connection_health.AuditContext) Credential {
	scheme := c.Credential.AnthropicAuth
	if scheme == "authorization_bearer" {
		scheme = "bearer"
	}
	return Credential{c.Credential.BaseURL, c.Credential.Key, c.Protocol, scheme}
}
func checkedCredential(c connection_health.AuditContext) (Credential, error) {
	switch c.Credential.AnthropicAuth {
	case "authorization_bearer", "x_api_key":
		return credential(c), nil
	case "":
		if c.Protocol != "anthropic" {
			return credential(c), nil
		}
	}
	return Credential{}, problem(422, "authentication", "无法确认此账号配置的Messages认证方式，请在主站检查API Key或Bearer认证配置后重新打开账号；尚未发送检测请求")
}

type StartInput struct {
	ReportID            string          `json:"reportId"`
	TargetID            string          `json:"targetId"`
	GroupID             string          `json:"groupId"`
	Model               string          `json:"model"`
	SelectedSectionIDs  []string        `json:"selectedSectionIds"`
	SelectedQuestionIDs []string        `json:"selectedQuestionIds"`
	ExpectedContext     ExpectedContext `json:"expectedContext"`
}

var reportIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Service) Start(ctx context.Context, owner Owner, input StartInput) (any, error) {
	if !reportIDPattern.MatchString(input.ReportID) || strings.TrimSpace(input.TargetID) == "" || len(input.Model) < 1 || len(input.Model) > 256 {
		return nil, problem(422, "input", "报告编号、账号或模型名称无效，请重新打开账号并选择模型后重试")
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	previous, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, false)
	if err == nil {
		s.mu.Lock()
		pending := s.active
		s.mu.Unlock()
		if pending != nil && pending.Owner == owner && pending.ReportID == input.ReportID && (pending.Job == nil || pending.Job.Info().Exited) {
			if err = s.recoverExited(ctx, pending); err != nil {
				return nil, err
			}
			previous, err = s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, false)
			if err != nil {
				return nil, err
			}
		}
		var result map[string]any
		if json.Unmarshal(previous, &result) != nil || result == nil || result["reportId"] != input.ReportID {
			return nil, problem(503, "storage", "旧报告摘要无法读取或编号不一致，请检查检测程序版本；不会重复执行模型请求")
		}
		result["processExited"] = s.processExited(owner, input.ReportID)
		result["accepted"] = true
		return result, nil
	}
	if asProblem(err).Status != 404 {
		return nil, err
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		if active.Job == nil || active.Job.Info().Exited {
			if err = s.recoverExited(ctx, active); err != nil {
				return nil, err
			}
		} else {
			return nil, problem(409, "busy", "当前已有检测正在执行，请等待或查看结果；本次尚未发出检测请求")
		}
	}
	audit, err := s.targets.ResolveAuditContext(ctx, owner.UserID, input.TargetID, input.GroupID, true)
	if err != nil {
		return nil, problem(409, "account", err.Error())
	}
	if !audit.CanRun {
		return nil, problem(422, "account", audit.Reason)
	}
	if expected(audit) != input.ExpectedContext || audit.WorkspaceID != owner.WorkspaceID {
		return nil, problem(409, "context", "账号、分组、地址或协议已变化，请刷新账号信息后重新确认；尚未发出检测请求")
	}
	cred, err := checkedCredential(audit)
	if err != nil {
		return nil, err
	}
	active = &activeTask{Owner: owner, ReportID: input.ReportID, AccountName: audit.AccountName, OperationResult: "not_started"}
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
	created, err := s.queryJSON(ctx, "create-report", owner, map[string]any{
		"reportId": input.ReportID, "targetId": input.TargetID, "groupId": input.GroupID, "model": input.Model, "selectedSectionIds": input.SelectedSectionIDs, "selectedQuestionIds": input.SelectedQuestionIDs, "expectedContext": input.ExpectedContext, "request": input,
		"target": map[string]any{"accountId": audit.AccountID, "accountName": audit.AccountName, "groupName": audit.GroupName, "baseURL": audit.BaseURL, "protocol": audit.Protocol},
	}, nil, false)
	if err != nil {
		e := asProblem(err)
		e.ReportID = input.ReportID
		e.OperationResult = "not_started"
		s.mu.Lock()
		active.Diagnostic = e.Message
		s.mu.Unlock()
		_ = s.recoverExited(context.WithoutCancel(ctx), active)
		return nil, e
	}
	var report struct {
		MemoryLimitMiB      int    `json:"memoryLimitMiB"`
		TotalTimeoutSeconds int    `json:"totalTimeoutSeconds"`
		Status              string `json:"status"`
	}
	if json.Unmarshal(created, &report) != nil {
		e := problem(503, "storage", "已创建报告，但无法读取运行设置；尚未发送模型请求，请查看此报告，不要重复新建")
		e.ReportID = input.ReportID
		s.mu.Lock()
		active.Diagnostic = e.Message
		s.mu.Unlock()
		_ = s.recoverExited(context.WithoutCancel(ctx), active)
		return nil, e
	}
	job, startErr := s.runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: owner, Args: map[string]any{"reportId": input.ReportID}}, cred, report.MemoryLimitMiB, report.TotalTimeoutSeconds)
	s.mu.Lock()
	active.Job = job
	active.OperationResult = "unknown"
	s.mu.Unlock()
	if startErr != nil {
		s.mu.Lock()
		active.Diagnostic = asProblem(startErr).Message
		active.OperationResult = asProblem(startErr).OperationResult
		s.mu.Unlock()
		if job == nil || job.Info().Exited {
			_ = s.recoverExited(context.WithoutCancel(ctx), active)
		}
		e := asProblem(startErr)
		e.ReportID = input.ReportID
		return nil, e
	}
	go func() {
		<-job.Done
		s.admission.Lock()
		defer s.admission.Unlock()
		_ = s.recoverExited(context.Background(), active)
	}()
	return map[string]any{"reportId": input.ReportID, "status": "running", "accepted": true, "processExited": false}, nil
}
func (s *Service) recoverExited(ctx context.Context, active *activeTask) error {
	s.mu.Lock()
	if s.active != active {
		s.mu.Unlock()
		return nil
	}
	job, diagnostic, operationResult := active.Job, active.Diagnostic, active.OperationResult
	s.mu.Unlock()
	if job != nil && !job.Info().Exited {
		return problem(503, "exit", "尚未确认整个检测进程组已退出，新检测已暂停；请稍后核查运行状态")
	}
	message := diagnostic
	info := ExitInfo{Exited: true}
	if job != nil {
		info = job.Info()
	}
	if message == "" {
		if info.OOM {
			message = "检测程序超过本次允许的内存而中断；已保存部分仍可查看，未保存的请求可能已计费，请减少检测项后手动新建"
		} else {
			message = "检测程序已退出，未保存的检测项标为未完成；部分请求可能已计费，不会自动重试"
		}
	}
	if operationResult == "" {
		operationResult = "unknown"
	}
	_, err := s.queryJSON(ctx, "finish-interrupted", active.Owner, map[string]any{"reportId": active.ReportID, "error": map[string]any{"message": message, "step": "exit", "operationResult": operationResult}}, nil, true)
	if err != nil && job == nil && operationResult == "not_started" && asProblem(err).Status == 404 {
		err = nil
	}
	if err != nil {
		s.mu.Lock()
		active.Diagnostic = "检测程序已退出，但报告状态尚未保存：" + asProblem(err).Message + "；请处理后点击停止检测以重试保存，不会再次调用模型"
		message = active.Diagnostic
		s.mu.Unlock()
		return problem(503, "recovery", message)
	}
	s.mu.Lock()
	if s.active == active {
		s.active = nil
	}
	s.mu.Unlock()
	return nil
}
func (s *Service) processExited(owner Owner, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.Owner == owner && s.active.ReportID == id {
		return s.active.Job != nil && s.active.Job.Info().Exited
	}
	return true
}
func (s *Service) Cancel(ctx context.Context, owner Owner, id string) (any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if _, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, false); err != nil {
		return nil, err
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil && active.ReportID == id && active.Owner == owner {
		if active.Job != nil && !active.Job.Info().Exited {
			if err := active.Job.Stop(ctx); err != nil {
				return nil, problem(503, "cancel", "停止检测的请求未完成："+err.Error()+"；请核查运行状态后重试，不能确认已经停止")
			}
			return map[string]any{"reportId": id, "status": "stopping", "processExited": false}, nil
		}
		if err := s.recoverExited(ctx, active); err != nil {
			return nil, err
		}
	}
	data, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, false)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	result["processExited"] = true
	return result, nil
}
func (s *Service) Shutdown(ctx context.Context) error {
	if !s.cfg.ChanshuiEnabled {
		return nil
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	if err := s.runner.Shutdown(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active == nil {
		return nil
	}
	if active.Job != nil {
		select {
		case <-active.Job.Done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.recoverExited(ctx, active)
}
func (s *Service) runtime(ctx context.Context, owner Owner) map[string]any {
	result := map[string]any{"enabled": s.cfg.ChanshuiEnabled, "available": false, "memoryCeilingBytes": MemoryCeiling}
	if !s.cfg.ChanshuiEnabled {
		result["reason"] = "当前实例仅提供接口，不能运行检测；请打开正常调度站实例，原调度功能不受影响"
		return result
	}
	if err := s.ensureInitialized(ctx); err != nil {
		result["reason"] = asProblem(err).Message
		return result
	}
	result["available"] = true
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		active := s.active
		if active.Owner == owner {
			info := ExitInfo{}
			if active.Job != nil {
				info = active.Job.Info()
			}
			result["active"] = map[string]any{"reportId": active.ReportID, "accountName": active.AccountName, "status": "running", "processExited": info.Exited, "diagnostic": active.Diagnostic, "memoryPeakBytes": info.MemoryPeak, "taskMemoryMaxBytes": info.TaskMemoryMaxBytes, "sliceMemoryMaxBytes": info.SliceMemoryMaxBytes, "swapMaxBytes": info.SwapMaxBytes}
		} else {
			result["busy"] = true
			result["reason"] = "当前已有检测正在执行，请等待后再开始"
		}
	}
	return result
}
