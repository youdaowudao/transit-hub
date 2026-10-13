package upstream

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type singaporeGroupCache struct {
	*syncTestCache
	costsMu sync.Mutex
	state   GroupCostSamplingState
	samples map[string][]GroupCostSample
	writes  int
}

func (c *singaporeGroupCache) TryStartGroupCostSampling(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *singaporeGroupCache) GetGroupCostSamplingState(context.Context, string) (GroupCostSamplingState, error) {
	c.costsMu.Lock()
	defer c.costsMu.Unlock()
	return c.state, nil
}
func (c *singaporeGroupCache) SetGroupCostSamplingState(_ context.Context, _ string, state GroupCostSamplingState, _ time.Duration) error {
	c.costsMu.Lock()
	defer c.costsMu.Unlock()
	c.state = state
	c.writes++
	return nil
}
func (c *singaporeGroupCache) AppendGroupCostSamples(_ context.Context, _, group, _ string, samples []GroupCostSample, _ int, _ time.Duration) error {
	c.costsMu.Lock()
	defer c.costsMu.Unlock()
	c.samples[group] = append(c.samples[group], samples...)
	c.writes++
	return nil
}
func (c *singaporeGroupCache) ListGroupCostSamples(_ context.Context, _, group, _ string) ([]GroupCostSample, error) {
	c.costsMu.Lock()
	defer c.costsMu.Unlock()
	return append([]GroupCostSample(nil), c.samples[group]...), nil
}
func (c *singaporeGroupCache) DeleteGroupCostSamples(context.Context, string) error { return nil }
func (c *singaporeGroupCache) ClearGroupCostSamples(context.Context) error          { return nil }
func TestSingaporeCostGroupSampleProjectionOnlyUsesIDs(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, groups := range [][]GroupInfo{{{ID: "1", Name: "同名"}, {ID: "2", Name: "同名"}}, {{ID: "1", Name: "2"}, {ID: "2", Name: "其他"}}} {
		p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
			if r.URL.Path == "/api/v1/usage/stats" {
				return 200, map[string]any{"data": map[string]any{"total_actual_cost": 5}}
			}
			return 200, map[string]any{"data": map[string]any{"start_date": date, "end_date": date, "groups": []any{map[string]any{"group_id": 1, "actual_cost": 2}, map[string]any{"group_id": 2, "actual_cost": 3}}}}
		})
		site := newTestSite("site", "user", "workspace", 2, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
		site.Metrics.Groups = groups
		cache := &singaporeGroupCache{syncTestCache: newSyncTestCache(site), samples: map[string][]GroupCostSample{}}
		svc := NewService(p, nil, nil, cache)
		svc.now = func() time.Time { return now }
		t.Cleanup(svc.Close)
		svc.sampleGroupCosts(*site, *site.Session, groups)
		response := svc.toResponse(t.Context(), site)
		if len(response.Metrics.Groups) != 2 || response.Metrics.Groups[0].TodayCost == nil || *response.Metrics.Groups[0].TodayCost != 4 || response.Metrics.Groups[1].TodayCost == nil || *response.Metrics.Groups[1].TodayCost != 6 {
			t.Fatal("same names or a name colliding with another ID changed raw attribution or multiplied twice")
		}
		cache.state.LastAttemptAt = now.Add(time.Minute)
		cache.state.LastAttemptStatus = "failed"
		cache.state.LastReason = "读取期间费用变化，本轮未更新，可同步这个站点重试。"
		svc.now = func() time.Time { return now.Add(2 * time.Minute) }
		response = svc.toResponse(t.Context(), site)
		group := response.Metrics.Groups[0]
		if group.CostMode != GroupCostModeRetained || group.TodayCost == nil || *group.TodayCost != 4 || !strings.Contains(group.CostReason, "沿用截至") || !strings.Contains(group.CostReason, "读取期间费用变化") {
			t.Fatal("latest failure must mark even a fresh earlier sample retained with its failure cause")
		}
	}
}
func TestSingaporeCostGroupLateSuccessAndFailureCannotPublishForReplacedSession(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				if r.URL.Path == "/api/v1/usage/stats" {
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 2}}
				}
				close(entered)
				<-release
				if fail {
					return 403, map[string]any{"message": "本轮分组查询被拒绝"}
				}
				return 200, map[string]any{"data": map[string]any{"start_date": date, "end_date": date, "groups": []any{map[string]any{"group_id": 1, "actual_cost": 2}}}}
			})
			site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
			site.Metrics.Groups = []GroupInfo{{ID: "1", Name: "组"}}
			cache := &singaporeGroupCache{syncTestCache: newSyncTestCache(site), samples: map[string][]GroupCostSample{}}
			svc := NewService(p, nil, nil, cache)
			svc.now = func() time.Time { return now }
			t.Cleanup(svc.Close)
			done := make(chan struct{})
			go func() { svc.sampleGroupCosts(*site, *site.Session, site.Metrics.Groups); close(done) }()
			<-entered
			current, _ := cache.Get(t.Context(), site.ID)
			copy := cloneSite(current)
			copy.Session.AccessToken = "fixture-new"
			if err := cache.Set(t.Context(), &copy); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-done
			if cache.writes != 0 || len(cache.samples) != 0 || !cache.state.LastAttemptAt.IsZero() {
				t.Fatal("late sampling wrote costs or backoff for a replaced login")
			}
		})
	}
}
func TestSingaporeCostHourRequiresCompleteSameSourceAndRetainsRollback(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	baseline := GroupCostSample{Date: date, RawAmount: 2, ObservedAt: now.Add(-65 * time.Minute), Source: GroupCostSourceSummary, Complete: true}
	for _, tc := range []struct {
		name, source string
		complete     bool
		amount       float64
		wantToday    float64
		wantHour     *float64
	}{{"complete", GroupCostSourceSummary, true, 5, 10, float64Pointer(6)}, {"incomplete", GroupCostSourceSummary, false, 5, 10, nil}, {"changed source", GroupCostSourceKeyFallback, true, 5, 10, nil}, {"rollback", GroupCostSourceSummary, true, 1, 4, nil}, {"incomplete rollback", GroupCostSourceSummary, false, 1, 4, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			latest := GroupCostSample{Date: date, RawAmount: tc.amount, ObservedAt: now.Add(-5 * time.Minute), Source: tc.source, Complete: tc.complete}
			today, hour, _, _ := calculateGroupCostSnapshot([]GroupCostSample{baseline, latest}, date, now, 2)
			if today == nil || *today != tc.wantToday || tc.wantHour == nil && hour != nil || tc.wantHour != nil && (hour == nil || *hour != *tc.wantHour) {
				t.Fatalf("today/hour=%v/%v violates source, completeness, or retained counter rules", today, hour)
			}
		})
	}
}
