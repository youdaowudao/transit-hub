package sales

import (
	"encoding/json"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

func reviewInventory(t *testing.T, scope Scope, s *Service, count int) {
	t.Helper()
	if _, err := s.repo.db.Exec(t.Context(), `INSERT INTO user_activity_site_daily(user_id,admin_account_id,day,user_count,total_balance,observed_at) VALUES($1,$2,$3::date,$4,0,now())`, scope.UserID, scope.AdminAccountID, businesstime.Today(), count); err != nil {
		t.Fatal(err)
	}
}
func reviewRegistered(t *testing.T, totals Totals) any {
	t.Helper()
	raw, err := json.Marshal(totals)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields["registered"]
}
func TestAbsorbReview1SalesRegistrationBusinessDayBounds(t *testing.T) {
	pool, scope := absorbStore(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, false)
	today := businesstime.Today()
	span := DayRange{From: shift(today, -7), To: shift(today, -1), Days: 7}
	start, _, _ := businesstime.Bounds(span.From)
	last, _, _ := businesstime.Bounds(span.To)
	next := last.AddDate(0, 0, 1)
	for _, p := range []struct {
		id string
		at time.Time
	}{{"before", start.Add(-time.Second)}, {"start", start}, {"last", next.Add(-time.Second)}, {"next-midnight", next}, {"next-noon", next.Add(12 * time.Hour)}} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_profiles(user_id,admin_account_id,site_user_id,email,username,role,status,site_created_at,first_observed_at,updated_at) VALUES($1,$2,$3,'fixture@example.invalid','fixture','user','active',$4,now(),now())`, scope.UserID, scope.AdminAccountID, p.id, p.at); err != nil {
			t.Fatal(err)
		}
	}
	reviewInventory(t, scope, s, 5)
	result, err := s.Summary(t.Context(), scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewRegistered(t, result.Totals); got != float64(2) {
		t.Fatal("registration boundary includes users outside the selected closed days")
	}
}
func TestAbsorbReview1SalesUnknownInventoryAndEmptyChannels(t *testing.T) {
	pool, scope := absorbStore(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, false)
	today := businesstime.Today()
	span := DayRange{From: shift(today, -7), To: shift(today, -1), Days: 7}
	unknown, err := s.Summary(t.Context(), scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if reviewRegistered(t, unknown.Totals) != nil {
		t.Error("inventory not observed was displayed as zero registrations")
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_recharge_daily VALUES($1,$2,'customer',$3::date,'payment',20,1)`, scope.UserID, scope.AdminAccountID, span.To); err != nil {
		t.Fatal(err)
	}
	partial, err := s.Summary(t.Context(), scope, span)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range partial.Channels {
		if ch.Channel == "affiliate" && (ch.RawAmount != nil || ch.ExcludedAmount != nil || ch.EffectiveAmount != nil) {
			t.Error("missing affiliate source was converted into a confirmed zero")
		}
		if ch.Channel == "payment" && (ch.RawAmount == nil || *ch.RawAmount != 20) {
			t.Error("known partial payment was lost")
		}
	}
	reviewInventory(t, scope, s, 0)
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,recharge_from,recharge_to) VALUES($1,$2,'success',$3::date,$4::date)`, scope.UserID, scope.AdminAccountID, span.From, span.To); err != nil {
		t.Fatal(err)
	}
	complete, err := s.Summary(t.Context(), scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if reviewRegistered(t, complete.Totals) != float64(0) {
		t.Fatal("confirmed empty inventory did not preserve actual zero")
	}
	for _, ch := range complete.Channels {
		if ch.Channel == "affiliate" && (ch.RawAmount == nil || *ch.RawAmount != 0 || ch.ExcludedAmount == nil || *ch.ExcludedAmount != 0 || ch.EffectiveAmount == nil || *ch.EffectiveAmount != 0) {
			t.Fatal("confirmed covered empty affiliate channel lost its zero")
		}
	}
}
