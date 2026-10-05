package upstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const (
	persistenceTimeout = 5 * time.Second
)

type SiteRepository interface {
	ListSites(ctx context.Context) ([]Site, error)
	ListSitesForUser(ctx context.Context, userID string) ([]Site, error)
	SaveSite(ctx context.Context, site Site) error
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
	platformService  *PlatformService
	snapshotWriter   SnapshotWriter
	repository       SiteRepository
	cache            SiteCache
	accounts         AdminAccountResolver
	references       SiteReferenceChecker
	refreshConfigs   map[refreshWorkspaceKey]RefreshConfig
	initialSchedules map[refreshWorkspaceKey]bool
	// groupCostSlots 限制跨站点成本采样的并发量；nil 仅用于不带 NewService 的单元测试。
	groupCostSlots   chan struct{}
	timers           map[string]*time.Timer
	deletedSites     map[string]struct{}
	syncFlights      map[string]*syncFlight
	keyUsageFlights  map[string]*keyUsageFlight
	keyUsageChanged  chan struct{}
	keyUsageFinished map[string]time.Time
	now              func() time.Time
	mu               sync.Mutex
	// AfterSync 在站点同步成功后被调用，传入同步前后的指标数据。
	// 由系统设置模块注入，用于余额预警和倍率变更检测。
	AfterSync func(ctx context.Context, userID, adminAccountID, siteID, siteName string, oldMetrics, newMetrics Metrics)
}

func (s *Service) SetAdminAccountResolver(accounts AdminAccountResolver) {
	s.accounts = accounts
}

func (s *Service) SetSiteReferenceChecker(checker SiteReferenceChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.references = checker
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
		groupCostSlots:   make(chan struct{}, 2),
		timers:           make(map[string]*time.Timer),
		refreshConfigs:   make(map[refreshWorkspaceKey]RefreshConfig),
		initialSchedules: make(map[refreshWorkspaceKey]bool),
		deletedSites:     make(map[string]struct{}),
		syncFlights:      make(map[string]*syncFlight),
	}
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

	// 将刷新后的会话写回缓存和数据库。
	site, err = s.cache.Get(ctx, id)
	if err == nil && site != nil && site.UserID == userID && site.IsEnabled() {
		site.Session = &refreshedSession
		_ = s.setCachedSite(ctx, site)
		_ = s.saveSite(ctx, site)
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
		date = businesstime.Today()
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
				siteResult.Error = ErrorRequest
				result.Sites[index] = siteResult
				return
			}
			stats, fetchErr := s.platformService.fetchKeyUsageForDate(ctx, refreshedSession, groups, date)
			if fetchErr != nil {
				siteResult.Error = ErrorRequest
				result.Sites[index] = siteResult
				return
			}
			if cached, cacheErr := s.cache.Get(ctx, site.ID); cacheErr == nil && cached != nil && cached.UserID == site.UserID && cached.IsEnabled() {
				cached.Session = &refreshedSession
				_ = s.setCachedSite(ctx, cached)
				_ = s.saveSite(ctx, cached)
			}
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

	if err := s.setCachedSite(ctx, site); err != nil {
		return Response{}, err
	}
	response := s.toResponse(ctx, site)
	if err := s.saveSite(ctx, site); err != nil {
		_ = s.cache.Delete(ctx, id, userID)
		return response, err
	}
	s.mu.Lock()
	s.scheduleSyncLocked(id, site)
	s.mu.Unlock()

	s.saveSnapshot(ctx, site)
	return response, nil
}

// Update 更新指定站点的配置。
// 如果提供了新的凭证，会重新执行平台登录。
func (s *Service) Update(ctx context.Context, userID string, id string, dto UpdateRequest) (Response, error) {
	if err := validateUpdate(dto); err != nil {
		return Response{}, err
	}

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

	// Changing the login method must replace the session, even when an edit
	// omits credentials. Same-method edits may still retain their old session.
	if normalizedAuthMode(dto.AuthMode) != siteAuthMode(site) && !hasNewCredentials(dto) {
		if normalizedAuthMode(dto.AuthMode) == AuthModePassword {
			return Response{}, invalidBodyError("password")
		}
		return Response{}, invalidBodyError("accessToken")
	}

	// 保存更新前的状态，以便数据库写入失败时回滚。
	previousSite := *site
	candidate := previousSite
	site = &candidate
	site.Name = strings.TrimSpace(dto.Name)
	site.BaseURL = strings.TrimSpace(dto.SiteURL)
	site.RequestedPlatform = dto.Platform
	site.Platform = resolvedPlatform(dto.Platform)
	site.Account = strings.TrimSpace(dto.Account)
	if normalizedAuthMode(dto.AuthMode) == AuthModeUserKey {
		site.Account = strings.TrimSpace(dto.UserID)
	}
	site.Remark = strings.TrimSpace(dto.Remark)
	site.RechargeRate = dto.RechargeRate
	shouldRelogin := hasNewCredentials(dto)

	if shouldRelogin {
		log.Printf("[upstream] 更新站点登录开始 id=%s name=%q host=%s", id, safeUpstreamMessage(dto.Name), safeHost(dto.SiteURL))
		result, loginErr := s.updateLogin(dto)
		if loginErr != nil {
			logSiteFailure("登录失败", id, dto.Name, dto.SiteURL, loginErr)
			return Response{}, loginErr
		}
		// Check existence without replacing the candidate's edited fields.
		current, readErr := s.cache.Get(ctx, id)
		if readErr != nil {
			return Response{}, readErr
		}
		if current == nil || current.UserID != userID || current.AdminAccountID != aid {
			return Response{}, newRequestError(ErrorNotFound, "")
		}
		// Preserve a concurrent enable/disable operation while login was in flight.
		site.Enabled = current.Enabled
		log.Printf("[upstream] 更新站点登录成功 id=%s name=%q", id, safeUpstreamMessage(dto.Name))

		observedAt := s.keyUsageNow()
		now := observedAt.UnixMilli()
		site.BaseURL = result.Session.BaseURL
		site.Platform = result.Platform
		site.Session = &result.Session
		site.Metrics = result.Metrics.WithSyncDate(businesstime.DateAt(observedAt), observedAt)
		site.Status = StatusConnected
		site.ErrorKey = nil
		site.LastSyncedAt = &now

		_ = s.setCachedSite(ctx, site)

		s.mu.Lock()
		s.scheduleSyncLocked(id, site)
		s.mu.Unlock()

		response := toResponse(site)
		if err := s.saveSite(ctx, site); err != nil {
			s.restoreSite(ctx, id, &previousSite)
			return response, err
		}
		s.saveSnapshot(ctx, site)
		return response, nil
	}

	// 无需重新登录：仅更新基本字段。
	_ = s.setCachedSite(ctx, site)
	if err := s.saveSite(ctx, site); err != nil {
		s.restoreSite(ctx, id, &previousSite)
		return toResponse(site), err
	}

	// 重新读取确保返回最新状态。
	site, err = s.cache.Get(ctx, id)
	if err != nil || site == nil || site.UserID != userID {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	return toResponse(site), nil
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
			return syncSiteResult(siteID, toResponse(site), nil)
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

func syncSiteResult(siteID string, response Response, err error) SyncSiteResult {
	if errors.Is(err, context.DeadlineExceeded) || response.syncTimedOut {
		return SyncSiteResult{SiteID: siteID, Status: "timeout", ErrorKey: "site_sync_timeout"}
	}
	if err != nil {
		return SyncSiteResult{SiteID: siteID, Status: "unavailable", ErrorKey: "site_sync_failed"}
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
			responses = append(responses, toResponse(site))
			continue
		}
		if site.Session == nil {
			responses = append(responses, toResponse(site))
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
					response = toResponse(cached)
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
			resp := toResponse(site)
			safeEmit(SyncEvent{Event: SyncEventDone, SiteID: site.ID, Site: &resp})
			continue
		}
		ids = append(ids, site.ID)
	}

	// 有会话的站点并发同步，用 channel 信号量限制并发数。
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}

			log.Printf("[upstream-stream] 开始同步站点 id=%s", id)
			safeEmit(SyncEvent{Event: SyncEventSyncing, SiteID: id})

			response, syncErr := s.sync(ctx, id)
			if syncErr != nil || response.Status != StatusConnected {
				logKey := siteErrorKey(syncErr)
				if syncErr == nil && response.ErrorKey != nil {
					logKey = *response.ErrorKey
				}
				log.Printf("[upstream-stream] 同步失败 id=%s reason=%s", id, logKey)
				if cached, cacheErr := s.cache.Get(ctx, id); cacheErr == nil && cached != nil {
					response = toResponse(cached)
				}
				key := ErrorUnknown
				if syncErr != nil {
					key = siteErrorKey(syncErr)
				} else if response.ErrorKey != nil && *response.ErrorKey != "" {
					key = *response.ErrorKey
				}
				safeEmit(SyncEvent{Event: SyncEventError, SiteID: id, ErrorKey: key, Site: &response})
			} else {
				log.Printf("[upstream-stream] 同步成功 id=%s", id)
				safeEmit(SyncEvent{Event: SyncEventDone, SiteID: id, Site: &response})
			}
		}(id)
	}

	wg.Wait()
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
		if response.keyUsageCostFailure != nil {
			if site, readErr := s.cache.Get(context.Background(), id); readErr == nil && site != nil {
				s.recordKnownKeyUsageCostFailure(*site, *response.keyUsageCostFailure)
			}
			return
		}
		if err == nil && response.Status == StatusConnected {
			if site, readErr := s.cache.Get(context.Background(), id); readErr == nil && site != nil {
				s.enqueueKeyUsageCollection(*site)
			}
		}
	}()
	response, err = s.syncOnce(context.Background(), id)
}

// syncOnce 执行单个站点的同步流程：刷新会话 → 拉取最新指标 → 更新缓存和数据库。
func (s *Service) syncOnce(ctx context.Context, id string) (Response, error) {
	site, err := s.cache.Get(ctx, id)
	if err != nil {
		return Response{}, err
	}
	if site == nil || site.Session == nil {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !site.IsEnabled() {
		return Response{}, newRequestError(ErrorDisabled, "")
	}

	// 标记为同步中。
	site.Status = StatusSyncing
	site.ErrorKey = nil
	_ = s.setCachedSite(ctx, site)
	session := *site.Session

	// 刷新会话并拉取指标（无锁操作，可能耗时较长）。
	syncStartedAt := s.keyUsageNow()
	syncDate := businesstime.DateAt(syncStartedAt) // 同步开始时生成一次新加坡业务日期，所有指标复用。
	refreshedSession, refreshErr := s.platformService.RefreshSession(session)
	metrics := Metrics{}
	if refreshErr == nil {
		metrics, refreshErr = s.platformService.FetchMetrics(refreshedSession)
		if refreshErr == nil {
			metrics = metrics.WithSyncDate(syncDate, s.keyUsageNow())
		}
	}

	// 重新读取站点确认仍存在（可能在同步期间被删除）。
	site, err = s.cache.Get(ctx, id)
	if err != nil || site == nil {
		return Response{}, newRequestError(ErrorNotFound, "")
	}
	if !site.IsEnabled() {
		s.mu.Lock()
		s.clearTimerLocked(id)
		s.mu.Unlock()
		return toResponse(site), newRequestError(ErrorDisabled, "")
	}

	// 在覆盖前保存旧指标，用于同步后的预警检测。
	oldMetrics := site.Metrics

	if refreshErr != nil {
		site.Status = StatusError
		logSiteFailure("同步失败", id, site.Name, site.BaseURL, refreshErr)
		key := siteErrorKey(refreshErr)
		site.ErrorKey = &key
	} else {
		now := s.keyUsageNow().UnixMilli()
		site.Session = &refreshedSession
		metrics = mergeCostReadStatus(metrics, oldMetrics)
		site.Metrics = metrics
		site.Status = StatusConnected
		site.ErrorKey = nil
		site.LastSyncedAt = &now
	}

	_ = s.setCachedSite(ctx, site)

	s.mu.Lock()
	s.scheduleSyncLocked(id, site)
	s.mu.Unlock()

	response := s.toResponse(ctx, site)
	response.syncTimedOut = requestTimedOut(refreshErr)
	if metrics.TodayConsumeStatus == "unreadable" && metrics.TodayConsumeFailedAt != nil {
		if reason := KnownUpstreamCodeErrorKey(metrics.TodayConsumeUpstreamCode); reason != "" {
			response.keyUsageCostFailure = &keyUsageCostFailure{reason: reason, failedAt: *metrics.TodayConsumeFailedAt}
		}
	}
	if saveErr := s.saveSite(ctx, site); saveErr != nil {
		return response, saveErr
	}
	if refreshErr == nil {
		s.saveSnapshot(ctx, site)
		go s.sampleGroupCosts(*site, refreshedSession, append([]GroupInfo(nil), site.Metrics.Groups...))
		if s.AfterSync != nil {
			go s.AfterSync(context.Background(), site.UserID, site.AdminAccountID, site.ID, site.Name, oldMetrics, metrics)
		}
	}
	return response, nil
}

func (s *Service) saveSnapshot(ctx context.Context, site *Site) {
	if s.snapshotWriter == nil {
		return
	}
	if site == nil || strings.TrimSpace(site.UserID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.snapshotWriter.SaveSiteSnapshot(ctx, site.UserID, site.AdminAccountID, site.ID, site.Name, site.Platform, snapshotGroups(site.Metrics.Groups)); err != nil {
		log.Printf("group rate snapshot failed site_id=%s err=%v", site.ID, err)
	}
}

func snapshotGroups(groups []GroupInfo) []SnapshotGroup {
	snapshots := make([]SnapshotGroup, 0, len(groups))
	for _, group := range groups {
		snapshots = append(snapshots, SnapshotGroup{
			ID:         group.ID,
			Name:       group.Name,
			Platform:   group.Platform,
			Multiplier: group.Multiplier,
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
	defer s.mu.Unlock()
	for id := range s.timers {
		s.clearTimerLocked(id)
	}
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
	log.Printf("[upstream-timer] 定时同步已调度 id=%s delay=%s", id, delay)
	s.timers[id] = time.AfterFunc(delay, func() {
		log.Printf("[upstream-timer] 定时同步触发 id=%s", id)
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
func (s *Service) UpdateSettings(ctx context.Context, userID string, siteID string, dto SiteSettings) (Response, error) {
	site, err := s.cache.Get(ctx, siteID)
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
	site.Settings = dto
	_ = s.setCachedSite(ctx, site)
	if saveErr := s.saveSite(ctx, site); saveErr != nil {
		return Response{}, saveErr
	}
	return s.toResponse(ctx, site), nil
}

// UpdateEnabled 只切换本地站点生命周期。停用后停止定时刷新并退出各统计入口；
// 恢复后只安排下一次定时刷新，不立即请求上游。
func (s *Service) UpdateEnabled(ctx context.Context, userID string, siteID string, enabled bool) (Response, error) {
	site, err := s.cache.Get(ctx, siteID)
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

	previousSite := *site
	site.Enabled = boolPointer(enabled)
	if !enabled {
		s.mu.Lock()
		s.clearTimerLocked(siteID)
		s.mu.Unlock()
	}
	if err := s.setCachedSite(ctx, site); err != nil {
		return Response{}, err
	}
	if err := s.saveSite(ctx, site); err != nil {
		s.restoreSite(ctx, siteID, &previousSite)
		return Response{}, err
	}
	if enabled {
		s.mu.Lock()
		s.scheduleSyncLocked(siteID, site)
		s.mu.Unlock()
	}
	return s.toResponse(ctx, site), nil
}

// GetSite 根据 ID 获取站点（供 alert 逻辑读取站点级配置）。
func (s *Service) GetSite(ctx context.Context, siteID string) (*Site, error) {
	return s.cache.Get(ctx, siteID)
}

func (s *Service) toResponse(ctx context.Context, site *Site) Response {
	response := toResponse(site)
	if snapshots, err := s.groupCostSnapshotsForSite(ctx, site); err == nil {
		mergeGroupCostSnapshots(site, &response, snapshots)
	}
	return response
}

func toResponse(site *Site) Response {
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
		Metrics:           site.Metrics,
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
			rawCost, meta, fetchErr := s.platformService.FetchCostForDate(*site.Session, date)
			r.RawCost = rawCost
			r.Meta = meta
			r.Err = fetchErr
			results[idx] = r
		}(i, t.site)
	}
	wg.Wait()
	return results, nil
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
