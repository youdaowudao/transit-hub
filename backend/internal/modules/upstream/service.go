package upstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"hash/fnv"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/shared/businesstime"
)

const (
	persistenceTimeout = 5 * time.Second
)

type SiteRepository interface {
	ListSites(ctx context.Context) ([]Site, error)
	ListSitesForUser(ctx context.Context, userID string) ([]Site, error)
	SaveSite(ctx context.Context, site Site) error
	SaveExistingSiteIfUnchanged(ctx context.Context, expected, candidate Site) (bool, error)
	DeleteSite(ctx context.Context, userID string, id string) error
}

type AdminAccountResolver interface {
	RequireCurrentID(ctx context.Context, userID string) (string, error)
}

// RefreshConfig 控制后台定时同步行为，由系统设置模块驱动。
type RefreshConfig struct {
	Enabled  bool
	Interval time.Duration
}

type refreshWorkspaceKey struct {
	userID         string
	adminAccountID string
}

type syncFlight struct {
	done     chan struct{}
	response Response
	err      error
}

// SiteReferenceChecker reads complete local references without importing my_sites.
type SiteReferenceChecker interface {
	CountSiteReferences(ctx context.Context, userID, adminAccountID, siteID string) (connections, mappings int, err error)
}

// Service 管理上游站点的生命周期（创建、编辑、同步、删除）。
// 站点运行时状态缓存在 Redis（通过 SiteCache），PostgreSQL 负责持久化。
// 当系统设置开启了数据刷新频率时，定时器按配置的间隔自动同步各站点。
type Service struct {
	platformService         *PlatformService
	snapshotWriter          SnapshotWriter
	repository              SiteRepository
	cache                   SiteCache
	accounts                AdminAccountResolver
	references              SiteReferenceChecker
	refreshConfigs          map[refreshWorkspaceKey]RefreshConfig
	initialSchedules        map[refreshWorkspaceKey]bool
	backgroundTasksDisabled bool
	// groupCostQueueMu never nests service/cache operations. Workers own at most two reads.
	groupCostQueueMu  sync.Mutex
	groupCostQueue    *groupCostQueue
	groupCostClosed   bool
	groupCostDisabled bool
	timers            map[string]*time.Timer
	afterFunc         func(time.Duration, func()) *time.Timer
	deletedSites      map[string]struct{}
	syncFlights       map[string]*syncFlight
	keyUsageFlights   map[string]*keyUsageFlight
	keyUsageChanged   chan struct{}
	keyUsageFinished  map[string]time.Time
	now               func() time.Time
	mu                sync.Mutex

	siteLoginSuccessCallback func(userID, adminAccountID, siteID string)
	// AfterSync 在站点同步成功持久化后被调用，传入同步前后的指标及状态。
	// 由系统设置模块注入，用于余额预警和倍率变更检测。
	AfterSync func(ctx context.Context, userID, adminAccountID, siteID, siteName string, oldMetrics, newMetrics Metrics, oldStatus, newStatus Status)
}

func (s *Service) SetAdminAccountResolver(accounts AdminAccountResolver) {
	s.accounts = accounts
}

func (s *Service) SetSiteReferenceChecker(checker SiteReferenceChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.references = checker
}

// SetSiteLoginSuccessCallback installs an in-process notification, without credentials.
func (s *Service) SetSiteLoginSuccessCallback(callback func(userID, adminAccountID, siteID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.siteLoginSuccessCallback = callback
}

func (s *Service) notifySiteLoginSucceeded(site *Site) {
	s.mu.Lock()
	callback := s.siteLoginSuccessCallback
	s.mu.Unlock()
	if callback != nil {
		callback(site.UserID, site.AdminAccountID, site.ID)
	}
}

// requireCurrentAdminAccountID 解析当前工作区 ID，解析失败时返回错误（fail-closed）。
// 面向用户的业务接口必须使用此方法，确保无法解析 workspace 时不会泄露全量数据。
func (s *Service) requireCurrentAdminAccountID(ctx context.Context, userID string) (string, error) {
	if s.accounts == nil {
		return "", newRequestError("admin.adminAccounts.errors.noCurrentAccount", "")
	}
	return s.accounts.RequireCurrentID(ctx, userID)
}

func NewService(platformService *PlatformService, repository SiteRepository, snapshotWriter SnapshotWriter, cache SiteCache) *Service {
	return &Service{
		platformService:  platformService,
		snapshotWriter:   snapshotWriter,
		repository:       repository,
		cache:            cache,
		timers:           make(map[string]*time.Timer),
		afterFunc:        time.AfterFunc,
		refreshConfigs:   make(map[refreshWorkspaceKey]RefreshConfig),
		initialSchedules: make(map[refreshWorkspaceKey]bool),
		deletedSites:     make(map[string]struct{}),
		syncFlights:      make(map[string]*syncFlight),
	}
}

// SetBackgroundTasksDisabled affects only this instance, never persisted strategy
// settings or shared cache. Configure it before serving requests.
func (s *Service) SetBackgroundTasksDisabled(disabled bool) {
	s.mu.Lock()
	s.backgroundTasksDisabled = disabled
	if disabled {
		for id := range s.timers {
			s.clearTimerLocked(id)
		}
	}
	s.groupCostQueueMu.Lock()
	s.groupCostDisabled = disabled
	if disabled {
		s.stopGroupCostQueueLocked()
	}
	s.groupCostQueueMu.Unlock()
	s.mu.Unlock()
}

func (s *Service) backgroundTasksEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.backgroundTasksDisabled
}

// SetWorkspaceRefreshConfig 只更新指定工作区的后台定时同步配置。
func (s *Service) SetWorkspaceRefreshConfig(userID, adminAccountID string, config RefreshConfig) {
	userID = strings.TrimSpace(userID)
	adminAccountID = strings.TrimSpace(adminAccountID)
	if userID == "" || adminAccountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := refreshWorkspaceKey{userID: userID, adminAccountID: adminAccountID}
	s.refreshConfigs[key] = config
	if s.backgroundTasksDisabled {
		return
	}
	initial := !s.initialSchedules[key]

	if s.repository == nil {
		return
	}
	sites, err := s.repository.ListSites(context.Background())
	if err != nil {
		log.Printf("[upstream] 无法读取站点列表来调度定时器: %v", err)
		return
	}
	initialScheduled := false
	for i := range sites {
		if sites[i].UserID == userID && sites[i].AdminAccountID == adminAccountID {
			if initial {
				initialScheduled = s.scheduleInitialSyncLocked(sites[i].ID, &sites[i]) || initialScheduled
			} else {
				s.scheduleSyncLocked(sites[i].ID, &sites[i])
			}
		}
	}
	if initialScheduled {
		s.initialSchedules[key] = true
	}
	log.Printf("[upstream] 工作区后台定时同步配置已更新 user_id=%s admin_account_id=%s enabled=%t interval=%s", userID, adminAccountID, config.Enabled, config.Interval)
}

// RestoreSavedSites 从 PostgreSQL 恢复所有站点到 Redis 缓存。
// 先清空 Redis 中的旧数据，确保与数据库完全一致。
func (s *Service) RestoreSavedSites(ctx context.Context) error {
	if s.repository == nil {
		return nil
	}

	// 清空 Redis 旧数据，防止残留 key 与数据库不一致。
	if err := s.cache.Flush(ctx); err != nil {
		return err
	}
	if store := s.groupCostStore(); store != nil {
		if err := store.ClearGroupCostSamples(ctx); err != nil {
			return err
		}
	}

	sites, err := s.repository.ListSites(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	initialWorkspaces := make(map[refreshWorkspaceKey]struct{})
	for i := range sites {
		if strings.TrimSpace(sites[i].UserID) == "" {
			continue
		}
		site := &sites[i]
		if _, deleted := s.deletedSites[site.ID]; deleted {
			continue
		}
		if err := s.cache.Set(ctx, site); err != nil {
			return err
		}
		key := refreshWorkspaceKey{userID: site.UserID, adminAccountID: site.AdminAccountID}
		if _, configured := s.refreshConfigs[key]; configured && !s.initialSchedules[key] {
			if s.scheduleInitialSyncLocked(site.ID, site) {
				initialWorkspaces[key] = struct{}{}
			}
		} else {
			s.scheduleSyncLocked(site.ID, site)
		}
	}
	for key := range initialWorkspaces {
		s.initialSchedules[key] = true
	}
	return nil
}

// List 返回指定用户当前工作区的站点列表（从 Redis 缓存读取）。
// fail-closed：无法解析工作区时返回空列表，不泄露跨 workspace 数据。
func (s *Service) List(ctx context.Context, userID string) []Response {
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return nil
	}
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		log.Printf("upstream list: cache read failed user_id=%s err=%v", userID, err)
		return nil
	}
	responses := make([]Response, 0, len(sites))
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID {
			continue
		}
		responses = append(responses, s.toResponse(ctx, site))
	}
	return responses
}

// ListForAccount 按指定 adminAccountID 列出站点，供后台调度等跨工作区内部流程使用。
// 与 List 不同：不依赖当前工作区上下文，而是显式传入 adminAccountID。
func (s *Service) ListForAccount(ctx context.Context, userID, adminAccountID string) []Response {
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		log.Printf("upstream list-for-account: cache read failed user_id=%s err=%v", userID, err)
		return nil
	}
	responses := make([]Response, 0, len(sites))
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID {
			continue
		}
		responses = append(responses, s.toResponse(ctx, site))
	}
	return responses
}

// FetchGroupDailyStats 获取指定站点的分组每日统计数据。
// 从缓存读取站点会话和分组列表，调用平台 API 获取统计数据。
func (s *Service) FetchGroupDailyStats(ctx context.Context, userID string, id string) ([]GroupDailyStat, error) {
	site, err := s.cache.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if site == nil || site.UserID != userID || site.Session == nil {
		return nil, newRequestError(ErrorNotFound, "")
	}
	if !site.IsEnabled() {
		return nil, newRequestError(ErrorDisabled, "")
	}
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if site.AdminAccountID != aid {
		return nil, newRequestError(ErrorNotFound, "")
	}
	session := *site.Session
	groups := append([]GroupInfo(nil), site.Metrics.Groups...)

	refreshedSession, err := s.platformService.RefreshSession(session)
	if err != nil {
		return nil, err
	}
	var stats []GroupDailyStat
	if refreshedSession.Platform == PlatformNewAPI {
		stats, err = s.platformService.FetchNewAPIGroupDailyStats(refreshedSession, groups)
	} else if refreshedSession.Platform == PlatformSub2API {
		stats, err = s.platformService.FetchSub2APIGroupDailyStats(refreshedSession, groups)
	} else {
		return nil, newRequestError(ErrorNotFound, "")
	}
	if err != nil {
		return nil, err
	}

	proceed, saveErr := s.persistKeyUsageSession(*site, refreshedSession)
	if !proceed || saveErr != nil {
		if saveErr == nil {
			saveErr = staleSiteRequestError()
		}
		return nil, saveErr
	}
	return stats, nil
}

// KeyUsageToday 返回当前工作区所有上游站点中，今天有消费的 key 明细（仪表盘「今日成本」下钻数据源）。
// 只处理有 session 且 rechargeRate > 0 的站点：这与 dashboard.MetricsService.LiveMetrics() 中
// todayPurchase 的统计口径完全一致（rechargeRate <= 0 的站点被整体跳过），确保弹窗总额与卡片数值一致。
// 站点级并发限制 4；任一站点请求上游平台失败即让整个方法返回错误，不允许把失败站点当 0 处理。
func (s *Service) KeyUsageToday(ctx context.Context, userID string) ([]KeyUsageTodayItem, error) {
	return s.keyUsageToday(ctx, userID, false, s.keyUsageDate())
}

// KeyUsageTodayIncludingZero 保留真实存在但当日零消费的 key，供稳定绑定利润核算使用。
// 普通成本下钻仍使用 KeyUsageToday，只展示有消费的 key。
func (s *Service) KeyUsageTodayIncludingZero(ctx context.Context, userID string) ([]KeyUsageTodayItem, error) {
	return s.keyUsageToday(ctx, userID, true, s.keyUsageDate())
}

func (s *Service) KeyUsageTodayIncludingZeroForDate(ctx context.Context, userID, date string) ([]KeyUsageTodayItem, error) {
	return s.keyUsageToday(ctx, userID, true, date)
}

func (s *Service) KeyUsageForDate(ctx context.Context, userID, adminAccountID, date string) (KeyUsageForDateResult, error) {
	if strings.TrimSpace(date) == "" {
		date = s.keyUsageDate()
	}
	if strings.TrimSpace(adminAccountID) == "" {
		return KeyUsageForDateResult{}, errors.New("admin account id is required")
	}
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return KeyUsageForDateResult{}, err
	}
	targets := make([]*Site, 0, len(sites))
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID || !site.IsEnabled() || site.Session == nil || site.RechargeRate <= 0 {
			continue
		}
		targets = append(targets, site)
	}
	result := KeyUsageForDateResult{BusinessDate: date, ExpectedSites: len(targets), Sites: make([]KeyUsageSiteResult, len(targets))}
	const maxSiteConcurrency = 4
	sem := make(chan struct{}, maxSiteConcurrency)
	var wg sync.WaitGroup
	for index, site := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return KeyUsageForDateResult{}, ctx.Err()
		}
		wg.Add(1)
		go func(index int, site *Site) {
			defer wg.Done()
			defer func() { <-sem }()
			siteResult := KeyUsageSiteResult{
				SiteID: site.ID, SiteName: site.Name, Platform: site.Platform,
				RechargeRate: site.RechargeRate, Items: []KeyUsageTodayItem{},
			}
			session := *site.Session
			groups := append([]GroupInfo(nil), site.Metrics.Groups...)
			refreshedSession, refreshErr := s.platformService.RefreshSessionContext(ctx, session)
			if refreshErr != nil {
				logDatedUsageFailure(site, date, "refresh", refreshErr)
				siteResult.Error = formatCostFailure("逐Key登录资料刷新", refreshErr)
				result.Sites[index] = siteResult
				return
			}
			stats, total, collectedAt, fetchErr := s.platformService.fetchDatedKeyCollection(withDatedUsageScope(ctx, site.ID), refreshedSession, groups, date)
			if fetchErr != nil {
				logDatedUsageFailure(site, date, "usage", fetchErr)
				siteResult.Error = formatCostFailure("站点“"+safeCostReason(site.Name)+"”逐Key指定日费用读取", fetchErr)
				result.Sites[index] = siteResult
				return
			}
			proceed, saveErr := s.persistKeyUsageSession(*site, refreshedSession)
			if !proceed || saveErr != nil {
				if saveErr == nil {
					saveErr = staleSiteRequestError()
				}
				siteResult.Error = taskErrorDetail(saveErr)
				result.Sites[index] = siteResult
				return
			}
			siteResult.Timezone = businesstime.Timezone
			siteResult.ConsumeDate = date
			siteResult.CollectedRawCost = &total
			siteResult.CollectedAt = &collectedAt

			for _, stat := range stats {
				groupName := strings.TrimSpace(stat.GroupName)
				if groupName == "" {
					groupName = "Ungrouped"
				}
				siteResult.Items = append(siteResult.Items, KeyUsageTodayItem{
					SiteID: site.ID, SiteName: site.Name, Platform: site.Platform,
					KeyID: stat.KeyID, KeyIDs: append([]string(nil), stat.KeyIDs...), Merged: stat.Merged, KeyName: stat.KeyName, GroupName: groupName,
					TodayAmount: stat.TodayAmount * site.RechargeRate,
					RawAmount:   stat.TodayAmount, RechargeRate: site.RechargeRate,
				})
			}
			siteResult.Complete = true
			result.Sites[index] = siteResult
		}(index, site)
	}
	wg.Wait()
	for _, site := range result.Sites {
		if site.Complete {
			result.CompletedSites++
		}
	}
	return result, nil
}

func (s *Service) keyUsageToday(ctx context.Context, userID string, includeZero bool, date string) ([]KeyUsageTodayItem, error) {
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, err
	}
	result, err := s.CachedKeyUsageForDate(ctx, userID, adminAccountID, date)
	if err != nil {
		return nil, err
	}
	items := make([]KeyUsageTodayItem, 0)
	for _, site := range result.Sites {
		for _, item := range site.Items {
			if includeZero || item.TodayAmount > 0 {
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TodayAmount > items[j].TodayAmount })
	if result.CompletedSites < result.ExpectedSites {
		return items, &KeyUsageCollectionError{FailedSites: result.ExpectedSites - result.CompletedSites, TotalSites: result.ExpectedSites, Cause: newRequestError(ErrorRequest, "")}
	}
	return items, nil
}

func (s *Service) BalanceBreakdown(ctx context.Context, userID string) ([]BalanceBreakdownItem, error) {
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, err
	}
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	items := make([]BalanceBreakdownItem, 0, len(sites))
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID || !site.IsEnabled() {
			continue
		}
		item := BalanceBreakdownItem{
			SiteID:       site.ID,
			SiteName:     site.Name,
			Platform:     site.Platform,
			RechargeRate: site.RechargeRate,
			LastSyncedAt: site.LastSyncedAt,
			Status:       site.Status,
		}
		if site.RechargeRate > 0 && site.Metrics.Balance.Value != nil {
			raw := *site.Metrics.Balance.Value
			balance := raw * site.RechargeRate
			item.RawBalance = &raw
			item.Balance = &balance
		}
		items = append(items, item)
	}
	return items, nil
}

// Create 创建一个新的上游站点。
// 先执行平台登录，成功后保存站点并启动定时同步。
func (s *Service) Create(ctx context.Context, userID string, dto CreateRequest) (Response, error) {
	if err := validateCreate(dto); err != nil {
		return Response{}, err
	}
	// 创建站点必须归属于当前工作区。
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return Response{}, err
	}
	id, err := randomID()
	if err != nil {
		return Response{}, err
	}
	requestedPlatform := dto.Platform
	platform := resolvedPlatform(dto.Platform)
	account := strings.TrimSpace(dto.Account)
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey {
		account = strings.TrimSpace(dto.UserID)
	}
	site := &Site{
		ID:                id,
		UserID:            userID,
		AdminAccountID:    adminAccountID,
		Name:              strings.TrimSpace(dto.Name),
		BaseURL:           strings.TrimSpace(dto.SiteURL),
		Platform:          platform,
		RequestedPlatform: requestedPlatform,
		Account:           account,
		Remark:            strings.TrimSpace(dto.Remark),
		RechargeRate:      dto.RechargeRate,
		Enabled:           boolPointer(true),
		Status:            StatusConnecting,
		ErrorKey:          nil,
		Metrics:           defaultMetrics(),
		LastSyncedAt:      nil,
		Session:           nil,
	}

	log.Printf("[upstream] 创建站点登录开始 name=%q host=%s platform=%s", safeUpstreamMessage(dto.Name), safeHost(dto.SiteURL), dto.Platform)
	result, loginErr := s.createLogin(dto)
	if loginErr != nil {
		logSiteFailure("登录失败", "新建", dto.Name, dto.SiteURL, loginErr)
		return Response{}, loginErr
	}

	// 登录成功：更新站点状态。
	observedAt := s.keyUsageNow()
	now := observedAt.UnixMilli()
	site.BaseURL = result.Session.BaseURL
	site.Platform = result.Platform
	site.Session = &result.Session
	site.Metrics = result.Metrics.WithSyncDate(businesstime.DateAt(observedAt), observedAt)
	site.Status = StatusConnected
	site.ErrorKey = nil
	site.LastSyncedAt = &now

	audit := site.Metrics.GroupSnapshotAudit
	rateInput := currentRateNoticeInput(site.Metrics, nil, observedAt)
	if err := s.setCachedSite(ctx, site); err != nil {
		return Response{}, err
	}
	response := s.toResponse(ctx, site)
	sourceRowVersion, saveErr := s.saveSiteWithSnapshotVersion(ctx, site, rateInput)
	if saveErr != nil {
		cleanupErr := s.cache.Delete(ctx, id, userID)
		outcome := "新网站资料未保存，原有网站资料保留"
		var commitErr *siteCommitError
		commitUnknown := errors.As(saveErr, &commitErr)
		var requestErr *RequestError
		if !commitUnknown && errors.As(saveErr, &requestErr) && requestErr.MessageKey == ErrorNotFound {
			return response, saveErr
		}
		var cause string
		if commitUnknown {
			outcome = "新网站数据库保存结果尚未确认，请维护人员核查实际提交结果"
			cause = "数据库提交结果尚未确认"
		} else {
			var state interface{ SQLState() string }
			if errors.As(saveErr, &state) {
				cause = notifications.SafeStorageCause(saveErr)
			} else {
				cause = safePersistenceCause(saveErr)
			}
		}
		detail := "保存新网站资料失败：" + cause + "；" + outcome + "；处理原因并核查实际保存结果后可重新登录重试；不会修改主站。"
		if cleanupErr != nil {
			detail += "本次新网站运行资料的回收也未成功：" + safePersistenceCause(cleanupErr) + "；请维护人员核查当前运行资料后重试。"
		}
		return response, saveFailureError(detail, saveErr, 503)
	}
	s.mu.Lock()
	s.scheduleSyncLocked(id, site)
	s.mu.Unlock()

	response.GroupRatesWarning = s.saveSnapshot(ctx, site, audit, sourceRowVersion)
	return response, nil
}

// Update 更新指定站点的配置。
// 如果提供了新的凭证，会重新执行平台登录。
func (s *Service) Update(ctx context.Context, userID, id string, dto UpdateRequest) (Response, error) {
	if err := validateUpdate(dto); err != nil {
		return Response{}, err
	}
	site, err := s.readCurrentSite(ctx, id)
	if err != nil {
		return Response{}, err
	}
	if site == nil || site.UserID != userID {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return Response{}, err
	}
	if site.AdminAccountID != aid {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	original := cloneSite(site)
	normalizedURL, err := s.platformServiceURL(dto.SiteURL)
	if err != nil {
		return Response{}, err
	}
	originalURL, err := s.platformServiceURL(site.BaseURL)
	if err != nil {
		return Response{}, err
	}
	account := strings.TrimSpace(dto.Account)
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey {
		account = strings.TrimSpace(dto.UserID)
	}
	if normalizedURL != originalURL || dto.Platform != PlatformAuto && dto.Platform != site.Platform || normalizedAuthMode(dto.AuthMode) != siteAuthMode(site) || account != strings.TrimSpace(site.Account) {
		return Response{}, identityUpdateError(nil, "这个修改会换成另一个站点或账号，请新增站点，避免混入原账号的费用；原资料未保存，不会修改主站。")
	}
	relogin := hasNewCredentials(dto)
	var result LoginResult
	var audit *GroupSnapshotAudit
	var rateInput *RateNoticeInput
	if relogin {
		if original.Session == nil {
			return Response{}, identityUpdateError(nil, "原站点没有可核验的登录资料")
		}
		oldID, readErr := s.platformService.readLoginPrincipal(ctx, *original.Session)
		if readErr != nil {
			return Response{}, identityUpdateError(readErr, "读取原账号身份失败")
		}
		result, err = s.updateLogin(dto)
		audit = result.Metrics.GroupSnapshotAudit
		rateInput = currentRateNoticeInput(result.Metrics, err, s.keyUsageNow())
		if err != nil {
			return Response{}, identityUpdateError(err, "重新登录失败")
		}
		if result.Platform != original.Platform || result.Session.BaseURL != originalURL {
			return Response{}, identityUpdateError(nil, "重新登录后的站点地址或平台与原资料不同")
		}
		newID, readErr := s.platformService.readLoginPrincipal(ctx, result.Session)
		if readErr != nil {
			return Response{}, identityUpdateError(readErr, "读取新登录身份失败")
		}
		if oldID != newID {
			return Response{}, identityUpdateError(nil, "新登录身份与原账号不同")
		}
	}
	s.mu.Lock()
	current, readErr := s.readCurrentSite(ctx, id)
	_, deleted := s.deletedSites[id]
	if readErr != nil || deleted || current == nil || current.UserID != userID || current.AdminAccountID != aid {
		s.mu.Unlock()
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !reflect.DeepEqual(current.Session, original.Session) {
		s.mu.Unlock()
		return Response{}, staleSiteRequestError()
	}
	expected := cloneSite(current)
	candidate := cloneSite(current)
	candidate.Name = strings.TrimSpace(dto.Name)
	candidate.Remark = strings.TrimSpace(dto.Remark)
	candidate.RechargeRate = dto.RechargeRate
	candidate.RequestedPlatform = dto.Platform
	if relogin {
		candidate.BaseURL = result.Session.BaseURL
		candidate.Platform = result.Platform
		candidate.Session = &result.Session
		candidate.Metrics = mergeCostReadStatus(result.Metrics, current.Metrics)
		candidate.Status = StatusConnected
		candidate.ErrorKey = nil
		observed := s.keyUsageNow().UnixMilli()
		candidate.LastSyncedAt = &observed
	}
	var publication *groupPublication
	if relogin {
		publication = &groupPublication{Baseline: original}
	}
	err = s.publishExistingSiteLocked(expected, candidate, relogin, nil, rateInput, publication)
	if err == nil && publication != nil {
		candidate = publication.Actual
	}
	if err == nil {
		s.scheduleSyncLocked(id, &candidate)
	}
	s.mu.Unlock()
	if err != nil {
		return s.toResponse(ctx, current), err
	}
	response := s.toResponse(ctx, &candidate)
	if relogin {
		if publication.GroupsAccepted {
			response.GroupRatesWarning = s.saveSnapshot(ctx, &candidate, audit, publication.RowVersion)
			s.notifySiteLoginSucceeded(&candidate)
		} else {
			response.GroupRatesWarning = staleGroupsWarning()
		}
	}
	return response, nil
}
func (s *Service) platformServiceURL(value string) (string, error) {
	if s.platformService != nil {
		return s.platformService.NormalizeURL(value)
	}
	return (&PlatformService{}).NormalizeURL(value)
}
func identityUpdateError(cause error, step string) error {
	reason := step
	if cause != nil {
		reason += "：" + costFailureCause(cause)
	}
	reason += "；原资料和费用未保存，无法确认是原账号，请新增站点；不会修改主站。"
	var original *RequestError
	if errors.As(cause, &original) {
		detail := *original
		detail.UpstreamMessage = reason
		detail.Cause = cause
		return &detail
	}
	return &RequestError{MessageKey: ErrorRequest, Stage: "verify", UpstreamMessage: reason, Cause: cause}
}
func staleSiteRequestError() error {
	return saveFailureError("站点登录资料已更新，本次旧请求未保存，请重新同步；不会修改主站。", nil, 409)
}

func resolvedPlatform(platform Platform) Platform {
	if platform == PlatformSub2API {
		return PlatformSub2API
	}
	return PlatformNewAPI
}

func (s *Service) createLogin(dto CreateRequest) (LoginResult, error) {
	switch normalizedAuthMode(dto.AuthMode) {
	case AuthModeToken:
		return s.platformService.LoginWithToken(dto.SiteURL, dto.Platform, dto.Account, dto.AccessToken, dto.RefreshToken, dto.TokenType)
	case AuthModeUserKey:
		return s.platformService.LoginWithUserKey(dto.SiteURL, dto.UserID, dto.AccessToken)
	default:
		return s.platformService.Login(dto.SiteURL, dto.Platform, dto.Account, dto.Password)
	}
}

func (s *Service) updateLogin(dto UpdateRequest) (LoginResult, error) {
	switch normalizedAuthMode(dto.AuthMode) {
	case AuthModeToken:
		return s.platformService.LoginWithToken(dto.SiteURL, dto.Platform, dto.Account, dto.AccessToken, dto.RefreshToken, dto.TokenType)
	case AuthModeUserKey:
		return s.platformService.LoginWithUserKey(dto.SiteURL, dto.UserID, dto.AccessToken)
	default:
		return s.platformService.Login(dto.SiteURL, dto.Platform, dto.Account, dto.Password)
	}
}

func normalizedAuthMode(authMode AuthMode) AuthMode {
	switch authMode {
	case AuthModeToken:
		return AuthModeToken
	case AuthModeUserKey:
		return AuthModeUserKey
	default:
		return AuthModePassword
	}
}

// Sync 手动触发单个站点的同步。
func (s *Service) Sync(ctx context.Context, userID string, id string) (Response, error) {
	site, err := s.cache.Get(ctx, id)
	if err != nil {
		return Response{}, err
	}
	if site == nil || site.UserID != userID {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	// 工作区隔离：站点必须属于当前工作区，否则拒绝操作。
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return Response{}, err
	}
	if site.AdminAccountID != aid {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !site.IsEnabled() {
		return Response{}, newRequestError(ErrorDisabled, "")
	}
	return s.sync(ctx, id)
}

const relevantSiteSyncConcurrency = 5

// SyncSites 按显式 workspace 同步或等待指定站点。force=false 时只复用已经在途的
// 同站点同步，不会为了页面自动刷新启动新的上游同步。
func (s *Service) SyncSites(ctx context.Context, userID string, adminAccountID string, siteIDs []string, force bool) []SyncSiteResult {
	return s.SyncSitesProgress(ctx, userID, adminAccountID, siteIDs, force, nil)
}

// SyncSitesProgress 保留 SyncSites 的返回顺序，并在每个唯一站点真实终结时立即通知调用方。
func (s *Service) SyncSitesProgress(ctx context.Context, userID string, adminAccountID string, siteIDs []string, force bool, completed func(SyncSiteResult)) []SyncSiteResult {
	if ctx == nil {
		ctx = context.Background()
	}
	ids := normalizedSiteIDs(siteIDs)
	results := make([]SyncSiteResult, len(ids))
	sem := make(chan struct{}, relevantSiteSyncConcurrency)
	var wg sync.WaitGroup
	var completedMu sync.Mutex
	notifyCompleted := func(result SyncSiteResult) {
		if completed == nil {
			return
		}
		completedMu.Lock()
		defer completedMu.Unlock()
		completed(result)
	}
	for index, id := range ids {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[index] = SyncSiteResult{SiteID: id, Status: "unavailable", ErrorKey: "site_sync_cancelled"}
				notifyCompleted(results[index])
				return
			}
			results[index] = s.syncSiteForWorkspace(ctx, userID, adminAccountID, id, force)
			notifyCompleted(results[index])
		}(index, id)
	}
	wg.Wait()
	return results
}

func normalizedSiteIDs(siteIDs []string) []string {
	seen := make(map[string]struct{}, len(siteIDs))
	for _, siteID := range siteIDs {
		siteID = strings.TrimSpace(siteID)
		if siteID != "" {
			seen[siteID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for siteID := range seen {
		ids = append(ids, siteID)
	}
	sort.Strings(ids)
	return ids
}

func (s *Service) syncSiteForWorkspace(ctx context.Context, userID string, adminAccountID string, siteID string, force bool) SyncSiteResult {
	site, err := s.cache.Get(ctx, siteID)
	if err != nil || site == nil || site.UserID != userID || site.AdminAccountID != adminAccountID {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_unavailable"}
	}
	if !site.IsEnabled() {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_disabled"}
	}
	if site.Session == nil {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_session"}
	}
	if !force {
		s.mu.Lock()
		flight := s.syncFlights[siteID]
		s.mu.Unlock()
		if flight == nil {
			return syncSiteResult(siteID, toResponseForDate(site, s.keyUsageDate()), nil)
		}
		response, err := waitForSyncFlight(ctx, flight)
		return syncSiteResult(siteID, response, err)
	}
	response, err := s.sync(ctx, siteID)
	return syncSiteResult(siteID, response, err)
}

func waitForSyncFlight(ctx context.Context, flight *syncFlight) (Response, error) {
	select {
	case <-flight.done:
		return flight.response, flight.err
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func syncSiteResult(siteID string, response Response, err error) (result SyncSiteResult) {
	defer func() { result.GroupRatesWarning = response.GroupRatesWarning }()
	if errors.Is(err, context.DeadlineExceeded) || response.syncTimedOut {
		return SyncSiteResult{SiteID: siteID, Status: "timeout", ErrorKey: "site_sync_timeout"}
	}
	if err != nil {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_failed", ErrorDetail: taskErrorDetail(err)}
	}
	if response.Status == StatusConnected {
		return SyncSiteResult{SiteID: siteID, Status: "success"}
	}
	if response.Status == StatusSyncing {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_updating"}
	}
	if response.ErrorKey != nil {
		switch errorCategory(*response.ErrorKey) {
		case ErrorAuth:
			return SyncSiteResult{SiteID: siteID, Status: "auth_failed", ErrorKey: "site_sync_auth"}
		case ErrorNetwork:
			return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_network"}
		case ErrorInvalidResponse:
			return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_invalid_response"}
		case ErrorRequest:
			return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_request"}
		}
	}
	return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_failed"}
}

// SyncAll 并发同步指定用户当前工作区的所有站点。
// 有会话的站点并行同步，无会话的直接返回当前状态。
func (s *Service) SyncAll(ctx context.Context, userID string) ([]Response, error) {
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 工作区隔离：只同步当前工作区的站点。
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	responses := make([]Response, 0)
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID {
			continue
		}
		if !site.IsEnabled() {
			responses = append(responses, toResponseForDate(site, s.keyUsageDate()))
			continue
		}
		if site.Session == nil {
			responses = append(responses, toResponseForDate(site, s.keyUsageDate()))
			continue
		}
		ids = append(ids, site.ID)
	}

	results := make([]Response, len(ids))
	var wg sync.WaitGroup
	for index, id := range ids {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			response, err := s.sync(ctx, id)
			if err != nil {
				// 同步失败时返回缓存中的当前状态。
				if cached, cacheErr := s.cache.Get(ctx, id); cacheErr == nil && cached != nil {
					response = toResponseForDate(cached, s.keyUsageDate())
				}
				results[index] = response
				return
			}
			results[index] = response
		}(index, id)
	}
	wg.Wait()
	for _, response := range results {
		if response.ID != "" {
			responses = append(responses, response)
		}
	}
	return responses, nil
}

// SyncAllStream 以 SSE 流方式并发同步所有站点，完成一个推送一个。
// 并发上限 5，每个站点独立同步，结果实时推送给前端。
func (s *Service) SyncAllStream(ctx context.Context, userID string, emit SyncEventCallback) error {
	const maxConcurrency = 5
	started := time.Now()

	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return err
	}

	// emit 会写 ResponseWriter，多 goroutine 并发调用需要加锁。
	var emitMu sync.Mutex
	safeEmit := func(event SyncEvent) {
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(event)
	}

	// 工作区隔离：只同步当前工作区的站点。
	adminAccountID, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(sites))
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID {
			continue
		}
		if !site.IsEnabled() {
			continue
		}
		if site.Session == nil {
			resp := toResponseForDate(site, s.keyUsageDate())
			safeEmit(SyncEvent{Event: SyncEventDone, SiteID: site.ID, Site: &resp})
			continue
		}
		ids = append(ids, site.ID)
	}

	// 有会话的站点并发同步，用 channel 信号量限制并发数。
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var succeeded, failed, canceled atomic.Int64

	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				canceled.Add(1)
				return
			}

			safeEmit(SyncEvent{Event: SyncEventSyncing, SiteID: id})

			response, syncErr := s.sync(ctx, id)
			if syncErr != nil || response.Status != StatusConnected {
				failed.Add(1)
				logKey := siteErrorKey(syncErr)
				if syncErr == nil && response.ErrorKey != nil {
					logKey = *response.ErrorKey
				}
				log.Printf("[upstream-stream] 同步失败 id=%s reason=%s", id, logKey)
				if cached, cacheErr := s.cache.Get(ctx, id); cacheErr == nil && cached != nil {
					response = toResponseForDate(cached, s.keyUsageDate())
				}
				key := ErrorUnknown
				if syncErr != nil {
					key = siteErrorKey(syncErr)
				} else if response.ErrorKey != nil && *response.ErrorKey != "" {
					key = *response.ErrorKey
				}
				safeEmit(SyncEvent{Event: SyncEventError, SiteID: id, ErrorKey: key, ErrorDetail: taskErrorDetail(syncErr), Site: &response})
			} else {
				succeeded.Add(1)
				safeEmit(SyncEvent{Event: SyncEventDone, SiteID: id, Site: &response})
			}
		}(id)
	}

	wg.Wait()
	log.Printf("[upstream-stream] 全量同步完成 total=%d ok=%d failed=%d canceled=%d duration=%s", len(ids), succeeded.Load(), failed.Load(), canceled.Load(), time.Since(started))
	emit(SyncEvent{Event: SyncEventComplete})
	return nil
}

// sync 复用同一站点正在进行的同步。调用者取消只结束自身等待，底层同步继续完成。
func (s *Service) sync(ctx context.Context, id string) (Response, error) {
	s.mu.Lock()
	if s.syncFlights == nil {
		s.syncFlights = make(map[string]*syncFlight)
	}
	flight := s.syncFlights[id]
	if flight == nil {
		flight = &syncFlight{done: make(chan struct{})}
		s.syncFlights[id] = flight
		go s.runSyncFlight(id, flight)
	}
	s.mu.Unlock()

	return waitForSyncFlight(ctx, flight)
}

func (s *Service) runSyncFlight(id string, flight *syncFlight) {
	response := Response{}
	var err error
	defer func() {
		if recover() != nil {
			err = newRequestError(ErrorUnknown, "")
			log.Printf("[upstream] sync panic recovered id=%s", id)
		}
		s.mu.Lock()
		flight.response = response
		flight.err = err
		if s.syncFlights[id] == flight {
			delete(s.syncFlights, id)
		}
		close(flight.done)
		s.mu.Unlock()
		if err == nil && response.Status == StatusConnected &&
			(response.Metrics.TodayConsumeStatus != "unreadable" || KnownUpstreamCodeErrorKey(response.Metrics.TodayConsumeUpstreamCode) == "") {
			if site, readErr := s.cache.Get(context.Background(), id); readErr == nil && site != nil {
				s.enqueueKeyUsageCollection(*site)
			}
		}
	}()
	response, err = s.syncOnce(context.Background(), id)
}

// refreshSessionForSync uses only this site's workspace setting for early refresh.
func (s *Service) refreshSessionForSync(ctx context.Context, site *Site, session Session) (Session, error) {
	s.mu.Lock()
	config := s.refreshConfigs[refreshWorkspaceKey{userID: site.UserID, adminAccountID: site.AdminAccountID}]
	s.mu.Unlock()
	if config.Enabled && config.Interval > 0 {
		return s.platformService.refreshSessionContextWithWindow(ctx, session, config.Interval+5*time.Minute, false)
	}
	return s.platformService.RefreshSessionContext(ctx, session)
}

// syncOnce 执行单个站点的同步流程：刷新会话 → 拉取最新指标 → 更新缓存和数据库。
func (s *Service) syncOnce(ctx context.Context, id string) (Response, error) {
	s.mu.Lock()
	site, err := s.readCurrentSite(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	if _, deleted := s.deletedSites[id]; deleted || site == nil || site.Session == nil {
		s.mu.Unlock()
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !site.IsEnabled() {
		s.mu.Unlock()
		return s.toResponse(ctx, site), newRequestError(ErrorDisabled, "")
	}
	original := cloneSite(site)
	oldMetrics, oldStatus := original.Metrics, original.Status
	transient := cloneSite(site)
	transient.Status = StatusSyncing
	transient.ErrorKey = nil
	if err = s.cache.Set(ctx, &transient); err != nil {
		s.mu.Unlock()
		return Response{}, saveFailureError("标记本站同步状态失败："+safePersistenceCause(err)+"；未开始读取或更新费用，请维护人员检查存储连接后重试；不会修改主站。", err, 503)
	}
	s.mu.Unlock()
	syncDate := businesstime.DateAt(s.keyUsageNow())
	session := *original.Session
	refreshed, fetchErr := s.refreshSessionForSync(ctx, &original, session)
	refreshedOK := fetchErr == nil
	metrics := Metrics{}
	if fetchErr == nil {
		metrics, fetchErr = s.platformService.FetchMetricsForDate(refreshed, syncDate)
	}
	audit := metrics.GroupSnapshotAudit
	rateInput := currentRateNoticeInput(metrics, fetchErr, s.keyUsageNow())
	s.mu.Lock()
	current, readErr := s.readCurrentSite(ctx, id)
	_, deleted := s.deletedSites[id]
	if readErr != nil || deleted || current == nil || current.UserID != original.UserID || current.AdminAccountID != original.AdminAccountID {
		s.mu.Unlock()
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !current.IsEnabled() {
		s.clearTimerLocked(id)
		s.revokeSyncingLocked(current, original)
		s.mu.Unlock()
		return s.toResponse(ctx, current), newRequestError(ErrorDisabled, "")
	}
	if !reflect.DeepEqual(current.Session, original.Session) {
		s.mu.Unlock()
		return s.toResponse(ctx, current), staleSiteRequestError()
	}
	expected := cloneSite(current)
	candidate := cloneSite(current)
	if refreshedOK {
		candidate.Session = &refreshed
	}
	if fetchErr != nil {
		candidate.Status = StatusError
		key := siteErrorKey(fetchErr)
		candidate.ErrorKey = &key
		if metrics.TodayConsumeStatus == "unreadable" && metrics.TodayConsumeFailedAt != nil {
			failedMetrics := current.Metrics
			failedMetrics.TodayConsumeDate = syncDate
			failedMetrics = withCostReadFailure(failedMetrics, fetchErr, *metrics.TodayConsumeFailedAt)
			candidate.Metrics = mergeCostReadStatus(failedMetrics, current.Metrics)
		}
		logSiteFailure("同步失败", id, candidate.Name, candidate.BaseURL, fetchErr)
	} else {
		candidate.Session = &refreshed
		candidate.Metrics = mergeCostReadStatus(metrics, current.Metrics)
		candidate.Status = StatusConnected
		candidate.ErrorKey = nil
		now := s.keyUsageNow().UnixMilli()
		candidate.LastSyncedAt = &now
	}
	publication := &groupPublication{Baseline: original}
	saveErr := s.publishExistingSiteLocked(expected, candidate, refreshedOK, &original.Status, rateInput, publication)
	if saveErr == nil {
		candidate = publication.Actual
	}
	if saveErr != nil {
		s.revokeSyncingLocked(current, original)
		s.mu.Unlock()
		return s.toResponse(ctx, current), saveErr
	}
	s.scheduleSyncLocked(id, &candidate)
	enabledBackground := !s.backgroundTasksDisabled
	callback := s.AfterSync
	s.mu.Unlock()
	if metrics.TodayConsumeStatus == "unreadable" && metrics.TodayConsumeFailedAt != nil {
		if reason := KnownUpstreamCodeErrorKey(metrics.TodayConsumeUpstreamCode); reason != "" {
			s.recordKnownKeyUsageCostFailure(candidate, keyUsageCostFailure{reason: metrics.TodayConsumeErrorDetail, failedAt: *metrics.TodayConsumeFailedAt})
		}
	}
	response := s.toResponse(ctx, &candidate)
	response.syncTimedOut = requestTimedOut(fetchErr)
	if !publication.GroupsAccepted {
		response.GroupRatesWarning = staleGroupsWarning()
	}
	if fetchErr == nil && publication.GroupsAccepted {
		response.GroupRatesWarning = s.saveSnapshot(ctx, &candidate, audit, publication.RowVersion)
		if enabledBackground {
			s.enqueueGroupCostSampling(costWorkKey(&candidate), syncDate)
			if callback != nil {
				go callback(context.Background(), candidate.UserID, candidate.AdminAccountID, candidate.ID, candidate.Name, oldMetrics, candidate.Metrics, oldStatus, candidate.Status)
			}
		}
	}
	return response, nil
}
func (s *Service) revokeSyncingLocked(current *Site, original Site) {
	if current == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	latest, err := s.cache.Get(ctx, current.ID)
	if err != nil || latest == nil || latest.Status != StatusSyncing || !reflect.DeepEqual(latest.Session, original.Session) {
		return
	}
	next := cloneSite(latest)
	next.Status = original.Status
	_ = s.cache.Set(ctx, &next)
}
func taskErrorDetail(err error) string {
	var detail *RequestError
	if errors.As(err, &detail) && detail.Stage == "save" {
		return detail.UpstreamMessage
	}
	return ""
}

func (s *Service) saveSnapshot(ctx context.Context, site *Site, audit *GroupSnapshotAudit, sourceRowVersion string) *GroupRatesWarning {
	if site == nil {
		return nil
	}
	warning := auditWarning(site.Name, audit)
	if audit == nil || !audit.AllowUpdate {
		return warning
	}
	if s.snapshotWriter == nil {
		return warning
	}
	if site.LastSyncedAt == nil || sourceRowVersion == "" {
		return &GroupRatesWarning{Message: site.Name + "的分组倍率未更新", Detail: "本站保存返回的数据库版本或同步时间缺失，旧倍率和历史仍保留，本轮未清理", Next: "请维护人员核查站点保存记录后重新同步"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.snapshotWriter.SaveSiteSnapshot(ctx, site.UserID, site.AdminAccountID, site.ID, site.Name, site.Platform, snapshotGroups(site.Metrics.Groups), *site.LastSyncedAt, sourceRowVersion, audit.AllowCleanup); err != nil {
		cause := safeSnapshotCause(err)
		log.Printf("group rate snapshot failed site_id=%s reason=%s", site.ID, cause)
		var outcome interface{ UncertainSnapshotResult() bool }
		if errors.As(err, &outcome) && outcome.UncertainSnapshotResult() {
			return &GroupRatesWarning{Message: site.Name + "的本地倍率保存结果尚未确认", Detail: cause + "；当前不能确认哪些记录已更新或清理，本次未修改主站账号", Next: "请维护人员核查数据库提交结果后再同步"}
		}
		return &GroupRatesWarning{Message: site.Name + "的上游资料已读取，但本地倍率保存失败", Detail: cause + "；倍率新增与清理未完成，旧倍率记录保留", Next: "请恢复数据库连接并核查倍率保存记录后重新同步"}
	}
	return warning
}

func snapshotGroups(groups []GroupInfo) []SnapshotGroup {
	snapshots := make([]SnapshotGroup, 0, len(groups))
	for _, group := range groups {
		snapshots = append(snapshots, SnapshotGroup{
			ID:             group.ID,
			Name:           group.Name,
			Platform:       group.Platform,
			Multiplier:     group.Multiplier,
			MultiplierMode: group.MultiplierMode,
		})
	}
	return snapshots
}

// Remove 删除指定站点。
// 先从缓存和定时器中移除，再删除 PostgreSQL 记录。
// 数据库删除失败时回滚缓存。
func (s *Service) Remove(ctx context.Context, userID string, id string) error {
	site, err := s.cache.Get(ctx, id)
	if err != nil {
		return err
	}
	if site == nil || site.UserID != userID {
		return newRequestError(ErrorNotFound, "")
	}
	// 工作区隔离：站点必须属于当前工作区，否则拒绝删除。
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return err
	}
	if site.AdminAccountID != aid {
		return newRequestError(ErrorNotFound, "")
	}

	// An absent or unreadable checker is not evidence that references are empty.
	s.mu.Lock()
	checker := s.references
	s.mu.Unlock()
	if checker == nil {
		return newRequestError(ErrorRequest, "")
	}
	connections, mappings, checkErr := checker.CountSiteReferences(ctx, userID, aid, id)
	if checkErr != nil || connections < 0 || mappings < 0 {
		return newRequestError(ErrorRequest, "")
	}
	if connections > 0 || mappings > 0 {
		return &SiteInUseError{Connections: connections, Mappings: mappings}
	}

	var originalSnapshot *KeyUsageSnapshot
	if store := s.keyUsageStore(); store != nil {
		originalSnapshot, err = store.GetKeyUsageSnapshot(ctx, id)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.deletedSites[id] = struct{}{}
	s.clearTimerLocked(id)
	s.cancelGroupCostWork(costWorkKey(site))
	s.mu.Unlock()

	removedSite := *site
	if err := s.cache.Delete(ctx, id, userID); err != nil {
		s.mu.Lock()
		delete(s.deletedSites, id)
		s.mu.Unlock()
		return err
	}
	if store := s.keyUsageStore(); store != nil {
		if err := store.DeleteKeyUsageSnapshot(ctx, id); err != nil {
			return err
		}
	}
	if store := s.groupCostStore(); store != nil {
		if err := store.DeleteGroupCostSamples(ctx, id); err != nil {
			return err
		}
	}

	// 删除数据库记录。失败时把站点还原回缓存。
	if err := s.deleteSite(ctx, userID, id); err != nil {
		s.mu.Lock()
		delete(s.deletedSites, id)
		s.mu.Unlock()
		s.restoreSite(ctx, id, &removedSite)
		if originalSnapshot != nil {
			_ = s.keyUsageStore().SaveKeyUsageSnapshot(ctx, id, *originalSnapshot)
		}
		return err
	}
	return nil
}

// CleanupDeletedWorkspaceSites 清理工作区删除后遗留的本地运行时状态。
// 它只停止内存定时器并删除 Redis site cache，不调用任何上游远程删除接口。
func (s *Service) CleanupDeletedWorkspaceSites(ctx context.Context, userID string, siteIDs []string) error {
	ids := make([]string, 0, len(siteIDs))
	seen := make(map[string]struct{}, len(siteIDs))
	for _, id := range siteIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	s.mu.Lock()
	for _, id := range ids {
		s.deletedSites[id] = struct{}{}
		s.clearTimerLocked(id)
	}
	// Cleanup may run after the workspace row is gone, so match queued local keys.
	s.groupCostQueueMu.Lock()
	if q := s.groupCostQueue; q != nil {
		for key, work := range q.entries {
			if key.userID == userID {
				if _, deleted := seen[key.siteID]; deleted {
					work.cancel()
					work.followupDate = ""
					delete(q.entries, key)
				}
			}
		}
		kept := q.pending[:0]
		for _, work := range q.pending {
			if q.entries[work.key] == work {
				kept = append(kept, work)
			}
		}
		q.pending = kept
	}
	s.groupCostQueueMu.Unlock()
	s.mu.Unlock()

	var errs []error
	for _, id := range ids {
		if err := s.cache.Delete(ctx, id, userID); err != nil {
			errs = append(errs, err)
		}
		if store := s.keyUsageStore(); store != nil {
			if err := store.DeleteKeyUsageSnapshot(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
		if store := s.groupCostStore(); store != nil {
			if err := store.DeleteGroupCostSamples(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) Close() {
	s.mu.Lock()
	for id := range s.timers {
		s.clearTimerLocked(id)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	_ = s.ShutdownGroupCostSampling(ctx)
}

// scheduleSyncLocked 根据刷新配置为站点调度下一次定时同步。
// 仅在系统设置开启了数据刷新频率时才实际调度，否则为 no-op。
// 调用方必须持有 s.mu 锁。
func (s *Service) scheduleSyncLocked(id string, site *Site) bool {
	return s.scheduleSyncWithModeLocked(id, site, false)
}

func (s *Service) scheduleInitialSyncLocked(id string, site *Site) bool {
	return s.scheduleSyncWithModeLocked(id, site, true)
}

func (s *Service) scheduleSyncWithModeLocked(id string, site *Site, initial bool) bool {
	s.clearTimerLocked(id)
	if s.backgroundTasksDisabled {
		return false
	}
	if _, deleted := s.deletedSites[id]; deleted {
		return false
	}
	if site == nil || !site.IsEnabled() || site.Session == nil {
		return false
	}
	config, ok := s.refreshConfigs[refreshWorkspaceKey{userID: site.UserID, adminAccountID: site.AdminAccountID}]
	if !ok || !config.Enabled || config.Interval <= 0 {
		return false
	}
	delay := config.Interval
	if initial {
		delay = initialSyncDelay(id, config.Interval)
	}
	afterFunc := s.afterFunc
	if afterFunc == nil {
		afterFunc = time.AfterFunc
	}
	s.timers[id] = afterFunc(delay, func() {
		s.sync(context.Background(), id)
	})
	return true
}

func initialSyncDelay(siteID string, interval time.Duration) time.Duration {
	window := interval / 10
	if window > time.Minute {
		window = time.Minute
	}
	if window < time.Second {
		return interval
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(siteID))
	return interval + time.Duration(hash.Sum64()%uint64(window))
}

func (s *Service) clearTimerLocked(id string) {
	if timer := s.timers[id]; timer != nil {
		timer.Stop()
	}
	delete(s.timers, id)
}

func validateCreate(dto CreateRequest) error {
	fields := make([]string, 0)
	if strings.TrimSpace(dto.Name) == "" {
		fields = append(fields, "name")
	}
	if strings.TrimSpace(dto.SiteURL) == "" {
		fields = append(fields, "siteUrl")
	}
	if dto.Platform != PlatformAuto && dto.Platform != PlatformNewAPI && dto.Platform != PlatformSub2API {
		fields = append(fields, "platform")
	}
	if dto.AuthMode != "" && dto.AuthMode != AuthModePassword && dto.AuthMode != AuthModeToken && dto.AuthMode != AuthModeUserKey {
		fields = append(fields, "authMode")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeToken && dto.Platform == PlatformNewAPI {
		fields = append(fields, "platform")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && dto.Platform != PlatformNewAPI {
		fields = append(fields, "platform")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModePassword && strings.TrimSpace(dto.Account) == "" {
		fields = append(fields, "account")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModePassword && strings.TrimSpace(dto.Password) == "" {
		fields = append(fields, "password")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeToken && strings.TrimSpace(dto.AccessToken) == "" && strings.TrimSpace(dto.RefreshToken) == "" {
		fields = append(fields, "accessToken")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && strings.TrimSpace(dto.AccessToken) == "" {
		fields = append(fields, "accessToken")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && strings.TrimSpace(dto.UserID) == "" {
		fields = append(fields, "userId")
	}
	if dto.RechargeRate <= 0 {
		fields = append(fields, "rechargeRate")
	}
	if len(fields) > 0 {
		return invalidBodyError(fields...)
	}
	return nil
}

func validateUpdate(dto UpdateRequest) error {
	fields := make([]string, 0)
	hasCredentials := hasNewCredentials(dto)
	if strings.TrimSpace(dto.Name) == "" {
		fields = append(fields, "name")
	}
	if strings.TrimSpace(dto.SiteURL) == "" {
		fields = append(fields, "siteUrl")
	}
	if dto.Platform != PlatformAuto && dto.Platform != PlatformNewAPI && dto.Platform != PlatformSub2API {
		fields = append(fields, "platform")
	}
	if dto.AuthMode != "" && dto.AuthMode != AuthModePassword && dto.AuthMode != AuthModeToken && dto.AuthMode != AuthModeUserKey {
		fields = append(fields, "authMode")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeToken && dto.Platform == PlatformNewAPI {
		fields = append(fields, "platform")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && dto.Platform != PlatformNewAPI {
		fields = append(fields, "platform")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModePassword && strings.TrimSpace(dto.Account) == "" {
		fields = append(fields, "account")
	}
	if hasCredentials && normalizedAuthMode(dto.AuthMode) == AuthModeToken && strings.TrimSpace(dto.AccessToken) == "" && strings.TrimSpace(dto.RefreshToken) == "" {
		fields = append(fields, "accessToken")
	}
	if hasCredentials && normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && strings.TrimSpace(dto.AccessToken) == "" {
		fields = append(fields, "accessToken")
	}
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey && strings.TrimSpace(dto.UserID) == "" {
		fields = append(fields, "userId")
	}
	if hasCredentials && normalizedAuthMode(dto.AuthMode) == AuthModePassword && strings.TrimSpace(dto.Password) == "" {
		fields = append(fields, "password")
	}
	if dto.RechargeRate <= 0 {
		fields = append(fields, "rechargeRate")
	}
	if len(fields) > 0 {
		return invalidBodyError(fields...)
	}
	return nil
}

// UpdateSettings 更新站点级预警覆盖配置，不触发重新登录或同步。
func (s *Service) UpdateSettings(ctx context.Context, userID, siteID string, dto SiteSettings) (Response, error) {
	return s.updateLocalSiteFields(ctx, userID, siteID, func(site *Site) { site.Settings = dto })
}
func (s *Service) UpdateEnabled(ctx context.Context, userID, siteID string, enabled bool) (Response, error) {
	return s.updateLocalSiteFields(ctx, userID, siteID, func(site *Site) { site.Enabled = boolPointer(enabled) })
}
func (s *Service) updateLocalSiteFields(ctx context.Context, userID, siteID string, change func(*Site)) (Response, error) {
	aid, err := s.requireCurrentAdminAccountID(ctx, userID)
	if err != nil {
		return Response{}, err
	}
	s.mu.Lock()
	current, err := s.readCurrentSite(ctx, siteID)
	if err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	if _, deleted := s.deletedSites[siteID]; deleted || current == nil || current.UserID != userID || current.AdminAccountID != aid {
		s.mu.Unlock()
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	expected := cloneSite(current)
	candidate := cloneSite(current)
	change(&candidate)
	err = s.publishExistingSiteLocked(expected, candidate, false, nil, nil)
	if err == nil {
		s.scheduleSyncLocked(siteID, &candidate)
		if !candidate.IsEnabled() {
			s.cancelGroupCostWork(costWorkKey(&candidate))
		}
	}
	s.mu.Unlock()
	if err != nil {
		return Response{}, err
	}
	return s.toResponse(ctx, &candidate), nil
}

// GetSite 根据 ID 获取站点（供 alert 逻辑读取站点级配置）。
func (s *Service) GetSite(ctx context.Context, siteID string) (*Site, error) {
	return s.cache.Get(ctx, siteID)
}

func (s *Service) toResponse(ctx context.Context, site *Site) Response {
	response := toResponseForDate(site, s.keyUsageDate())
	if snapshots, err := s.groupCostSnapshotsForSite(ctx, site); err == nil {
		mergeGroupCostSnapshots(site, &response, snapshots)
	}
	return response
}

func toResponse(site *Site) Response { return toResponseForDate(site, businesstime.Today()) }
func toResponseForDate(site *Site, date string) Response {
	return Response{
		ID:                site.ID,
		UserID:            site.UserID,
		Name:              site.Name,
		BaseURL:           site.BaseURL,
		Platform:          site.Platform,
		RequestedPlatform: site.RequestedPlatform,
		AuthMode:          siteAuthMode(site),
		Account:           site.Account,
		Remark:            site.Remark,
		RechargeRate:      site.RechargeRate,
		Enabled:           boolPointer(site.IsEnabled()),
		Status:            site.Status,
		ErrorKey:          site.ErrorKey,
		Metrics:           maskUnverifiedTodayCost(site.Metrics, date),
		Settings:          site.Settings,
		LastSyncedAt:      site.LastSyncedAt,
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func (s *Service) saveSite(ctx context.Context, site *Site) error {
	if s.repository == nil || site == nil {
		return nil
	}
	if s.isSiteDeleted(site.ID) {
		return newRequestError(ErrorNotFound, "")
	}
	ctx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()
	return s.repository.SaveSite(ctx, *site)
}

func (s *Service) setCachedSite(ctx context.Context, site *Site) error {
	if site == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.deletedSites[site.ID]; deleted {
		return newRequestError(ErrorNotFound, "")
	}
	return s.cache.Set(ctx, site)
}

func (s *Service) isSiteDeleted(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, deleted := s.deletedSites[id]
	return deleted
}

func (s *Service) deleteSite(ctx context.Context, userID string, id string) error {
	if s.repository == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()
	return s.repository.DeleteSite(ctx, userID, id)
}

// restoreSite 将站点回滚到之前的状态（缓存和定时器）。
// 用于数据库写入失败后恢复一致性。
func (s *Service) restoreSite(ctx context.Context, id string, site *Site) {
	if site == nil {
		return
	}
	_ = s.setCachedSite(ctx, site)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleSyncLocked(id, site)
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.New("generate id")
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

// FetchSiteCostsForDate 用各上游站点自己的 session 查询指定日期的原始成本。
// 与 KeyUsageToday 模式一致：session 取自每个站点自身的缓存，不依赖仪表盘 admin 账户 session。
// 使用 semaphore 并发查询，上限 4 个并发，避免串行等待导致调度超时。
func (s *Service) FetchSiteCostsForDate(ctx context.Context, userID, adminAccountID, date string) ([]SiteCostForDateResult, error) {
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 先过滤出需要查询的站点，避免对无效站点发起请求。
	type targetSite struct{ site Site }
	var targets []targetSite
	for _, site := range sites {
		if site.AdminAccountID != adminAccountID || !site.IsEnabled() || site.RechargeRate <= 0 {
			continue
		}
		targets = append(targets, targetSite{*site})
	}

	if len(targets) == 0 {
		return []SiteCostForDateResult{}, nil
	}

	const maxConcurrency = 4
	sem := make(chan struct{}, maxConcurrency)
	results := make([]SiteCostForDateResult, len(targets))
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, site Site) {
			defer wg.Done()
			defer func() { <-sem }()

			r := SiteCostForDateResult{
				SiteID:       site.ID,
				SiteName:     site.Name,
				Platform:     site.Platform,
				RechargeRate: site.RechargeRate,
			}
			if site.Session == nil || !site.Session.IsAuthenticated() {
				r.Err = errors.New(ErrorAuth)
				results[idx] = r
				return
			}
			rawCost, meta, fetchErr := s.platformService.fetchCostForDateForSite(site.ID, *site.Session, date)
			r.RawCost = rawCost
			r.Meta = meta
			r.Err = fetchErr
			results[idx] = r
		}(i, t.site)
	}
	wg.Wait()
	return results, nil
}

func logDatedUsageFailure(site *Site, date, stage string, err error) {
	status := 0
	var detail *RequestError
	if errors.As(err, &detail) {
		status = detail.StatusCode
	}
	log.Printf("[upstream-usage] 日期用量读取失败 site=%s name=%q date=%s stage=%s reason=%s status=%d", site.ID, safeUpstreamMessage(site.Name), date, stage, siteErrorKey(err), status)
}

func logSiteFailure(event, id, name, baseURL string, err error) {
	stage, status := "", 0
	var detail *RequestError
	if errors.As(err, &detail) {
		stage, status = detail.Stage, detail.StatusCode
	}
	log.Printf("[upstream] %s site=%s name=%q host=%s stage=%s reason=%s status=%d", event, id, safeUpstreamMessage(name), safeHost(baseURL), stage, siteErrorKey(err), status)
}

// Legacy Sub2API token sessions cannot distinguish password from token login;
// keep the historical password selection until a new successful login records it.
func siteAuthMode(site *Site) AuthMode {
	if site.Session == nil {
		return AuthModePassword
	}
	switch site.Session.AuthMode {
	case AuthModePassword, AuthModeToken, AuthModeUserKey:
		return site.Session.AuthMode
	}
	if site.Platform == PlatformNewAPI && strings.TrimSpace(site.Session.Cookie) == "" {
		return AuthModeUserKey
	}
	return AuthModePassword
}

func hasNewCredentials(dto UpdateRequest) bool {
	return strings.TrimSpace(dto.Password) != "" || strings.TrimSpace(dto.AccessToken) != "" || strings.TrimSpace(dto.RefreshToken) != ""
}

// This narrow read never acquires s.mu; callers may use it within publication.
func (s *Service) readCurrentSite(ctx context.Context, id string) (*Site, error) {
	bounded, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()
	return s.cache.Get(bounded, id)
}
