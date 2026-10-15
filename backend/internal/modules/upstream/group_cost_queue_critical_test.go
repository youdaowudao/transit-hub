package upstream

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type criticalGroupQueueCache struct {
	*syncTestCache
	muCosts sync.Mutex
	states  map[string]GroupCostSamplingState
	samples map[string][]GroupCostSample
	saved   chan string
}

func (c *criticalGroupQueueCache) TryStartGroupCostSampling(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *criticalGroupQueueCache) GetGroupCostSamplingState(_ context.Context, id string) (GroupCostSamplingState, error) {
	c.muCosts.Lock()
	defer c.muCosts.Unlock()
	return c.states[id], nil
}
func (c *criticalGroupQueueCache) SetGroupCostSamplingState(_ context.Context, id string, state GroupCostSamplingState, _ time.Duration) error {
	c.muCosts.Lock()
	c.states[id] = state
	c.muCosts.Unlock()
	if state.LastAttemptStatus == "ok" {
		c.saved <- id
	}
	return nil
}
func (c *criticalGroupQueueCache) AppendGroupCostSamples(_ context.Context, id, group, date string, samples []GroupCostSample, _ int, _ time.Duration) error {
	c.muCosts.Lock()
	defer c.muCosts.Unlock()
	c.samples[id] = append(c.samples[id], samples...)
	return nil
}
func (c *criticalGroupQueueCache) ListGroupCostSamples(_ context.Context, id, group, date string) ([]GroupCostSample, error) {
	c.muCosts.Lock()
	defer c.muCosts.Unlock()
	return append([]GroupCostSample(nil), c.samples[id]...), nil
}
func (c *criticalGroupQueueCache) DeleteGroupCostSamples(context.Context, string) error { return nil }
func (c *criticalGroupQueueCache) ClearGroupCostSamples(context.Context) error          { return nil }

// 使用现有公开同步入口证明满额不丢任务，不以新函数缺失/编译错误代替RED。
func TestGroupCostQueueCriticalSuccessfulSyncsAllEventuallySample(t *testing.T) {
	for _, total := range []int{3, 42} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
			date := businesstime.DateAt(now)
			entered := make(chan string, total)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var active, peak atomic.Int32
			p := singaporeCostPlatform(t, now, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/auth/me":
					return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 10, "total_recharged": 20}}
				case "/api/v1/usage/stats", "/api/v1/usage/dashboard/stats":
					return 200, map[string]any{"data": map[string]any{"today_actual_cost": 2, "total_actual_cost": 2}}
				case "/api/v1/groups/available":
					return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": "组", "rate_multiplier": 1}}}
				case "/api/v1/groups/rates":
					return 200, map[string]any{"data": map[string]any{"1": 1}}
				case "/api/v1/usage/dashboard/snapshot-v2":
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old; old = peak.Load() {
						if peak.CompareAndSwap(old, n) {
							break
						}
					}
					entered <- r.URL.Host
					select {
					case <-release:
					case <-r.Context().Done():
						return 408, map[string]any{}
					}
					return 200, map[string]any{"data": map[string]any{"start_date": date, "end_date": date, "groups": []any{map[string]any{"group_id": 1, "actual_cost": 2}}}}
				default:
					return 404, map[string]any{}
				}
			})
			cache := &criticalGroupQueueCache{syncTestCache: newSyncTestCache(), states: map[string]GroupCostSamplingState{}, samples: map[string][]GroupCostSample{}, saved: make(chan string, total*2)}
			for i := 0; i < total; i++ {
				id := fmt.Sprint(i)
				site := newTestSite(id, "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: fmt.Sprintf("http://site-%d.invalid", i), AccessToken: "fixture-only"})
				site.Metrics.Groups = []GroupInfo{{ID: "1", Name: "组"}}
				_ = cache.Set(t.Context(), site)
			}
			svc := NewService(p, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			t.Cleanup(svc.Close)
			for i := 0; i < total; i++ {
				if _, err := svc.Sync(t.Context(), "user", fmt.Sprint(i)); err != nil {
					t.Fatal(err)
				}
				if i < 2 {
					select {
					case <-entered:
					case <-time.After(2 * time.Second):
						unblock()
						t.Fatal("前两站未进入费用读取")
					}
				}
			}
			unblock()
			seen := map[string]bool{}
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for len(seen) < total {
				select {
				case id := <-cache.saved:
					seen[id] = true
				case <-deadline.C:
					t.Fatalf("成功同步%d站只有%d站保存费用，名额满不得丢工作", total, len(seen))
				}
			}
			if peak.Load() > 2 {
				t.Fatalf("并发=%d，必须不超过2", peak.Load())
			}
			cache.muCosts.Lock()
			defer cache.muCosts.Unlock()
			for i := 0; i < total; i++ {
				samples := cache.samples[fmt.Sprint(i)]
				if len(samples) != 1 || samples[0].RawAmount != 2 || samples[0].Date != date {
					t.Fatalf("站%d未保存本日核验金额", i)
				}
			}
		})
	}
}
