package my_sites

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

type scheduleConfigFake struct {
	items []settings.MainAccountCheckWorkspace
}

func (f *scheduleConfigFake) ListMainAccountCheckWorkspaces(context.Context) ([]settings.MainAccountCheckWorkspace, error) {
	return f.items, nil
}
func (f *scheduleConfigFake) ReadMainAccountCheckConfig(_ context.Context, scope workspacescope.Scope, _ bool) (settings.MainAccountCheckWorkspace, error) {
	for _, v := range f.items {
		if v.Scope == scope {
			return v, nil
		}
	}
	return settings.MainAccountCheckWorkspace{}, workspacescope.ErrChanged
}

type scheduleMarkersFake struct {
	values map[string]string
	fail   bool
}

func (m *scheduleMarkersFake) claim(_ context.Context, k string, v []byte) (bool, error) {
	if m.fail {
		return false, errors.New("fixture")
	}
	if _, ok := m.values[k]; ok {
		return false, nil
	}
	m.values[k] = string(v)
	return true, nil
}
func (m *scheduleMarkersFake) complete(_ context.Context, k string, old, v []byte) error {
	if m.values[k] != string(old) {
		return errors.New("changed")
	}
	m.values[k] = string(v)
	return nil
}
func (m *scheduleMarkersFake) read(_ context.Context, keys []string) ([]any, error) {
	if m.fail {
		return nil, errors.New("fixture")
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		if v, ok := m.values[k]; ok {
			out[i] = v
		}
	}
	return out, nil
}
func TestInventoryScheduleDueAndDedup(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, businesstime.Location())
	cfg := &scheduleConfigFake{}
	for _, id := range []string{"a", "b", "c"} {
		cfg.items = append(cfg.items, settings.MainAccountCheckWorkspace{Scope: workspacescope.Scope{UserID: "u", AdminAccountID: id, CreatedAt: "2026-10-01T00:00:00Z"}, Config: settings.MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "09:01", "18:00", "22:00"}}})
	}
	markers := &scheduleMarkersFake{values: map[string]string{}}
	var called []string
	scheduler := &InventorySchedule{configs: cfg, markers: markers, now: func() time.Time { return now }, wake: make(chan struct{}, 1), check: func(_ context.Context, scope workspacescope.Scope, current bool, site string) (ScopedCheckResult, error) {
		if current || site != "" {
			t.Fatal("scheduled used selected workspace or partial scope")
		}
		called = append(called, scope.AdminAccountID)
		now = now.Add(35 * time.Second)
		return ScopedCheckResult{ConfirmedStatuses: map[string]string{"x": "missing"}}, nil
	}}
	scheduler.collect(t.Context(), now.Add(-time.Minute))
	if len(scheduler.queue) != 0 {
		t.Fatal("unconfigured minute scheduled")
	}
	scheduler.collect(t.Context(), now)
	first := append([]scheduledAccountCheck{}, scheduler.queue...)
	if len(first) != 3 {
		t.Fatal("same-minute workspaces lost")
	}
	job, _ := scheduler.pop()
	scheduler.run(t.Context(), job)
	// Next minute is collected while the serial worker is still draining the first batch.
	now = time.Date(2026, 11, 27, 9, 1, 0, 0, businesstime.Location())
	scheduler.collect(t.Context(), now)
	// Closing c's schedule after it was queued must prevent its remote check.
	cfg.items[2].Config.Enabled = false
	for {
		job, ok := scheduler.pop()
		if !ok {
			break
		}
		scheduler.run(t.Context(), job)
	}
	if len(called) != 4 || called[0] != "a" || called[1] != "b" || called[2] != "a" || called[3] != "b" {
		t.Fatalf("due queue wrong: %v", called)
	}
	for _, job := range first {
		scheduler.run(t.Context(), job)
	}
	if len(called) != 4 {
		t.Fatal("same day slot repeated")
	}
	recent, err := scheduler.RecentMainAccountCheck(t.Context(), cfg.items[0].Scope)
	if err != nil || recent == nil || recent.Status != "success" || recent.ConfirmedMissing != 1 {
		t.Fatalf("result not visible: %+v %v", recent, err)
	}
	markers.fail = true
	now = time.Date(2026, 11, 27, 18, 0, 0, 0, businesstime.Location())
	scheduler.collect(t.Context(), now)
	for {
		job, ok := scheduler.pop()
		if !ok {
			break
		}
		scheduler.run(t.Context(), job)
	}
	if len(called) != 4 {
		t.Fatal("Redis failure still contacted main site")
	}
}

func TestInventoryScheduleAllThreeAndMissedDay(t *testing.T) {
	now := time.Date(2026, 11, 27, 18, 0, 0, 0, businesstime.Location())
	cfg := &scheduleConfigFake{}
	for _, id := range []string{"a", "b", "c"} {
		cfg.items = append(cfg.items, settings.MainAccountCheckWorkspace{Scope: workspacescope.Scope{UserID: "u", AdminAccountID: id}, Config: settings.MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "13:00", "18:00", "22:00"}}})
	}
	var calls []string
	s := &InventorySchedule{configs: cfg, markers: &scheduleMarkersFake{values: map[string]string{}}, now: func() time.Time { return now }, wake: make(chan struct{}, 1), check: func(_ context.Context, scope workspacescope.Scope, _ bool, _ string) (ScopedCheckResult, error) {
		calls = append(calls, scope.AdminAccountID)
		now = now.Add(40 * time.Second)
		return ScopedCheckResult{}, nil
	}}
	s.collect(t.Context(), now)
	for {
		job, ok := s.pop()
		if !ok {
			break
		}
		s.run(t.Context(), job)
	}
	if len(calls) != 3 || calls[2] != "c" || now.Minute() != 2 {
		t.Fatalf("three same-minute workspaces starved: %v %v", calls, now)
	}
	now = time.Date(2026, 11, 27, 22, 0, 0, 0, businesstime.Location())
	s.collect(t.Context(), now)
	now = now.AddDate(0, 0, 1)
	for {
		job, ok := s.pop()
		if !ok {
			break
		}
		s.run(t.Context(), job)
	}
	if len(calls) != 3 {
		t.Fatal("old date ran after midnight")
	}
}

func TestInventoryScheduleChangedTimesAndDedup(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, businesstime.Location())
	cfg := &scheduleConfigFake{items: []settings.MainAccountCheckWorkspace{
		{Scope: workspacescope.Scope{AdminAccountID: "a"}, Config: settings.MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "13:00", "18:00", "22:00"}}},
		{Scope: workspacescope.Scope{AdminAccountID: "b"}, Config: settings.MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "13:00", "18:00", "22:00"}}},
	}}
	var calls []string
	s := &InventorySchedule{configs: cfg, markers: &scheduleMarkersFake{values: map[string]string{}}, now: func() time.Time { return now }, wake: make(chan struct{}, 1), check: func(_ context.Context, scope workspacescope.Scope, _ bool, _ string) (ScopedCheckResult, error) {
		calls = append(calls, scope.AdminAccountID)
		return ScopedCheckResult{ConfirmedStatuses: map[string]string{"x": "active"}}, nil
	}}
	drain := func() {
		for {
			job, ok := s.pop()
			if !ok {
				return
			}
			s.run(t.Context(), job)
		}
	}
	s.collect(t.Context(), now)
	// b was already queued; changing its time must cancel this old job.
	cfg.items[1].Config.Times[0] = "09:30"
	drain()
	if len(calls) != 1 || calls[0] != "a" {
		t.Fatalf("old queued time still ran: %v", calls)
	}
	// a's first slot already ran. Changing just its time cannot run it twice.
	cfg.items[0].Config.Times[0] = "09:30"
	now = now.Add(30 * time.Minute)
	s.collect(t.Context(), now)
	drain()
	if len(calls) != 2 || calls[1] != "b" {
		t.Fatalf("changed time ran duplicate or missed new time: %v", calls)
	}
	now = now.AddDate(0, 0, 1)
	s.collect(t.Context(), now)
	drain()
	if len(calls) != 4 || calls[2] != "a" || calls[3] != "b" {
		t.Fatalf("new day did not reset slot: %v", calls)
	}
}

func TestInventoryScheduleStartShutdown(t *testing.T) {
	// Start within a minute already past its scheduled instant. The real
	// minute collector will wake at the next slot; no missed slot is replayed.
	base := time.Date(2026, 11, 27, 9, 0, 59, 700_000_000, businesstime.Location())
	start := time.Now()
	cfg := &scheduleConfigFake{}
	for _, id := range []string{"a", "b", "c"} {
		cfg.items = append(cfg.items, settings.MainAccountCheckWorkspace{Scope: workspacescope.Scope{AdminAccountID: id}, Config: settings.MainAccountCheckConfig{Enabled: true, Times: []string{"09:00", "09:01", "18:00", "22:00"}}})
	}
	markers := &scheduleMarkersFake{values: map[string]string{}}
	called := make(chan string, 3)
	cancelled := make(chan struct{}, 1)
	s := &InventorySchedule{configs: cfg, markers: markers, now: func() time.Time { return base.Add(time.Since(start)) }, wake: make(chan struct{}, 1), check: func(ctx context.Context, scope workspacescope.Scope, _ bool, _ string) (ScopedCheckResult, error) {
		called <- scope.AdminAccountID
		<-ctx.Done()
		cancelled <- struct{}{}
		return ScopedCheckResult{}, ctx.Err()
	}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	s.Start()
	s.Start() // repeat startup cannot create another collector or worker
	select {
	case got := <-called:
		if got != "a" {
			t.Fatalf("serial worker order: %s", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("minute collector did not dispatch next slot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown did not cancel active check")
	}
	select {
	case id := <-called:
		t.Fatalf("shutdown started queued check: %s", id)
	default:
	}
	if len(s.queue) != 0 {
		t.Fatal("shutdown retained queued work")
	}
	if len(markers.values) != 1 {
		t.Fatalf("shutdown or missed slot created extra markers: %d", len(markers.values))
	}
	if _, ok := markers.values[accountCheckKey(cfg.items[0].Scope, "2026-11-27", 1)]; !ok {
		t.Fatal("startup replayed missed slot or never dispatched next slot")
	}
}

// Explicit real Redis fixture: only this marker is created and removed.
func TestInventoryScheduleRedisMarker(t *testing.T) {
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("explicit TEST_REDIS_URL required")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal("invalid explicit Redis fixture URL")
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	scope := workspacescope.Scope{UserID: fmt.Sprintf("fixture-account-check-%d", time.Now().UnixNano()), AdminAccountID: "workspace", CreatedAt: "2026-11-27T01:00:00Z"}
	key := accountCheckKey(scope, "2026-11-27", 0)
	store := redisAccountCheckMarkers{client}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Del(cleanup, key).Err(); err != nil {
			t.Error("could not remove isolated Redis marker")
		}
	})
	original := []byte(`{"status":"running"}`)
	final := []byte(`{"status":"success"}`)
	ok, err := store.claim(ctx, key, original)
	if err != nil || !ok {
		t.Fatal("first slot claim failed")
	}
	ok, err = store.claim(ctx, key, original)
	if err != nil || ok {
		t.Fatal("duplicate slot claim succeeded")
	}
	if err := store.complete(ctx, key, []byte(`wrong`), final); err == nil {
		t.Fatal("stale marker update succeeded")
	}
	if err := store.complete(ctx, key, original, final); err != nil {
		t.Fatal("matching marker update failed")
	}
	values, err := store.read(ctx, []string{key})
	if err != nil || len(values) != 1 || values[0] != string(final) {
		t.Fatal("actual Redis marker readback failed")
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl < 47*time.Hour || ttl > 48*time.Hour {
		t.Fatal("original TTL not retained")
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal("cleanup marker failed")
	}
	if err := store.complete(ctx, key, original, final); err == nil {
		t.Fatal("terminal update recreated removed marker")
	}
}
