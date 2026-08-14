package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/admin_accounts"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type fakeSessionStore struct {
	records        map[string]*AdminSession
	activeSessions []ActiveSessionRef
	activeErr      error
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{records: map[string]*AdminSession{}}
}

func (f *fakeSessionStore) key(userID, adminAccountID string) string {
	return userID + "|" + adminAccountID
}
func (f *fakeSessionStore) set(userID, adminAccountID string, session AdminSession) {
	f.records[f.key(userID, adminAccountID)] = &session
}
func (f *fakeSessionStore) Get(_ context.Context, userID, adminAccountID string) (*AdminSession, error) {
	return f.records[f.key(userID, adminAccountID)], nil
}
func (f *fakeSessionStore) Save(_ context.Context, userID, adminAccountID string, session AdminSession) error {
	f.set(userID, adminAccountID, session)
	return nil
}
func (f *fakeSessionStore) Delete(_ context.Context, userID, adminAccountID string) error {
	delete(f.records, f.key(userID, adminAccountID))
	return nil
}
func (f *fakeSessionStore) ActiveSessions(context.Context) ([]ActiveSessionRef, error) {
	return f.activeSessions, f.activeErr
}

type fakeAdminAccounts struct {
	current     map[string]string
	upsertInput admin_accounts.UpsertInput
}

func (f *fakeAdminAccounts) RequireCurrentID(_ context.Context, userID string) (string, error) {
	id, ok := f.current[userID]
	if !ok {
		return "", requestError(ErrorAdminOnly)
	}
	return id, nil
}
func (f *fakeAdminAccounts) UpsertAndSwitch(_ context.Context, userID string, input admin_accounts.UpsertInput) (admin_accounts.Account, error) {
	f.upsertInput = input
	return admin_accounts.Account{ID: f.current[userID], UserID: userID, Platform: input.Platform}, nil
}

type fakePlatformClient struct {
	verifyAdminErr       error
	usageStats           float64
	usageStatsErr        error
	capturedUsageStart   string
	capturedUsageEnd     string
	siteBalance          upstream.AdminSiteBalance
	siteBalanceErr       error
	groups               []upstream.GroupInfo
	groupsErr            error
	adminGroups          []upstream.AdminGroupInfo
	adminGroupsErr       error
	dailyStats           []upstream.GroupDailyStat
	dailyStatsErr        error
	scopeUsage           map[string]upstream.AdminUsageStats
	scopeUsageErr        map[string]error
	capturedGroupDate    string
	refreshSessionErr    error
	refreshSessionResult *upstream.Session
	adminKeyResult       *upstream.Session
	adminKeyErr          error
	capturedAdminKey     string
	capturedUserID       string
	capturedPlatform     upstream.Platform
}

func (f *fakePlatformClient) NormalizeURL(value string) (string, error) { return value, nil }
func (f *fakePlatformClient) LoginAdmin(string, upstream.Platform, string, string) (upstream.Session, error) {
	return upstream.Session{}, errors.New("not implemented")
}
func (f *fakePlatformClient) LoginAdminWithKey(_ string, platform upstream.Platform, key, userID string) (upstream.Session, error) {
	f.capturedAdminKey, f.capturedUserID, f.capturedPlatform = key, userID, platform
	if f.adminKeyErr != nil {
		return upstream.Session{}, f.adminKeyErr
	}
	if f.adminKeyResult != nil {
		return *f.adminKeyResult, nil
	}
	return upstream.Session{}, errors.New("not implemented")
}
func (f *fakePlatformClient) VerifyAdmin(upstream.Session) error { return f.verifyAdminErr }
func (f *fakePlatformClient) RefreshSession(session upstream.Session) (upstream.Session, error) {
	if f.refreshSessionErr != nil {
		return upstream.Session{}, f.refreshSessionErr
	}
	if f.refreshSessionResult != nil {
		return *f.refreshSessionResult, nil
	}
	return session, nil
}
func (f *fakePlatformClient) FetchAdminUsageStats(_ upstream.Session, startDate, endDate string) (float64, error) {
	f.capturedUsageStart, f.capturedUsageEnd = startDate, endDate
	return f.usageStats, f.usageStatsErr
}
func (f *fakePlatformClient) FetchAdminUsageStatsForScope(_ upstream.Session, accountID, groupID, _ string, _ string) (upstream.AdminUsageStats, error) {
	key := accountID + "|" + groupID
	if err := f.scopeUsageErr[key]; err != nil {
		return upstream.AdminUsageStats{}, err
	}
	return f.scopeUsage[key], nil
}
func (f *fakePlatformClient) FetchAdminSiteBalanceFiltered(upstream.Session, upstream.BalanceFilter) (upstream.AdminSiteBalance, error) {
	return f.siteBalance, f.siteBalanceErr
}
func (f *fakePlatformClient) FetchAdminGroups(upstream.Session) ([]upstream.GroupInfo, error) {
	return f.groups, f.groupsErr
}
func (f *fakePlatformClient) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return f.adminGroups, f.adminGroupsErr
}
func (f *fakePlatformClient) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	return nil, nil
}
func (f *fakePlatformClient) FetchAdminGroupDailyStats(upstream.Session, []upstream.GroupInfo) ([]upstream.GroupDailyStat, error) {
	return f.dailyStats, f.dailyStatsErr
}
func (f *fakePlatformClient) FetchAdminGroupDailyStatsForDate(_ upstream.Session, _ []upstream.GroupInfo, date string) ([]upstream.GroupDailyStat, error) {
	f.capturedGroupDate = date
	return f.dailyStats, f.dailyStatsErr
}
func (f *fakePlatformClient) FetchSub2APIAdminGroupDailyStatsByIDForDate(_ upstream.Session, date string) ([]upstream.GroupDailyStat, error) {
	f.capturedGroupDate = date
	return f.dailyStats, f.dailyStatsErr
}
func (f *fakePlatformClient) LoginSub2APIAdmin(string, string, string) (upstream.Session, error) {
	return upstream.Session{}, errors.New("not implemented")
}
func (f *fakePlatformClient) LoginWithToken(string, upstream.Platform, string, string, string, string) (upstream.LoginResult, error) {
	return upstream.LoginResult{}, errors.New("not implemented")
}
func (f *fakePlatformClient) VerifySub2APIAdmin(upstream.Session) error { return nil }
func (f *fakePlatformClient) FetchSub2APIAdminUsageStats(upstream.Session, string, string) (float64, error) {
	return 0, nil
}
func (f *fakePlatformClient) FetchSub2APIAdminSiteBalanceFiltered(upstream.Session, upstream.BalanceFilter) (upstream.AdminSiteBalance, error) {
	return upstream.AdminSiteBalance{}, nil
}
func (f *fakePlatformClient) FetchSub2APIAdminGroups(upstream.Session) ([]upstream.GroupInfo, error) {
	return nil, nil
}
func (f *fakePlatformClient) FetchSub2APIAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return nil, nil
}
func (f *fakePlatformClient) FetchCostForDate(upstream.Session, string) (float64, upstream.CostFetchMeta, error) {
	return 0, upstream.CostFetchMeta{}, nil
}

func authenticatedSession() upstream.Session {
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://example.com", AccessToken: "token"}
}

func groupUsageItemByID(t *testing.T, groups []GroupUsageTodayItem, groupID string) GroupUsageTodayItem {
	t.Helper()
	for _, group := range groups {
		if group.GroupID == groupID {
			return group
		}
	}
	t.Fatalf("group %q not found", groupID)
	return GroupUsageTodayItem{}
}

func TestGroupUsageTodayUnauthenticated(t *testing.T) {
	service := NewMetricsService(newFakeSessionStore(), &fakePlatformClient{}, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	_, err := service.GroupUsageToday(context.Background(), "user-1")
	var reqErr requestError
	if !errors.As(err, &reqErr) || reqErr.Error() != ErrorAdminOnly {
		t.Fatalf("expected ErrorAdminOnly, got %v", err)
	}
}

func TestGroupUsageTodayReturnsMainAuthoritativeRevenue(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	platform := &fakePlatformClient{
		adminGroups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "主站分组一"}, {ID: "group-2", Name: "主站分组二"}},
		dailyStats: []upstream.GroupDailyStat{
			{GroupID: "group-1", GroupName: "过期名称", TodayActualCost: 100},
			{GroupID: "group-1", GroupName: "过期名称", TodayActualCost: 25},
			{GroupID: "group-2", GroupName: "主站分组二", TodayActualCost: 40},
		},
	}
	service := NewMetricsService(store, platform, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	response, err := service.GroupUsageToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupUsageToday() error: %v", err)
	}
	if response.Total != 165 || response.TotalRevenue != 165 {
		t.Fatalf("total = %+v, want 165", response)
	}
	group1 := groupUsageItemByID(t, response.Groups, "group-1")
	group2 := groupUsageItemByID(t, response.Groups, "group-2")
	if group1.GroupName != "主站分组一" || group1.TodayRevenue != 125 || group2.TodayRevenue != 40 {
		t.Fatalf("unexpected authoritative groups: %+v", response.Groups)
	}
}

func TestGroupUsageTodayUsesExplicitBusinessDate(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	platform := &fakePlatformClient{}
	service := NewMetricsService(store, platform, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	response, err := service.GroupUsageToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupUsageToday() error: %v", err)
	}
	if response.Date == "" || platform.capturedGroupDate != response.Date {
		t.Fatalf("response date=%q, captured date=%q", response.Date, platform.capturedGroupDate)
	}
}

func TestGroupUsageTodayUsesLastSuccessfulRevenueWhenRefreshFails(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	observedAt := time.Now().Add(-time.Hour)
	revenue := 17.25
	repo := &fakeMetricsRepository{groupMetricCache: []GroupMetricCacheItem{{
		MetricType: "revenue", GroupID: "5", GroupName: "分组五", TodayRevenue: &revenue, ObservedAt: observedAt,
	}}}
	service := NewMetricsService(store, &fakePlatformClient{adminGroupsErr: errors.New("main group refresh failed")}, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	response, err := service.GroupUsageToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupUsageToday() error: %v", err)
	}
	if !response.Fallback || response.FallbackAt == nil || !response.FallbackAt.Equal(observedAt) || response.TotalRevenue != 17.25 {
		t.Fatalf("group revenue fallback = %+v", response)
	}
}

type fakeRealConnectionReader struct {
	connections []my_sites.RealConnection
	err         error
}

func (f fakeRealConnectionReader) ListRealConnectionsForWorkspace(context.Context, string, string) ([]my_sites.RealConnection, error) {
	return f.connections, f.err
}

func TestGroupUsageTodayDoesNotWaitForDirectProfitReads(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	platform := &fakePlatformClient{
		adminGroups: []upstream.AdminGroupInfo{{ID: "5", Name: "分组五"}},
		dailyStats:  []upstream.GroupDailyStat{{GroupID: "5", GroupName: "分组五", TodayActualCost: 17.25}},
		scopeUsage: map[string]upstream.AdminUsageStats{
			"93|5": {TotalActualCost: 10},
		},
	}
	upstreams := &fakeUpstreamLister{keyUsageItems: []upstream.KeyUsageTodayItem{{SiteID: "site-1", KeyID: "key-93", TodayAmount: 4}}}
	service := NewMetricsService(store, platform, upstreams, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.SetRealConnectionReader(fakeRealConnectionReader{connections: []my_sites.RealConnection{
		{ID: "connection-93", Status: "active", UpstreamSiteID: "site-1", UpstreamKeyID: "key-93", AdminAccountID: "93", OwnGroupIDs: []string{"5"}},
		{ID: "ambiguous", Status: "active", UpstreamSiteID: "site-1", UpstreamKeyID: "key-other", AdminAccountID: "94", OwnGroupIDs: []string{"5", "10"}},
	}})

	response, err := service.GroupUsageToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupUsageToday() error: %v", err)
	}
	group := groupUsageItemByID(t, response.Groups, "5")
	if group.TodayRevenue != 17.25 {
		t.Fatalf("authoritative group revenue = %.2f, want 17.25", group.TodayRevenue)
	}
	if group.DirectRevenue != nil || group.DirectCost != nil || group.TodayProfit != nil {
		t.Fatalf("group revenue endpoint unexpectedly waited for direct profit: %+v", group)
	}
	if upstreams.keyUsageCalls != 0 {
		t.Fatalf("GroupUsageToday() queried upstream key cost %d time(s)", upstreams.keyUsageCalls)
	}
}

func TestGroupProfitTodayReturnsReliableDirectProfit(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	platform := &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{
		"93|5": {TotalActualCost: 10},
	}}
	upstreams := &fakeUpstreamLister{keyUsageItems: []upstream.KeyUsageTodayItem{{SiteID: "site-1", KeyID: "key-93", TodayAmount: 4}}}
	repo := &fakeMetricsRepository{}
	service := NewMetricsService(store, platform, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.SetRealConnectionReader(fakeRealConnectionReader{connections: []my_sites.RealConnection{{
		ID: "connection-93", Status: "active", UpstreamSiteID: "site-1", UpstreamKeyID: "key-93",
		AdminAccountID: "93", OwnGroupIDs: []string{"5"}, OwnGroupNames: []string{"分组五"},
	}}})

	response, err := service.GroupProfitToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupProfitToday() error: %v", err)
	}
	group := groupUsageItemByID(t, response.Groups, "5")
	if group.DirectRevenue == nil || *group.DirectRevenue != 10 || group.DirectCost == nil || *group.DirectCost != 4 || group.TodayProfit == nil || *group.TodayProfit != 6 {
		t.Fatalf("direct group profit = %+v, want revenue=10 cost=4 profit=6", group)
	}
	if response.TotalProfit != 6 || response.FallbackGroups != 0 || response.UnavailableGroups != 0 {
		t.Fatalf("profit response = %+v", response)
	}
}

func TestGroupProfitTodayUnauthenticated(t *testing.T) {
	service := NewMetricsService(newFakeSessionStore(), &fakePlatformClient{}, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	_, err := service.GroupProfitToday(context.Background(), "user-1")
	var reqErr requestError
	if !errors.As(err, &reqErr) || reqErr.Error() != ErrorAdminOnly {
		t.Fatalf("expected ErrorAdminOnly, got %v", err)
	}
}

func TestGroupProfitTodayKeepsLastSuccessfulValueForFailedGroup(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	observedAt := time.Now().Add(-time.Hour)
	oldRevenue, oldCost, oldProfit := 10.0, 4.0, 6.0
	repo := &fakeMetricsRepository{groupMetricCache: []GroupMetricCacheItem{{
		MetricType: "profit", GroupID: "5", GroupName: "分组五",
		DirectRevenue: &oldRevenue, DirectCost: &oldCost, TodayProfit: &oldProfit, ObservedAt: observedAt,
	}}}
	service := NewMetricsService(store, &fakePlatformClient{}, &fakeUpstreamLister{keyUsageErr: errors.New("upstream unavailable")}, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.SetRealConnectionReader(fakeRealConnectionReader{connections: []my_sites.RealConnection{{
		ID: "connection-93", Status: "active", UpstreamSiteID: "site-1", UpstreamKeyID: "key-93",
		AdminAccountID: "93", OwnGroupIDs: []string{"5"}, OwnGroupNames: []string{"分组五"},
	}}})

	response, err := service.GroupProfitToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GroupProfitToday() error: %v", err)
	}
	group := groupUsageItemByID(t, response.Groups, "5")
	if group.TodayProfit == nil || *group.TodayProfit != 6 || response.FallbackGroups != 1 || response.UnavailableGroups != 0 {
		t.Fatalf("profit fallback response = %+v", response)
	}
}
