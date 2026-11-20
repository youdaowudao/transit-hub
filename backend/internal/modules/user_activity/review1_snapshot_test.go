package user_activity

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

type reviewSnapshotPause struct {
	needle           string
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (p *reviewSnapshotPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, p.needle) && p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*reviewSnapshotPause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (p *reviewSnapshotPause) unblock()                                                      { p.once.Do(func() { close(p.release) }) }

func TestAbsorbReview1AnalysisReadsOnePublishedSnapshot(t *testing.T) {
	for _, kind := range []string{"retention", "today observation"} {
		t.Run(kind, func(t *testing.T) {
			needle := "INTERSECT SELECT site_user_id"
			if kind == "today observation" {
				needle = "SELECT day::text,"
			}
			pause := &reviewSnapshotPause{needle: needle, entered: make(chan struct{}), release: make(chan struct{})}
			pool, scope := absorbStore(t, func(cfg *pgxpool.Config) { cfg.ConnConfig.Tracer = pause })
			t.Cleanup(pause.unblock)
			now := time.Now().Truncate(time.Microsecond)
			today := businesstime.DateAt(now)
			before := now.Add(-time.Minute)
			if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,usage_from,usage_to,today_usage_observed_at) VALUES($1,$2,'success',$3::date,$4::date,$5)`, scope.UserID, scope.AdminAccountID, shift(today, -60), shift(today, -1), before); err != nil {
				t.Fatal(err)
			}
			for _, day := range []string{shift(today, -10), shift(today, -3), today} {
				if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_daily VALUES($1,$2,'first',$3::date,1,0,1)`, scope.UserID, scope.AdminAccountID, day); err != nil {
					t.Fatal(err)
				}
			}
			service := NewService(NewRepository(pool), nil, nil, true)
			defer service.cancel()
			done := make(chan struct {
				value Summary
				err   error
			}, 1)
			pause.armed.Store(true)
			go func() {
				v, e := service.Summary(t.Context(), scope, 2)
				done <- struct {
					value Summary
					err   error
				}{v, e}
			}()
			select {
			case <-pause.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("read never reached the controlled publication boundary")
			}
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if kind == "retention" {
				for _, day := range []string{shift(today, -10), shift(today, -3)} {
					if _, err := tx.Exec(t.Context(), `INSERT INTO user_activity_daily VALUES($1,$2,'second',$3::date,1,0,1)`, scope.UserID, scope.AdminAccountID, day); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if _, err := tx.Exec(t.Context(), `UPDATE user_activity_sync_states SET today_usage_observed_at=$1`, now); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(t.Context(), `UPDATE user_activity_daily SET requests=9,actual_cost=9 WHERE day=$1::date`, today); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			pause.unblock()
			select {
			case result := <-done:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if kind == "retention" {
					p := result.value.Periods["week"]
					if p.Current == nil || p.Previous == nil || p.Retention == nil || p.Current.ActiveUsers != 1 || p.Previous.ActiveUsers != 1 || p.Retention.Retained != 1 || p.Retention.Gained != 0 || p.Retention.Churned != 0 {
						t.Fatal("one GET mixed active counts and intersection from different publications")
					}
				} else {
					p := result.value.Today
					if p.Cost == nil || *p.Cost != 1 || p.Requests == nil || *p.Requests != 1 || p.ObservedAt == nil || !p.ObservedAt.Equal(before) {
						t.Fatal("one GET combined new usage with the previous observation timestamp")
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("snapshot read did not finish")
			}
		})
	}
}
