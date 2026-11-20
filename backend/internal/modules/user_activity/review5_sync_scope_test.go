package user_activity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/shared/workspacescope"
)

type review5SyncPause struct {
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (p *review5SyncPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM user_activity_sync_states") && strings.Contains(data.SQL, "SELECT status,") && p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*review5SyncPause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (p *review5SyncPause) unblock()                                                      { p.once.Do(func() { close(p.release) }) }

func TestAbsorbReview5DuplicateSyncRechecksScope(t *testing.T) {
	for _, mode := range []string{"active", "external-lock"} {
		for _, mutation := range []string{"unchanged", "switch", "delete", "background-switch"} {
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				pause := &review5SyncPause{entered: make(chan struct{}), release: make(chan struct{})}
				pool, scope := absorbStore(t, func(cfg *pgxpool.Config) { cfg.ConnConfig.Tracer = pause })
				t.Cleanup(pause.unblock)
				if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,error_detail) VALUES($1,$2,'running','private-old')`, scope.UserID, scope.AdminAccountID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(t.Context(), `INSERT INTO admin_accounts VALUES('fixture-workspace-b','fixture-user',now())`); err != nil {
					t.Fatal(err)
				}
				s := NewService(NewRepository(pool), nil, nil, false)
				defer s.Shutdown(context.Background())
				if mode == "active" {
					s.active = &scope
				} else {
					conn, err := pool.Acquire(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					var locked bool
					if err := conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked); err != nil || !locked {
						conn.Release()
						t.Fatal("cannot hold isolated global lock")
					}
					defer releaseLock(conn)
				}
				if err := workspacescope.Resolve(t.Context(), pool, scope); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					state SyncState
					err   error
				}
				done := make(chan outcome, 1)
				pause.armed.Store(true)
				go func() {
					st, err := s.StartSync(t.Context(), scope, mutation != "background-switch")
					done <- outcome{st, err}
				}()
				select {
				case <-pause.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("duplicate sync did not reach controlled read")
				}
				if mutation != "unchanged" {
					stmt := `UPDATE users SET current_admin_account_id='fixture-workspace-b' WHERE id='fixture-user'`
					if mutation == "delete" {
						stmt = `DELETE FROM admin_accounts WHERE id='fixture-workspace'`
					}
					if _, err := pool.Exec(t.Context(), stmt); err != nil {
						t.Fatal(err)
					}
				}
				pause.unblock()
				select {
				case got := <-done:
					if mutation == "switch" || mutation == "delete" {
						if !errors.Is(got.err, workspacescope.ErrChanged) {
							t.Errorf("duplicate read accepted changed scope: status=%s err=%v", got.state.Status, got.err)
						}
					} else if got.err != nil || got.state.Status != "running" {
						t.Errorf("valid existing/background task response changed: status=%s err=%v", got.state.Status, got.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("duplicate sync did not complete")
				}
			})
		}
	}
}
