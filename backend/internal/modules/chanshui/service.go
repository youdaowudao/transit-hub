package chanshui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
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
	Owner                          Owner
	ReportID, AccountName, BatchID string
	Job                            *Job
	Diagnostic                     string
	RecoveryReason                 string
	OperationResult                string
	Input                          StartInput
	Preparing                      bool
	CancelRequested                bool
	StopConfirmDeadlineMs          int64
	StopCallPending                bool
	RecoveryAttempted              bool
	recoveryDeadlineMs             int64
	CancelPrepare                  context.CancelFunc
	PreparationDone                chan struct{}
	prepareContext                 context.Context
	memoryMiB, totalSeconds        int
}
type Service struct {
	cfg              config.Config
	workspace        WorkspaceResolver
	targets          TargetReader
	runner           Executor
	webDir           string
	initMu           sync.Mutex
	initialized      bool
	admission        sync.Mutex
	mu               sync.Mutex
	active           *activeTask
	waiting          []*waitingTask
	blocked          map[reportKey]*blockedReport
	closing          bool
	recoveryRunning  bool
	recoveryDeadline time.Time
	recoveryCancel   context.CancelFunc
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
	return &Service{cfg: cfg, workspace: workspace, targets: targets, webDir: web, runner: NewRunner(RunnerConfig{BinaryPath: binary, DataDir: data, ProjectDir: root, Manager: cfg.ChanshuiSystemdManager, AllowPrivateTargets: cfg.ChanshuiTestAllowPrivateTargets})}
}
func (s *Service) ensureInitialized(ctx context.Context) error {
	if !s.cfg.ChanshuiEnabled {
		return problem(503, "configuration", "当前实例仅提供接口，不能运行检测；请打开正常调度站实例。原调度功能不受影响")
	}
	if status := s.queryStatus(Owner{}); status.State == "retained" {
		return status.problem()
	}
	if err := lockContext(ctx, &s.initMu); err != nil {
		return problem(503, "initialization", "检测初始化未完成，无法确认旧程序状态，请检查systemd后按审核重启本调度站后端")
	}
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
		return nil, s.withQueryStatus(owner, err)
	}
	defer response.Body.Close()
	if response.Length > 64*1024 {
		return nil, problem(503, "query", "检测程序返回的摘要超过64KiB，请检查程序版本后重试")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || int64(len(data)) != response.Length {
		message := "检测摘要读取中断，请重新读取；本操作不会重新调用模型"
		if errors.Is(err, os.ErrDeadlineExceeded) || ctx.Err() != nil {
			message = "辅助查询的执行期限或本次操作剩余时间已到，正在确认程序退出；若本次是创建或补测，请先按原编号查询，不要重复提交"
		} else if err != nil {
			message += "：" + err.Error()
		}
		return nil, s.withQueryStatus(owner, problem(503, "query", message))
	}
	if err := response.Body.Close(); err != nil {
		return nil, s.withQueryStatus(owner, problem(503, "query-exit", "检测数据已返回，但辅助程序清理未完成；请核查运行状态后重试："+err.Error()))
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
	SingleAsk           json.RawMessage `json:"singleAsk,omitempty"`
	RetryID             string          `json:"-"`
	ReportID            string          `json:"reportId"`
	TargetID            string          `json:"targetId"`
	GroupID             string          `json:"groupId"`
	Model               string          `json:"model"`
	SelectedSectionIDs  []string        `json:"selectedSectionIds"`
	SelectedQuestionIDs []string        `json:"selectedQuestionIds"`
	ExpectedContext     ExpectedContext `json:"expectedContext"`
}

var reportIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Service) recoverExited(ctx context.Context, active *activeTask) error {
	s.mu.Lock()
	if s.active != active {
		s.mu.Unlock()
		return nil
	}
	job, diagnostic, operationResult := active.Job, active.Diagnostic, active.OperationResult
	if diagnostic == active.RecoveryReason {
		diagnostic = ""
	}
	preparing, userCancelled := active.Preparing, active.CancelRequested
	mayFinalize := s.mayFinalizeBatchLocked(active.Owner, active.BatchID, active, nil)
	s.mu.Unlock()
	if preparing {
		return problem(503, "prepare", "正在结束检测准备，请稍后核查；已保存回答仍可查看")
	}
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
	var err error
	var saved json.RawMessage
	if userCancelled && operationResult == "not_started" {
		args := map[string]any{"reportId": active.ReportID}
		if active.BatchID != "" {
			args["mayFinalizeBatch"] = mayFinalize
		}
		saved, err = s.queryJSON(ctx, "cancel-queued", active.Owner, args, nil, true)
	} else {
		args := map[string]any{"reportId": active.ReportID, "error": map[string]any{"message": message, "step": "exit", "operationResult": operationResult}}
		if active.BatchID != "" {
			args["mayFinalizeBatch"] = mayFinalize
		}
		saved, err = s.queryJSON(ctx, "finish-interrupted", active.Owner, args, nil, true)
	}
	if err == nil {
		err = confirmTerminal(saved)
	}
	if err != nil && job == nil && operationResult == "not_started" && asProblem(err).Status == 404 {
		err = nil
	}
	if err != nil {
		s.mu.Lock()
		message = "检测程序已退出，但报告状态尚未保存：" + asProblem(err).Message + "；请处理后点击核查并重试保存，不会再次调用模型"
		if active.Diagnostic == "" || active.Diagnostic == active.RecoveryReason {
			active.Diagnostic = message
		}
		active.RecoveryReason = message
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
func (s *Service) runtime(ctx context.Context, owner Owner) map[string]any {
	result := map[string]any{"enabled": s.cfg.ChanshuiEnabled, "available": false, "memoryCeilingBytes": MemoryCeiling}
	if !s.cfg.ChanshuiEnabled {
		result["reason"] = "当前实例仅提供接口，不能运行检测；请打开正常调度站实例，原调度功能不受影响"
		return result
	}
	status := s.queryStatus(owner)
	s.mu.Lock()
	hasRuntime := s.active != nil || s.recoveryRunning || len(s.blocked) != 0
	s.mu.Unlock()
	if !hasRuntime && status.State == "" {
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.ensureInitialized(work); err != nil {
			result["reason"] = asProblem(err).Message
			return result
		}
	}
	return s.captureRuntime(owner).runtime(owner)
}
