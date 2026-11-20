package vip

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
)

type review1AggregateCount struct {
	count  atomic.Int32
	before func()
}

func (c *review1AggregateCount) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "WITH usage AS") {
		c.count.Add(1)
		if c.before != nil {
			c.before()
		}
	}
	return ctx
}
func (*review1AggregateCount) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func review1VIPFixture(t *testing.T, n int, tracer *review1AggregateCount) (*Service, *pgxpool.Pool, Scope, *int) {
	t.Helper()
	pool, scope := absorbStore(t, func(cfg *pgxpool.Config) {
		if tracer != nil {
			cfg.ConnConfig.Tracer = tracer
		}
	})
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Truncate(time.Microsecond)
	today := businesstime.DateAt(started)
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,$3::jsonb,now())`, scope.UserID, scope.AdminAccountID, `{"telegram":[{"id":"fixture-bot","name":"fixture","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,usage_from,usage_to,user_count,last_started_at,last_synced_at) VALUES($1,$2,'success',$3::date,$4::date,$5,$6,$6)`, scope.UserID, scope.AdminAccountID, shift(today, -14), shift(today, -1), n, started); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		if _, err := pool.Exec(ctx, `INSERT INTO user_activity_profiles(user_id,admin_account_id,site_user_id,email,username,role,status,balance,first_observed_at,updated_at) VALUES($1,$2,$3,'fixture@example.invalid','fixture','user','active',5,$4,$4)`, scope.UserID, scope.AdminAccountID, id, started); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind,note) VALUES($1,$2,$3,'manual','')`, scope.UserID, scope.AdminAccountID, id); err != nil {
			t.Fatal(err)
		}
		for n := 1; n <= 14; n++ {
			if _, err := pool.Exec(ctx, `INSERT INTO user_activity_daily VALUES($1,$2,$3,$4::date,1,0,10)`, scope.UserID, scope.AdminAccountID, id, shift(today, -n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	calls := new(int)
	notifier := settings.NewService(&http.Client{Transport: absorbVIPTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		var registered int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM vip_alert_states WHERE delivery_status='sending'`).Scan(&registered); err != nil || registered != 1 {
			t.Error("HTTP preceded committed unique sending state")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)), Request: r}, nil
	})}, settings.NewRepository(pool))
	s := NewService(repo, notifier, false)
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	cfg := DefaultSettings()
	cfg.AlertsEnabled = true
	cfg.BotIDs = []string{"fixture-bot"}
	if err := s.SaveSettings(ctx, scope, cfg); err != nil {
		t.Fatal(err)
	}
	return s, pool, scope, calls
}

func TestAbsorbReview1VIPUnknownDoesNotRecoverOrRepeat(t *testing.T) {
	s, pool, scope, calls := review1VIPFixture(t, 1, nil)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO vip_alert_states VALUES($1,$2,'1','low',now()-interval '25 hours',false,'sent')`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{
		`UPDATE user_activity_profiles SET balance=NULL`,
		`UPDATE user_activity_profiles SET balance=5`,
		`UPDATE user_activity_profiles SET balance=50;UPDATE user_activity_daily SET requests=0,actual_cost=0`,
		`UPDATE user_activity_profiles SET balance=5;UPDATE user_activity_daily SET requests=1,actual_cost=10`,
	} {
		if _, err := pool.Exec(ctx, change); err != nil {
			t.Fatal(err)
		}
		if err := s.alertWorkspace(ctx, scope); err != nil {
			t.Fatal(err)
		}
		var recovered bool
		if err := pool.QueryRow(ctx, `SELECT recovered FROM vip_alert_states`).Scan(&recovered); err != nil || recovered {
			t.Fatal("unknown balance or unavailable consumption estimate was marked recovered")
		}
		if *calls != 0 {
			t.Fatal("same low episode repeated after unproven recovery")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_profiles SET balance=50`); err != nil {
		t.Fatal(err)
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	var recovered bool
	if err := pool.QueryRow(ctx, `SELECT recovered FROM vip_alert_states`).Scan(&recovered); err != nil || !recovered {
		t.Fatal("verified balance and days recovery was not retained")
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_profiles SET balance=5`); err != nil {
		t.Fatal(err)
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatal("verified recovered episode after 24 hours was not allowed")
	}
}

func TestAbsorbReview1VIPSkipsIdleRowsBeforeWholeWorkspaceAggregation(t *testing.T) {
	count := &review1AggregateCount{}
	s, pool, scope, calls := review1VIPFixture(t, 12, count)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO vip_alert_states SELECT user_id,admin_account_id,site_user_id,'low',now()-interval '25 hours',false,'sent' FROM user_activity_profiles`); err != nil {
		t.Fatal(err)
	}
	count.count.Store(0)
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if got := count.count.Load(); got != 1 || *calls != 0 {
		t.Fatalf("no-work tick repeated aggregation: queries=%d calls=%d", got, *calls)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM vip_alert_states WHERE site_user_id='1'`); err != nil {
		t.Fatal(err)
	}
	count.count.Store(0)
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if got := count.count.Load(); got > 2 || *calls != 1 {
		t.Fatalf("one pending row repeated aggregation: queries=%d calls=%d", got, *calls)
	}
}

func TestAbsorbReview1VIPCandidateStillRechecksOriginalGuards(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"workspace generation", `UPDATE admin_accounts SET created_at=created_at+interval '1 second'`},
		{"disabled settings", `UPDATE vip_settings SET alerts_enabled=false`},
		{"disabled target", `UPDATE notification_channel_settings SET settings='{"telegram":[{"id":"fixture-bot","name":"fixture","enabled":false,"botToken":"fixture-token","chatId":"fixture-chat"}]}'::jsonb`},
		{"failed sync", `UPDATE user_activity_sync_states SET status='failed'`},
		{"changed observed batch", `UPDATE user_activity_profiles SET updated_at=updated_at-interval '1 second'`},
		{"dismissed eligibility", `UPDATE vip_watch SET kind='dismissed'`},
		{"disabled profile", `UPDATE user_activity_profiles SET status='disabled'`},
		{"new sent state", `INSERT INTO vip_alert_states VALUES('fixture-user','fixture-workspace','1','low',now(),false,'sent')`},
	} {
		t.Run(change.name, func(t *testing.T) {
			count := &review1AggregateCount{}
			s, pool, scope, calls := review1VIPFixture(t, 1, count)
			// Change after the initial statement's Repeatable Read snapshot has been taken.
			count.before = func() {
				if count.count.Load() == 1 {
					if _, err := pool.Exec(t.Context(), change.sql); err != nil {
						t.Fatal(err)
					}
				}
			}
			_ = s.alertWorkspace(t.Context(), scope)
			count.before = nil
			if *calls != 0 {
				t.Fatal("draft bypassed latest locked guard")
			}
		})
	}
}
