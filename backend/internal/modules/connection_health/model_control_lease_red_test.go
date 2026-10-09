package connection_health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type c3REDLeaseCapture struct {
	*Repository
	mu         sync.Mutex
	handles    []*RuntimeLeaseHandle
	expireSoon bool
}

func (r *c3REDLeaseCapture) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	handle, acquired, err := r.Repository.AcquireActionLease(ctx, key, wait)
	if handle != nil {
		if r.expireSoon && strings.Contains(handle.Key, "target") {
			if _, expiryErr := r.db.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE lease_key=$1 AND owner_id=$2`, handle.Key, handle.OwnerID); expiryErr != nil {
				handle.Release()
				return nil, false, expiryErr
			}
		}
		r.mu.Lock()
		r.handles = append(r.handles, handle)
		r.mu.Unlock()
	}
	return handle, acquired, err
}

func TestC3ModelControlRED20ExpiredLeaseWhileWaitingCannotSavePending(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	capture := &c3REDLeaseCapture{Repository: f.service.questionAnswers.(*Repository), expireSoon: true}
	f.service.repo = capture
	ready := make(chan pgx.Tx, 1)
	f.beforeDetail = func() {
		f.mu.Lock()
		f.beforeDetail = nil
		f.mu.Unlock()
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		var id string
		if err := tx.QueryRow(context.Background(), `SELECT id FROM connection_health_model_control_targets WHERE user_id=$1 AND target_id=$2 AND model_name='A' FOR UPDATE`, c3REDUser, c3REDTarget("1")).Scan(&id); err != nil {
			_ = tx.Rollback(context.Background())
			t.Error(err)
			return
		}
		ready <- tx
	}
	finished := make(chan int, 1)
	go func() { code, _ := f.execute("1", "A", "close", p); finished <- code }()
	var holder pgx.Tx
	select {
	case holder = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("prepared path did not reach object lock barrier")
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	deadline := time.Now().Add(4 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query ILIKE '%connection_health_model_control_targets%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("ownership path did not wait for the real object row")
	}
	time.Sleep(600 * time.Millisecond)
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 409 {
			t.Fatalf("expired authority status=%d, want 409", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expired owner did not stop")
	}
	if f.writeCount() != 0 || f.item("1", "A")["control"].(map[string]any)["pending"] != nil || len(f.closed("1", "A")) != 0 {
		t.Fatal("expired authority saved pending, ownership, or wrote the main")
	}
	capture.expireSoon = false
	f.verify("1")
}

func (r *c3REDLeaseCapture) loseTarget(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, handle := range r.handles {
		if strings.Contains(handle.Key, "target") {
			handle.lose()
			return
		}
	}
	t.Fatal("actual target lease handle was not captured")
}

type c3REDCommitSetter interface {
	setModelControlCommitter(func(context.Context, pgx.Tx, string) error)
}

func (f *c3REDFixture) commitSetter() c3REDCommitSetter {
	f.t.Helper()
	setter, ok := any(f.service.questionAnswers).(c3REDCommitSetter)
	if !ok {
		f.t.Fatal("C3 commit substitution unavailable for committed-but-unacknowledged regression")
	}
	return setter
}

func TestC3ModelControlRED20CommittedFinalizeWithLostAcknowledgementReturnsActualResult(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	injected := false
	f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if stage == "finalize" && !injected {
			injected = true
			return errors.New("C3 injected acknowledgement loss after successful commit")
		}
		return nil
	})
	result := f.close("1", "A")
	if !injected {
		t.Fatal("successful finalize transaction was not intercepted")
	}
	if result["outcome"] != "closed" || len(f.closed("1", "A")) != 2 || f.eventCount("close_unknown") != 0 {
		t.Fatal("committed finalize was misreported as unknown")
	}
	var pendingID string
	if err := f.pool.QueryRow(t.Context(), `SELECT last_pending_id FROM connection_health_model_control_targets WHERE user_id=$1 AND target_id=$2 AND model_name='A'`, c3REDUser, c3REDTarget("1")).Scan(&pendingID); err != nil || pendingID == "" {
		t.Fatal("finalize commit marker was not persisted")
	}
}

func TestC3ModelControlRED08LeaseLostAfterPreparedNeverSends(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	capture := &c3REDLeaseCapture{Repository: f.service.questionAnswers.(*Repository)}
	f.service.repo = capture
	f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if stage == "prepared" {
			capture.loseTarget(t)
		}
		return nil
	})
	p := f.preview("1", "A", "close")
	f.execute("1", "A", "close", p)
	control := f.item("1", "A")["control"].(map[string]any)
	if f.writeCount() != 0 || control["pending"] == nil || control["pending"].(map[string]any)["phase"] != "prepared" {
		t.Fatal("lost permit lease sent or discarded prepared record")
	}
	f.commitSetter().setModelControlCommitter(nil)
	f.verify("1")
	control = f.item("1", "A")["control"].(map[string]any)
	if control["pending"] != nil || control["unconfirmedClose"] != nil || f.writeCount() != 0 {
		t.Fatal("prepared pending did not reconcile as never sent")
	}
}

func TestC3ModelControlRED24HandleLostAfterSendingPermitRecordsNotSent(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	capture := &c3REDLeaseCapture{Repository: f.service.questionAnswers.(*Repository)}
	f.service.repo = capture
	f.commitSetter().setModelControlCommitter(func(ctx context.Context, tx pgx.Tx, stage string) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if stage == "sending" {
			capture.loseTarget(t)
		}
		return nil
	})
	p := f.preview("1", "A", "close")
	f.execute("1", "A", "close", p)
	control := f.item("1", "A")["control"].(map[string]any)
	if f.writeCount() != 0 || control["pending"] == nil || control["pending"].(map[string]any)["receipt"] != "not_sent" {
		t.Fatal("lost handle after permit sent or omitted not_sent receipt")
	}
	f.commitSetter().setModelControlCommitter(nil)
	f.verify("1")
	control = f.item("1", "A")["control"].(map[string]any)
	if control["pending"] != nil || control["unconfirmedClose"] != nil {
		t.Fatal("not_sent receipt waited or reserved uncertain deletion")
	}
}

func TestC3ModelControlRED24HandleLostWhileFinalizeWaitsForObjectLock(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	capture := &c3REDLeaseCapture{Repository: f.service.questionAnswers.(*Repository)}
	f.service.repo = capture
	ready := make(chan pgx.Tx, 1)
	f.beforeDetail = func() {
		if f.writeCount() == 0 {
			return
		}
		f.mu.Lock()
		f.beforeDetail = nil
		f.mu.Unlock()
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		var id string
		if err := tx.QueryRow(context.Background(), `SELECT id FROM connection_health_model_control_targets WHERE user_id=$1 AND target_id=$2 AND model_name='A' FOR UPDATE`, c3REDUser, c3REDTarget("1")).Scan(&id); err != nil {
			_ = tx.Rollback(context.Background())
			t.Error(err)
			return
		}
		ready <- tx
	}
	p := f.preview("1", "A", "close")
	type response struct {
		code int
		body map[string]any
	}
	finished := make(chan response, 1)
	go func() { code, body := f.execute("1", "A", "close", p); finished <- response{code, body} }()
	var holder pgx.Tx
	select {
	case holder = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not reach real object lock barrier")
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var waiting int
		err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query ILIKE '%connection_health_model_control_targets%'`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("finalize did not wait for held object row")
	}
	capture.loseTarget(t)
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.code != 200 || result.body["outcome"] != "unknown" {
			t.Fatalf("handle-lost finalize result=%+v", result)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("finalize did not terminate after lock release")
	}
	control := f.item("1", "A")["control"].(map[string]any)
	if len(f.closed("1", "A")) != 0 || control["pending"] == nil || control["pending"].(map[string]any)["receipt"] != "applied" {
		t.Fatal("handle-lost transaction acquired ownership or lost applied receipt")
	}
	f.verify("1")
	control = f.item("1", "A")["control"].(map[string]any)
	if len(f.closed("1", "A")) != 2 || control["pending"] != nil || control["unconfirmedClose"] != nil {
		t.Fatal("new holder did not immediately finalize applied receipt")
	}
}

func TestC3ModelControlRED17LateBlockedPreviewCannotReviveAfterSuccessfulClose(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.mapping("2", map[string]string{"b": "B"})
	ready := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.beforeDetail = func() {
		f.mu.Lock()
		f.beforeDetail = nil
		f.mu.Unlock()
		close(ready)
		<-release
	}
	basis := f.item("1", "A")["basis"]
	finished := make(chan int, 1)
	go func() {
		code, _ := f.request("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": "A", "operation": "close", "basis": basis})
		finished <- code
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("old preview did not reach detail barrier")
	}
	f.mapping("2", map[string]string{"a": "A", "a-alias": "A", "b": "B"})
	result := f.close("1", "A")
	if result["outcome"] != "closed" {
		t.Fatal("new close did not succeed")
	}
	once.Do(func() { close(release) })
	select {
	case code := <-finished:
		if code != 200 {
			t.Fatalf("old preview status=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late preview failed to terminate")
	}
	if f.item("1", "A")["control"].(map[string]any)["lastAttempt"] != nil || f.eventCount("close_blocked") != 0 {
		t.Fatal("late old block overwrote newer successful conclusion or created event")
	}
}
