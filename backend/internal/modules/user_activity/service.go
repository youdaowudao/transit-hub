package user_activity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/workspacescope"
)

type SessionReader interface {
	StoredSession(context.Context, string, string) (upstream.Session, bool, error)
}
type Platform interface {
	FetchSub2APIAdminUsersPageWithContext(context.Context, upstream.Session, upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error)
	FetchSub2APIUserUsageTrendContext(context.Context, upstream.Session, string, string, int) ([]upstream.Sub2APIUserUsageTrendPoint, error)
	FetchSub2APIActivityUsageStatsContext(context.Context, upstream.Session, string) (upstream.Sub2APIActivityUsageStats, error)
	FetchSub2APIUsedRedeemRecordsContext(context.Context, upstream.Session, string, int, int) (upstream.Sub2APIRedeemRecordsPage, error)
	FetchSub2APIAffiliateTransfersContext(context.Context, upstream.Session, string, string, int, int) (upstream.Sub2APIAffiliateTransfersPage, error)
}
type Service struct {
	repo     *Repository
	sessions SessionReader
	platform Platform
	apiOnly  bool
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   *Scope
	closed   bool
	wg       sync.WaitGroup
}

func NewService(repo *Repository, sessions SessionReader, platform Platform, apiOnly bool) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, sessions: sessions, platform: platform, apiOnly: apiOnly, ctx: ctx, cancel: cancel}
}

type businessError string

func (e businessError) Error() string { return string(e) }
func fail(reason string) error        { return businessError(reason) }
func safeReason(err error) string {
	var r *upstream.RequestError
	if errors.As(err, &r) {
		if r.StatusCode == http.StatusUnauthorized || r.MessageKey == upstream.ErrorAuth {
			return "主站登录资料已失效或无法使用，请到管理员账号入口重新连接"
		}
		reason := r.UpstreamMessage
		if reason == "" {
			reason = "主站未返回可核实的完整资料"
		}
		if r.StatusCode > 0 {
			reason += fmt.Sprintf("（主站HTTP %d）", r.StatusCode)
		}
		return reason
	}
	var b businessError
	if errors.As(err, &b) {
		return string(b)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "读取超过允许时间，请检查主站响应和网络后重试"
	}
	if errors.Is(err, context.Canceled) {
		return "服务已停止或读取已被取消"
	}
	if errors.Is(err, workspacescope.ErrChanged) {
		return workspacescope.ErrChanged.Error()
	}
	return "未取得完整结果，具体原因尚未确认；请检查本站服务日志、数据库连接和主站管理端"
}
func failure(step, object string, err error) string {
	var commit *workspacescope.CommitError
	if errors.As(err, &commit) {
		return "保存" + object + "的结果尚未确认：" + commit.Error() + "已核实部分可能已经更新，请先重新读取，确认当前资料后再操作。"
	}
	return "读取" + step + "/" + object + "时失败：" + safeReason(err) + "。已核实部分可能已经更新，失败部分保留原值；请核查主站管理端、管理员账号连接和本站服务日志后重新同步。"
}

const activityLock int64 = 0x55414354495649

func releaseLock(c *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ok bool
	if e := c.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, activityLock).Scan(&ok); e != nil || !ok {
		_ = c.Hijack().Close(ctx)
		return
	}
	c.Release()
}
func (s *Service) StartSync(ctx context.Context, scope Scope, current bool) (SyncState, error) {
	if s.apiOnly {
		return SyncState{}, fail("当前实例不提供这项操作，请在正常服务执行。")
	}
	if e := s.repo.Ready(ctx); e != nil {
		return SyncState{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SyncState{}, fail("服务正在退出，本次没有开始；请稍后重新同步。")
	}
	if s.active != nil {
		if *s.active == scope {
			return s.repo.Sync(ctx, scope)
		}
		return SyncState{}, ErrBusy
	}
	conn, e := s.repo.db.Acquire(ctx)
	if e != nil {
		return SyncState{}, e
	}
	var locked bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked); e != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(closeCtx)
		return SyncState{}, e
	}
	if !locked {
		st, readErr := readSync(ctx, conn, scope)
		conn.Release()
		if readErr == nil && st.Status == "running" {
			return st, nil
		}
		return SyncState{}, ErrBusy
	}
	if e = s.recoverInterrupted(ctx, conn); e != nil {
		releaseLock(conn)
		return SyncState{}, e
	}
	var previous, started SyncState
	e = workspacescope.Transaction(ctx, conn, scope, current, func(tx pgx.Tx) error {
		var e error
		previous, e = readSync(ctx, tx, scope)
		if e != nil {
			return e
		}
		started = previous
		now := time.Now()
		started.Status = "running"
		started.Error = ""
		started.Warnings = []string{}
		started.StartedAt = &now
		return writeSync(ctx, tx, scope, started)
	})
	if e != nil {
		releaseLock(conn)
		return SyncState{}, e
	}
	captured := scope
	s.active = &captured
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			releaseLock(conn)
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		}()
		taskCtx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		s.collect(taskCtx, conn, captured, previous, started)
	}()
	return started, nil
}

// With the global lock held, no prior running state can still belong to an active collector.
func (s *Service) recoverInterrupted(ctx context.Context, conn *pgxpool.Conn) error {
	rows, e := conn.Query(ctx, `SELECT s.user_id,
        s.admin_account_id,
        a.created_at
        FROM user_activity_sync_states s JOIN admin_accounts a ON a.user_id=s.user_id
        AND a.id=s.admin_account_id
        WHERE s.status='running'
        ORDER BY s.admin_account_id,
        s.user_id`)
	if e != nil {
		return e
	}
	scopes := []Scope{}
	for rows.Next() {
		var scope Scope
		var created time.Time
		if e = rows.Scan(&scope.UserID, &scope.AdminAccountID, &created); e != nil {
			rows.Close()
			return e
		}
		scope.CreatedAt = created.Format(time.RFC3339Nano)
		scopes = append(scopes, scope)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, scope := range scopes {
		e = publish(ctx, conn, scope, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE user_activity_sync_states
        SET status='failed',
        error_detail=$3
        WHERE user_id=$1
        AND admin_account_id=$2
        AND status='running'`, scope.UserID, scope.AdminAccountID, "上次读取因服务中断未完成，已核实资料仍保留；请重新同步。")
			return e
		})
		if e != nil && !errors.Is(e, workspacescope.ErrChanged) {
			return e
		}
	}
	return nil
}
func (s *Service) Start() error {
	if s.apiOnly || s.repo.db == nil {
		return nil
	}
	// Recover interrupted registrations only after confirming the global lock is free.
	recoveryCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	conn, err := s.repo.db.Acquire(recoveryCtx)
	if err != nil {
		return fail("恢复用户分析读取状态失败：" + safeReason(err))
	}
	var locked bool
	err = conn.QueryRow(recoveryCtx, `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked)
	if err != nil {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		_ = conn.Hijack().Close(closeCtx)
		return fail("确认上次用户分析读取是否中断时失败：" + safeReason(err))
	}
	if locked {
		err = s.recoverInterrupted(recoveryCtx, conn)
		releaseLock(conn)
		if err != nil {
			return fail("恢复用户分析读取状态失败：" + safeReason(err))
		}
	} else {
		conn.Release()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				if scopes, e := s.repo.Due(s.ctx, false); e == nil {
					for _, scope := range scopes {
						if _, e = s.StartSync(s.ctx, scope, false); e == nil || errors.Is(e, ErrBusy) {
							break
						}
					}
				}
			}
		}
	}()
	return nil
}
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type pacedReader struct{ last time.Time }

func (p *pacedReader) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay := time.Until(p.last.Add(150 * time.Millisecond))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.last = time.Now()
	return nil
}
func call[T any](ctx context.Context, p *pacedReader, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if e := p.wait(ctx); e != nil {
		return zero, e
	}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return fn(requestCtx)
}
func joinWarnings(st *SyncState, warning string) {
	if warning != "" && !strings.Contains(strings.Join(st.Warnings, "\n"), warning) {
		st.Warnings = append(st.Warnings, warning)
	}
}
