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
}
type Runner struct {
	cfg       RunnerConfig
	querySlot chan struct{}
}

func NewRunner(cfg RunnerConfig) *Runner { return &Runner{cfg: cfg, querySlot: make(chan struct{}, 1)} }
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
	m.conn.Close()
	return nil
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
}

func (p *process) info() ExitInfo { p.mu.Lock(); defer p.mu.Unlock(); return p.exit }
func (p *process) setExit(info ExitInfo, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exit.Exited && !info.Exited {
		return
	}
	if info.TaskMemoryMaxBytes == 0 {
		info.TaskMemoryMaxBytes = p.exit.TaskMemoryMaxBytes
		info.SliceMemoryMaxBytes = p.exit.SliceMemoryMaxBytes
		info.SwapMaxBytes = p.exit.SwapMaxBytes
	}
	p.exit = info
	p.exitErr = err
}
func (p *process) stop(ctx context.Context) error {
	if p.info().Exited {
		return nil
	}
	return p.manager.stop(ctx, p.name)
}
func (p *process) stopAndWait() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.manager.stop(ctx, p.name)
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		info, err := p.manager.inspect(ctx, p.name)
		if err == nil && info.Exited {
			p.setExit(info, nil)
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer killCancel()
	_ = p.manager.kill(killCtx, p.name)
	until = time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		info, err := p.manager.inspect(killCtx, p.name)
		if err == nil && info.Exited {
			p.setExit(info, nil)
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	p.setExit(ExitInfo{}, fmt.Errorf("停止后仍无法确认整个检测进程组已退出，请核查systemd运行诊断"))
}
func (p *process) monitor(ctx context.Context) {
	defer close(p.done)
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
	p.release.Do(func() {
		if p.monitored {
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				p.stopAndWait()
				<-p.done
			}
		} else if !p.info().Exited {
			p.stopAndWait()
		}
		p.input.Close()
		p.output.Close()
		p.stderr.Close()
		if err := p.manager.release(p.name); err != nil {
			p.mu.Lock()
			p.exitErr = err
			p.mu.Unlock()
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
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
		m.conn.Close()
		return nil, err
	}
	name := "transithub-chanshui-" + role + "-" + hex.EncodeToString(nonce[:]) + ".service"
	inR, inW, err := os.Pipe()
	if err != nil {
		m.conn.Close()
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		m.conn.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		m.conn.Close()
		return nil, err
	}
	p := &process{manager: m, name: name, input: inW, output: outR, stderr: errR, reader: bufio.NewReaderSize(outR, 4096), done: make(chan struct{})}
	err = m.start(ctx, name, role, r.cfg, limit, seconds, inR, outW, errW)
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		p.close()
		return nil, problem(503, "startup", err.Error()+"；尚未向上游发送检测请求")
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
	env.Deployment = Deployment{r.cfg.AllowPrivateTargets}
	encoded, err := json.Marshal(env)
	if err != nil || len(encoded) > 128*1024 {
		p.close()
		return nil, problem(422, "input", "检测参数超过128KiB或无法编码，请减少选择后重试")
	}
	if _, err = p.input.Write(append(encoded, '\n')); err != nil {
		p.close()
		return nil, problem(503, "handshake", "无法交付检测启动参数，尚未发送模型请求；请检查程序版本后重试")
	}
	_ = p.output.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(p.reader)
	if err != nil {
		p.close()
		return nil, problem(503, "handshake", "检测程序在10秒内未完成启动核验，尚未发送模型请求；请检查程序版本、内存限制和运行诊断")
	}
	var ready struct {
		V      int                                         `json:"v"`
		Event  string                                      `json:"event"`
		Limits struct{ TaskMax, SliceMax, SwapMax uint64 } `json:"limits"`
	}
	if json.Unmarshal(line, &ready) != nil || ready.V != 1 || ready.Event != "ready" || ready.Limits.TaskMax == 0 || ready.Limits.TaskMax > limit || ready.Limits.SliceMax == 0 || ready.Limits.SliceMax > MemoryCeiling || ready.Limits.SwapMax != 0 {
		p.close()
		return nil, problem(503, "limits", "检测程序没有确认有效硬内存限制，已拒绝交付密钥；尚未发送模型请求。请核对slice及程序版本")
	}
	p.setExit(ExitInfo{TaskMemoryMaxBytes: ready.Limits.TaskMax, SliceMemoryMaxBytes: ready.Limits.SliceMax, SwapMaxBytes: ready.Limits.SwapMax}, nil)
	_ = p.output.SetReadDeadline(time.Time{})
	return p, nil
}
func continueProcess(p *process, cred *Credential) error {
	wire := struct {
		V          int         `json:"v"`
		Command    string      `json:"command"`
		Credential *Credential `json:"credential,omitempty"`
	}{1, "continue", cred}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	if len(encoded) > 16*1024 {
		return fmt.Errorf("账号凭据超过16KiB，请检查主站账号配置")
	}
	_, err = p.input.Write(append(encoded, '\n'))
	p.input.Close()
	return err
}

func (r *Runner) Query(ctx context.Context, env Envelope, cred *Credential, internal bool) (*QueryResponse, error) {
	if internal {
		wait, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		select {
		case r.querySlot <- struct{}{}:
		case <-wait.Done():
			return nil, problem(503, "recovery", "保存检测结束状态时等待其他查询超时，请稍后点击停止检测以重试保存；不会重新调用模型")
		}
	} else {
		select {
		case r.querySlot <- struct{}{}:
		default:
			return nil, problem(429, "query", "另一个检测查询正在处理，请稍后重试；尚未重复执行本次操作")
		}
	}
	release := func() { <-r.querySlot }
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	p, err := r.launch(queryCtx, "query", env, 64*1024*1024, 15)
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	if err = continueProcess(p, cred); err != nil {
		p.close()
		cancel()
		release()
		e := problem(503, "query", "交付查询参数时连接中断，请重新读取结果；尚未重新开始检测")
		if cred != nil {
			e.OperationResult = "unknown"
		}
		return nil, e
	}
	p.monitored = true
	go p.monitor(queryCtx)
	_ = p.output.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := readLine(p.reader)
	if err != nil {
		cancel()
		<-p.done
		p.close()
		release()
		return nil, problem(503, "query", "读取检测数据失败，程序已停止；请检查磁盘权限、可用空间及运行诊断后重试")
	}
	var header wireHeader
	if json.Unmarshal(line, &header) != nil || header.V != 1 || header.Length < 0 || header.Length > 2*1024*1024 {
		cancel()
		<-p.done
		p.close()
		release()
		return nil, problem(503, "query", "检测程序返回的数据格式或大小不符合要求，请核对安装包版本后重试")
	}
	if !header.OK {
		cancel()
		<-p.done
		p.close()
		release()
		e := problem(header.Status, "query", "检测数据读取或保存未完成，请检查设置后重试")
		var detail Problem
		if json.Unmarshal(header.Error, &detail) == nil && detail.Message != "" {
			e = &detail
			e.Status = header.Status
		}
		if e.Status < 400 {
			e.Status = 503
		}
		return nil, e
	}
	if header.Status == 0 {
		header.Status = 200
	}
	if header.ContentType == "" {
		header.ContentType = "application/json"
	}
	return &QueryResponse{header.Status, header.ContentType, header.Length, &queryBody{reader: p.reader, left: header.Length, p: p, cancel: cancel, release: release}}, nil
}

type queryBody struct {
	reader  *bufio.Reader
	left    int64
	p       *process
	cancel  context.CancelFunc
	release func()
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
			<-b.p.done
		}
		b.err = b.p.close()
		b.cancel()
		b.release()
	})
	return b.err
}

func (r *Runner) Start(ctx context.Context, env Envelope, cred Credential, memoryMiB, totalSeconds int) (*Job, error) {
	if memoryMiB < 128 || memoryMiB > 384 || totalSeconds < 60 || totalSeconds > 1800 {
		return nil, problem(422, "settings", "任务内存须为128～384MiB，总时限须为60～1800秒；请修改运行设置后重试")
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := r.launch(startup, "run", env, uint64(memoryMiB)*1024*1024, totalSeconds)
	if err != nil {
		return nil, err
	}
	runCtx, runCancel := context.WithTimeout(context.Background(), time.Duration(totalSeconds+10)*time.Second)
	p.monitored = true
	go func() { p.monitor(runCtx); runCancel() }()
	job := &Job{ID: p.name, Done: p.done, stop: p.stop, info: p.info}
	if err = continueProcess(p, &cred); err != nil {
		p.stopAndWait()
		p.close()
		e := problem(503, "accept", "交付账号信息时连接中断，无法确认请求是否已开始；部分请求可能已计费，请查询同一报告，不要重复新建")
		e.OperationResult = "unknown"
		return job, e
	}
	_ = p.output.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(p.reader)
	var event struct {
		V     int    `json:"v"`
		Event string `json:"event"`
	}
	if err != nil || json.Unmarshal(line, &event) != nil || event.Event != "accepted" {
		p.stopAndWait()
		p.close()
		e := problem(503, "accept", "未能确认检测受理结果，已请求停止；部分请求可能已计费，请查询此报告，不要重复新建")
		e.OperationResult = "unknown"
		return job, e
	}
	_ = p.output.SetReadDeadline(time.Time{})
	go func() {
		defer p.close()
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

func (r *Runner) Recover(ctx context.Context) error {
	m, err := connectManager(ctx, r.cfg)
	if err != nil {
		return err
	}
	defer m.conn.Close()
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
