package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type fakeTargetSchedulableActioner struct {
	calls                int
	wantAccount          string
	err                  error
	afterWrite           func(bool)
	afterWriteForAccount func(accountID string, schedulable bool)
}

type blockingTargetSchedulableActioner struct {
	mu         sync.Mutex
	calls      []string
	started    chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	afterWrite func(accountID string, value bool)
}

func (a *blockingTargetSchedulableActioner) SetSub2APIAdminAccountSchedulable(_ upstream.Session, accountID string, schedulable bool) error {
	a.mu.Lock()
	a.calls = append(a.calls, accountID)
	a.mu.Unlock()
	a.startOnce.Do(func() { close(a.started) })
	<-a.release
	if a.afterWrite != nil {
		a.afterWrite(accountID, schedulable)
	}
	return nil
}

func (a *blockingTargetSchedulableActioner) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (f *fakeTargetSchedulableActioner) SetSub2APIAdminAccountSchedulable(session upstream.Session, accountID string, schedulable bool) error {
	f.calls++
	if f.wantAccount != "" && accountID != f.wantAccount {
		return errors.New("unexpected account")
	}
	if f.err != nil {
		return f.err
	}
	if f.afterWrite != nil {
		f.afterWrite(schedulable)
	}
	if f.afterWriteForAccount != nil {
		f.afterWriteForAccount(accountID, schedulable)
	}
	return nil
}

func schedulableActionService(initial *bool, actioner *fakeTargetSchedulableActioner) (*Service, *fakeRepository) {
	repo := newFakeRepository()
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "1515", Name: "account-1515", Status: "active", Schedulable: initial}},
	}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "default", Platform: string(upstream.PlatformSub2API)}},
		accountsByGrp: accounts,
	}
	if actioner.afterWrite == nil {
		actioner.afterWrite = func(value bool) {
			updated := accounts["g1"]
			updated[0].Schedulable = boolPointer(value)
			accounts["g1"] = updated
		}
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	service.schedulableActions = actioner
	return service, repo
}

func schedulableActionServiceWithGroups(
	groups []upstream.AdminGroupInfo,
	accountsByGroup map[string][]upstream.AdminGroupAccountInfo,
	errByGroup map[string]error,
	actioner TargetSchedulableActioner,
) (*Service, *fakeRepository) {
	repo := newFakeRepository()
	reader := fakePlatformGroupReader{
		groups:        groups,
		accountsByGrp: accountsByGroup,
		errByGrp:      errByGroup,
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	service.schedulableActions = actioner
	return service, repo
}

func schedulableActionServiceWithSurvivor(initial *bool, actioner *fakeTargetSchedulableActioner) (*Service, *fakeRepository) {
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {
			{ID: "1515", Name: "account-1515", Status: "active", Schedulable: initial},
			{ID: "1616", Name: "survivor-1616", Status: "active", Schedulable: boolPointer(true)},
		},
	}
	service, repo := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{{ID: "g1", Name: "default", Platform: string(upstream.PlatformSub2API)}},
		accounts, nil, actioner,
	)
	if actioner.afterWrite == nil {
		actioner.afterWrite = func(value bool) {
			updated := accounts["g1"]
			updated[0].Schedulable = boolPointer(value)
			accounts["g1"] = updated
		}
	}
	return service, repo
}

func TestSetTargetSchedulable_RejectsCrossWorkspaceBeforeWrite(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:other:1515", false)
	if err == nil || err.Error() != ErrorProbeTargetNotFound {
		t.Fatalf("expected cross-workspace target rejection, got %v", err)
	}
	if actioner.calls != 0 || len(repo.events) != 0 {
		t.Fatalf("cross-workspace rejection must not write upstream or audit event: calls=%d events=%+v", actioner.calls, repo.events)
	}
}

func TestSetTargetSchedulable_UpstreamFailureIsNotSuccess(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515", err: errors.New("upstream failed")}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil || err.Error() != ErrorSchedulableActionFailed {
		t.Fatalf("expected safe upstream failure, got %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].Result != SchedulableActionFailed || repo.events[0].ActionSource != ActionSourceUser {
		t.Fatalf("failed user action must be audited, got %+v", repo.events)
	}
	if repo.events[0].RemoteAction != RemoteActionSchedulableDisableFailed {
		t.Fatalf("failed user action must not use a success-looking action code: %+v", repo.events[0])
	}
	groups, listErr := service.AdminGroups(context.Background(), "user1")
	if listErr != nil || len(groups) != 1 || len(groups[0].Accounts) != 2 {
		t.Fatalf("failed action must remain listable: groups=%+v err=%v", groups, listErr)
	}
	account := groups[0].Accounts[0]
	if account.LastSchedulableAction != RemoteActionSchedulableDisableFailed || account.LastSchedulableActionResult != SchedulableActionFailed || account.LastSchedulableActionErrorKey != ErrorSchedulableActionFailed {
		t.Fatalf("account row lost latest failed action result: %+v", account)
	}
	if account.SchedulableSource != "upstream_observed" {
		t.Fatalf("failed action must not own the observed scheduling state: %+v", account)
	}
}

func TestSetTargetSchedulable_ReadbackMissingOrMismatchIsNotSuccess(t *testing.T) {
	tests := []struct {
		name     string
		readback *bool
	}{
		{name: "missing", readback: nil},
		{name: "mismatch", readback: boolPointer(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actioner := &fakeTargetSchedulableActioner{}
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
			actioner.afterWrite = func(bool) {
				reader := service.platformGroups.(fakePlatformGroupReader)
				updated := reader.accountsByGrp["g1"]
				updated[0].Schedulable = tt.readback
				reader.accountsByGrp["g1"] = updated
			}

			_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
			if err == nil || err.Error() != ErrorSchedulableReadbackFailed {
				t.Fatalf("expected readback failure, got %v", err)
			}
			if len(repo.events) != 1 || repo.events[0].Result != SchedulableActionFailed {
				t.Fatalf("readback failure must be audited as failure, got %+v", repo.events)
			}
		})
	}
}

func TestSetTargetSchedulable_ReturnsReadbackAndAuditsUserSource(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)

	result, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err != nil {
		t.Fatalf("unexpected action error: %v", err)
	}
	if result.TargetID != "sub2api:ws1:1515" || result.Schedulable || result.ActionSource != ActionSourceUser || result.ActionAt.IsZero() {
		t.Fatalf("unexpected readback result: %+v", result)
	}
	if actioner.calls != 1 || len(repo.events) != 1 {
		t.Fatalf("expected one narrow write and one audit event: calls=%d events=%+v", actioner.calls, repo.events)
	}
	event := repo.events[0]
	if event.Result != SchedulableActionSucceeded || event.ActionSource != ActionSourceUser || event.RemoteAction != RemoteActionSchedulableDisabled {
		t.Fatalf("unexpected success audit event: %+v", event)
	}
}

func TestSetTargetSchedulable_RejectsDisablingLastUsableAccount(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{{ID: "g1", Name: "only", Platform: string(upstream.PlatformSub2API)}},
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "account-1515", Status: "active", Schedulable: boolPointer(true)}},
		}, nil, actioner,
	)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil {
		t.Fatal("disabling the only active and schedulable account must be rejected")
	}
	if actioner.calls != 0 {
		t.Fatalf("last usable account must not be written upstream, calls=%d", actioner.calls)
	}
	if len(repo.events) != 1 || repo.events[0].ErrorKey != ErrorSub2APIGroupLastUsable || repo.events[0].RemoteAction != RemoteActionSkippedSub2APILastActive || repo.events[0].AdminGroupID != "g1" {
		t.Fatalf("last usable rejection must be audited with group context, events=%+v", repo.events)
	}
}

func TestSetTargetSchedulable_AllowsDisablingOneOfTwoUsableAccounts(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)

	result, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err != nil {
		t.Fatalf("closing one of two usable accounts should succeed: %v", err)
	}
	if result.Schedulable || actioner.calls != 1 || len(repo.events) != 1 || repo.events[0].Result != SchedulableActionSucceeded {
		t.Fatalf("unexpected successful partial closure: result=%+v calls=%d events=%+v", result, actioner.calls, repo.events)
	}
}

func TestSetTargetSchedulable_AllowsIdempotentDisableOfAlreadyUnschedulableAccount(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(false), actioner)

	result, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err != nil {
		t.Fatalf("idempotent schedulable=false should succeed: %v", err)
	}
	if result.Schedulable || actioner.calls != 1 || len(repo.events) != 1 {
		t.Fatalf("unexpected idempotent disable result: result=%+v calls=%d events=%+v", result, actioner.calls, repo.events)
	}
}

func TestSetTargetSchedulable_AllowsRestoringSchedulableAccount(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(false), actioner)

	result, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", true)
	if err != nil {
		t.Fatalf("schedulable=true restore should succeed: %v", err)
	}
	if !result.Schedulable || actioner.calls != 1 || len(repo.events) != 1 || repo.events[0].RemoteAction != RemoteActionSchedulableEnabled {
		t.Fatalf("unexpected schedulable restore result: result=%+v calls=%d events=%+v", result, actioner.calls, repo.events)
	}
}

func TestSetTargetSchedulable_RejectsWhenAnyMembershipWouldLoseLastUsableAccount(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, _ := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{
			{ID: "g1", Name: "only-target", Platform: string(upstream.PlatformSub2API)},
			{ID: "g2", Name: "shared", Platform: string(upstream.PlatformSub2API)},
		},
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(true)}},
			"g2": {
				{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
				{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
			},
		},
		nil,
		actioner,
	)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil {
		t.Fatal("a shared account must not be disabled when any group would lose its last usable account")
	}
	if actioner.calls != 0 {
		t.Fatalf("shared last usable account must not be written upstream, calls=%d", actioner.calls)
	}
}

func TestSetTargetSchedulable_RejectsUnknownSchedulableState(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, _ := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{{ID: "g1", Name: "unknown", Platform: string(upstream.PlatformSub2API)}},
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "account-1515", Status: "active"}},
		}, nil, actioner,
	)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil {
		t.Fatal("unknown schedulable state must fail closed before a destructive write")
	}
	if actioner.calls != 0 {
		t.Fatalf("unknown schedulable state must not be written upstream, calls=%d", actioner.calls)
	}
}

func TestSetTargetSchedulable_RejectsIncompleteInventory(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{
			{ID: "g1", Name: "target", Platform: string(upstream.PlatformSub2API)},
			{ID: "g2", Name: "unreadable", Platform: string(upstream.PlatformSub2API)},
		},
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(true)}},
		},
		map[string]error{"g2": errors.New("accounts unavailable")},
		actioner,
	)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil {
		t.Fatal("incomplete inventory must fail closed before a destructive write")
	}
	if actioner.calls != 0 {
		t.Fatalf("incomplete inventory must not be written upstream, calls=%d", actioner.calls)
	}
	if len(repo.events) != 1 || repo.events[0].ErrorKey != ErrorSub2APIInventoryIncomplete || repo.events[0].RemoteAction != RemoteActionSkippedSub2APIInventory || repo.events[0].AdminGroupID != "g2" {
		t.Fatalf("incomplete inventory audit = %+v", repo.events)
	}
}

func TestSetTargetSchedulable_AllowsIdempotentDisableWithIncompleteOtherGroup(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(false)}},
	}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "target"}, {ID: "g2", Name: "unreadable"}},
		accountsByGrp: accounts,
		errByGrp:      map[string]error{"g2": errors.New("accounts unavailable")},
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	service.schedulableActions = actioner

	result, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err != nil {
		t.Fatalf("idempotent disable should not require unrelated group inventory: %v", err)
	}
	if result.Schedulable || actioner.calls != 1 {
		t.Fatalf("unexpected idempotent disable result: %+v calls=%d", result, actioner.calls)
	}
}

func TestSetTargetSchedulable_RejectsConflictingSharedAccountState(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{
			{ID: "g1", Name: "first", Platform: string(upstream.PlatformSub2API)},
			{ID: "g2", Name: "second", Platform: string(upstream.PlatformSub2API)},
		},
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(false)}},
			"g2": {{ID: "1515", Status: "active", Schedulable: boolPointer(true)}},
		}, nil, actioner,
	)

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil || err.Error() != ErrorSub2APIInventoryIncomplete {
		t.Fatalf("conflicting shared account state must fail closed: %v", err)
	}
	if actioner.calls != 0 {
		t.Fatalf("conflicting shared account state must not write upstream, calls=%d", actioner.calls)
	}
	if len(repo.events) != 1 || repo.events[0].RemoteAction != RemoteActionSkippedSub2APIInventory {
		t.Fatalf("conflicting shared account audit = %+v", repo.events)
	}
}

func TestSetTargetSchedulable_UsesOneFullInventoryAndNarrowReadback(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	reader := &countingAdminInventoryReader{inner: fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "default", Platform: string(upstream.PlatformSub2API)}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {
				{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
				{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
			},
		},
	}}
	actioner.afterWrite = func(value bool) {
		accounts := reader.inner.accountsByGrp["g1"]
		accounts[0].Schedulable = boolPointer(value)
		reader.inner.accountsByGrp["g1"] = accounts
	}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	service.schedulableActions = actioner
	if _, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false); err != nil {
		t.Fatalf("schedulable action failed: %v", err)
	}
	if reader.fetchCalls != 1 || reader.listCalls != 2 {
		t.Fatalf("manual action should use one full inventory plus one narrow readback: fetch=%d list=%d", reader.fetchCalls, reader.listCalls)
	}
}

func TestSetTargetSchedulable_RefreshClearsPersistentReservationAfterRecovery(t *testing.T) {
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {
			{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
			{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
		},
	}
	actioner := &fakeTargetSchedulableActioner{}
	actioner.afterWriteForAccount = func(accountID string, schedulable bool) {
		updated := accounts["g1"]
		for index := range updated {
			if updated[index].ID == accountID {
				updated[index].Schedulable = boolPointer(schedulable)
			}
		}
		accounts["g1"] = updated
	}
	service, _ := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{{ID: "g1", Name: "shared", Platform: string(upstream.PlatformSub2API)}},
		accounts, nil, actioner,
	)

	if _, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if _, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1616", false); err != nil {
		t.Fatalf("fresh inventory after restore must allow closing the other account: %v", err)
	}
	if actioner.calls != 3 {
		t.Fatalf("unexpected write count after close, restore, close: %d", actioner.calls)
	}
}

func TestSetTargetSchedulable_ConcurrentClosuresConsumeAtMostOneUsableAccount(t *testing.T) {
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {
			{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
			{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
		},
	}
	actioner := &blockingTargetSchedulableActioner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, _ := schedulableActionServiceWithGroups(
		[]upstream.AdminGroupInfo{{ID: "g1", Name: "shared", Platform: string(upstream.PlatformSub2API)}},
		accounts,
		nil,
		actioner,
	)

	results := make(chan error, 2)
	go func() {
		_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
		results <- err
	}()
	go func() {
		_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1616", false)
		results <- err
	}()
	select {
	case <-actioner.started:
	case <-time.After(time.Second):
		t.Fatal("concurrent schedulable writes did not start")
	}
	close(actioner.release)
	firstErr := <-results
	secondErr := <-results
	if firstErr == nil && secondErr == nil {
		t.Fatal("concurrent closures must reject at least one last-usable-account removal")
	}
	if actioner.callCount() != 1 {
		t.Fatalf("at most one concurrent closure may reach upstream, calls=%d", actioner.callCount())
	}
}

func TestSetTargetSchedulable_AuditFailureIsNotReportedAsSuccess(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
	repo.insertEventErr = errors.New("audit unavailable")

	_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", false)
	if err == nil || err.Error() != ErrorSchedulableAuditFailed {
		t.Fatalf("audit failure must not be reported as success, got %v", err)
	}
	if actioner.calls != 1 {
		t.Fatalf("upstream write should already have completed once, calls=%d", actioner.calls)
	}
}

func TestSetTargetSchedulableHandler_RequiresBooleanField(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{}
	service, _ := schedulableActionService(boolPointer(true), actioner)
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)

	request := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/sub2api:ws1:1515/schedulable", strings.NewReader(`{}`))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if actioner.calls != 0 {
		t.Fatalf("invalid request must not write upstream, calls=%d", actioner.calls)
	}
}

func TestSetTargetSchedulableHandler_ReturnsConflictForLastUsableGuard(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{}
	service, _ := schedulableActionService(boolPointer(true), actioner)
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)

	request := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/sub2api:ws1:1515/schedulable", strings.NewReader(`{"schedulable":false}`))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("last usable guard status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	if actioner.calls != 0 {
		t.Fatalf("last usable guard must not write upstream, calls=%d", actioner.calls)
	}
}

func TestSetTargetSchedulableHandler_ReturnsVerifiedReadback(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{wantAccount: "1515"}
	service, _ := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)

	request := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/sub2api:ws1:1515/schedulable", strings.NewReader(`{"schedulable":false}`))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var result TargetSchedulableActionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.TargetID != "sub2api:ws1:1515" || result.Schedulable || result.ActionSource != ActionSourceUser || result.ActionAt.IsZero() {
		t.Fatalf("unexpected handler response: %+v", result)
	}
	if actioner.calls != 1 {
		t.Fatalf("handler must perform exactly one narrow upstream write, calls=%d", actioner.calls)
	}
}

func TestSchedulableUserActionMatchesObserved_RejectsStaleDirection(t *testing.T) {
	actionAt := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	disabledEvent := ConnectionHealthEvent{Result: SchedulableActionSucceeded, RemoteAction: RemoteActionSchedulableDisabled, CreatedAt: actionAt}
	observedAtAction := actionAt.Add(-time.Second)
	laterUpstreamChange := actionAt.Add(time.Minute)
	if schedulableUserActionMatchesObserved(disabledEvent, boolPointer(true), &observedAtAction) {
		t.Fatal("a later observed enabled value must not be attributed to the older disable action")
	}
	if !schedulableUserActionMatchesObserved(disabledEvent, boolPointer(false), &observedAtAction) {
		t.Fatal("matching observed value should retain the explicit user-action source")
	}
	if schedulableUserActionMatchesObserved(disabledEvent, boolPointer(false), &laterUpstreamChange) {
		t.Fatal("a later upstream update must not be attributed to the historical user action")
	}
	if schedulableUserActionMatchesObserved(disabledEvent, nil, &observedAtAction) {
		t.Fatal("an unknown observed value cannot be attributed to a historical user action")
	}
	if schedulableUserActionMatchesObserved(disabledEvent, boolPointer(false), nil) {
		t.Fatal("a value without authoritative upstream update time cannot prove user-action ownership")
	}
}
