package user_activity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func authenticationFailure(err error) bool {
	return authenticationError(err) != nil
}

func authenticationError(err error) *upstream.RequestError {
	if r, ok := err.(*upstream.RequestError); ok && (r.StatusCode == http.StatusUnauthorized || (r.StatusCode == 0 && r.MessageKey == upstream.ErrorAuth)) {
		return r
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if r := authenticationError(cause); r != nil {
				return r
			}
		}
		return nil
	}
	if cause := errors.Unwrap(err); cause != nil {
		return authenticationError(cause)
	}
	return nil
}

func requestReason(r *upstream.RequestError) string {
	if r.UpstreamMessage != "" {
		return r.UpstreamMessage
	}
	key := r.Reason
	if key == "" {
		key = r.MessageKey
	}
	switch key {
	case upstream.ErrorInvalidURL:
		return "网站地址无效，请检查后重试。"
	case upstream.ErrorInvalidResponse:
		return "主站返回内容无法解析或读取不完整；请检查主站管理端返回内容、网络连接和本站服务日志后重试。"
	case upstream.ErrorNetwork:
		return "网络请求未完成，具体原因暂时无法确认；请检查主站地址与网络连接后重试。"
	case upstream.ErrorRequest:
		return "主站请求未成功，具体原因暂时无法确认；请核查主站管理端和本站服务日志后重试。"
	case upstream.ErrorNetworkTimeout:
		return "连接上游超时。"
	case upstream.ErrorNetworkUnreachable:
		return "无法连接上游地址，请检查域名和端口。"
	case upstream.ErrorTLSFailed:
		return "与上游建立 HTTPS 连接失败。"
	case upstream.ErrorForbidden:
		return "主站拒绝访问，具体原因暂时无法确认；请在主站管理端核查访问记录后重试。"
	case upstream.ErrorNotFound:
		return "主站未找到请求的资料或接口；请核对主站地址和请求对象，并查看本站服务日志后重试。"
	case upstream.ErrorRateLimited:
		return "上游请求过于频繁（429），请稍后再试。"
	case upstream.ErrorUpstreamServer:
		return "上游服务异常（5xx），请稍后再试。"
	case upstream.ErrorAnnouncementAckRequired:
		return "上游要求先在网页上确认新公告，确认前无法读取数据。"
	case upstream.ErrorUpstreamInsufficientBalance:
		return "上游账户余额不足，上游不允许读取。"
	case upstream.ErrorUpstreamKeyQuotaExhausted:
		return "该 Key 在上游的额度已用尽。"
	case upstream.ErrorUpstreamKeyExpired:
		return "该 Key 在上游已过期。"
	default:
		return "主站未提供具体原因，暂时无法确认"
	}
}

func safeReason(err error) string {
	auth := authenticationError(err)
	r := auth
	if r != nil || errors.As(err, &r) {
		reason := requestReason(r)
		if r.StatusCode > 0 {
			reason += fmt.Sprintf("（主站HTTP %d）", r.StatusCode)
		}
		if auth != nil {
			reason = "主站认证未通过：" + reason + "；请到管理员账号入口重新连接后重试"
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

func logStorageFailure(operation string, err error) {
	var database *pgconn.PgError
	if errors.As(err, &database) {
		// Analysis-state SQL never reads credential columns. Retain the server's
		// actual cause, excluding query details and client connection strings.
		log.Printf("[user-activity] %s：%s（SQLSTATE %s）", operation, database.Message, database.Code)
		return
	}
	log.Printf("[user-activity] %s：%s（错误类型 %T）", operation, safeReason(err), err)
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
			st, err := s.repo.Sync(ctx, scope)
			if err != nil {
				return SyncState{}, err
			}
			if current {
				if err := workspacescope.Resolve(ctx, s.repo.db, scope); err != nil {
					return SyncState{}, err
				}
			}
			return st, nil
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
			if current {
				if err := workspacescope.Resolve(ctx, s.repo.db, scope); err != nil {
					return SyncState{}, err
				}
			}
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
        AND status='running'`, scope.UserID, scope.AdminAccountID, "上次读取的完成状态未能保存，或读取已经中断，具体原因请查看本站服务日志；已核实资料仍保留，请核查后重新同步。")
			return e
		})
		if e != nil && !errors.Is(e, workspacescope.ErrChanged) {
			return e
		}
	}
	return nil
}

// Recovery shares the collector's global lock, including across service instances.
func (s *Service) recoverIdle(ctx context.Context) error {
	conn, err := s.repo.db.Acquire(ctx)
	if err != nil {
		return err
	}
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked)
	if err != nil {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		_ = conn.Hijack().Close(closeCtx)
		return err
	}
	if !locked {
		conn.Release()
		return nil
	}
	defer releaseLock(conn)
	return s.recoverInterrupted(ctx, conn)
}

func (s *Service) Start() error {
	if s.apiOnly || s.repo.db == nil {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	if err := s.recoverIdle(recoveryCtx); err != nil {
		logStorageFailure("恢复用户分析读取状态失败", err)
		return fail("恢复用户分析读取状态失败：" + safeReason(err))
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
				s.runScheduled(s.ctx)
			}
		}
	}()
	return nil
}

// runScheduled is the existing minute check, extracted so failure recovery can
// be verified without starting timers in storage tests.
func (s *Service) runScheduled(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	running, err := s.repo.Due(ctx, true)
	if err != nil {
		logStorageFailure("检查未保存的用户分析完成状态失败", err)
		return
	}
	if len(running) > 0 {
		if err := s.recoverIdle(ctx); err != nil {
			logStorageFailure("恢复用户分析读取状态失败，将在下次检查重试", err)
			return
		}
	}
	// Recovery does not change last_started_at or start a new collection. The
	// existing hourly eligibility check still decides when another read may run.
	if scopes, e := s.repo.Due(ctx, false); e == nil {
		for _, scope := range scopes {
			if _, e = s.StartSync(ctx, scope, false); e == nil || errors.Is(e, ErrBusy) {
				break
			}
		}
	}
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
