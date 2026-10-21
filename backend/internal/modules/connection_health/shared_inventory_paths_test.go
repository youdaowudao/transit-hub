package connection_health

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type inventoryPathsReader struct {
	fakePlatformGroupReader
	mu      sync.Mutex
	fetches int
	lists   int
	fetch   func(context.Context, upstream.Session, int) ([]upstream.AdminGroupInfo, error)
	list    func(context.Context, upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error)
}

func (r *inventoryPathsReader) FetchAdminAllGroupsContext(ctx context.Context, session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.mu.Lock()
	r.fetches++
	n := r.fetches
	r.mu.Unlock()
	if r.fetch != nil {
		return r.fetch(ctx, session, n)
	}
	return []upstream.AdminGroupInfo{{ID: "g1", Name: "fixture", Multiplier: inventoryPathsPointer(1.0)}}, nil
}

func (r *inventoryPathsReader) ListAdminGroupAccountsContext(ctx context.Context, session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.mu.Lock()
	r.lists++
	r.mu.Unlock()
	if r.list != nil {
		return r.list(ctx, session, group)
	}
	deadline := time.Now().UTC().Truncate(time.Second)
	return []upstream.AdminGroupAccountInfo{{ID: "a", Name: "original", Status: "active", Priority: inventoryPathsPointer(1), Concurrency: inventoryPathsPointer(2), RateMultiplier: inventoryPathsPointer(1.0), LoadFactor: inventoryPathsPointer(3), Weight: inventoryPathsPointer(4), GroupIDs: []string{"g1"}, Schedulable: inventoryPathsPointer(true), ModelMapping: map[string]string{"m1": "remote"}, UpdatedAt: &deadline, TempUnschedulableUntil: &deadline, RateLimitResetAt: &deadline, OverloadUntil: &deadline, ExpiresAt: &deadline, AutoPauseOnExpired: inventoryPathsPointer(true), ModelRateLimits: []upstream.Sub2APIModelRateLimit{{Model: "m1", ResetAt: &deadline, Known: true}}, QuotaLimit: inventoryPathsPointer(10.0), QuotaUsed: inventoryPathsPointer(1.0), QuotaDailyLimit: inventoryPathsPointer(20.0), QuotaDailyUsed: inventoryPathsPointer(2.0), QuotaWeeklyLimit: inventoryPathsPointer(30.0), QuotaWeeklyUsed: inventoryPathsPointer(3.0), InventoryResponseTimes: []upstream.InventoryResponseTime{{HTTPDate: "original"}}}}, nil
}

func inventoryPathsPointer[T any](value T) *T { return &value }

func (r *inventoryPathsReader) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetches, r.lists
}

// The fallback keeps the unchanged original refresh path executable in the old
// worktree. The candidate always exercises the scheduled shared-read entry.
func inventoryPathsRead(ctx context.Context, s *Service, user, workspace string, session upstream.Session) (adminWorkspaceInventory, error) {
	if shared, ok := any(s).(interface {
		sharedWorkspaceInventory(context.Context, string, string, upstream.Session) (adminWorkspaceInventory, error)
	}); ok {
		return shared.sharedWorkspaceInventory(ctx, user, workspace, session)
	}
	refresh, err := s.refreshAdminTarget(ctx, session, workspace, "a")
	return refresh.inventory, err
}

type inventoryPathsResult struct {
	inventory adminWorkspaceInventory
	err       error
}

type inventoryPathsWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *inventoryPathsWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func inventoryPathsWaitingContext() *inventoryPathsWaitContext {
	return &inventoryPathsWaitContext{Context: context.Background(), entered: make(chan struct{})}
}

func inventoryPathsWaitEntered(t *testing.T, ctx *inventoryPathsWaitContext) {
	t.Helper()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		t.Fatal("inventory waiter did not reach its cancellation wait")
	}
}

func inventoryPathsStart(ctx context.Context, s *Service, user, workspace string, session upstream.Session) <-chan inventoryPathsResult {
	done := make(chan inventoryPathsResult, 1)
	go func() {
		inventory, err := inventoryPathsRead(ctx, s, user, workspace, session)
		done <- inventoryPathsResult{inventory, err}
	}()
	return done
}

func inventoryPathsReceive(t *testing.T, done <-chan inventoryPathsResult) inventoryPathsResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("inventory read did not finish")
		return inventoryPathsResult{}
	}
}

func inventoryPathsService(t *testing.T, reader *inventoryPathsReader) *Service {
	t.Helper()
	s := &Service{repo: newFakeRepository(), platformGroups: reader}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("inventory fixture shutdown: %v", err)
		}
	})
	return s
}

func TestInventoryWaiterCancelKeepsSharedRead(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, _ int) ([]upstream.AdminGroupInfo, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
		}
	}}
	s := inventoryPathsService(t, reader)
	t.Cleanup(unblock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := inventoryPathsStart(ctx, s, "user1", "ws1", upstream.Session{})
	<-started
	secondContext := inventoryPathsWaitingContext()
	second := inventoryPathsStart(secondContext, s, "user1", "ws1", upstream.Session{})
	inventoryPathsWaitEntered(t, secondContext)
	cancel()
	if result := inventoryPathsReceive(t, first); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled waiter result = %v", result.err)
	}
	select {
	case result := <-second:
		t.Fatalf("another waiter stopped with the canceled waiter: %v", result.err)
	case <-time.After(15 * time.Millisecond):
	}
	unblock()
	if result := inventoryPathsReceive(t, second); result.err != nil || len(result.inventory.groups) != 1 {
		t.Fatalf("remaining waiter lost inventory: %v", result.err)
	}
}

func TestSharedInventoryCopiesIndependent(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, _ int) ([]upstream.AdminGroupInfo, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			evidence := &upstream.InventoryTimeEvidence{}
			return []upstream.AdminGroupInfo{{ID: "g1", Multiplier: inventoryPathsPointer(1.0), InventoryResponseTimes: []upstream.InventoryResponseTime{{HTTPDate: "original"}}, InventoryTimeEvidence: evidence}, {ID: "g2", InventoryTimeEvidence: evidence}}, nil
		}
	}}
	s := inventoryPathsService(t, reader)
	t.Cleanup(unblock)
	expires := int64(10)
	session := upstream.Session{ExpiresAt: &expires}
	first := inventoryPathsStart(context.Background(), s, "user1", "ws1", session)
	<-started
	secondContext := inventoryPathsWaitingContext()
	second := inventoryPathsStart(secondContext, s, "user1", "ws1", session)
	inventoryPathsWaitEntered(t, secondContext)
	unblock()
	a, b := inventoryPathsReceive(t, first), inventoryPathsReceive(t, second)
	if a.err != nil || b.err != nil {
		t.Fatalf("inventory read failed: %v / %v", a.err, b.err)
	}
	if a.inventory.groups[0].group.InventoryTimeEvidence == nil || a.inventory.groups[0].group.InventoryTimeEvidence == b.inventory.groups[0].group.InventoryTimeEvidence || a.inventory.groups[0].group.InventoryTimeEvidence != a.inventory.groups[1].group.InventoryTimeEvidence || b.inventory.groups[0].group.InventoryTimeEvidence != b.inventory.groups[1].group.InventoryTimeEvidence {
		t.Fatal("evidence must be independent between waiters and shared within one complete inventory")
	}
	before := b.inventory
	// Save a value-only serialization of every mutable field before changing a.
	want := inventoryPathsMutableValues(before)
	*a.inventory.session.ExpiresAt = 99
	g := &a.inventory.groups[0]
	*g.group.Multiplier = 99
	g.group.Name = "changed"
	g.group.InventoryResponseTimes[0].HTTPDate = "changed"
	account := &g.accounts[0]
	account.Name = "changed"
	account.ModelMapping["m1"] = "changed"
	account.GroupIDs[0] = "changed"
	*account.Priority, *account.Concurrency, *account.LoadFactor, *account.Weight = 99, 99, 99, 99
	*account.RateMultiplier = 99
	*account.Schedulable, *account.AutoPauseOnExpired = false, false
	for _, ptr := range []*time.Time{account.UpdatedAt, account.TempUnschedulableUntil, account.RateLimitResetAt, account.OverloadUntil, account.ExpiresAt, account.ModelRateLimits[0].ResetAt} {
		*ptr = time.Time{}
	}
	account.ModelRateLimits[0].Model = "changed"
	for _, ptr := range []*float64{account.QuotaLimit, account.QuotaUsed, account.QuotaDailyLimit, account.QuotaDailyUsed, account.QuotaWeeklyLimit, account.QuotaWeeklyUsed} {
		*ptr = 99
	}
	account.InventoryResponseTimes[0].HTTPDate = "changed"
	if got := inventoryPathsMutableValues(b.inventory); !reflect.DeepEqual(got, want) {
		t.Fatal("mutating one scheduled inventory copy changed another waiter's result")
	}
	if fetches, lists := reader.counts(); fetches != 1 || lists != 2 {
		t.Fatalf("copy independence must cover one shared read, got %d/%d", fetches, lists)
	}
}

func inventoryPathsMutableValues(inventory adminWorkspaceInventory) []any {
	group := inventory.groups[0]
	a := group.accounts[0]
	return []any{*inventory.session.ExpiresAt, group.group.Name, *group.group.Multiplier, group.group.InventoryResponseTimes[0].HTTPDate, a.Name, a.ModelMapping["m1"], a.GroupIDs[0], *a.Priority, *a.Concurrency, *a.RateMultiplier, *a.LoadFactor, *a.Weight, *a.Schedulable, *a.UpdatedAt, *a.TempUnschedulableUntil, *a.RateLimitResetAt, *a.OverloadUntil, *a.ExpiresAt, *a.AutoPauseOnExpired, a.ModelRateLimits[0].Model, *a.ModelRateLimits[0].ResetAt, *a.QuotaLimit, *a.QuotaUsed, *a.QuotaDailyLimit, *a.QuotaDailyUsed, *a.QuotaWeeklyLimit, *a.QuotaWeeklyUsed, a.InventoryResponseTimes[0].HTTPDate}
}

func TestCompletedInventoryReadIsNotReused(t *testing.T) {
	reader := &inventoryPathsReader{}
	s := inventoryPathsService(t, reader)
	for i := 0; i < 2; i++ {
		if result := inventoryPathsReceive(t, inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})); result.err != nil {
			t.Fatalf("read %d failed: %v", i, result.err)
		}
	}
	if fetches, lists := reader.counts(); fetches != 2 || lists != 2 {
		t.Fatalf("finished read was reused: fetches=%d lists=%d", fetches, lists)
	}
}

func TestDifferentSessionDoesNotShareRead(t *testing.T) {
	for _, field := range []string{"Platform", "BaseURL", "UserID", "AccessToken", "Cookie", "AdminAPIKey", "RefreshToken", "AuthMode", "UserIDConfirmed", "TokenType", "ExpiresAt", "QuotaPerUnit"} {
		t.Run(field, func(t *testing.T) {
			started, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, _ int) ([]upstream.AdminGroupInfo, error) {
				started <- struct{}{}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
				}
			}}
			s := inventoryPathsService(t, reader)
			t.Cleanup(unblock)
			a, b := upstream.Session{}, upstream.Session{}
			value := reflect.ValueOf(&b).Elem().FieldByName(field)
			switch value.Kind() {
			case reflect.String:
				value.SetString("different")
			case reflect.Bool:
				value.SetBool(true)
			case reflect.Float64:
				value.SetFloat(1)
			case reflect.Pointer:
				value.Set(reflect.ValueOf(inventoryPathsPointer(int64(1))))
			}
			first := inventoryPathsStart(context.Background(), s, "user1", "ws1", a)
			<-started
			second := inventoryPathsStart(context.Background(), s, "user1", "ws1", b)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("different session joined an unrelated inventory read")
			}
			unblock()
			if one, two := inventoryPathsReceive(t, first), inventoryPathsReceive(t, second); one.err != nil || two.err != nil {
				t.Fatalf("independent reads failed: %v / %v", one.err, two.err)
			}
			if fetches, _ := reader.counts(); fetches != 2 {
				t.Fatalf("different sessions issued %d reads", fetches)
			}
		})
	}
}

func TestRemoteActionFreshReadNotShared(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	s := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "a")
	inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "a", Status: "active", Schedulable: boolPointer(true)}, {ID: "b", Status: "active", Schedulable: boolPointer(true)}}})
	stageAFloorService(s, inventory)
	started, fresh, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, n int) ([]upstream.AdminGroupInfo, error) {
		if n == 1 {
			close(started)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
		} else if n == 2 {
			close(fresh)
		}
		return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
	}, list: func(context.Context, upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
		return []upstream.AdminGroupAccountInfo{{ID: "a", Status: "active", Schedulable: boolPointer(true)}, {ID: "b", Status: "active", Schedulable: boolPointer(false)}}, nil
	}}
	s.platformGroups = reader
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shared := inventoryPathsStart(ctx, s, "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://127.0.0.1:8080"})
	<-started
	type actionResult struct {
		result targetRemoteActionResult
		err    error
	}
	done := make(chan actionResult, 1)
	go func() {
		result, err := s.reconcileTargetRemoteActionWithFloor(ctx, "user1", "ws1", inventory.session, target, []probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory))
		done <- actionResult{result, err}
	}()
	select {
	case <-fresh:
	case <-time.After(time.Second):
		t.Fatal("destructive action reused an unfinished scheduled read instead of its locked fresh read")
	}
	if result := <-done; result.err != nil || result.result.remoteAction != RemoteActionSkippedSub2APILastActive || len(platform.sub2APICalls) != 0 {
		t.Fatalf("fresh read must protect the last usable account: action=%s err=%v writes=%d", result.result.remoteAction, result.err, len(platform.sub2APICalls))
	}
	unblock()
	if result := inventoryPathsReceive(t, shared); result.err != nil {
		t.Fatalf("scheduled inventory read failed: %v", result.err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestSharedInventorySameSessionValuesCoalesce(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, n int) ([]upstream.AdminGroupInfo, error) {
		if n == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
		}
	}}
	s := inventoryPathsService(t, reader)
	t.Cleanup(unblock)
	a := upstream.Session{ExpiresAt: inventoryPathsPointer(int64(10))}
	b := upstream.Session{ExpiresAt: inventoryPathsPointer(int64(10))}
	first := inventoryPathsStart(context.Background(), s, "user1", "ws1", a)
	<-started
	secondContext := inventoryPathsWaitingContext()
	second := inventoryPathsStart(secondContext, s, "user1", "ws1", b)
	inventoryPathsWaitEntered(t, secondContext)
	unblock()
	if one, two := inventoryPathsReceive(t, first), inventoryPathsReceive(t, second); one.err != nil || two.err != nil {
		t.Fatalf("shared read failed: %v / %v", one.err, two.err)
	}
	if fetches, _ := reader.counts(); fetches != 1 {
		t.Fatalf("equal session values issued %d reads", fetches)
	}
}

func TestSharedInventoryDifferentWorkspaceReadsIndependently(t *testing.T) {
	for _, other := range [][2]string{{"user1", "ws2"}, {"user2", "ws1"}} {
		t.Run(other[0]+"/"+other[1], func(t *testing.T) {
			started, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, _ int) ([]upstream.AdminGroupInfo, error) {
				started <- struct{}{}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
				}
			}}
			s := inventoryPathsService(t, reader)
			t.Cleanup(unblock)
			first := inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})
			<-started
			second := inventoryPathsStart(context.Background(), s, other[0], other[1], upstream.Session{})
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("different workspace joined a scheduled read")
			}
			unblock()
			if one, two := inventoryPathsReceive(t, first), inventoryPathsReceive(t, second); one.err != nil || two.err != nil {
				t.Fatalf("independent reads failed: %v / %v", one.err, two.err)
			}
		})
	}
}

func TestManualRefreshDoesNotJoinScheduledInventory(t *testing.T) {
	started, manual, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, n int) ([]upstream.AdminGroupInfo, error) {
		if n == 1 {
			close(started)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
		} else if n == 2 {
			close(manual)
		}
		return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
	}}
	s := inventoryPathsService(t, reader)
	t.Cleanup(unblock)
	shared := inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})
	<-started
	refresh, err := s.refreshAdminTarget(context.Background(), upstream.Session{}, "ws1", "a")
	if err != nil || !refresh.found {
		t.Fatalf("manual refresh failed: %v", err)
	}
	select {
	case <-manual:
	default:
		t.Fatal("manual refresh did not issue its independent read")
	}
	unblock()
	if result := inventoryPathsReceive(t, shared); result.err != nil {
		t.Fatalf("shared read failed: %v", result.err)
	}
}

func TestSharedInventoryPanicFinishesAndAllowsFreshRead(t *testing.T) {
	for _, step := range []string{"groups", "accounts"} {
		t.Run(step, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reader := &inventoryPathsReader{}
			reader.fetch = func(ctx context.Context, _ upstream.Session, n int) ([]upstream.AdminGroupInfo, error) {
				if n == 1 {
					close(started)
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
					}
					if step == "groups" {
						panic("fixture secret must not escape")
					}
				}
				return []upstream.AdminGroupInfo{{ID: "g1"}}, nil
			}
			reader.list = func(context.Context, upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
				if _, lists := reader.counts(); step == "accounts" && lists == 1 {
					panic("fixture secret must not escape")
				}
				return []upstream.AdminGroupAccountInfo{{ID: "a"}}, nil
			}
			s := inventoryPathsService(t, reader)
			t.Cleanup(unblock)
			first := inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})
			<-started
			secondContext := inventoryPathsWaitingContext()
			second := inventoryPathsStart(secondContext, s, "user1", "ws1", upstream.Session{})
			inventoryPathsWaitEntered(t, secondContext)
			unblock()
			for _, done := range []<-chan inventoryPathsResult{first, second} {
				if result := inventoryPathsReceive(t, done); !errors.Is(result.err, errScheduledInventoryPanic) || len(result.inventory.groups) != 0 {
					t.Fatalf("panic did not close shared read safely: %v", result.err)
				}
			}
			if result := inventoryPathsReceive(t, inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})); result.err != nil || len(result.inventory.groups) != 1 {
				t.Fatalf("panicked shared read blocked its next read: %v", result.err)
			}
			if fetches, _ := reader.counts(); fetches != 2 {
				t.Fatalf("panic failure was reused: reads=%d", fetches)
			}
		})
	}
}

func TestShutdownCancelsAndWaitsForSharedInventory(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reader := &inventoryPathsReader{fetch: func(ctx context.Context, _ upstream.Session, _ int) ([]upstream.AdminGroupInfo, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}}
	s := inventoryPathsService(t, reader)
	t.Cleanup(unblock)
	read := inventoryPathsStart(context.Background(), s, "user1", "ws1", upstream.Session{})
	<-started
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { done <- s.Shutdown(ctx) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the shared reader root")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before the shared reader exited: %v", err)
	case <-time.After(15 * time.Millisecond):
	}
	unblock()
	if result := inventoryPathsReceive(t, read); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("root cancellation result = %v", result.err)
	}
	if err := <-done; err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if _, err := s.sharedWorkspaceInventory(context.Background(), "user1", "ws1", upstream.Session{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed scheduler admitted another shared read: %v", err)
	}
	if fetches, _ := reader.counts(); fetches != 1 {
		t.Fatalf("closed scheduler made %d reads", fetches)
	}
}
