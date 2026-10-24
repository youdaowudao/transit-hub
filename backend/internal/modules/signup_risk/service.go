// Package signup_risk only reads existing Sub2API administrator connections.
// The notifications module owns all local settings, processed IDs and facts.
package signup_risk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

type Repository interface {
	ListSignupRiskTargets(context.Context) ([]notifications.SignupRiskTarget, error)
	ProcessedSignupRiskIDs(context.Context, notifications.SignupRiskTarget, []string) (map[string]bool, error)
	CommitSignupRiskRound(context.Context, notifications.SignupRiskTarget, []notifications.SignupRiskUser, time.Time) error
}

type SessionReader interface {
	StoredSession(context.Context, string, string) (upstream.Session, bool, error)
}

type UsersClient interface {
	FetchSub2APIAdminUsersPageWithContext(context.Context, upstream.Session, upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error)
}

type Service struct {
	repository  Repository
	sessions    SessionReader
	users       UsersClient
	runMu       sync.Mutex
	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewService(repository Repository, sessions SessionReader, users UsersClient) *Service {
	return &Service{repository: repository, sessions: sessions, users: users}
}

// RunOnce is explicit and never starts timers or listeners. Independent targets
// start together; a slow connection cannot postpone another site's first page.
func (s *Service) RunOnce(ctx context.Context) error {
	if !s.runMu.TryLock() {
		return nil
	}
	defer s.runMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	targets, err := s.repository.ListSignupRiskTargets(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var errorMu sync.Mutex
	var failures []error
	for _, target := range targets {
		wg.Add(1)
		go func(target notifications.SignupRiskTarget) {
			defer wg.Done()
			if err := s.pollTarget(ctx, target); err != nil {
				errorMu.Lock()
				failures = append(failures, fmt.Errorf("工作区 %s 的高风险注册检查未完成：%w", target.AdminAccountID, err))
				errorMu.Unlock()
			}
		}(target)
	}
	wg.Wait()
	return errors.Join(failures...)
}

func (s *Service) pollTarget(ctx context.Context, target notifications.SignupRiskTarget) error {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	session, exists, err := s.sessions.StoredSession(ctx, target.UserID, target.AdminAccountID)
	if err != nil {
		return fmt.Errorf("读取已保存的管理员连接失败：%s；本轮没有登记或通知，请核查该工作区的管理员连接，下分钟重试", notifications.SafeStorageCause(err))
	}
	if !exists || session.Platform != upstream.PlatformSub2API || !session.IsAuthenticated() || notifications.SignupRiskSiteKey(session.BaseURL) != notifications.SignupRiskSiteKey(target.BaseURL) {
		return errors.New("没有找到与这个站点一致的 Sub2API 管理员连接；本轮没有请求用户或生成消息，请重新确认该工作区已保存的管理员连接，下分钟重试")
	}
	var candidates []notifications.SignupRiskUser
	seen := make(map[string]bool)
	var stopAt *time.Time
	var previous *time.Time
	discovered := time.Now().UTC()
	includeSubscriptions := false
	for page := 1; ; page++ {
		pageCtx, pageCancel := context.WithTimeout(ctx, 10*time.Second)
		result, fetchErr := s.users.FetchSub2APIAdminUsersPageWithContext(pageCtx, session, upstream.Sub2APIAdminUsersQuery{
			Page: page, PageSize: 50, Search: notifications.SignupRiskMarker,
			IncludeSubscriptions: &includeSubscriptions, StrictPagination: true,
		})
		pageCancel()
		if fetchErr != nil {
			return pollFailure(page, fetchErr)
		}
		if !result.TotalKnown || !result.PagesKnown || result.Page != page || result.PageSize != 50 || result.Pages < 1 || len(result.Items) > 50 {
			return pollFailure(page, &upstream.RequestError{MessageKey: upstream.ErrorInvalidResponse})
		}
		ids := make([]string, 0, len(result.Items))
		for index, user := range result.Items {
			if strings.Contains(user.Notes, notifications.SignupRiskMarker) {
				missing := ""
				switch {
				case user.ID == "":
					missing = "用户编号缺失"
				case strings.TrimSpace(user.Email) == "":
					missing = "注册邮箱为空"
				case user.CreatedAt == nil || user.CreatedAt.IsZero():
					missing = "注册时间缺失"
				}
				if missing != "" {
					return fmt.Errorf("读取用户列表第%d页后发现第%d条风险用户的%s；本轮没有登记或通知，请核查 Sub2API 管理员用户列表，下分钟从第一页重试", page, index+1, missing)
				}
				ids = append(ids, user.ID)
			}
		}
		processed, err := s.repository.ProcessedSignupRiskIDs(ctx, target, ids)
		if err != nil {
			return err
		}
		stop := false
		newOnPage := 0
		for _, user := range result.Items {
			if user.CreatedAt != nil {
				// Offset paging may repeat rows after new registrations. Only
				// reject out-of-order fresh rows, not such harmless repeats.
				if previous != nil && user.CreatedAt.After(*previous) && !seen[user.ID] {
					return fmt.Errorf("第%d页用户未按注册时间从新到旧返回；本轮没有登记或通知，请核查 Sub2API 列表排序，下分钟从第一页重试", page)
				}
				previous = user.CreatedAt
				if stopAt != nil && user.CreatedAt.Before(*stopAt) {
					stop = true
					break
				}
			}
			if seen[user.ID] {
				continue
			}
			seen[user.ID] = true
			newOnPage++
			if !strings.Contains(user.Notes, notifications.SignupRiskMarker) {
				continue
			}
			if target.Initialized && processed[user.ID] {
				if stopAt == nil || user.CreatedAt.After(*stopAt) {
					instant := *user.CreatedAt
					stopAt = &instant
				}
				continue
			}
			candidates = append(candidates, notifications.SignupRiskUser{ID: user.ID, Email: user.Email, CreatedAt: *user.CreatedAt})
		}
		// Count and offset reads are separate upstream queries. A full
		// advertised last page may have had its tail shifted by new signups;
		// confirm a short/empty end page before advancing processed IDs.
		if stop || page >= result.Pages && len(result.Items) < 50 {
			break
		}
		if len(result.Items) == 0 || newOnPage == 0 {
			return fmt.Errorf("用户列表第%d页未返回可继续核查的用户，后续页是否完整无法确认；本轮没有登记或通知，请核查 Sub2API 分页，下分钟从第一页重试", page)
		}
	}
	return s.repository.CommitSignupRiskRound(ctx, target, candidates, discovered)
}

func pollFailure(page int, err error) error {
	cause := "原因尚无法确认，请维护人员核查 Sub2API 管理员用户列表接口"
	var remote *upstream.RequestError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cause = "读取等待超时，请检查站点是否可访问"
	case errors.Is(err, context.Canceled):
		cause = "本次检查已取消"
	case errors.As(err, &remote):
		switch remote.MessageKey {
		case upstream.ErrorInvalidResponse:
			cause = "站点返回的用户列表或分页格式不完整，请维护人员核查接口返回字段"
		case upstream.ErrorAuth:
			cause = "站点未接受已保存的管理员身份，请重新确认该工作区的管理员连接"
		case upstream.ErrorNetwork:
			cause = "未取得站点响应，请检查网络和站点可访问性"
		}
		if remote.UpstreamMessage != "" {
			cause += "；返回详情：" + remote.UpstreamMessage
		}
		if remote.StatusCode != 0 {
			cause += fmt.Sprintf("（HTTP %d）", remote.StatusCode)
		}
	}
	return fmt.Errorf("读取用户列表第%d页失败：%s；本轮没有登记或生成通知，下分钟从第一页重试", page, cause)
}
