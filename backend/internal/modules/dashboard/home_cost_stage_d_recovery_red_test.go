package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type homeCostRecoveryHandler interface {
	RecoverSiteCostsAfterSync(context.Context, string, string, string, upstream.Metrics, upstream.Metrics, upstream.Status, upstream.Status) error
}
type homeCostRecoveryDates interface {
	ListCostRecoveryDates(context.Context, string, string, string, string) ([]string, error)
}

func TestHomeCostStageDRecoveryDatesRespectTargetsAndSevenDayWindow(t *testing.T) {
	pool := accountAssetTestPool(t)
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 10, 4, 5, 6, 0, time.UTC)
	var want []string
	for day := 1; day <= 8; day++ {
		date := businesstime.DateAt(now.Add(-time.Duration(day) * 24 * time.Hour))
		if day != 6 {
			if _, err := pool.Exec(t.Context(), `INSERT INTO dashboard_daily_cost_targets(user_id,admin_account_id,date,site_id,site_name,platform,recharge_rate) VALUES('user','ws',$1::date,'site','验收-恢复','newapi',1)`, date); err != nil {
				t.Fatal(err)
			}
		}
		status := map[int]string{2: "missing", 3: "failed", 4: "ok", 5: "partial", 6: "failed", 7: "missing"}[day]
		if status != "" {
			if _, err := pool.Exec(t.Context(), `INSERT INTO upstream_site_daily_costs(id,user_id,admin_account_id,date,site_id,site_name,platform,recharge_rate,status,source) VALUES($1,'user','ws',$2::date,'site','验收-恢复','newapi',1,$3,'none')`, fmt.Sprint(day), date, status); err != nil {
				t.Fatal(err)
			}
		}
		if day == 1 || day == 2 || day == 3 || day == 7 {
			want = append(want, date)
		}
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO dashboard_daily_cost_targets(user_id,admin_account_id,date,site_id,site_name,platform,recharge_rate) VALUES('other','ws','2031-02-06','site','验收-隔离','newapi',1),('user','other','2031-02-06','site','验收-隔离','newapi',1)`); err != nil {
		t.Fatal(err)
	}
	var got []string
	if reader, ok := any(repo).(homeCostRecoveryDates); ok {
		var err error
		got, err = reader.ListCostRecoveryDates(t.Context(), "user", "ws", "site", businesstime.DateAt(now))
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recovery dates=%v want=%v (include unattempted target, exclude confirmed/other scope/day8)", got, want)
	}
}

type homeCostRecoveryRepo struct {
	*fakeMetricsRepository
	mu    sync.Mutex
	dates []string
	reads chan struct{}
}

func (r *homeCostRecoveryRepo) ListCostRecoveryDates(context.Context, string, string, string, string) ([]string, error) {
	if r.reads != nil {
		r.reads <- struct{}{}
	}
	return append([]string(nil), r.dates...), nil
}
func (r *homeCostRecoveryRepo) Upsert(ctx context.Context, s DailySnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakeMetricsRepository.Upsert(ctx, s)
}

type homeCostDatedHTTPUpstreams struct {
	*fakeUpstreamLister
	client *http.Client
	url    string
}

func (u *homeCostDatedHTTPUpstreams) FetchSiteCostsForDate(ctx context.Context, _, _, date string) ([]upstream.SiteCostForDateResult, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", u.url+"/cost?date="+date, nil)
	if err != nil {
		return nil, err
	}
	response, err := u.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 1, RechargeRate: 1, Meta: upstream.CostFetchMeta{Source: "dated_query"}}}, nil
}
func homeCostRecoveryService(t *testing.T, server *httptest.Server, repo *homeCostRecoveryRepo, now time.Time) *MetricsService {
	t.Helper()
	store := newFakeSessionStore()
	store.set("user", "ws", AdminSession{Session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AdminAPIKey: "fixture-only"}})
	s := NewMetricsService(store, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), &homeCostDatedHTTPUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, client: server.Client(), url: server.URL}, repo, nil)
	s.now = func() time.Time { return now }
	return s
}
func homeCostRecoveryResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"total_actual_cost": 10}})
}

func TestHomeCostStageDRecoveryTriggersAndDeduplicatesSameRoundDates(t *testing.T) {
	for _, tc := range []struct {
		oldCost, newCost string
		oldStatus        upstream.Status
		want             int
	}{
		{"unreadable", "ok", upstream.StatusConnected, 2},
		{"", "ok", upstream.StatusError, 2},
		{"", "ok", upstream.StatusConnected, 0},
		{"unreadable", "unreadable", upstream.StatusError, 0},
	} {
		t.Run(tc.oldCost+"/"+string(tc.oldStatus)+"/"+tc.newCost, func(t *testing.T) {
			now := time.Date(2031, 2, 10, 4, 5, 6, 0, time.UTC)
			var mu sync.Mutex
			var requests []time.Time
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cost" {
					mu.Lock()
					requests = append(requests, time.Now())
					first := len(requests) == 1
					mu.Unlock()
					if first {
						started <- struct{}{}
						<-release
					}
				}
				homeCostRecoveryResponse(w)
			}))
			defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
			repo := &homeCostRecoveryRepo{fakeMetricsRepository: &fakeMetricsRepository{}, dates: []string{businesstime.DateAt(now.Add(-24 * time.Hour)), businesstime.DateAt(now.Add(-48 * time.Hour))}, reads: make(chan struct{}, 4)}
			s := homeCostRecoveryService(t, server, repo, now)
			handler, ok := any(s).(homeCostRecoveryHandler)
			amount := 1.0
			old := upstream.Metrics{TodayConsumeStatus: tc.oldCost}
			next := upstream.Metrics{TodayConsumeStatus: tc.newCost, TodayConsume: upstream.MetricValue{Value: &amount}, TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &now}
			if ok && tc.want > 0 {
				done := make(chan error, 2)
				go func() {
					done <- handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", "site", old, next, tc.oldStatus, upstream.StatusConnected)
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("no recovery query started")
				}
				<-repo.reads
				go func() {
					done <- handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", "site-two", old, next, tc.oldStatus, upstream.StatusConnected)
				}()
				select {
				case <-repo.reads:
				case <-time.After(time.Second):
					t.Fatal("concurrent recovery did not register its dates")
				}
				releaseOnce.Do(func() { close(release) })
				for range 2 {
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("recovery did not finish")
					}
				}
			} else if ok {
				if err := handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", "site", old, next, tc.oldStatus, upstream.StatusConnected); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			count := len(requests)
			times := append([]time.Time(nil), requests...)
			mu.Unlock()
			if count != tc.want {
				t.Errorf("recovery cost requests=%d want=%d", count, tc.want)
			}
			if count == 2 && times[1].Sub(times[0]) < 450*time.Millisecond {
				t.Error("recovery dates were not paced at 500ms")
			}
			releaseOnce.Do(func() { close(release) })
		})
	}
}

func TestHomeCostStageDFinalizationWaitsThenRunsAgain(t *testing.T) {
	now := time.Date(2031, 2, 10, 4, 5, 6, 0, time.UTC)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/usage/stats" {
			entered <- struct{}{}
			<-release
		}
		homeCostRecoveryResponse(w)
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
	s := homeCostRecoveryService(t, server, &homeCostRecoveryRepo{fakeMetricsRepository: &fakeMetricsRepository{}}, now)
	done := make(chan error, 2)
	ref := ActiveSessionRef{UserID: "user", AdminAccountID: "ws"}
	date := businesstime.DateAt(now.Add(-24 * time.Hour))
	go func() { done <- s.finalizeBusinessDate(t.Context(), ref, date, SnapshotSourceDatedQuery) }()
	<-entered
	go func() { done <- s.finalizeBusinessDate(t.Context(), ref, date, SnapshotSourceDatedQuery) }()
	early := false
	select {
	case <-entered:
		early = true
		t.Error("same workspace/date finalized concurrently")
	case <-time.After(75 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if !early {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Error("waiting finalization was skipped instead of running again")
		}
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Fatal("finalization did not finish")
		}
	}
}
