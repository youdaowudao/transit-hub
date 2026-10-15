package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func groupCostSnapshotRequest(t *testing.T, svc *Service, target string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	h := Handler{service: svc, accounts: &fakeAccountResolver{current: map[string]string{"user": "workspace"}}}
	r := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.list(w, r)
	return w
}
func TestGroupCostSnapshotFilteredContractAndIsolation(t *testing.T) {
	var network atomic.Int32
	svc, cache, _ := groupCostRuntimeFixture(t, 2, func(*http.Request) { network.Add(1) })
	foreign, _ := cache.Get(t.Context(), "1")
	foreign.UserID = "other-user"
	foreign.AdminAccountID = "other-workspace"
	_ = cache.Set(t.Context(), foreign)
	ctx := authctx.WithUserID(t.Context(), "user")
	for _, tc := range []struct {
		query  string
		status int
	}{{"?siteId=0", 200}, {"?siteId=", 400}, {"?siteId=0&siteId=1", 400}, {"?siteId=missing", 404}, {"?siteId=1", 404}} {
		t.Run(tc.query, func(t *testing.T) {
			w := groupCostSnapshotRequest(t, svc, "/api/upstream-sites"+tc.query, ctx)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/cache=%d/%s body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
			}
			if strings.Contains(w.Body.String(), "fixture-old") || strings.Contains(w.Body.String(), "other-user") {
				t.Fatal("credentials or foreign owner leaked")
			}
			if tc.status == 200 {
				var result []Response
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result) != 1 || result[0].ID != "0" {
					t.Fatal("filtered contract is not one item array")
				}
			} else {
				var result struct {
					Message string      `json:"message"`
					Failure formFailure `json:"failure"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				if result.Failure.Stage != "metrics" || result.Failure.HTTPStatus != tc.status || result.Failure.ErrorKey != result.Message || !strings.Contains(result.Failure.ErrorDetail, "不会修改主站") {
					t.Fatal("read error lost actionable detail")
				}
			}
		})
	}
	missing := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=missing", ctx)
	forbidden := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=1", ctx)
	if missing.Body.String() != forbidden.Body.String() {
		t.Fatal("existence revealed across workspace")
	}
	foreign.UserID = "user"
	_ = cache.Set(t.Context(), foreign)
	otherWorkspace := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=1", ctx)
	if otherWorkspace.Body.String() != missing.Body.String() {
		t.Fatal("other workspace existence revealed")
	}
	old := groupCostSnapshotRequest(t, svc, "/api/upstream-sites", ctx)
	var list []Response
	_ = json.Unmarshal(old.Body.Bytes(), &list)
	if old.Code != 200 || len(list) != 1 {
		t.Fatal("old unfiltered list contract changed")
	}
	unauthorized := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=0", t.Context())
	if unauthorized.Code != 401 {
		t.Fatal("filtered read bypassed authentication")
	}
	if network.Load() != 0 || cache.writes != 0 || cache.lockCalls != 0 || svc.groupCostQueue != nil {
		t.Fatal("cache read triggered network/writes/queue")
	}
}

func TestGroupCostSnapshotCacheFailuresKeepOtherGroupAndReason(t *testing.T) {
	svc, cache, clock := groupCostRuntimeFixture(t, 1, nil)
	now := time.Unix(0, clock.Load())
	site, _ := cache.Get(t.Context(), "0")
	site.Metrics.Groups = append(site.Metrics.Groups, GroupInfo{ID: "2", Name: "other"})
	_ = cache.Set(t.Context(), site)
	for _, id := range []string{"1", "2"} {
		cache.samples["0:"+id] = []GroupCostSample{{Date: svc.keyUsageDate(), RawAmount: 2, ObservedAt: now, Source: GroupCostSourceSummary, Complete: true}}
	}
	cache.sampleErrKey = "0:1"
	response, err := svc.ReadSiteSnapshot(t.Context(), "user", "0")
	if err != nil {
		t.Fatal(err)
	}
	bad, good := response.Metrics.Groups[0], response.Metrics.Groups[1]
	if bad.TodayCost != nil || bad.CostMode != GroupCostModeUnknown || !strings.Contains(bad.CostReason, "分组费用记录") || strings.Contains(bad.CostReason, "fixture-secret") {
		t.Fatal("broken sample lost cause or leaked credentials")
	}
	if good.TodayCost == nil || *good.TodayCost != 2 {
		t.Fatal("one bad sample erased another group")
	}
	cache.sampleErrKey = ""
	cache.stateErr = errors.New("fixture latest result unavailable")
	response, err = svc.ReadSiteSnapshot(t.Context(), "user", "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range response.Metrics.Groups {
		if group.TodayCost == nil || group.CostMode != GroupCostModeRetained || !strings.Contains(group.CostReason, "最近读取结果") || !strings.Contains(group.CostReason, "fixture latest result unavailable") {
			t.Fatal("state error was hidden or claimed fresh confirmed result")
		}
	}
	cache.samples = map[string][]GroupCostSample{}
	response, err = svc.ReadSiteSnapshot(t.Context(), "user", "0")
	if err != nil || response.Metrics.Groups[0].TodayCost != nil || !strings.Contains(response.Metrics.Groups[0].CostReason, "最近读取结果") {
		t.Fatal("missing sample/state failure became zero or vague upstream error")
	}
}

func TestGroupCostSnapshotRootFailureAndCancellationAre503(t *testing.T) {
	svc, cache, _ := groupCostRuntimeFixture(t, 1, nil)
	ctx := authctx.WithUserID(t.Context(), "user")
	cache.rootErr = errors.New("fixture read unavailable token=fixture-secret")
	w := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=0", ctx)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "读取 Hub 保存的本站资料失败") || strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), `"stage":"save"`) {
		t.Fatal("root read error lost its step, status, or redaction")
	}
	cache.rootErr = nil
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	w = groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=0", cancelled)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "读取已取消") {
		t.Fatal("cancelled read became successful empty list")
	}
	if cache.writes != 0 || cache.lockCalls != 0 {
		t.Fatal("failure read wrote or acquired sampling lock")
	}
}

func TestGroupCostSnapshotProgressFailurePriorityAndExpiredSuccess(t *testing.T) {
	svc, cache, clock := groupCostRuntimeFixture(t, 1, nil)
	site, _ := cache.Get(t.Context(), "0")
	now := time.Unix(0, clock.Load())
	key := costWorkKey(site)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc.groupCostQueue = &groupCostQueue{entries: map[groupCostWorkKey]*groupCostWork{key: {key: key, ctx: ctx, cancel: cancel}}}
	// No goroutines are started by this read-only runtime snapshot fixture.
	t.Cleanup(func() { svc.groupCostQueue = nil })
	for _, running := range []bool{false, true} {
		svc.groupCostQueue.entries[key].running = running
		response, err := svc.ReadSiteSnapshot(t.Context(), "user", "0")
		if err != nil || response.Metrics.Groups[0].TodayCost != nil {
			t.Fatal("progress turned unknown into amount")
		}
		want := "等待读取"
		if running {
			want = "正在读取"
		}
		if !strings.Contains(response.Metrics.Groups[0].CostReason, want) {
			t.Fatal("runtime state absent from reason")
		}
	}
	cache.stateErr = errors.New("fixture state read unavailable")
	response, _ := svc.ReadSiteSnapshot(t.Context(), "user", "0")
	if strings.Contains(response.Metrics.Groups[0].CostReason, "正在读取") || !strings.Contains(response.Metrics.Groups[0].CostReason, "fixture state read unavailable") {
		t.Fatal("progress hid known cache read failure")
	}
	cache.stateErr = nil
	svc.groupCostQueue = nil
	cache.states["0"] = GroupCostSamplingState{LastSuccessAt: now.Add(-91 * time.Minute)}
	response, _ = svc.ReadSiteSnapshot(t.Context(), "user", "0")
	if !strings.Contains(response.Metrics.Groups[0].CostReason, "超过九十分钟") {
		t.Fatal("expired successful read had no specific reason")
	}
	cache.states["0"] = GroupCostSamplingState{LastAttemptAt: now, LastAttemptStatus: "failed", LastReason: "读取期间费用从 2 变为 3，本轮未更新。", SummaryNextAllowedAt: now.Add(5 * time.Minute)}
	response, _ = svc.ReadSiteSnapshot(t.Context(), "user", "0")
	reason := response.Metrics.Groups[0].CostReason
	if !strings.Contains(reason, "2031-02-03 12:05:00后允许重试") || !strings.Contains(reason, "点击本站同步按钮") {
		t.Fatalf("retry time/action not actionable SG text: %s", reason)
	}
}

type groupCostSnapshotDeadlineCache struct {
	*groupCostRuntimeCache
	reads     atomic.Int32
	badBudget atomic.Bool
}

func (c *groupCostSnapshotDeadlineCache) Get(ctx context.Context, id string) (*Site, error) {
	c.reads.Add(1)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
		c.badBudget.Store(true)
	}
	return c.groupCostRuntimeCache.Get(ctx, id)
}
func TestGroupCostSnapshotParallelReadsHaveFiveSecondBudgetAndNoWrites(t *testing.T) {
	svc, cache, _ := groupCostRuntimeFixture(t, 1, nil)
	bounded := &groupCostSnapshotDeadlineCache{groupCostRuntimeCache: cache}
	svc.cache = bounded
	ctx := authctx.WithUserID(t.Context(), "user")
	done := make(chan bool, 20)
	for range 20 {
		go func() {
			w := groupCostSnapshotRequest(t, svc, "/api/upstream-sites?siteId=0", ctx)
			done <- w.Code == 200
		}()
	}
	for range 20 {
		if !<-done {
			t.Fatal("parallel snapshot read failed")
		}
	}
	if bounded.reads.Load() != 20 || bounded.badBudget.Load() || cache.writes != 0 || cache.lockCalls != 0 || svc.groupCostQueue != nil {
		t.Fatal("parallel read escaped budget or changed sampling state")
	}
}
func TestGroupCostSnapshotMissingWorkspaceUsesExistingGuard(t *testing.T) {
	svc, _, _ := groupCostRuntimeFixture(t, 1, nil)
	h := Handler{service: svc, accounts: &fakeAccountResolver{current: map[string]string{}}}
	r := httptest.NewRequest(http.MethodGet, "/api/upstream-sites?siteId=0", nil).WithContext(authctx.WithUserID(t.Context(), "user"))
	w := httptest.NewRecorder()
	h.list(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing workspace guard changed: %d", w.Code)
	}
}
