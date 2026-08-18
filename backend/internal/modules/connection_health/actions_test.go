package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type fakeSiteLookup struct {
	site *upstream.Site
	err  error
}

func (f fakeSiteLookup) GetSite(ctx context.Context, siteID string) (*upstream.Site, error) {
	return f.site, f.err
}

type fakeSessionProvider struct {
	session upstream.Session
	err     error
}

func (f fakeSessionProvider) RequireSession(ctx context.Context, userID string, adminAccountID string) (upstream.Session, error) {
	return f.session, f.err
}

type fakePlatformActioner struct {
	err        error
	panicValue any
	calls      []struct {
		channelID string
		weight    int
		status    int
	}
	sub2APICalls []struct {
		accountID string
		status    string
	}
	sub2APIErr error
}

func (f *fakePlatformActioner) UpdateNewAPIChannelWeightStatus(session upstream.Session, channelID string, weight int, status int) error {
	if f.panicValue != nil {
		panic(f.panicValue)
	}
	f.calls = append(f.calls, struct {
		channelID string
		weight    int
		status    int
	}{channelID, weight, status})
	return f.err
}

func (f *fakePlatformActioner) UpdateSub2APIAdminAccountStatus(session upstream.Session, accountID string, status string) error {
	if f.panicValue != nil {
		panic(f.panicValue)
	}
	f.sub2APICalls = append(f.sub2APICalls, struct {
		accountID string
		status    string
	}{accountID, status})
	return f.sub2APIErr
}

func TestActions_NewAPIDegradeSuccess(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformNewAPI}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "channel-42", UserID: "u1", WorkspaceAdminAccountID: "ws1"}
	action, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "newapi_channel_disabled" {
		t.Fatalf("unexpected remote action: %s", action)
	}
	if len(platform.calls) != 1 || platform.calls[0].weight != 0 || platform.calls[0].status != 2 {
		t.Fatalf("expected one call with weight=0 status=2, got %+v", platform.calls)
	}
}

func TestActions_NewAPIDegradeFailurePropagatesAsUnsupported(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformNewAPI}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	platform := &fakePlatformActioner{err: errors.New("upstream 500")}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "channel-42"}
	action, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{})
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if action != RemoteActionUnsupported {
		t.Fatalf("expected unsupported on failure, got %s", action)
	}
}

func TestActions_NewAPIDegradePanicRecovered(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformNewAPI}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	platform := &fakePlatformActioner{panicValue: "boom"}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "channel-42"}

	action, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{})
	if err == nil {
		t.Fatalf("expected panic to surface as error, scheduler must not crash")
	}
	if action != RemoteActionUnsupported {
		t.Fatalf("expected unsupported after panic recovery, got %s", action)
	}
}

// TestActions_Sub2APIDegradeUpdatesAccountInactive 验证 sub2api 自动降级会调用
// UpdateSub2APIAdminAccountStatus(session, accountID, "inactive")，并返回对应的 remoteAction。
func TestActions_Sub2APIDegradeUpdatesAccountInactive(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "sub-account-1", UpstreamKeyID: "key-1"}
	action, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSub2APIStatusInactive {
		t.Fatalf("expected sub2api_account_status_inactive, got %s", action)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "sub-account-1" || platform.sub2APICalls[0].status != "inactive" {
		t.Fatalf("expected one call with accountID=sub-account-1 status=inactive, got %+v", platform.sub2APICalls)
	}
}

// TestActions_Sub2APIRestoreUpdatesAccountActive 验证 sub2api 自动恢复会调用
// UpdateSub2APIAdminAccountStatus(session, accountID, "active")，并返回对应的 remoteAction。
func TestActions_Sub2APIRestoreUpdatesAccountActive(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "sub-account-1"}
	action, err := dispatcher.Restore(context.Background(), conn, ConnectionHealthState{CurrentWeight: 25})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSub2APIStatusActive {
		t.Fatalf("expected sub2api_account_status_active, got %s", action)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "sub-account-1" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("expected one call with accountID=sub-account-1 status=active, got %+v", platform.sub2APICalls)
	}
}

func TestDisableConnection_RejectsLastUsableSub2APIAccount(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{
		connections: []my_sites.RealConnection{{
			ID: "conn-1", UserID: "user1", WorkspaceAdminAccountID: "ws1",
			UpstreamSiteID: "site-1", AdminAccountID: "1515",
		}},
		session: upstream.Session{Platform: upstream.PlatformSub2API},
	}
	site := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	service := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		sites: site,
		platformGroups: fakePlatformGroupReader{
			groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "only", Platform: string(upstream.PlatformSub2API)}},
			accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
				"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(true)}},
			},
		},
		dispatcher: newRemoteActionDispatcher(site, mySites, platform),
	}
	service.sub2APIFloorGuardFor("user1", "ws1").rememberInventory(adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "only"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "1515", Status: "active", Schedulable: boolPointer(true)}},
		}},
	})

	err := service.DisableConnection(context.Background(), "user1", "conn-1")
	if err == nil {
		t.Fatal("legacy disable must reject closing the only active and schedulable account")
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("legacy disable must not write the last usable account upstream: %+v", platform.sub2APICalls)
	}
	if len(repo.events) != 1 {
		t.Fatalf("blocked legacy disable must leave one audit event, got %+v", repo.events)
	}
	event := repo.events[0]
	if event.Result != SchedulableActionFailed || event.ErrorKey != ErrorSub2APIGroupLastUsable || event.RemoteAction != RemoteActionSkippedSub2APILastUsable || event.AdminGroupID != "g1" || event.OwnGroupName != "only" {
		t.Fatalf("blocked legacy disable audit = %+v", event)
	}
}

func TestDisableConnection_AllowsClosingOneOfTwoUsableSub2APIAccountsFromCachedInventory(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{
		connections: []my_sites.RealConnection{{
			ID: "conn-1", UserID: "user1", WorkspaceAdminAccountID: "ws1",
			UpstreamSiteID: "site-1", AdminAccountID: "1515",
		}},
		session: upstream.Session{Platform: upstream.PlatformSub2API},
	}
	site := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	service := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		sites:          site,
		platformGroups: fakePlatformGroupReader{},
		dispatcher:     newRemoteActionDispatcher(site, mySites, platform),
	}
	service.sub2APIFloorGuardFor("user1", "ws1").rememberInventory(adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{{
			group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
			accounts: []upstream.AdminGroupAccountInfo{
				{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
				{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
			},
		}},
	})

	if err := service.DisableConnection(context.Background(), "user1", "conn-1"); err != nil {
		t.Fatalf("closing one of two usable accounts should succeed: %v", err)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "1515" || platform.sub2APICalls[0].status != "inactive" {
		t.Fatalf("expected one inactive write from cached inventory, got %+v", platform.sub2APICalls)
	}
}

func TestDisableConnection_AllowsIdempotentInactiveTargetWithIncompleteOtherGroup(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{
		connections: []my_sites.RealConnection{{
			ID: "conn-1", UserID: "user1", WorkspaceAdminAccountID: "ws1",
			UpstreamSiteID: "site-1", AdminAccountID: "1515",
		}},
		session: upstream.Session{Platform: upstream.PlatformSub2API},
	}
	site := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	service := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		sites: site, platformGroups: fakePlatformGroupReader{},
		dispatcher: newRemoteActionDispatcher(site, mySites, platform),
	}
	service.sub2APIFloorGuardFor("user1", "ws1").rememberInventory(adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{
			{group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "1515", Status: "inactive", Schedulable: boolPointer(true)}}},
			{group: upstream.AdminGroupInfo{ID: "g2", Name: "unreadable"}, err: errors.New("upstream unavailable")},
		},
	})

	if err := service.DisableConnection(context.Background(), "user1", "conn-1"); err != nil {
		t.Fatalf("idempotent disable must ignore unrelated incomplete inventory: %v", err)
	}
}

func TestDisableConnection_RejectsConflictingSharedAccountState(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{
		connections: []my_sites.RealConnection{{
			ID: "conn-1", UserID: "user1", WorkspaceAdminAccountID: "ws1",
			UpstreamSiteID: "site-1", AdminAccountID: "1515",
		}},
		session: upstream.Session{Platform: upstream.PlatformSub2API},
	}
	site := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	service := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		sites: site, platformGroups: fakePlatformGroupReader{},
		dispatcher: newRemoteActionDispatcher(site, mySites, platform),
	}
	service.sub2APIFloorGuardFor("user1", "ws1").rememberInventory(adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{
			{group: upstream.AdminGroupInfo{ID: "g1", Name: "first"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "1515", Status: "active", Schedulable: boolPointer(false)}}},
			{group: upstream.AdminGroupInfo{ID: "g2", Name: "second"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "1515", Status: "active", Schedulable: boolPointer(true)}}},
		},
	})

	err := service.DisableConnection(context.Background(), "user1", "conn-1")
	if err == nil || err.Error() != ErrorSub2APIInventoryIncomplete {
		t.Fatalf("conflicting shared account state must fail closed: %v", err)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("conflicting shared account state must not write upstream: %+v", platform.sub2APICalls)
	}
	if len(repo.events) != 1 || repo.events[0].RemoteAction != RemoteActionSkippedSub2APIInventory {
		t.Fatalf("conflicting shared account audit = %+v", repo.events)
	}
}

func TestDisableConnection_RejectsStaleCachedSub2APIInventory(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{
		connections: []my_sites.RealConnection{{
			ID: "conn-1", UserID: "user1", WorkspaceAdminAccountID: "ws1",
			UpstreamSiteID: "site-1", AdminAccountID: "1515",
		}},
		session: upstream.Session{Platform: upstream.PlatformSub2API},
	}
	site := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	service := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		sites: site, platformGroups: fakePlatformGroupReader{},
		dispatcher: newRemoteActionDispatcher(site, mySites, platform),
	}
	guard := service.sub2APIFloorGuardFor("user1", "ws1")
	guard.rememberInventory(adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{{
			group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
			accounts: []upstream.AdminGroupAccountInfo{
				{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
				{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
			},
		}},
	})
	guard.mu.Lock()
	guard.snapshotAt = time.Now().Add(-2 * schedulerTickInterval)
	guard.mu.Unlock()

	err := service.DisableConnection(context.Background(), "user1", "conn-1")
	if err == nil || err.Error() != ErrorSub2APIInventoryIncomplete {
		t.Fatalf("stale cached inventory error = %v", err)
	}
	if len(platform.sub2APICalls) != 0 || len(repo.events) != 1 {
		t.Fatalf("stale inventory must not write upstream and must audit, calls=%+v events=%+v", platform.sub2APICalls, repo.events)
	}
}

// TestActions_Sub2APIDoesNotUseNewAPIWeightStatus 验证 sub2api 降级/恢复绝不调用
// new-api 专用的 UpdateNewAPIChannelWeightStatus（不把 priority 当 weight 处理）。
func TestActions_Sub2APIDoesNotUseNewAPIWeightStatus(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "sub-account-1"}
	if _, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := dispatcher.Restore(context.Background(), conn, ConnectionHealthState{CurrentWeight: 50}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(platform.calls) != 0 {
		t.Fatalf("sub2api must never call the new-api channel weight/status update, got %d calls", len(platform.calls))
	}
}

// TestActions_Sub2APIRemoteFailureIsReturned 验证平台方法返回错误时，dispatcher 不 panic，
// 错误可被上层记录；remoteAction 必须是 sub2api_account_status_inactive_failed，不能回退成
// unsupported——sub2api 已经支持这个动作，真的发起了调用只是失败了，和「不支持」是两回事，
// 混为一谈会让排查者误判成平台能力问题而不是一次真实的上游调用故障。
func TestActions_Sub2APIRemoteFailureIsReturned(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream 500")}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "sub-account-1"}
	action, err := dispatcher.Degrade(context.Background(), conn, ConnectionHealthState{})
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if action != RemoteActionSub2APIStatusInactiveFailed {
		t.Fatalf("expected sub2api_account_status_inactive_failed, got %s", action)
	}
}

// TestActions_Sub2APIDegradeTargetUpdatesAccountInactive 验证 target 维度的 DegradeTarget
// 直接用调用方传入的 session + AdminProbeTarget.AccountID，不依赖 real_connections。
func TestActions_Sub2APIDegradeTargetUpdatesAccountInactive(t *testing.T) {
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)

	session := upstream.Session{Platform: upstream.PlatformSub2API}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := dispatcher.DegradeTarget(context.Background(), session, target, ConnectionHealthState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSub2APIStatusInactive {
		t.Fatalf("expected sub2api_account_status_inactive, got %s", action)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "acc-1" || platform.sub2APICalls[0].status != "inactive" {
		t.Fatalf("expected one call with accountID=acc-1 status=inactive, got %+v", platform.sub2APICalls)
	}
}

// TestActions_Sub2APIRestoreTargetUpdatesAccountActive 验证 target 维度的 RestoreTarget。
func TestActions_Sub2APIRestoreTargetUpdatesAccountActive(t *testing.T) {
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)

	session := upstream.Session{Platform: upstream.PlatformSub2API}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := dispatcher.RestoreTarget(context.Background(), session, target, ConnectionHealthState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSub2APIStatusActive {
		t.Fatalf("expected sub2api_account_status_active, got %s", action)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "acc-1" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("expected one call with accountID=acc-1 status=active, got %+v", platform.sub2APICalls)
	}
}

// TestActions_Sub2APIDegradeTargetFailureReturnsFailedAction 验证 target 维度 DegradeTarget
// 在 UpdateSub2APIAdminAccountStatus 返回 error 时，返回 sub2api_account_status_inactive_failed
// 而不是 unsupported（unsupported 只应表示这个平台/维度本身不支持远端动作）。
func TestActions_Sub2APIDegradeTargetFailureReturnsFailedAction(t *testing.T) {
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream 500")}
	dispatcher := newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)

	session := upstream.Session{Platform: upstream.PlatformSub2API}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := dispatcher.DegradeTarget(context.Background(), session, target, ConnectionHealthState{})
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if action != RemoteActionSub2APIStatusInactiveFailed {
		t.Fatalf("expected sub2api_account_status_inactive_failed, got %s", action)
	}
}

// TestActions_Sub2APIRestoreTargetFailureReturnsFailedAction 验证 target 维度 RestoreTarget
// 失败时返回 sub2api_account_status_active_failed，不能回退成 unsupported。
func TestActions_Sub2APIRestoreTargetFailureReturnsFailedAction(t *testing.T) {
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream 500")}
	dispatcher := newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)

	session := upstream.Session{Platform: upstream.PlatformSub2API}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := dispatcher.RestoreTarget(context.Background(), session, target, ConnectionHealthState{})
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if action != RemoteActionSub2APIStatusActiveFailed {
		t.Fatalf("expected sub2api_account_status_active_failed, got %s", action)
	}
}

// TestActions_NewAPITargetRemoteActionDisablesChannel 验证独立 New API target 降级时直接更新
// channel weight/status，不依赖旧 RealConnection。
func TestActions_NewAPITargetRemoteActionDisablesChannel(t *testing.T) {
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)

	session := upstream.Session{Platform: upstream.PlatformNewAPI}
	target := AdminProbeTarget{TargetID: "newapi:ws1:100", Platform: string(upstream.PlatformNewAPI), AccountID: "100"}
	action, err := dispatcher.DegradeTarget(context.Background(), session, target, ConnectionHealthState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "newapi_channel_disabled" {
		t.Fatalf("expected newapi channel disable action, got %s", action)
	}
	if len(platform.sub2APICalls) != 0 || len(platform.calls) != 1 || platform.calls[0].weight != 0 || platform.calls[0].status != 2 {
		t.Fatalf("expected one newapi disable call, sub2api=%+v newapi=%+v", platform.sub2APICalls, platform.calls)
	}
}

func TestActions_NewAPIRestoreUsesCurrentWeight(t *testing.T) {
	sites := fakeSiteLookup{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformNewAPI}}
	sessions := fakeSessionProvider{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	platform := &fakePlatformActioner{}
	dispatcher := newRemoteActionDispatcher(sites, sessions, platform)

	conn := my_sites.RealConnection{UpstreamSiteID: "site-1", AdminAccountID: "channel-42"}
	state := ConnectionHealthState{CurrentWeight: 25}
	action, err := dispatcher.Restore(context.Background(), conn, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "newapi_channel_weight_25" {
		t.Fatalf("unexpected remote action: %s", action)
	}
	if len(platform.calls) != 1 || platform.calls[0].weight != 25 || platform.calls[0].status != 1 {
		t.Fatalf("expected weight=25 status=1, got %+v", platform.calls)
	}
}
