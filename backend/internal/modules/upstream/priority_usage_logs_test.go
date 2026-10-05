package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamStreamLogsOnlyFailuresAndOneSummary(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled_%t", canceled), func(t *testing.T) {
			logs := failureFixCaptureLogs(t)
			a := newTestSite("stream-ok", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AccessToken: "fixture-only"})
			b := newTestSite("stream-failed", "user", "workspace", 1, a.Session)
			noSession := newTestSite("stream-without-session", "user", "workspace", 1, nil)
			service := NewService(nil, nil, nil, newSyncTestCache(a, b, noSession))
			t.Cleanup(service.Close)
			service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			finished := make(chan struct{})
			close(finished)
			service.syncFlights = map[string]*syncFlight{
				a.ID: {done: finished, response: Response{ID: a.ID, Status: StatusConnected}},
				b.ID: {done: finished, err: errors.New("fixture failure")},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			if err := service.SyncAllStream(ctx, "user", func(SyncEvent) {}); err != nil {
				t.Fatal(err)
			}
			want := "全量同步完成 total=2 ok=1 failed=1 canceled=0 duration="
			if canceled {
				want = "全量同步完成 total=2 ok=0 failed=0 canceled=2 duration="
			}
			if strings.Count(logs.String(), "[upstream-stream] 全量同步完成") != 1 || !strings.Contains(logs.String(), want) {
				t.Errorf("full sync must log one conserved-count summary: want=%q logs=%s", want, logs.String())
			}
			for _, routine := range []string{"[upstream-stream] 开始同步站点", "[upstream-stream] 同步成功"} {
				if strings.Contains(logs.String(), routine) {
					t.Errorf("routine stream log remains: %s", routine)
				}
			}
			if !canceled && !strings.Contains(logs.String(), "[upstream-stream] 同步失败 id="+b.ID) {
				t.Errorf("real failure log was removed: %s", logs.String())
			}
		})
	}
}

func TestUpstreamTimerAndMetricsDoNotLogRoutineLines(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "1", "name": "fixture", "rate_multiplier": 1}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"1": 1}})
		default:
			t.Errorf("unexpected fixture path: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	site := newTestSite("timer-fixture", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "https://timer.test", AccessToken: "fixture-only"})
	service := NewService(platform, nil, nil, newSyncTestCache(site))
	t.Cleanup(service.Close)
	completed := make(chan struct{}, 1)
	service.AfterSync = func(context.Context, string, string, string, string, Metrics, Metrics, Status, Status) {
		select {
		case completed <- struct{}{}:
		default:
		}
	}
	service.mu.Lock()
	service.refreshConfigs[refreshWorkspaceKey{userID: "user", adminAccountID: "workspace"}] = RefreshConfig{Enabled: true, Interval: 10 * time.Millisecond}
	service.scheduleSyncLocked(site.ID, site)
	service.mu.Unlock()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("explicit fixture timer did not finish sync")
	}
	service.Close()
	for _, routine := range []string{"[upstream-timer] 定时同步已调度", "[upstream-timer] 定时同步触发", "[sub2api-metrics] 开始拉取指标"} {
		if strings.Contains(logs.String(), routine) {
			t.Errorf("routine log remains: %s", routine)
		}
	}
	logs.Reset()
	failed := failureFixPlatform(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	_, err := failed.fetchSub2APIMetrics(*site.Session)
	if err == nil || !strings.Contains(logs.String(), "[sub2api-metrics] /api/v1/auth/me 失败") {
		t.Errorf("metrics failure log must remain: err=%v logs=%s", err, logs.String())
	}
}

type priorityUsageLogCostCache struct {
	*failureFixDeleteCache
	state GroupCostSamplingState
	saves int
}

func (c *priorityUsageLogCostCache) TryStartGroupCostSampling(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *priorityUsageLogCostCache) GetGroupCostSamplingState(context.Context, string) (GroupCostSamplingState, error) {
	return c.state, nil
}
func (c *priorityUsageLogCostCache) SetGroupCostSamplingState(_ context.Context, _ string, state GroupCostSamplingState, _ time.Duration) error {
	c.state = state
	c.saves++
	return nil
}

func TestGroupCostCooldownIsSilentAndRealFailureStillLogs(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	var requests atomic.Int32
	platform := failureFixPlatform(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	session := Session{Platform: PlatformSub2API, BaseURL: "https://group-cost.test", AccessToken: "fixture-only"}
	site := newTestSite("group-cost", "user", "workspace", 1, &session)
	cache := &priorityUsageLogCostCache{failureFixDeleteCache: &failureFixDeleteCache{fakeSiteCache: newFakeSiteCache()}, state: GroupCostSamplingState{
		SummaryNextAllowedAt: time.Now().Add(time.Hour), FallbackNextAllowedAt: time.Now().Add(time.Hour), LastReason: "auth_403",
	}}
	service := NewService(platform, nil, nil, cache)
	t.Cleanup(service.Close)
	service.sampleGroupCosts(*site, session, []GroupInfo{{ID: "1", Name: "fixture"}})
	if requests.Load() != 0 || cache.saves != 0 || strings.Contains(logs.String(), "group cost sample failed") {
		t.Errorf("cooldown must send no request and no failure log: requests=%d saves=%d logs=%s", requests.Load(), cache.saves, logs.String())
	}
	logs.Reset()
	cache.state = GroupCostSamplingState{}
	service.sampleGroupCosts(*site, session, []GroupInfo{{ID: "1", Name: "fixture"}})
	if requests.Load() != 1 || cache.saves != 1 || !strings.Contains(logs.String(), "group cost sample failed") {
		t.Errorf("real sampling failure must remain visible: requests=%d saves=%d logs=%s", requests.Load(), cache.saves, logs.String())
	}
}
