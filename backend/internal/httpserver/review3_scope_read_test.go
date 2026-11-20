package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/sales"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/vip"
)

type review3ReadPause struct {
	needle           string
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (p *review3ReadPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, p.needle) && p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*review3ReadPause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (p *review3ReadPause) unblock()                                                      { p.once.Do(func() { close(p.release) }) }

func TestAbsorbReview3SalesVipGETRejectScopeChangedDuringRead(t *testing.T) {
	for _, path := range []string{"/api/sales/summary", "/api/sales/entries", "/api/vip/summary", "/api/vip/search"} {
		for _, mutation := range []string{"switch", "delete"} {
			t.Run(path+"/"+mutation, func(t *testing.T) {
				f := absorbAPIFixture(t)
				absorbInitializeAPI(t, f)
				created := absorbCreated(t, f, "workspace-a")
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_activity_profiles VALUES('user','workspace-a','private-old','private-old@example.test','private-old','user','active',10,NULL,NULL,now(),now());INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind) VALUES('user','workspace-a','private-old','manual');INSERT INTO user_activity_recharge_daily VALUES('user','workspace-a','private-old',current_date-1,'payment',20,1)`); err != nil {
					t.Fatal(err)
				}
				needle := "WITH src AS"
				if path == "/api/sales/entries" {
					needle = "SELECT EXISTS(SELECT 1 FROM user_activity_recharge_daily"
				}
				if path == "/api/vip/summary" {
					needle = "FROM user_activity_sync_states"
				}
				if path == "/api/vip/search" {
					needle = "FROM user_activity_profiles p"
				}
				pause := &review3ReadPause{needle: needle, entered: make(chan struct{}), release: make(chan struct{})}
				cfg := f.pool.Config()
				cfg.ConnConfig.Tracer = pause
				pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(pool.Close)
				t.Cleanup(pause.unblock)
				notifier := settings.NewService(&http.Client{Transport: &absorbRejectHTTP{}}, settings.NewRepository(pool))
				vipService := vip.NewService(vip.NewRepository(pool), notifier, true)
				t.Cleanup(func() { _ = vipService.Shutdown(context.Background()) })
				mux := http.NewServeMux()
				sales.RegisterRoutes(mux, sales.NewService(sales.NewRepository(pool), true))
				vip.RegisterRoutes(mux, vipService)
				f.server.mux = mux
				done := make(chan *httptest.ResponseRecorder, 1)
				pause.armed.Store(true)
				go func() { done <- f.request(t, http.MethodGet, absorbScopePath(path, "workspace-a", created), nil, true) }()
				select {
				case <-pause.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("GET did not reach controlled in-snapshot read")
				}
				statement := `UPDATE users SET current_admin_account_id='workspace-b' WHERE id='user'`
				if mutation == "delete" {
					statement = `DELETE FROM admin_accounts WHERE id='workspace-a'`
				}
				if _, err := f.pool.Exec(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
				pause.unblock()
				select {
				case response := <-done:
					if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "private-old") {
						t.Errorf("GET exposed stale workspace after %s: status=%d body=%s", mutation, response.Code, response.Body.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("GET did not finish")
				}
			})
		}
	}
}
