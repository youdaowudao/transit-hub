package user_activity

import (
	"context"
	"errors"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type absorbInventoryPlatform struct {
	Platform
	pages []upstream.Sub2APIAdminUsersPage
	p     *pacedReader
	calls int
}

func (f *absorbInventoryPlatform) FetchSub2APIAdminUsersPageWithContext(ctx context.Context, _ upstream.Session, q upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error) {
	if q.PageSize != 100 || !q.StrictPagination || q.SortBy != "created_at" || q.SortOrder != "asc" || q.IncludeSubscriptions == nil || *q.IncludeSubscriptions {
		panic("inventory query is not stable and strict")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("inventory request is unbounded")
	}
	f.p.last = time.Time{}
	f.calls++
	return f.pages[min(f.calls-1, len(f.pages)-1)], nil
}
func TestAbsorbInventoryRejectsIncompleteOrUnstablePages(t *testing.T) {
	for _, kind := range []string{"empty", "one", "unknown total", "early end", "duplicate", "wrong page", "too many users", "total changed"} {
		t.Run(kind, func(t *testing.T) {
			page := upstream.Sub2APIAdminUsersPage{Page: 1, PageSize: 100, Total: 1, TotalKnown: true, Items: []upstream.Sub2APIAdminUser{{ID: "1"}}}
			pages := []upstream.Sub2APIAdminUsersPage{page}
			valid := false
			switch kind {
			case "empty":
				pages[0].Total = 0
				pages[0].Items = nil
				valid = true
			case "one":
				valid = true
			case "unknown total":
				pages[0].TotalKnown = false
			case "early end":
				pages[0].Total = 2
			case "duplicate":
				pages[0].Total = 2
				pages[0].Items = append(pages[0].Items, upstream.Sub2APIAdminUser{ID: "1"})
			case "wrong page":
				pages[0].Page = 2
			case "too many users":
				pages[0].Total = 50001
			case "total changed":
				pages[0].Total = 101
				pages[0].Items = nil
				for i := 0; i < 100; i++ {
					pages[0].Items = append(pages[0].Items, upstream.Sub2APIAdminUser{ID: time.Unix(int64(i), 0).String()})
				}
				second := page
				second.Page = 2
				second.Total = 102
				pages = append(pages, second)
			}
			p := &pacedReader{}
			f := &absorbInventoryPlatform{pages: pages, p: p}
			s := NewService(nil, nil, f, false)
			defer s.cancel()
			_, err := s.fetchInventory(t.Context(), p, upstream.Session{})
			if (err == nil) != valid {
				t.Fatal("inventory completeness contract changed")
			}
		})
	}
}

type absorbBlockedSessions struct {
	entered chan context.Context
	done    chan struct{}
}

func (f absorbBlockedSessions) StoredSession(ctx context.Context, _, _ string) (upstream.Session, bool, error) {
	f.entered <- ctx
	<-ctx.Done()
	close(f.done)
	return upstream.Session{}, false, ctx.Err()
}
func TestAbsorbSyncSurvivesRequestCancellationAndShutdownReleasesLock(t *testing.T) {
	pool, scope := absorbStore(t)
	repo := NewRepository(pool)
	sessions := absorbBlockedSessions{entered: make(chan context.Context, 1), done: make(chan struct{})}
	s := NewService(repo, sessions, nil, false)
	defer s.Shutdown(context.Background())
	request, cancel := context.WithCancel(t.Context())
	started, err := s.StartSync(request, scope, true)
	if err != nil || started.Status != "running" {
		t.Fatal("sync was not registered")
	}
	var task context.Context
	select {
	case task = <-sessions.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("registered task never started")
	}
	cancel()
	if task.Err() != nil {
		t.Fatal("browser cancellation killed a registered background sync")
	}
	other := scope
	other.AdminAccountID = "other"
	if _, err := s.StartSync(t.Context(), other, false); !errors.Is(err, ErrBusy) {
		t.Fatal("second workspace bypassed the single task gate")
	}
	second := NewService(repo, sessions, nil, false)
	defer second.Shutdown(context.Background())
	if _, err := second.StartSync(t.Context(), other, false); !errors.Is(err, ErrBusy) {
		t.Fatal("second service bypassed the shared database gate")
	}
	deadline, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if err := s.Shutdown(deadline); err != nil {
		t.Fatal("shutdown did not cancel and wait for active task")
	}
	select {
	case <-sessions.done:
	default:
		t.Fatal("shutdown left a live reader")
	}
	state, err := repo.Sync(t.Context(), scope)
	if err != nil || state.Status != "failed" || state.LastSyncedAt != nil {
		t.Fatal("interrupted task was reported successful")
	}
	var locked bool
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked); err != nil || !locked {
		conn.Release()
		t.Fatal("task leaked global advisory lock")
	}
	releaseLock(conn)
	if _, err := s.StartSync(t.Context(), scope, true); err == nil {
		t.Fatal("stopped service accepted a new task")
	}
	api := NewService(repo, nil, nil, true)
	defer api.Shutdown(context.Background())
	if _, err := api.StartSync(t.Context(), scope, true); err == nil {
		t.Fatal("API-only instance started collection")
	}
}
