package sales

import (
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

func TestAbsorbSalesExclusionSnapshotsAndCohorts(t *testing.T) {
	pool, scope := absorbStore(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, false)
	today := businesstime.Today()
	from, to := shift(today, -7), shift(today, -1)
	span := DayRange{From: from, To: to, Days: 7}
	if _, err := pool.Exec(ctx, `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,recharge_from,recharge_to) VALUES($1,$2,'success',$3::date,$4::date)`, scope.UserID, scope.AdminAccountID, shift(today, -40), to); err != nil {
		t.Fatal(err)
	}
	start, _, _ := businesstime.Bounds(from)
	old := start.AddDate(0, 0, -10)
	for _, p := range []struct {
		id, role, status string
		created          time.Time
	}{{"new", "user", "active", start}, {"old", "user", "active", old}, {"admin", "admin", "active", start}, {"disabled", "user", "disabled", start}} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_activity_profiles(user_id,admin_account_id,site_user_id,email,username,role,status,site_created_at,first_observed_at,updated_at) VALUES($1,$2,$3,'fixture@example.invalid','fixture',$4,$5,$6,now(),now())`, scope.UserID, scope.AdminAccountID, p.id, p.role, p.status, p.created); err != nil {
			t.Fatal(err)
		}
	}
	// These profiles represent a completed inventory publication, including its observation.
	if _, err := pool.Exec(ctx, `INSERT INTO user_activity_site_daily(user_id,admin_account_id,day,user_count,total_balance,observed_at) VALUES($1,$2,$3::date,4,0,now())`, scope.UserID, scope.AdminAccountID, today); err != nil {
		t.Fatal(err)
	}
	insert := func(id, day, channel string, amount float64, events int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO user_activity_recharge_daily VALUES($1,$2,$3,$4::date,$5,$6,$7)`, scope.UserID, scope.AdminAccountID, id, day, channel, amount, events); err != nil {
			t.Fatal(err)
		}
	}
	insert("new", to, "payment", 100, 10)
	insert("new", to, "redeem", 10, 2)
	insert("old", to, "payment", 50, 1)
	insert("old", shift(to, -1), "payment", 50, 1)
	insert("old", shift(from, -1), "payment", 1, 1)
	insert("old", to, "admin_deduct", -5, 1)
	before, err := s.Summary(ctx, scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if before.Totals.Credits == nil || *before.Totals.Credits != 210 || before.Totals.Deductions == nil || *before.Totals.Deductions != 5 || before.Totals.Payers == nil || *before.Totals.Payers != 2 {
		t.Fatal("positive income, deductions and payers are inconsistent")
	}
	if before.Totals.RepeatPayers == nil || *before.Totals.RepeatPayers != 1 {
		t.Fatal("same-day channels/events incorrectly counted as repeat days")
	}
	if before.Totals.Registered == nil || *before.Totals.Registered != 1 || before.Totals.RegisteredPaid == nil || *before.Totals.RegisteredPaid != 1 || before.Totals.Conversion == nil || *before.Totals.Conversion != 1 {
		t.Fatal("registration conversion mixed in old, admin or disabled customers")
	}
	key := EntryKey{SiteUserID: "new", Day: to, Channel: "payment"}
	result, err := s.ChangeExclusions(ctx, scope, []EntryKey{key, key}, false)
	if err != nil || result.Requested != 1 || result.Changed != 1 {
		t.Fatal("initial exclusion was not unique/committed")
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_recharge_daily SET amount=120 WHERE site_user_id='new' AND channel='payment'`); err != nil {
		t.Fatal(err)
	}
	result, err = s.ChangeExclusions(ctx, scope, []EntryKey{key}, false)
	if err != nil || result.Changed != 0 || result.Unchanged != 1 {
		t.Fatal("retry expanded or recreated the original exclusion")
	}
	var excluded float64
	if err := pool.QueryRow(ctx, `SELECT amount FROM sales_excluded_entries`).Scan(&excluded); err != nil || excluded != 100 {
		t.Fatal("exclusion amount did not retain its first snapshot")
	}
	after, err := s.Summary(ctx, scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if after.Totals.Credits == nil || *after.Totals.Credits != 130 || after.Totals.Deductions == nil || *after.Totals.Deductions != 5 {
		t.Fatal("net growth or deduction formula changed")
	}
	entries, err := s.Entries(ctx, scope, span, EntryQuery{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries.Items {
		if entry.SiteUserID == "new" && entry.Channel == "payment" {
			found = true
			if entry.RawAmount != 120 || entry.ExcludedAmount != 100 || entry.EffectiveAmount != 20 {
				t.Fatal("management detail lost the partial exclusion")
			}
		}
	}
	if !found {
		t.Fatal("excluded row disappeared from management")
	}
	missing := EntryKey{SiteUserID: "missing", Day: to, Channel: "payment"}
	if _, err := s.ChangeExclusions(ctx, scope, []EntryKey{{SiteUserID: "old", Day: to, Channel: "payment"}, missing}, false); err == nil {
		t.Fatal("invalid selection partly succeeded")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sales_excluded_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatal("invalid batch saved a partial exclusion")
	}
	if _, err := s.SaveSettings(ctx, scope, []string{"payment", "payment"}); err != nil {
		t.Fatal(err)
	}
	filtered, err := s.Summary(ctx, scope, span)
	if err != nil || filtered.Totals.Credits == nil || *filtered.Totals.Credits != 10 {
		t.Fatal("channel exclusion did not take priority over individual entries")
	}
	if _, err := s.ChangeExclusions(ctx, scope, []EntryKey{key}, true); err != nil {
		t.Fatal(err)
	}
	result, err = s.ChangeExclusions(ctx, scope, []EntryKey{key}, true)
	if err != nil || result.Changed != 0 {
		t.Fatal("restore was not idempotent")
	}
	if _, err := pool.Exec(ctx, `UPDATE user_activity_sync_states SET recharge_from=NULL,recharge_to=NULL`); err != nil {
		t.Fatal(err)
	}
	partial, err := s.Summary(ctx, scope, span)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Totals.Conversion != nil || partial.Totals.NewPayers != nil || partial.Totals.RepeatPayers != nil {
		t.Fatal("incomplete income produced reliable conversion/first/repeat metrics")
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_accounts SET created_at=created_at+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeExclusions(ctx, scope, []EntryKey{key}, false); !errors.Is(err, workspacescope.ErrChanged) {
		t.Fatal("stale workspace saved exclusions")
	}
}
