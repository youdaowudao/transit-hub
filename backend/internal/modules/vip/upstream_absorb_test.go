package vip

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

func TestAbsorbVIPQualificationAndEpisodes(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	today := businesstime.DateAt(now)
	balance := 5.0
	state := syncSnapshot{Status: "success", From: shift(today, -14), To: shift(today, -1), StartedAt: &now, SyncedAt: &now}
	raw := Row{ProfilePresent: true, ObservedAt: &now, UserStatus: "active", Role: "user", Balance: &balance, Requests7: 7, Cost7: 70, CostPrevious7: 140}
	row := raw
	classify(&row, DefaultSettings(), state, today)
	if row.Status != "low" || row.DailyAverage == nil || *row.DailyAverage != 10 || row.DaysLeft == nil || *row.DaysLeft != 0.5 || !coherent(state, row, today, now) {
		t.Fatal("complete eligible low-balance customer was not classified correctly")
	}
	for _, bad := range []string{"running", "failed", "idle"} {
		copy := state
		copy.Status = bad
		if coherent(copy, row, today, now) {
			t.Fatal("mixed or incomplete sync admitted an alert")
		}
	}
	old := now.Add(-3 * time.Hour)
	copy := state
	copy.StartedAt = &old
	copy.SyncedAt = &old
	rowCopy := row
	rowCopy.ObservedAt = &old
	if coherent(copy, rowCopy, today, now) {
		t.Fatal("stale data admitted an alert")
	}
	rowCopy = row
	rowCopy.Role = "admin"
	if coherent(state, rowCopy, today, now) {
		t.Fatal("administrator admitted an alert")
	}
	rowCopy = row
	rowCopy.UserStatus = "disabled"
	classify(&rowCopy, DefaultSettings(), state, today)
	if rowCopy.Status != "disabled" || coherent(state, rowCopy, today, now) {
		t.Fatal("disabled customer was not excluded")
	}
	copy = state
	copy.From = ""
	copy.To = ""
	rowCopy = raw
	classify(&rowCopy, DefaultSettings(), copy, today)
	if rowCopy.Status != "low" || rowCopy.DaysLeft != nil || rowCopy.DailyAverage != nil || coherent(copy, rowCopy, today, now) {
		t.Fatal("partial known low balance was hidden or estimated as complete")
	}
	for _, tc := range []struct {
		name, state, delivery string
		recovered             bool
		age                   time.Duration
		want                  bool
	}{
		{"no daily repeat", "low", "sent", false, 48 * time.Hour, false},
		{"sent permits low to empty", "empty", "sent", false, time.Minute, true},
		{"unknown never appends", "empty", "unknown", false, 48 * time.Hour, false},
		{"sending never appends", "empty", "sending", false, 48 * time.Hour, false},
		{"recovered before minimum", "low", "sent", true, 23 * time.Hour, false},
		{"new episode after minimum", "low", "unknown", true, 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := &alertState{State: "low", Delivery: tc.delivery, Recovered: tc.recovered, At: now.Add(-tc.age)}
			if shouldNotify(tc.state, previous, now) != tc.want {
				t.Fatal("notification episode rule changed")
			}
		})
	}
	if DefaultSettings().AlertsEnabled || len(DefaultSettings().BotIDs) != 0 {
		t.Fatal("new alerts are not opt-in")
	}
}

type absorbVIPTransport func(*http.Request) (*http.Response, error)

func (f absorbVIPTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAbsorbVIPSearchRejectsRecreatedWorkspace(t *testing.T) {
	pool, scope := absorbStore(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE admin_accounts SET created_at=created_at+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_profiles(user_id,admin_account_id,site_user_id,email,username,role,status,first_observed_at,updated_at) VALUES($1,$2,'new-generation','new@example.invalid','new','user','active',now(),now())`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Search(t.Context(), scope, ""); !errors.Is(err, workspacescope.ErrChanged) {
		t.Fatal("search returned replacement-generation profiles to an old request")
	}
}

func TestAbsorbVIPCommittedAdmissionAndNoResurrection(t *testing.T) {
	pool, scope := absorbStore(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Truncate(time.Microsecond)
	today := businesstime.DateAt(started)
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,$3::jsonb,now())`, scope.UserID, scope.AdminAccountID, `{"telegram":[{"id":"fixture-bot","name":"isolated recipient","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_activity_profiles(user_id,admin_account_id,site_user_id,email,username,role,status,balance,first_observed_at,updated_at) VALUES($1,$2,'customer','fixture@example.invalid','fixture','user','active',5,$3,$3)`, scope.UserID, scope.AdminAccountID, started); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,usage_from,usage_to,user_count,last_started_at,last_synced_at) VALUES($1,$2,'success',$3::date,$4::date,1,$5,$5)`, scope.UserID, scope.AdminAccountID, shift(today, -14), shift(today, -1), started); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 14; n++ {
		if _, err := pool.Exec(ctx, `INSERT INTO user_activity_daily VALUES($1,$2,'customer',$3::date,1,0,10)`, scope.UserID, scope.AdminAccountID, shift(today, -n)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	committed := false
	reply := `{"ok":true,"result":{"message_id":42}}`
	notifier := settings.NewService(&http.Client{Transport: absorbVIPTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var status string
		err := pool.QueryRow(ctx, `SELECT delivery_status FROM vip_alert_states WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id='customer'`, scope.UserID, scope.AdminAccountID).Scan(&status)
		committed = err == nil && status == "sending"
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply)), Request: r}, nil
	})}, settings.NewRepository(pool))
	s := NewService(repo, notifier, false)
	defer s.Shutdown(context.Background())
	cfg := DefaultSettings()
	cfg.AlertsEnabled = true
	cfg.BotIDs = []string{"fixture-bot"}
	if err := s.SaveSettings(ctx, scope, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !committed {
		t.Fatal("HTTP occurred without committed registration or did not occur")
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT delivery_status FROM vip_alert_states`).Scan(&status); err != nil || status != "sent" {
		t.Fatal("confirmed delivery was not persisted")
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("same episode was sent again")
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_sync_states SET status='failed';UPDATE user_activity_profiles SET balance=0`); err != nil {
		t.Fatal(err)
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("failed new sync reused old coverage to send")
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_sync_states SET status='success'`); err != nil {
		t.Fatal(err)
	}
	reply = `{"ok":true}`
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("confirmed low-to-empty escalation was not attempted")
	}
	if err := pool.QueryRow(ctx, `SELECT delivery_status FROM vip_alert_states`).Scan(&status); err != nil || status != "unknown" {
		t.Fatal("unconfirmed Telegram result was called sent")
	}
	if err := s.alertWorkspace(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("unknown result was automatically repeated")
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_accounts SET created_at=created_at+interval '1 second';DELETE FROM vip_alert_states`); err != nil {
		t.Fatal(err)
	}
	item := attempt{Scope: scope, SiteUserID: "customer", State: "empty", At: started}
	if err := finish(ctx, pool, item, settings.DeliveryResult{Status: settings.DeliverySent}); err == nil {
		t.Fatal("late old-generation completion was accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM vip_alert_states`).Scan(&count); err != nil || count != 0 {
		t.Fatal("late completion recreated deleted state")
	}
}
