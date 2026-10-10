package connection_health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func TestModelControlSharedSchedulableExtraCheckNeverClaimsOrSends(t *testing.T) {
	f := newC3REDFixture(t)
	sentinel := errors.New("fixture extra check rejection")
	called := 0
	_, _, err := f.service.setTargetSchedulable(t.Context(), c3REDUser, c3REDTarget("1"), false, func(context.Context, adminTargetRefresh) error { called++; return sentinel })
	if !errors.Is(err, sentinel) || called != 1 || f.scheduleWrites != 0 {
		t.Fatal("shared core ignored extra check")
	}
	var events, checkpoints int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_target_action_states`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if events != 0 || checkpoints != 0 {
		t.Fatal("extra check failure claimed checkpoint or wrote health event")
	}
}
func TestModelControlSameAccountConcurrentCloseSendsOnce(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	preview := f.preview("1", "A", "close")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); code, _ := f.execute("1", "A", "close", preview); codes <- code }()
	}
	wg.Wait()
	close(codes)
	succeeded, rejected := 0, 0
	for code := range codes {
		if code == 200 {
			succeeded++
		} else if code == 409 {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent code=%d", code)
		}
	}
	if succeeded != 1 || rejected != 1 || f.writeCount() != 1 {
		t.Fatalf("succeeded=%d rejected=%d writes=%d", succeeded, rejected, f.writeCount())
	}
}
func TestModelControlReadIsolationAndInvalidRequests(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	for _, path := range []string{"model-control/targets/sub2api:other:1", "model-control/events?targetId=sub2api:other:1"} {
		code, _ := f.request(http.MethodGet, path, nil)
		if code != 404 {
			t.Fatalf("cross workspace GET %s=%d", path, code)
		}
	}
	for _, input := range []map[string]any{{"targetIds": []string{}}, {"targetIds": []string{"sub2api:other:1"}}} {
		code, _ := f.request(http.MethodPost, "model-control/verify", input)
		if code != 400 && code != 404 {
			t.Fatalf("invalid verify=%d", code)
		}
	}
	f.service.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI, AccessToken: "fixture-only"}}
	code, _ := f.request(http.MethodGet, "model-control/settings", nil)
	if code == 200 {
		t.Fatal("NewAPI workspace supported model control")
	}
}

type modelControlRecreatedAccountLease struct {
	*Repository
	beforeAcquire func()
}

func (r *modelControlRecreatedAccountLease) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	handle, acquired, err := r.Repository.AcquireActionLease(ctx, key, wait)
	if acquired && r.beforeAcquire != nil {
		fn := r.beforeAcquire
		r.beforeAcquire = nil
		fn()
	}
	return handle, acquired, err
}

type modelControlStaleMissingInventory struct{ PlatformGroupReader }

func (r modelControlStaleMissingInventory) ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error) {
	return []upstream.AdminGroupAccountInfo{}, nil
}

func TestModelControlMissingPreviewCannotClearNewOperationAfterAccountRecreated(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	item := f.item("1", "A")
	f.mu.Lock()
	saved := c3REDClone(f.accounts["1"]).(map[string]any)
	delete(f.accounts, "1")
	f.mu.Unlock()
	f.service.platformGroups = modelControlStaleMissingInventory{f.service.platformGroups}
	f.service.repo = &modelControlRecreatedAccountLease{Repository: f.service.questionAnswers.(*Repository), beforeAcquire: func() {
		f.mu.Lock()
		f.accounts["1"] = saved
		f.mu.Unlock()
		_, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET pending=$1,version=version+1 WHERE target_id=$2 AND model_name='A'`, &modelControlPending{ID: "fresh-pending", Phase: "prepared", Operation: "close", Entries: map[string]string{"a": "A"}}, c3REDTarget("1"))
		if err != nil {
			t.Fatal(err)
		}
	}}
	code, result := f.request(http.MethodPost, "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "operation": "close", "basis": item["basis"]})
	if code != 400 || !strings.Contains(fmt.Sprint(result), "AccountReadFailed") {
		t.Fatalf("stale missing proof accepted: %d %v", code, result)
	}
	control := f.item("1", "A")["control"].(map[string]any)
	var pendingID string
	if err := f.pool.QueryRow(t.Context(), `SELECT pending->>'id' FROM connection_health_model_control_targets WHERE target_id=$1 AND model_name='A'`, c3REDTarget("1")).Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	if control["pending"] == nil || pendingID != "fresh-pending" || control["observation"].(map[string]any)["state"] == "account_missing" || f.eventCount("account_missing_resolved") != 0 {
		t.Fatal("stale proof cleared the recreated account's new operation")
	}
	if f.writeCount() != 0 || f.scheduleWrites != 0 {
		t.Fatal("stale proof sent a remote write")
	}
}
