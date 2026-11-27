package my_sites

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

const accountCheckMarkerTTL = 48 * time.Hour

type accountCheckConfigReader interface {
	ListMainAccountCheckWorkspaces(context.Context) ([]settings.MainAccountCheckWorkspace, error)
	ReadMainAccountCheckConfig(context.Context, workspacescope.Scope, bool) (settings.MainAccountCheckWorkspace, error)
}
type accountCheckMarkers interface {
	claim(context.Context, string, []byte) (bool, error)
	complete(context.Context, string, []byte, []byte) error
	read(context.Context, []string) ([]any, error)
}
type redisAccountCheckMarkers struct{ client *redis.Client }

func (s redisAccountCheckMarkers) claim(ctx context.Context, key string, raw []byte) (bool, error) {
	if s.client == nil {
		return false, errors.New("Redis unavailable")
	}
	return s.client.SetNX(ctx, key, raw, accountCheckMarkerTTL).Result()
}

const finishAccountCheckMarker = `if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end local ttl=redis.call('PTTL',KEYS[1]) if ttl<=0 then return 0 end redis.call('PSETEX',KEYS[1],ttl,ARGV[2]) return 1`

func (s redisAccountCheckMarkers) complete(ctx context.Context, key string, original, raw []byte) error {
	if s.client == nil {
		return errors.New("Redis unavailable")
	}
	n, err := s.client.Eval(ctx, finishAccountCheckMarker, []string{key}, original, raw).Int()
	if err == nil && n != 1 {
		return errors.New("marker changed")
	}
	return err
}
func (s redisAccountCheckMarkers) read(ctx context.Context, keys []string) ([]any, error) {
	if s.client == nil {
		return nil, errors.New("Redis unavailable")
	}
	return s.client.MGet(ctx, keys...).Result()
}

type scheduledAccountCheck struct {
	scope     workspacescope.Scope
	scheduled time.Time
	slot      int
}

func accountCheckKey(scope workspacescope.Scope, day string, slot int) string {
	raw, _ := json.Marshal([]string{scope.UserID, scope.AdminAccountID, scope.CreatedAt})
	return fmt.Sprintf("my-sites:account-check:%s:%s:%d", base64.RawURLEncoding.EncodeToString(raw), day, slot+1)
}

// InventorySchedule keeps minute collection separate from serial dispatch. It
// holds only slots observed by the running service, never historical backlog.
// The backend DB remains the sole source of the configuration and connection state.
type InventorySchedule struct {
	configs accountCheckConfigReader
	markers accountCheckMarkers
	check   func(context.Context, workspacescope.Scope, bool, string) (ScopedCheckResult, error)
	now     func() time.Time
	mu      sync.Mutex
	queue   []scheduledAccountCheck
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewInventorySchedule(configs *settings.Service, sites *Service, redisClient *redis.Client) *InventorySchedule {
	return &InventorySchedule{configs: configs, markers: redisAccountCheckMarkers{redisClient}, check: sites.checkConnectionInventoryForWorkspace, now: time.Now, wake: make(chan struct{}, 1)}
}
func (s *InventorySchedule) collect(ctx context.Context, minute time.Time) {
	minute = minute.In(businesstime.Location()).Truncate(time.Minute)
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	items, err := s.configs.ListMainAccountCheckWorkspaces(readCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Print("[main-account-check] 未能读取定时核对设置，请检查本地数据库；本分钟未请求主站")
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	var due []scheduledAccountCheck
	for _, item := range items {
		if item.ConfigError != "" || !item.Config.Enabled {
			continue
		}
		for slot, v := range item.Config.Times {
			if v == minute.Format("15:04") {
				due = append(due, scheduledAccountCheck{item.Scope, minute, slot})
			}
		}
	}
	if len(due) == 0 {
		return
	}
	s.mu.Lock()
	s.queue = append(s.queue, due...)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *InventorySchedule) pop() (scheduledAccountCheck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return scheduledAccountCheck{}, false
	}
	job := s.queue[0]
	s.queue[0] = scheduledAccountCheck{}
	s.queue = s.queue[1:]
	return job, true
}
func (s *InventorySchedule) run(ctx context.Context, job scheduledAccountCheck) {
	if ctx.Err() != nil || s.now().Before(job.scheduled) || businesstime.DateAt(s.now()) != businesstime.DateAt(job.scheduled) {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	item, err := s.configs.ReadMainAccountCheckConfig(readCtx, job.scope, false)
	cancel()
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, workspacescope.ErrChanged) {
			log.Print("[main-account-check] 未能读取到点工作区的最新设置，本次未请求主站")
		}
		return
	}
	if item.ConfigError != "" || !item.Config.Enabled || job.slot >= len(item.Config.Times) || item.Config.Times[job.slot] != job.scheduled.In(businesstime.Location()).Format("15:04") || ctx.Err() != nil || s.now().Before(job.scheduled) || businesstime.DateAt(s.now()) != businesstime.DateAt(job.scheduled) {
		return
	}
	key := accountCheckKey(job.scope, businesstime.DateAt(job.scheduled), job.slot)
	record := settings.MainAccountCheckRecord{ScheduledAt: job.scheduled, StartedAt: s.now(), Status: "running"}
	original, _ := json.Marshal(record)
	claimCtx, claimCancel := context.WithTimeout(ctx, 5*time.Second)
	claimed, err := s.markers.claim(claimCtx, key, original)
	claimCancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Print("[main-account-check] Redis登记定时核对失败，本次未请求主站，请检查Redis连接")
		}
		return
	}
	if !claimed {
		return
	}
	if ctx.Err() != nil {
		return
	}
	result, checkErr := s.check(ctx, job.scope, false, "")
	finished := s.now()
	record.FinishedAt = &finished
	for _, status := range result.ConfirmedStatuses {
		switch status {
		case ConnectionStatusActive:
			record.ConfirmedActive++
		case ConnectionStatusMissing:
			record.ConfirmedMissing++
		}
	}
	record.Unconfirmed = len(result.UnconfirmedConnectionIDs) + len(result.ChangedConnectionIDs)
	record.Skipped = len(result.SkippedConnectionIDs)
	confirmed := record.ConfirmedActive + record.ConfirmedMissing
	record.Status = "success"
	if checkErr != nil {
		record.Status = "failed"
		var known *scopedRequestError
		if errors.As(checkErr, &known) {
			record.Reason = known.Message + "：" + known.Detail
			record.Next = known.Next
			if strings.Contains(known.Message, "正在核对主站账号") {
				record.Status = "skipped"
			}
		} else {
			record.Reason = "Hub未能确认本次核对结果，请检查Hub后端记录。"
			record.Next = "请手动核对。"
		}
	} else if record.Unconfirmed > 0 || result.StorageUncertain {
		record.Status = "failed"
		if confirmed > 0 {
			record.Status = "partial"
		}
	} else if confirmed == 0 {
		record.Status = "skipped"
		record.Reason = "当前没有可核对的主站账号对接，本次未更新账号状态。"
		record.Next = "无需额外操作；需要时手动核对。"
	} else if record.Skipped > 0 {
		record.Status = "partial"
	}
	// Only store a safe summary, not the per-account names/IDs, session or inventory.
	if checkErr == nil && len(result.Issues) > 0 {
		for _, issue := range result.Issues {
			if issue.ConnectionID == "" {
				record.Reason = issue.Detail
				record.Next = issue.Next
				break
			}
		}
		if record.Reason == "" {
			record.Reason = "部分对接未能确认或不适用，仍保留原状态。"
			record.Next = "请手动核对，查看具体账号及原因。"
		}
	}
	raw, _ := json.Marshal(record)
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err = s.markers.complete(saveCtx, key, original, raw)
	saveCancel()
	if err != nil {
		log.Print("[main-account-check] 本次定时核对记录未能保存，执行结果尚未确认；不会自动重跑")
	}
}
func (s *InventorySchedule) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			now := s.now()
			next := now.Truncate(time.Minute).Add(time.Minute)
			timer := time.NewTimer(next.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				actual := s.now()
				if actual.Truncate(time.Minute).Equal(next) {
					s.collect(ctx, actual)
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				for ctx.Err() == nil {
					job, ok := s.pop()
					if !ok {
						break
					}
					s.run(ctx, job)
				}
			}
		}
	}()
	go func() { workers.Wait(); s.mu.Lock(); s.queue = nil; s.mu.Unlock(); close(done) }()
}
func (s *InventorySchedule) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *InventorySchedule) RecentMainAccountCheck(ctx context.Context, scope workspacescope.Scope) (*settings.MainAccountCheckRecord, error) {
	now := s.now().In(businesstime.Location())
	keys := make([]string, 0, 8)
	for day := 0; day < 2; day++ {
		for slot := 0; slot < 4; slot++ {
			keys = append(keys, accountCheckKey(scope, businesstime.DateAt(now.AddDate(0, 0, -day)), slot))
		}
	}
	values, err := s.markers.read(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(values) != len(keys) {
		return nil, errors.New("incomplete recent record")
	}
	var latest *settings.MainAccountCheckRecord
	for i, value := range values {
		if value == nil {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid record")
		}
		var record settings.MainAccountCheckRecord
		if json.Unmarshal([]byte(raw), &record) != nil || record.ScheduledAt.IsZero() || record.StartedAt.IsZero() || record.StartedAt.Before(record.ScheduledAt) || businesstime.DateAt(record.ScheduledAt) != businesstime.DateAt(now.AddDate(0, 0, -i/4)) || record.ConfirmedActive < 0 || record.ConfirmedMissing < 0 || record.Unconfirmed < 0 || record.Skipped < 0 {
			return nil, errors.New("invalid record")
		}
		switch record.Status {
		case "running":
			if now.Sub(record.StartedAt) > 60*time.Second {
				record.Status = "unknown"
				record.Reason = "上次核对结果尚未确认，请手动核对，不会自动重跑。"
				record.Next = "请手动核对。"
			}
		case "success", "partial", "failed", "skipped":
			if record.FinishedAt == nil || record.FinishedAt.Before(record.StartedAt) || (record.Status == "success" && (record.ConfirmedActive+record.ConfirmedMissing == 0 || record.Unconfirmed != 0 || record.Skipped != 0)) {
				return nil, errors.New("incomplete record")
			}
		default:
			return nil, errors.New("invalid record status")
		}
		if latest == nil || record.StartedAt.After(latest.StartedAt) {
			latest = &record
		}
	}
	return latest, nil
}
