package dashboard

import (
	"context"
	"errors"
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

func TestHomeCostStageDGroupFallbackFiltersEachObservation(t *testing.T) {
	now := time.Date(2031, 2, 3, 16, 0, 1, 0, time.UTC)
	date := businesstime.DateAt(now)
	amount := 12.0
	var cached []GroupMetricCacheItem
	for _, kind := range []string{"revenue", "profit"} {
		for _, item := range []struct {
			id string
			at time.Time
		}{{"current", now}, {"previous", now.Add(-2 * time.Second)}, {"absent", time.Time{}}} {
			cached = append(cached, GroupMetricCacheItem{MetricType: kind, GroupID: item.id, TodayRevenue: &amount, TodayProfit: &amount, ObservedAt: item.at})
		}
	}
	s := NewMetricsService(nil, nil, nil, &fakeMetricsRepository{groupMetricCache: cached}, nil)
	s.now = func() time.Time { return now }
	cause := errors.New("fixture unavailable")
	revenue, err := s.cachedGroupRevenue(t.Context(), "user", "ws", date, cause)
	if err != nil || revenue.TotalRevenue != 12 || len(revenue.Groups) != 1 || revenue.Groups[0].GroupID != "current" || revenue.FallbackAt == nil || !revenue.FallbackAt.Equal(now) {
		t.Errorf("mixed-date revenue=%+v err=%v", revenue, err)
	}
	profit, err := s.cachedGroupProfit(t.Context(), "user", "ws", date, cause)
	if err != nil || profit.TotalProfit != 12 || len(profit.Groups) != 1 || profit.FallbackGroups != 1 || profit.FallbackAt == nil || !profit.FallbackAt.Equal(now) {
		t.Errorf("mixed-date profit=%+v err=%v", profit, err)
	}
	merged, err := s.mergeGroupProfitFallback(t.Context(), "user", "ws", date, nil, map[string]struct{}{"current": {}, "previous": {}, "absent": {}})
	if err != nil || merged.TotalProfit != 12 || len(merged.Groups) != 1 || merged.FallbackGroups != 1 || merged.UnavailableGroups != 2 || merged.FallbackAt == nil || !merged.FallbackAt.Equal(now) {
		t.Errorf("mixed-date merged profit=%+v err=%v", merged, err)
	}
}

type homeCostRecoveryUnionRepository struct {
	*homeCostRecoveryRepo
	bySite map[string][]string
}

func (r *homeCostRecoveryUnionRepository) ListCostRecoveryDates(_ context.Context, _, _, siteID, _ string) ([]string, error) {
	r.reads <- struct{}{}
	return append([]string(nil), r.bySite[siteID]...), nil
}

func TestHomeCostStageDRecoveryMergesDifferentSiteDateSets(t *testing.T) {
	now := time.Date(2031, 2, 10, 4, 5, 6, 0, time.UTC)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	var dates []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cost" {
			mu.Lock()
			dates = append(dates, r.URL.Query().Get("date"))
			first := len(dates) == 1
			mu.Unlock()
			if first {
				started <- struct{}{}
				<-release
			}
		}
		homeCostRecoveryResponse(w)
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
	r := &homeCostRecoveryUnionRepository{
		homeCostRecoveryRepo: &homeCostRecoveryRepo{fakeMetricsRepository: &fakeMetricsRepository{}, reads: make(chan struct{}, 2)},
		bySite:               map[string][]string{"one": {"2031-02-07", "2031-02-08"}, "two": {"2031-02-08", "2031-02-09"}},
	}
	s := homeCostRecoveryService(t, server, r.homeCostRecoveryRepo, now)
	s.metricsRepo = r
	handler, ok := any(s).(homeCostRecoveryHandler)
	if !ok {
		t.Fatal("restored sites did not schedule their missing historical costs")
	}
	amount := 1.0
	old := upstream.Metrics{TodayConsumeStatus: "unreadable"}
	next := upstream.Metrics{TodayConsumeStatus: "ok", TodayConsumeDate: businesstime.DateAt(now), TodayConsume: upstream.MetricValue{Value: &amount}}
	done := make(chan error, 2)
	go func() {
		done <- handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", "one", old, next, upstream.StatusConnected, upstream.StatusConnected)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first recovery did not begin")
	}
	<-r.reads
	go func() {
		done <- handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", "two", old, next, upstream.StatusConnected, upstream.StatusConnected)
	}()
	select {
	case <-r.reads:
	case <-time.After(time.Second):
		t.Fatal("second site did not register recovery dates")
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("merged recovery did not complete")
		}
	}
	mu.Lock()
	got := append([]string(nil), dates...)
	mu.Unlock()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"2031-02-07", "2031-02-08", "2031-02-09"}) {
		t.Errorf("merged recovery dates=%v", got)
	}
}

type homeCostRecoveryRegistrationRepository struct {
	*homeCostRecoveryRepo
	registering chan struct{}
	releaseRead chan struct{}
}

func (r *homeCostRecoveryRegistrationRepository) ListCostRecoveryDates(ctx context.Context, _, _, siteID, _ string) ([]string, error) {
	if siteID == "two" {
		close(r.registering)
		select {
		case <-r.releaseRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]string(nil), r.dates...), nil
}

func TestHomeCostStageDRecoveryWaitsForConcurrentDateRegistration(t *testing.T) {
	now := time.Date(2031, 2, 10, 4, 5, 6, 0, time.UTC)
	started := make(chan struct{}, 1)
	releaseCost := make(chan struct{})
	releaseRead := make(chan struct{})
	var costOnce, readOnce sync.Once
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cost" {
			mu.Lock()
			count++
			first := count == 1
			mu.Unlock()
			if first {
				started <- struct{}{}
				<-releaseCost
			}
		}
		homeCostRecoveryResponse(w)
	}))
	defer func() {
		costOnce.Do(func() { close(releaseCost) })
		readOnce.Do(func() { close(releaseRead) })
		server.Close()
	}()
	repo := &homeCostRecoveryRegistrationRepository{homeCostRecoveryRepo: &homeCostRecoveryRepo{fakeMetricsRepository: &fakeMetricsRepository{}, dates: []string{"2031-02-09"}}, registering: make(chan struct{}), releaseRead: releaseRead}
	s := homeCostRecoveryService(t, server, repo.homeCostRecoveryRepo, now)
	s.metricsRepo = repo
	handler, ok := any(s).(homeCostRecoveryHandler)
	if !ok {
		t.Fatal("site recovery handler is unavailable")
	}
	old := upstream.Metrics{TodayConsumeStatus: "unreadable"}
	amount := 1.0
	next := upstream.Metrics{TodayConsumeStatus: "ok", TodayConsumeDate: businesstime.DateAt(now), TodayConsume: upstream.MetricValue{Value: &amount}}
	done := make(chan error, 2)
	for _, siteID := range []string{"one", "two"} {
		go func() {
			done <- handler.RecoverSiteCostsAfterSync(t.Context(), "user", "ws", siteID, old, next, upstream.StatusConnected, upstream.StatusConnected)
		}()
		if siteID == "one" {
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("first recovery did not start")
			}
		} else {
			select {
			case <-repo.registering:
			case <-time.After(time.Second):
				t.Fatal("second recovery did not begin date registration")
			}
		}
	}
	costOnce.Do(func() { close(releaseCost) })
	completed := 0
	select {
	case <-done:
		completed++
		t.Error("recovery round ended while a concurrent site's dates were being read")
	case <-time.After(75 * time.Millisecond):
	}
	readOnce.Do(func() { close(releaseRead) })
	for ; completed < 2; completed++ {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Fatal("recovery did not complete after date registration")
		}
	}
	mu.Lock()
	got := count
	mu.Unlock()
	if got != 1 {
		t.Errorf("concurrent recovery cost requests=%d, want one shared round", got)
	}
}
