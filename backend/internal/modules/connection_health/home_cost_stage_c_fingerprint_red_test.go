package connection_health

import (
	"context"
	"testing"
	"time"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// C-1 先复现：仅同步中临时状态变化不得丢失已累计的三次失败。
// 指纹其他字段、会话、分组、绑定完全不变。
func TestHomeCostStageCSyncingFingerprintPreservesFailureCount(t *testing.T) {
	for _, stable := range []upstream.Status{upstream.StatusConnected, upstream.StatusError} {
		t.Run(string(stable), func(t *testing.T) {
			site := snapshotSite("site-1")
			site.Status = stable
			reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}}, directErrs: map[string]error{"site-1": &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}}}
			cache := &failureFixRecoverableSiteRead{site: site}
			svc := &Service{mySites: reader, sites: cache}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := svc.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			refresh := func(force bool) {
				svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
			}
			refresh(false)
			refresh(true)
			refresh(true)
			key := multiplierSnapshotKey("user1", "ws1", "site-1")
			svc.multiplierSnapshotMu.Lock()
			before := svc.multiplierSnapshots[key]
			failures := before.consecutiveFailures
			retry := before.nextRetryAt
			svc.multiplierSnapshotMu.Unlock()
			if failures != 3 {
				t.Fatalf("fixture did not accumulate three failures: %d", failures)
			}
			d, l := reader.callCounts("site-1")
			syncing := *site
			syncing.Status = upstream.StatusSyncing
			cache.set(&syncing, nil)
			refresh(false)
			svc.multiplierSnapshotMu.Lock()
			during := svc.multiplierSnapshots[key]
			count := during.consecutiveFailures
			deadline := during.nextRetryAt
			svc.multiplierSnapshotMu.Unlock()
			if count != 3 || deadline != retry || during != before {
				t.Errorf("syncing reset retry state: failures=%d before=%d replaced=%v", count, failures, during != before)
			}
			if nd, nl := reader.callCounts("site-1"); nd != d || nl != l {
				t.Errorf("syncing retried during backoff: direct %d -> %d, list %d -> %d", d, nd, l, nl)
			}
			cache.set(site, nil)
			refresh(false)
			svc.multiplierSnapshotMu.Lock()
			after := svc.multiplierSnapshots[key]
			count = after.consecutiveFailures
			deadline = after.nextRetryAt
			svc.multiplierSnapshotMu.Unlock()
			if count != 3 || deadline != retry || after != before {
				t.Errorf("%s→syncing→%s reset accumulated retry state: failures=%d replaced=%v", stable, stable, count, after != before)
			}
		})
	}
}
