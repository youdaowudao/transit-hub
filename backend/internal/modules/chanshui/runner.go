package chanshui

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RunnerConfig struct {
	BinaryPath, DataDir, ProjectDir, Manager string
	AllowPrivateTargets                      bool
	DisableBankSync                          bool
}

// The one existing admission slot also owns an unconfirmed helper. Keeping the
// process here retains its control connection without adding a recovery worker.
type queryProcessSlot struct {
	process                       *process
	owner                         Owner
	step                          string
	workDeadline, cleanupDeadline time.Time
	reason                        string
}
type QueryState struct {
	State, Reason            string
	DeadlineMs, RetryAfterMs int64
}

func (q QueryState) problem() *Problem {
	p := problem(429, "query", q.Reason)
	p.QueryState = q.State
	p.RecoveryDeadlineMs = q.DeadlineMs
	p.RetryAfterMs = q.RetryAfterMs
	return p
}

type Runner struct {
	cfg       RunnerConfig
	querySlot chan *queryProcessSlot
	queryMu   sync.Mutex
	queryRef  *queryProcessSlot
}

func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{cfg: cfg, querySlot: make(chan *queryProcessSlot, 1)}
}
func queryStep(operation string) string {
	switch operation {
	case "create-report", "create-batch", "retry-report":
		return "保存检测受理"
	case "finish-interrupted", "cancel-queued", "interrupt-batch-unstarted", "recover":
		return "核查并保存结束状态"
	case "init":
		return "初始化检测数据库"
	case "models":
		return "读取上游模型列表"
	case "save-settings", "save-question", "delete-question", "judge-item":
		return "保存检测设置或结果"
	default:
		return "读取已保存数据"
	}
}
func (r *Runner) QueryStatus(owner Owner) QueryState {
	r.queryMu.Lock()
	defer r.queryMu.Unlock()
	slot := r.queryRef
	if slot == nil {
		return QueryState{}
	}
	state := "busy"
	if slot.reason != "" || !slot.cleanupDeadline.IsZero() && time.Now().After(slot.cleanupDeadline) {
		state = "retained"
	}
	reason := "正在读取或保存其他检测状态，稍后自动核查；此时不会重发检测题目"
	if state == "retained" {
		reason = queryExitUnconfirmedMessage
		if slot.owner == owner && slot.reason != "" {
			reason = slot.reason
		}
	} else if slot.owner == owner {
		reason = "正在处理检测状态（" + slot.step + "），稍后重新读取"
	}
	return QueryState{State: state, Reason: reason, DeadlineMs: unixMilli(slot.cleanupDeadline), RetryAfterMs: 1000}
}
func (s *Service) queryStatus(owner Owner) QueryState {
	if r, ok := s.runner.(interface{ QueryStatus(Owner) QueryState }); ok {
		return r.QueryStatus(owner)
	}
	return QueryState{}
}
func (s *Service) withQueryStatus(owner Owner, err error) error {
	status := s.queryStatus(owner)
	if status.State == "" {
		return err
	}
	p := asProblem(err)
	if p.QueryState == "" && p.Status != 429 && !(p.Status >= 500 && p.Step == "query-exit" && status.State == "retained") {
		return err
	}
	p.QueryState = status.State
	p.RecoveryDeadlineMs = status.DeadlineMs
	s.mu.Lock()
	if s.recoveryRunning {
		p.RecoveryDeadlineMs = unixMilli(s.recoveryDeadline)
	}
	s.mu.Unlock()
	p.RetryAfterMs = status.RetryAfterMs
	if status.State == "retained" {
		if p.Status != 429 {
			p.Status = 503
		}
		if !strings.Contains(status.Reason, p.Message) {
			p.Message = status.Reason + "；本次操作：" + p.Message
		} else {
			p.Message = status.Reason
		}
	}
	return p
}
func deadlineCap(ctx context.Context, d time.Duration) time.Time {
	limit := time.Now().Add(d)
	if end, ok := ctx.Deadline(); ok && end.Before(limit) {
		return end
	}
	return limit
}
func (r *Runner) prepareDataDir() error {
	if !filepath.IsAbs(r.cfg.DataDir) || !within(r.cfg.ProjectDir, r.cfg.DataDir) {
		return fmt.Errorf("检测数据目录必须位于当前项目内，请修正部署配置")
	}
	parent := r.cfg.DataDir
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if !within(r.cfg.ProjectDir, resolved) {
				return fmt.Errorf("检测数据目录的父目录指向项目外，请修正目录后重试")
			}
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("无法核查检测数据目录：%w", err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("无法找到检测数据目录的有效父目录")
		}
		parent = next
	}
	if err := os.MkdirAll(r.cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("无法创建检测数据目录，请修正目录写入权限后重试：%w", err)
	}
	if err := os.Chmod(r.cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("无法限制检测数据目录权限，请核对目录所有者后重试：%w", err)
	}
	return nil
}
func (r *Runner) Available(ctx context.Context) error {
	for label, path := range map[string]string{"可执行文件": r.cfg.BinaryPath, "数据目录": r.cfg.DataDir} {
		if !filepath.IsAbs(path) || !within(r.cfg.ProjectDir, path) {
			return fmt.Errorf("检测%s必须位于当前项目目录内，请修正部署路径", label)
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil {
			return fmt.Errorf("检测%s不可访问：%w", label, e)
		}
		if !within(r.cfg.ProjectDir, resolved) {
			return fmt.Errorf("检测%s指向项目外，不能启用", label)
		}
	}
	info, err := os.Stat(r.cfg.BinaryPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("检测程序不是可执行的普通文件，或允许其他用户修改；请修正权限")
	}
	m, err := connectManager(ctx, r.cfg)
	if err != nil {
		return err
	}
	releaseCtx, cancel := context.WithDeadline(context.Background(), deadlineCap(ctx, 3*time.Second))
	defer cancel()
	return m.releaseContext(releaseCtx, "")
}
func within(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type process struct {
	manager               *systemManager
	name                  string
	input, output, stderr *os.File
	reader                *bufio.Reader
	done                  chan struct{}
	monitored             bool
	mu                    sync.Mutex
	exit                  ExitInfo
	exitErr               error
	release               sync.Once
	cleanupMu             sync.Mutex
	cleanupDeadline       time.Time
	cleanupMaximum        time.Time
	lateCleanup           func(time.Time) error
}

func (p *process) info() ExitInfo { p.mu.Lock(); defer p.mu.Unlock(); return p.exit }
func (p *process) setExit(info ExitInfo, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Keep the highest peak seen, even from a late sample whose state is
	// otherwise ignored: later checks may sample lower, and the cgroup (and
	// its peak file) disappears once the process exits.
	p.exit.MemoryPeak = max(p.exit.MemoryPeak, info.MemoryPeak)
	if p.exit.Exited && !info.Exited {
		return
	}
	if info.TaskMemoryMaxBytes == 0 {
		info.TaskMemoryMaxBytes = p.exit.TaskMemoryMaxBytes
		info.SliceMemoryMaxBytes = p.exit.SliceMemoryMaxBytes
		info.SwapMaxBytes = p.exit.SwapMaxBytes
	}
	info.MemoryPeak = p.exit.MemoryPeak
	p.exit = info
	p.exitErr = err
}
func (p *process) stop(ctx context.Context) error {
	control, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if p.monitorFinished() {
		// A new, explicit Stop is the only control action that can supply a
		// fresh release deadline once the original monitor returned unknown.
		// It does not renew any report or query execution budget.
		p.mu.Lock()
		if !p.exit.Exited {
			p.cleanupDeadline = deadlineCap(control, 3*time.Second)
		}
		p.mu.Unlock()
	}
	if p.info().Exited {
		return p.finishLateStopCleanup(control)
	}
	normal, stop := context.WithDeadline(control, deadlineCap(control, time.Second))
	defer stop()
	err := p.manager.stop(normal, p.name)
	if p.awaitExit(normal) {
		return p.finishLateStopCleanup(control)
	}
	stop()
	if control.Err() != nil {
		return errors.Join(err, control.Err())
	}
	if killErr := p.manager.kill(control, p.name); killErr != nil {
		return errors.Join(err, killErr)
	}
	// Kill acceptance is not an exit. If the original monitor is gone,
	// use the remaining control window to obtain a real exit observation.
	if p.monitorFinished() && p.awaitExit(control) {
		return p.finishLateStopCleanup(control)
	}
	return nil
}
func (p *process) monitorFinished() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
func (p *process) finishLateStopCleanup(ctx context.Context) error {
	if !p.monitorFinished() || !p.info().Exited {
		return nil
	}
	if err := p.confirmedCleanup(nil)(deadlineCap(ctx, 3*time.Second)); err != nil {
		return fmt.Errorf("检测程序已确认退出，但原进程管理资源清理未完成：%w", err)
	}
	return nil
}
func (p *process) awaitExit(ctx context.Context) bool {
	for ctx.Err() == nil {
		info, err := p.manager.inspect(ctx, p.name)
		if err == nil && info.Exited {
			p.setExit(info, nil)
			return true
		}
		if namedBusError(err, "org.freedesktop.systemd1.NoSuchUnit") {
			p.setExit(ExitInfo{Exited: true}, nil)
			return true
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Millisecond):
		}
	}
	return p.info().Exited
}
func (p *process) cleanupLimit() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleanupDeadline.IsZero() {
		p.cleanupDeadline = time.Now().Add(10 * time.Second)
		if !p.cleanupMaximum.IsZero() && p.cleanupMaximum.Before(p.cleanupDeadline) {
			p.cleanupDeadline = p.cleanupMaximum
		}
	}
	return p.cleanupDeadline
}
func (p *process) stopAndWait() {
	deadline := p.cleanupLimit()
	lockUntil, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if lockContext(lockUntil, &p.cleanupMu) != nil {
		return
	}
	defer p.cleanupMu.Unlock()
	if p.info().Exited {
		return
	}
	stopDeadline := deadline.Add(-5 * time.Second)
	stopCtx, stop := context.WithDeadline(context.Background(), stopDeadline)
	_ = p.manager.stop(stopCtx, p.name)
	exited := p.awaitExit(stopCtx)
	stop()
	if exited {
		return
	}
	killCtx, stopKill := context.WithDeadline(context.Background(), deadline.Add(-3*time.Second))
	defer stopKill()
	_ = p.manager.kill(killCtx, p.name)
	if p.awaitExit(killCtx) {
		return
	}
	p.setExit(ExitInfo{}, fmt.Errorf("停止后仍无法确认整个检测进程组已退出，请核查systemd运行诊断"))
}
func (p *process) monitor(ctx context.Context) {
	defer func() {
		close(p.done)
		p.mu.Lock()
		late := p.lateCleanup
		p.mu.Unlock()
		if late != nil && p.info().Exited {
			_ = late(p.cleanupLimit())
		}
	}()
	tick := time.NewTicker(75 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.stopAndWait()
			return
		case <-tick.C:
			check, cancel := context.WithTimeout(context.Background(), time.Second)
			info, err := p.manager.inspect(check, p.name)
			cancel()
			if err != nil {
				p.stopAndWait()
				return
			}
			p.setExit(info, nil)
			if info.Exited {
				return
			}
		}
	}
}
func (p *process) close() error {
	return p.closeUntil(p.cleanupLimit())
}
func (p *process) closeUntil(deadline time.Time) error {
	if !p.info().Exited {
		if !p.monitored {
			p.stopAndWait()
		}
		if !p.info().Exited {
			return errors.New(queryExitUnconfirmedMessage)
		}
	}
	p.release.Do(func() {
		p.input.Close()
		p.output.Close()
		p.stderr.Close()
		ctx, cancel := context.WithDeadline(context.Background(), deadlineCapTime(deadline, 3*time.Second))
		defer cancel()
		if err := p.manager.releaseContext(ctx, p.name); err != nil {
			p.mu.Lock()
			p.exitErr = err
			p.mu.Unlock()
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}
func deadlineCapTime(limit time.Time, d time.Duration) time.Time {
	now := time.Now().Add(d)
	if !limit.IsZero() && limit.Before(now) {
		return limit
	}
	return now
}
func (p *process) interruptControlIO(ctx context.Context) func() bool {
	return context.AfterFunc(ctx, func() {
		_ = p.input.Close()
		_ = p.output.Close()
	})
}

func preparationCancelled(ctx context.Context) *Problem {
	message := "检测准备等待超时"
	if errors.Is(ctx.Err(), context.Canceled) {
		message = "检测准备已取消"
	}
	return problem(409, "cancel", message+"，未交付账号凭据，也未发送检测请求；已结束本次准备")
}

func (p *process) failLaunch(issue error) (*process, error) {
	p.stopAndWait()
	if !p.info().Exited {
		e := asProblem(issue)
		e.Message += "；仍无法确认准备进程退出，已暂停后续任务，请核查运行状态"
		return p, e
	}
	_ = p.close()
	return nil, issue
}

// An unknown exit must retain its admission slot; a closed Done channel alone
// is not evidence that the systemd process group has exited.
const queryExitUnconfirmedMessage = "查询程序退出尚未确认，后续查询已暂停；请检查本实例systemd与原查询进程后按审核重新启动本调度站后端；已保存报告保留，不会自动重跑模型"

// The original reader, original monitor and a later explicitly accepted Stop
// all share the same cleanup. Files/manager close and any query slot release
// each happen only once, after a genuine exit observation.
func (p *process) confirmedCleanup(release func()) func(time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lateCleanup == nil {
		var releaseOnce sync.Once
		p.lateCleanup = func(deadline time.Time) error {
			err := p.closeUntil(deadline)
			if release != nil && p.info().Exited {
				releaseOnce.Do(release)
			}
			return err
		}
	}
	return p.lateCleanup
}
func (p *process) closeAfterConfirmedExit(release func()) error {
	deadline := p.cleanupLimit()
	cleanup := p.confirmedCleanup(release)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if !p.monitorFinished() {
		select {
		case <-p.done:
		case <-ctx.Done():
			return errors.New(queryExitUnconfirmedMessage)
		}
	}
	if !p.info().Exited {
		p.mu.Lock()
		err := p.exitErr
		p.mu.Unlock()
		return errors.Join(errors.New(queryExitUnconfirmedMessage), err)
	}
	return cleanup(deadline)
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > 4096 {
		return nil, fmt.Errorf("检测程序返回的控制信息过大")
	}
	return line, nil
}
func (r *Runner) launch(ctx context.Context, role string, env Envelope, limit uint64, seconds int) (*process, error) {
	if err := r.Available(ctx); err != nil {
		return nil, problem(503, "startup", err.Error()+"；尚未向上游发送检测请求")
	}
	m, err := connectManager(ctx, r.cfg)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, errors.Join(err, m.releaseContext(ctx, ""))
	}
	name := "transithub-chanshui-" + role + "-" + hex.EncodeToString(nonce[:]) + ".service"
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, m.releaseContext(ctx, ""))
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, errors.Join(err, m.releaseContext(ctx, ""))
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, errors.Join(err, m.releaseContext(ctx, ""))
	}
	p := &process{manager: m, name: name, input: inW, output: outR, stderr: errR, reader: bufio.NewReaderSize(outR, 4096), done: make(chan struct{}), cleanupMaximum: deadlineCap(ctx, 30*time.Second).Add(10 * time.Second)}
	stopControlIO := p.interruptControlIO(ctx)
	defer stopControlIO()
	err = m.start(ctx, name, role, r.cfg, limit, seconds, inR, outW, errW)
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		return p.failLaunch(problem(503, "startup", err.Error()+"；尚未向上游发送检测请求"))
	}
	// Never retain stderr, which may contain arbitrary upstream text. A child
	// violating the bounded protocol is stopped rather than blocking a pipe.
	go func() {
		n, _ := io.CopyN(io.Discard, errR, 8193)
		if n > 8192 {
			p.stopAndWait()
		}
	}()
	env.V = 1
	env.Deployment = Deployment{AllowPrivateTargets: r.cfg.AllowPrivateTargets, DisableBankSync: r.cfg.DisableBankSync}
	encoded, err := json.Marshal(env)
	if err != nil || len(encoded) > 128*1024 {
		return p.failLaunch(problem(422, "input", "检测参数超过128KiB或无法编码，请减少选择后重试"))
	}
	if ctx.Err() != nil {
		return p.failLaunch(preparationCancelled(ctx))
	}
	if _, err = p.input.Write(append(encoded, '\n')); err != nil {
		if ctx.Err() != nil {
			return p.failLaunch(preparationCancelled(ctx))
		}
		return p.failLaunch(problem(503, "handshake", "无法交付检测启动参数，尚未发送模型请求；请检查程序版本后重试"))
	}
	_ = p.output.SetReadDeadline(deadlineCap(ctx, 10*time.Second))
	line, err := readLine(p.reader)
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			return p.failLaunch(preparationCancelled(ctx))
		}
		return p.failLaunch(problem(503, "handshake", "检测程序在10秒内未完成启动核验，尚未发送模型请求；请检查程序版本、内存限制和运行诊断"))
	}
	var ready struct {
		V      int                                         `json:"v"`
		Event  string                                      `json:"event"`
		Limits struct{ TaskMax, SliceMax, SwapMax uint64 } `json:"limits"`
	}
	if json.Unmarshal(line, &ready) != nil || ready.V != 1 || ready.Event != "ready" || ready.Limits.TaskMax == 0 || ready.Limits.TaskMax > limit || ready.Limits.SliceMax == 0 || ready.Limits.SliceMax > MemoryCeiling || ready.Limits.SwapMax != 0 {
		return p.failLaunch(problem(503, "limits", "检测程序没有确认有效硬内存限制，已拒绝交付密钥；尚未发送模型请求。请核对slice及程序版本"))
	}
	stopControlIO()
	if ctx.Err() != nil {
		return p.failLaunch(preparationCancelled(ctx))
	}
	p.setExit(ExitInfo{TaskMemoryMaxBytes: ready.Limits.TaskMax, SliceMemoryMaxBytes: ready.Limits.SliceMax, SwapMaxBytes: ready.Limits.SwapMax}, nil)
	_ = p.output.SetReadDeadline(time.Time{})
	return p, nil
}

// The bool describes bytes actually written in this one handoff. Once any
// credential bytes were sent, a missing acknowledgement must remain unknown.
func continueProcess(ctx context.Context, p *process, cred *Credential) (bool, error) {
	wire := struct {
		V          int         `json:"v"`
		Command    string      `json:"command"`
		Credential *Credential `json:"credential,omitempty"`
	}{1, "continue", cred}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return false, err
	}
	if len(encoded) > 16*1024 {
		return false, fmt.Errorf("账号凭据超过16KiB，请检查主站账号配置")
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	stopControlIO := context.AfterFunc(ctx, func() { _ = p.input.Close() })
	defer stopControlIO()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	wireBytes := append(encoded, '\n')
	n, err := p.input.Write(wireBytes)
	_ = p.input.Close()
	if err == nil && n != len(wireBytes) {
		err = io.ErrShortWrite
	}
	return n > 0, err
}

func (r *Runner) Query(ctx context.Context, env Envelope, cred *Credential, internal bool) (*QueryResponse, error) {
	return r.query(ctx, env, cred, internal, r.launch)
}

// The launch boundary is passed explicitly so the full query protocol can be
// tested with real pipes and a deterministic process-exit result.
func (r *Runner) query(ctx context.Context, env Envelope, cred *Credential, internal bool, launch func(context.Context, string, Envelope, uint64, int) (*process, error)) (*QueryResponse, error) {
	if status := r.QueryStatus(env.Owner); status.State == "retained" {
		return nil, status.problem()
	}
	slot := &queryProcessSlot{owner: env.Owner, step: queryStep(env.Operation)}
	waitDuration := 25 * time.Second
	if !internal {
		waitDuration = 0
	}
	wait, cancelWait := context.WithTimeout(ctx, waitDuration)
	defer cancelWait()
	var queryCtx context.Context
	var cancel context.CancelFunc
	var workEnd time.Time
	for {
		if ctx.Err() != nil {
			return nil, problem(503, "query", "读取时间已到，本次查询尚未开始，请重新核查")
		}
		r.queryMu.Lock()
		acquired := false
		select {
		case r.querySlot <- slot:
			queryCtx, cancel = context.WithTimeout(ctx, 15*time.Second)
			workEnd, _ = queryCtx.Deadline()
			slot.workDeadline = workEnd
			slot.cleanupDeadline = workEnd.Add(10 * time.Second)
			r.queryRef = slot
			acquired = true
		default:
		}
		r.queryMu.Unlock()
		if acquired {
			break
		}
		if state := r.QueryStatus(env.Owner); state.State == "retained" || !internal {
			if state.State == "" {
				state = QueryState{State: "busy", Reason: "另一个检测查询正在处理，请稍后重试", RetryAfterMs: 1000}
			}
			return nil, state.problem()
		}
		select {
		case <-wait.Done():
			state := r.QueryStatus(env.Owner)
			if state.State != "" {
				return nil, state.problem()
			}
			return nil, problem(503, "query", "等待检测查询位置超时，请核查运行状态；不会重新调用模型")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var released sync.Once
	release := func() {
		released.Do(func() {
			r.queryMu.Lock()
			if r.queryRef == slot {
				r.queryRef = nil
			}
			<-r.querySlot
			r.queryMu.Unlock()
		})
	}
	retain := func(err error) {
		r.queryMu.Lock()
		if r.queryRef == slot {
			slot.reason = queryExitUnconfirmedMessage
			if err != nil {
				slot.reason += "：" + err.Error()
			}
		}
		r.queryMu.Unlock()
	}
	annotateRetained := func(issue *Problem) {
		state := r.QueryStatus(env.Owner)
		issue.QueryState = state.State
		issue.RecoveryDeadlineMs = state.DeadlineMs
		issue.RetryAfterMs = state.RetryAfterMs
		if issue.Status < 500 {
			issue.Status = 503
		}
	}
	p, err := launch(queryCtx, "query", env, 64*1024*1024, 15)
	r.queryMu.Lock()
	slot.process = p
	r.queryMu.Unlock()
	if err != nil {
		cancel()
		if p == nil {
			release()
		} else {
			if !p.monitored {
				if p.info().Exited {
					_ = p.close()
					release()
				} else {
					retain(err)
					issue := asProblem(err)
					issue.Message += "；" + queryExitUnconfirmedMessage
					annotateRetained(issue)
					err = issue
				}
			} else if cleanupErr := p.closeAfterConfirmedExit(release); cleanupErr != nil {
				issue := asProblem(err)
				issue.Message += "；" + cleanupErr.Error()
				err = issue
				if !p.info().Exited {
					retain(cleanupErr)
					annotateRetained(issue)
				}
			}
		}
		return nil, err
	}
	p.mu.Lock()
	p.cleanupMaximum = slot.cleanupDeadline
	p.mu.Unlock()
	if !p.monitored {
		p.monitored = true
		go p.monitor(queryCtx)
	}
	failQuery := func(issue *Problem) (*QueryResponse, error) {
		cancel()
		if cleanupErr := p.closeAfterConfirmedExit(release); cleanupErr != nil {
			issue.Message += "；" + cleanupErr.Error()
			if !p.info().Exited {
				retain(cleanupErr)
				annotateRetained(issue)
			}
		}
		return nil, issue
	}
	if _, err = continueProcess(queryCtx, p, cred); err != nil {
		e := problem(503, "query", "交付查询参数时连接中断，请重新读取结果；尚未重新开始检测")
		if cred != nil {
			e.OperationResult = "unknown"
		}
		return failQuery(e)
	}
	_ = p.output.SetReadDeadline(workEnd)
	line, err := readLine(p.reader)
	if err != nil {
		if queryCtx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) || !time.Now().Before(workEnd) {
			issue := problem(503, "query", "辅助查询的15秒执行期限或本次操作剩余时间已到，正在确认程序退出；若本次是创建或补测，请先按原编号查询，不要重复提交")
			issue.OperationResult = "unknown"
			return failQuery(issue)
		}
		return failQuery(problem(503, "query", "读取检测数据的控制信息未完成："+err.Error()+"；请核查检测程序运行诊断后重试"))
	}
	var header wireHeader
	if json.Unmarshal(line, &header) != nil || header.V != 1 || header.Length < 0 || header.Length > 16*1024*1024 {
		return failQuery(problem(503, "query", "检测程序返回的数据格式或大小不符合要求，请重新执行整站升级，并核对检测程序与调度站版本一致后重试"))
	}
	if !header.OK {
		e := problem(header.Status, "query", "检测数据读取或保存未完成，请检查设置后重试")
		var detail Problem
		if json.Unmarshal(header.Error, &detail) == nil && detail.Message != "" {
			e = &detail
			e.Status = header.Status
		}
		if e.Status < 400 {
			e.Status = 503
		}
		return failQuery(e)
	}
	if header.Status == 0 {
		header.Status = 200
	}
	if header.ContentType == "" {
		header.ContentType = "application/json"
	}
	return &QueryResponse{header.Status, header.ContentType, header.Length, &queryBody{reader: p.reader, left: header.Length, p: p, cancel: cancel, release: release, retain: retain}}, nil
}

type queryBody struct {
	reader  *bufio.Reader
	left    int64
	p       *process
	cancel  context.CancelFunc
	release func()
	retain  func(error)
	once    sync.Once
	err     error
}

func (b *queryBody) Read(dst []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	if int64(len(dst)) > b.left {
		dst = dst[:b.left]
	}
	n, err := b.reader.Read(dst)
	b.left -= int64(n)
	if err == io.EOF && b.left > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
func (b *queryBody) Close() error {
	b.once.Do(func() {
		if b.left > 0 {
			b.cancel()
		}
		select {
		case <-b.p.done:
		case <-time.After(2 * time.Second):
			b.cancel()
		}
		b.err = b.p.closeAfterConfirmedExit(b.release)
		if b.err != nil && !b.p.info().Exited && b.retain != nil {
			b.retain(b.err)
		}
		b.cancel()
	})
	return b.err
}

func (r *Runner) Start(ctx context.Context, env Envelope, cred Credential, memoryMiB, totalSeconds int) (*Job, error) {
	if status := r.QueryStatus(env.Owner); status.State == "retained" {
		return nil, status.problem()
	}
	if memoryMiB < 128 || memoryMiB > 384 || totalSeconds < 60 || totalSeconds > maxTaskSeconds {
		return nil, problem(422, "settings", "任务内存须为128～384MiB，总时限须为60～9223371976秒；请修改运行设置后重试")
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := r.launch(startup, "run", env, uint64(memoryMiB)*1024*1024, totalSeconds)
	if p == nil {
		return nil, err
	}
	runCtx, runCancel := context.WithTimeout(context.Background(), time.Duration(totalSeconds+10)*time.Second)
	runEnd, _ := runCtx.Deadline()
	p.mu.Lock()
	p.cleanupMaximum = runEnd.Add(10 * time.Second)
	p.mu.Unlock()
	p.monitored = true
	go func() { p.monitor(runCtx); runCancel() }()
	job := &Job{ID: p.name, Done: p.done, stop: p.stop, info: p.info, ExitConfirmDeadline: runEnd.Add(10 * time.Second)}
	if err != nil {
		go p.closeAfterConfirmedExit(nil)
		return job, err
	}
	stopControlIO := p.interruptControlIO(startup)
	defer stopControlIO()
	delivered, err := continueProcess(startup, p, &cred)
	if err != nil || startup.Err() != nil {
		stopControlIO()
		p.stopAndWait()
		if p.info().Exited {
			_ = p.close()
		} else {
			go p.closeAfterConfirmedExit(nil)
		}
		e := problem(503, "accept", "交付账号信息时连接中断，无法确认请求是否已开始；部分请求可能已计费，请查询同一报告，不要重复新建")
		e.OperationResult = "unknown"
		if !delivered {
			e.OperationResult = "not_started"
			e.Message = "账号凭据未交付，尚未发送检测请求；本次准备已结束，请查看原报告"
			if startup.Err() != nil {
				e = preparationCancelled(startup)
			}
			if p.info().Exited {
				return nil, e
			}
		}
		return job, e
	}
	_ = p.output.SetReadDeadline(deadlineCap(startup, 10*time.Second))
	line, err := readLine(p.reader)
	stopControlIO()
	var event struct {
		V     int    `json:"v"`
		Event string `json:"event"`
	}
	if err != nil || startup.Err() != nil || json.Unmarshal(line, &event) != nil || event.Event != "accepted" {
		p.stopAndWait()
		if p.info().Exited {
			_ = p.close()
		} else {
			go p.closeAfterConfirmedExit(nil)
		}
		e := problem(503, "accept", "账号凭据已交付，但未能确认检测受理结果，已请求停止；部分请求可能已计费，请查询此报告，不要重复新建")
		e.OperationResult = "unknown"
		return job, e
	}
	_ = p.output.SetReadDeadline(time.Time{})
	go func() {
		defer p.closeAfterConfirmedExit(nil)
		total := 0
		for {
			line, err := readLine(p.reader)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					p.stopAndWait()
				}
				return
			}
			total += len(line)
			if total > 512*1024 {
				p.stopAndWait()
				return
			}
			if json.Unmarshal(line, &event) != nil {
				p.stopAndWait()
				return
			}
			if event.Event == "persisted" {
				select {
				case <-p.done:
				case <-time.After(2 * time.Second):
					p.stopAndWait()
				}
				return
			}
		}
	}()
	return job, nil
}

func (r *Runner) Recover(ctx context.Context) (err error) {
	m, err := connectManager(ctx, r.cfg)
	if err != nil {
		return err
	}
	defer func() {
		release, cancel := context.WithDeadline(context.Background(), deadlineCap(ctx, 3*time.Second))
		defer cancel()
		err = errors.Join(err, m.releaseContext(release, ""))
	}()
	var units []listedUnit
	if err = m.call(ctx, "ListUnitsByPatterns", []string{}, []string{"transithub-chanshui-run-*.service", "transithub-chanshui-query-*.service"}).Store(&units); err != nil {
		return err
	}
	for _, u := range units {
		if _, err = m.inspect(ctx, u.Name); err != nil {
			return err
		}
		if err = m.call(ctx, "RefUnit", u.Name).Err; err != nil {
			return err
		}
		if err = m.stop(ctx, u.Name); err != nil {
			return err
		}
		end := time.Now().Add(5 * time.Second)
		for {
			info, e := m.inspect(ctx, u.Name)
			if e != nil {
				return e
			}
			if info.Exited {
				break
			}
			if time.Now().After(end) {
				if e = m.kill(ctx, u.Name); e != nil {
					return e
				}
				return fmt.Errorf("遗留检测程序尚未确认退出，请再次核查运行状态后重试")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		_ = m.call(ctx, "UnrefUnit", u.Name).Err
		_ = m.call(ctx, "ResetFailedUnit", u.Name).Err
	}
	return nil
}
func (r *Runner) Shutdown(ctx context.Context) error { return r.Recover(ctx) }
