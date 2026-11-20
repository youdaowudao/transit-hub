package sales

import (
	"testing"
	"transithub/backend/internal/shared/businesstime"
)

func TestAbsorbReview4FirstCreditUsesOnlyCompleteCoverage(t *testing.T) {
	pool, scope := absorbStore(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	today := businesstime.Today()
	coverFrom := shift(today, -30)
	outside := shift(coverFrom, -1)
	inside := shift(coverFrom, 1)
	from, to := shift(today, -7), shift(today, -1)
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,recharge_from,recharge_to) VALUES($1,$2,'success',$3::date,$4::date)`, scope.UserID, scope.AdminAccountID, coverFrom, to); err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct {
		id, day, channel string
		amount           float64
	}{{"outside-user", outside, "affiliate", 5}, {"outside-user", to, "payment", 20}, {"inside-user", inside, "payment", 6}, {"inside-user", to, "payment", 30}} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_recharge_daily VALUES($1,$2,$3,$4::date,$5,$6,1)`, scope.UserID, scope.AdminAccountID, record.id, record.day, record.channel, record.amount); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(repo, false)
	summary, err := s.Summary(t.Context(), scope, DayRange{From: from, To: to, Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.NewPayers == nil || *summary.Totals.NewPayers != 1 {
		t.Error("partial record before complete coverage changed first-credit cohort")
	}
	entries, err := s.Entries(t.Context(), scope, DayRange{From: outside, To: to, Days: 31}, EntryQuery{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, entry := range entries.Items {
		if entry.SiteUserID == "outside-user" && entry.Day == outside {
			visible = true
		}
	}
	if !visible || entries.Coverage.Complete {
		t.Error("outside partial history was removed or incorrectly marked complete")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM user_activity_recharge_daily WHERE site_user_id='inside-user' AND day=$1::date`, inside); err != nil {
		t.Fatal(err)
	}
	summary, err = s.Summary(t.Context(), scope, DayRange{From: from, To: to, Days: 7})
	if err != nil || summary.Totals.NewPayers == nil || *summary.Totals.NewPayers != 2 {
		t.Error("earlier covered first credit did not exclude only its actual cohort", err)
	}
}
