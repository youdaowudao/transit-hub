package reports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrReportRandom = errors.New("生成本次日报发送编号失败，本次没有开始发送；请检查系统随机数服务后重试。")

type stepError struct {
	Step   string
	Object string
	Cause  error
}

func (e *stepError) Error() string {
	return "读取" + e.Step + "未完成；请检查本地资料后重试。"
}
func (e *stepError) Unwrap() error { return e.Cause }

type Service struct {
	repo     *Repository
	metrics  dailySnapshotReader
	sales    salesReader
	notifier *settings.Service
	store    *dailyStore
	apiOnly  bool
	now      func() time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	started  bool
	tasks    sync.WaitGroup
}

func NewService(repo *Repository, metrics dailySnapshotReader, sales salesReader, notifier *settings.Service, redisClient *redis.Client, apiOnly bool) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, metrics: metrics, sales: sales, notifier: notifier, store: &dailyStore{client: redisClient}, apiOnly: apiOnly, now: time.Now, ctx: ctx, cancel: cancel}
}

func (s *Service) config(ctx context.Context, scope Scope) (ReportConfiguration, []BotOption, string, error) {
	tx, err := s.repo.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ReportConfiguration{}, nil, "", err
	}
	defer tx.Rollback(ctx)
	if err := verifyParent(ctx, tx, scope); err != nil {
		return ReportConfiguration{}, nil, "", err
	}
	strategy, _, err := settings.DailyReportStrategy(ctx, tx, scope.UserID, scope.AdminAccountID, false)
	if err != nil {
		return ReportConfiguration{}, nil, "", err
	}
	raw := ReportConfiguration{Enabled: strategy.DailyReportEnabled, Time: strategy.DailyReportTime, BotIDs: strategy.DailyReportBotIDs}
	targets, err := s.notifier.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, false)
	if err != nil {
		// A broken target pauses sending, while the independently built local preview remains readable.
		return raw, []BotOption{}, publicReason(err), nil
	}
	options := []BotOption{}
	for _, target := range targets {
		if target.ID != "" {
			options = append(options, BotOption{ID: target.ID, Name: target.Name, Enabled: target.Enabled})
		}
	}
	cfg, err := settings.ParseDailyReportConfig(strategy)
	pause := ""
	if err != nil {
		pause = err.Error()
	} else if _, err := settings.SelectTelegramTarget(targets, cfg.BotIDs); err != nil {
		pause = err.Error()
	}
	return raw, options, pause, nil
}

func (s *Service) Preview(ctx context.Context, scope Scope) (Preview, error) {
	if s.metrics == nil || s.sales == nil || s.notifier == nil {
		return Preview{}, ErrUnavailable
	}
	if err := s.repo.Ready(ctx); err != nil {
		return Preview{}, err
	}
	if err := workspacescope.Resolve(ctx, s.repo.db, scope); err != nil {
		return Preview{}, err
	}
	now := s.now()
	document, err := s.build(ctx, scope, now)
	if err != nil {
		return Preview{}, err
	}
	cfg, bots, pause, err := s.config(ctx, scope)
	if err != nil {
		return Preview{}, &stepError{Step: "已保存日报策略与机器人", Object: scope.AdminAccountID, Cause: err}
	}
	result := Preview{Document: document, Configuration: cfg, Bots: bots, PauseReason: pause}
	if s.apiOnly {
		result.PauseReason = "当前实例不提供这项操作，请在正常服务执行。这里仍可只读预览。"
	}
	result.Scheduled, err = s.store.latest(ctx, scope, businesstime.DateAt(now))
	if err != nil {
		result.ScheduledWarning = publicReason(err) + "本次预览没有发送消息。"
	}
	if err := workspacescope.Resolve(ctx, s.repo.db, scope); err != nil {
		return Preview{}, err
	}
	return result, nil
}

func (s *Service) beginTest(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrStopped
	}
	s.tasks.Add(1)
	s.mu.Unlock()
	task, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return task, func() { stop(); cancel(); s.tasks.Done() }, nil
}

func (s *Service) Test(ctx context.Context, scope Scope) (TestResult, error) {
	if s.apiOnly {
		return TestResult{}, settings.ErrDailyReportWritesDisabled
	}
	task, finish, err := s.beginTest(ctx)
	if err != nil {
		return TestResult{}, err
	}
	defer finish()
	if s.metrics == nil || s.sales == nil || s.notifier == nil {
		return TestResult{}, ErrUnavailable
	}
	if err := s.repo.Ready(task); err != nil {
		return TestResult{}, err
	}
	if err := workspacescope.Resolve(task, s.repo.db, scope); err != nil {
		return TestResult{}, err
	}
	document, err := s.build(task, scope, s.now())
	if err != nil {
		return TestResult{}, err
	}
	prepared, err := s.prepare(task, scope, document, true, "")
	if err != nil {
		return TestResult{}, err
	}
	delivery := s.notifier.SendFrozenTelegramWithResult(task, prepared.Target, document.Text, settings.NotificationTemplateFormatText)
	result := TestResult{Results: []BotResult{{BotID: prepared.Target.ID, Name: prepared.Target.Name, Status: delivery.Status, Reason: delivery.Reason}}}
	if delivery.Attempted {
		result.Attempted = 1
	}
	switch delivery.Status {
	case settings.DeliverySent:
		result.Sent = 1
	case settings.DeliveryFailed:
		result.Failed = 1
	default:
		result.Unknown = 1
	}
	return result, nil
}

func newAttemptID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", ErrReportRandom
	}
	return hex.EncodeToString(value), nil
}

func (s *Service) failedRegistration(prepared preparedSend, reason string) {
	if prepared.Key == "" {
		return
	}
	marker := prepared.Marker
	marker.Status = settings.DeliveryFailed
	marker.Reason = reason
	marker.Attempted = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.replace(ctx, prepared.Key, prepared.Original, marker); err != nil {
		log.Printf("[reports] 日报尚未调用Telegram，登记结果记录未能写回；请核查Redis和本站日志")
	}
}

func (s *Service) prepare(ctx context.Context, scope Scope, document Document, manual bool, sendingDate string) (*preparedSend, error) {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := workspacescope.Lock(ctx, tx, scope, manual); err != nil {
		return nil, err
	}
	targets, err := s.notifier.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, true)
	if err != nil {
		return nil, err
	}
	strategy, exists, err := settings.DailyReportStrategy(ctx, tx, scope.UserID, scope.AdminAccountID, true)
	if err != nil {
		return nil, err
	}
	if !exists {
		if manual {
			return nil, &settings.TelegramTargetError{Reason: "尚未保存日报策略或机器人，本次没有发送；请先在通知策略中选择一个已保存的可用机器人并保存。"}
		}
		return nil, nil
	}
	ids, err := settings.DailyReportBotIDs(strategy)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !manual {
		cfg, err := settings.ParseDailyReportConfig(strategy)
		if err != nil {
			return nil, err
		}
		if !cfg.Enabled || businesstime.DateAt(now) != sendingDate || now.In(businesstime.Location()).Format("15:04") < cfg.Time || document.BusinessDate != shift(sendingDate, -1) {
			return nil, nil
		}
	}
	target, err := settings.SelectTelegramTarget(targets, ids)
	if err != nil {
		return nil, err
	}
	prepared := &preparedSend{Target: target}
	if !manual {
		id, err := newAttemptID()
		if err != nil {
			return nil, err
		}
		name := target.Name
		if name == "" {
			name = "已保存的Telegram机器人"
		}
		prepared.Key = reportKey(scope, sendingDate)
		prepared.Marker = ScheduledResult{AttemptID: id, SendingDate: sendingDate, BusinessDate: document.BusinessDate, At: now,
			BotID: target.ID, Name: name, Status: "sending", Reason: "日报已登记，发送结果尚未确认。"}
		raw, err := json.Marshal(prepared.Marker)
		if err != nil {
			return nil, err
		}
		prepared.Original = string(raw)
		saved, err := s.store.register(ctx, prepared.Key, prepared.Original)
		if err != nil {
			s.failedRegistration(*prepared, "日报登记结果尚未确认，本次没有调用Telegram；请核查Redis和本站日志，今天不自动重发，可在确认后手动测试。")
			return nil, err
		}
		if !saved {
			return nil, nil
		}
	}
	if err := workspacescope.Commit(ctx, tx); err != nil {
		s.failedRegistration(*prepared, "检查日报配置的事务结果未确认，本次没有调用Telegram；请先核查已保存策略和本站日志，今天不自动重发。")
		return nil, err
	}
	return prepared, nil
}

func (s *Service) runScheduled(ctx context.Context, scope Scope) error {
	if err := s.repo.Ready(ctx); err != nil {
		return err
	}
	strategy, exists, err := settings.DailyReportStrategy(ctx, s.repo.db, scope.UserID, scope.AdminAccountID, false)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	cfg, err := settings.ParseDailyReportConfig(strategy)
	if err != nil {
		return err
	}
	now := s.now()
	if !cfg.Enabled || now.In(businesstime.Location()).Format("15:04") < cfg.Time {
		return nil
	}
	document, err := s.build(ctx, scope, now)
	if err != nil {
		return err
	}
	prepared, err := s.prepare(ctx, scope, document, false, businesstime.DateAt(now))
	if err != nil || prepared == nil {
		return err
	}
	delivery := s.notifier.SendFrozenTelegramWithResult(ctx, prepared.Target, document.Text, settings.NotificationTemplateFormatText)
	marker := prepared.Marker
	marker.Status, marker.Reason, marker.Attempted = delivery.Status, delivery.Reason, delivery.Attempted
	resultCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = s.store.replace(resultCtx, prepared.Key, prepared.Original, marker)
	cancel()
	return err
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.apiOnly || s.started || s.closed || s.repo == nil || s.repo.db == nil || s.store == nil || s.store.client == nil {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.tasks.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.tasks.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				scopes, err := s.repo.ScheduledWorkspaces(s.ctx)
				if err != nil {
					log.Printf("[reports] 读取日报工作区未完成：%s", publicReason(err))
					continue
				}
				for _, scope := range scopes {
					if s.ctx.Err() != nil {
						return
					}
					if err := s.runScheduled(s.ctx, scope); err != nil && !errors.Is(err, workspacescope.ErrChanged) {
						log.Printf("[reports] 日报未完成 user_id=%s workspace_id=%s reason=%s", scope.UserID, scope.AdminAccountID, publicReason(err))
					}
				}
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.tasks.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
