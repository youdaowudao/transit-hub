package user_activity

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync/atomic"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type review3UnavailableSession struct{ calls atomic.Int32 }

func (s *review3UnavailableSession) StoredSession(context.Context, string, string) (upstream.Session, bool, error) {
	s.calls.Add(1)
	return upstream.Session{}, false, nil
}

func TestAbsorbReview3FailedTerminalSaveRecoversWithoutEarlyCollection(t *testing.T) {
	pool, scope := absorbStore(t)
	day := shift(businesstime.Today(), -1)
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_daily VALUES($1,$2,'preserved',$3::date,5,0,2)`, scope.UserID, scope.AdminAccountID, day); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_terminal_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status <> 'running' THEN RAISE EXCEPTION 'fixture-terminal-save-unavailable'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_terminal_fixture BEFORE INSERT OR UPDATE ON user_activity_sync_states FOR EACH ROW EXECUTE FUNCTION reject_terminal_fixture()`); err != nil {
		t.Fatal(err)
	}
	sessions := &review3UnavailableSession{}
	s := NewService(NewRepository(pool), sessions, nil, false)
	defer s.Shutdown(context.Background())
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(original)
	started, err := s.StartSync(t.Context(), scope, true)
	if err != nil || started.Status != "running" {
		t.Fatal("cannot start isolated task", err)
	}
	s.wg.Wait()
	state, err := s.repo.Sync(t.Context(), scope)
	if err != nil || state.Status != "running" {
		t.Fatal("terminal write injection did not preserve orphan running state", err)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_terminal_fixture ON user_activity_sync_states;DROP FUNCTION reject_terminal_fixture()`); err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var locked bool
	if err := conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, activityLock).Scan(&locked); err != nil || !locked {
		conn.Release()
		t.Fatal("completed collector left lock held")
	}
	s.runScheduled(t.Context())
	blocked, err := s.repo.Sync(t.Context(), scope)
	if err != nil || blocked.Status != "running" || sessions.calls.Load() != 1 {
		releaseLock(conn)
		t.Fatal("periodic check ignored another active collector lock", err)
	}
	releaseLock(conn)
	s.runScheduled(t.Context())
	s.wg.Wait()
	recovered, err := s.repo.Sync(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "failed" || !strings.Contains(recovered.Error, "保存") || recovered.LastSyncedAt != nil {
		t.Errorf("restored storage left recently finished task running or claimed success: %+v", recovered)
	}
	if sessions.calls.Load() != 1 {
		t.Error("recovery started another collection before the hourly due time")
	}
	var requests int64
	if err := pool.QueryRow(t.Context(), `SELECT requests FROM user_activity_daily WHERE site_user_id='preserved'`).Scan(&requests); err != nil || requests != 5 {
		t.Error("recovery changed already-published usage", err)
	}
	if !strings.Contains(logs.String(), "fixture-terminal-save-unavailable") {
		t.Error("terminal publication failure was discarded instead of logged")
	}
}
