package signup_risk

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestSignupRiskHoursOutsideDoesNotReadOrCommit(t *testing.T) {
	store := newRiskStore()
	now := time.Now().UTC()
	start, end := "00:00", "00:01"
	if now.Hour() == 0 && now.Minute() < 2 {
		start, end = "12:00", "13:00"
	}
	// JSON keeps this test executable on the old type; old code ignores hours
	// and demonstrably performs the forbidden external read.
	raw, _ := json.Marshal(map[string]string{"workStart": start, "workEnd": end, "timeZone": "UTC"})
	if err := json.Unmarshal(raw, &store.targets[0]); err != nil {
		t.Fatal(err)
	}
	remote := &riskRemote{rows: riskRows(1, 1, now)}
	svc := NewService(store, riskSessions{}, remote.client(t))
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(remote.calls) != 0 || store.commits != 0 || len(store.processed) != 0 {
		t.Fatalf("off-hours performed reads=%d commits=%d processed=%d", len(remote.calls), store.commits, len(store.processed))
	}
}

func TestSignupRiskHoursNextDayResumesWithoutReinitializingOrDuplicating(t *testing.T) {
	store := newRiskStore()
	scheduleJSON := []byte(`{"workStart":"09:00","workEnd":"18:00","timeZone":"UTC"}`)
	if err := json.Unmarshal(scheduleJSON, &store.targets[0]); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 1, 1, 9, 0, 0, 0, time.UTC)
	remote := &riskRemote{rows: riskRows(1, 1, now.Add(-time.Hour))}
	svc := NewService(store, riskSessions{}, remote.client(t))
	svc.now = func() time.Time { return now }
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.targets[0].Initialized || len(store.processed) != 1 || len(store.notified) != 0 {
		t.Fatal("initial scan must remain silent")
	}
	now = time.Date(2031, 1, 1, 18, 0, 0, 0, time.UTC)
	remote.rows = append(riskRows(2, 1, now.Add(time.Hour)), remote.rows...)
	remote.calls = nil
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(remote.calls) != 0 || !store.targets[0].Initialized || len(store.processed) != 1 {
		t.Fatal("end boundary must stop reads without resetting the baseline")
	}
	now = time.Date(2031, 1, 2, 9, 0, 0, 0, time.UTC)
	if err := svc.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.processed) != 2 || store.notified[riskStoreKey(store.targets[0], "2")] != 1 {
		t.Fatal("next working start must notify the overnight user once")
	}
	restarted := NewService(store, riskSessions{}, remote.client(t))
	restarted.now = func() time.Time { return now }
	if err := restarted.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.notified) != 1 || store.notified[riskStoreKey(store.targets[0], "2")] != 1 {
		t.Fatal("restart or repeat must not duplicate the overnight registration")
	}
}

type hoursEndDuringPage struct {
	client UsersClient
	finish func()
}

func (c hoursEndDuringPage) FetchSub2APIAdminUsersPageWithContext(ctx context.Context, session upstream.Session, query upstream.Sub2APIAdminUsersQuery) (upstream.Sub2APIAdminUsersPage, error) {
	result, err := c.client.FetchSub2APIAdminUsersPageWithContext(ctx, session, query)
	c.finish()
	return result, err
}
func TestSignupRiskHoursEndDuringPageCannotFetchNextOrCommit(t *testing.T) {
	for _, count := range []int{1, 51} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store := newRiskStore()
			if err := json.Unmarshal([]byte(`{"workStart":"09:00","workEnd":"18:00","timeZone":"UTC"}`), &store.targets[0]); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2031, 1, 1, 17, 59, 59, 0, time.UTC)
			remote := &riskRemote{rows: riskRows(count, count, now.Add(-time.Hour))}
			client := hoursEndDuringPage{client: remote.client(t), finish: func() { now = time.Date(2031, 1, 1, 18, 0, 0, 0, time.UTC) }}
			svc := NewService(store, riskSessions{}, client)
			svc.now = func() time.Time { return now }
			if err := svc.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(remote.calls) != 1 || store.commits != 0 || len(store.processed) != 0 || store.targets[0].Initialized {
				t.Fatal("a page crossing work end must neither fetch another page nor advance stored state")
			}
		})
	}
}
