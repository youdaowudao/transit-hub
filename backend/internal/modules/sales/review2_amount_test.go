package sales

import (
	"testing"
	"transithub/backend/internal/shared/businesstime"
)

func TestAbsorbReview2SalesAmountCategoriesStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, channel       string
		amount              float64
		complete, excluded  bool
		credits, deductions *float64
	}{
		{"only positive", "payment", 20, false, false, reviewAmount(20), nil},
		{"only negative", "admin_deduct", -8, false, false, nil, reviewAmount(8)},
		{"confirmed zero", "", 0, true, false, reviewAmount(0), reviewAmount(0)},
		{"excluded positive is known zero", "payment", 20, false, true, reviewAmount(0), nil},
		{"excluded negative is known zero", "admin_deduct", -8, false, true, nil, reviewAmount(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope := absorbStore(t)
			repo := NewRepository(pool)
			if err := repo.EnsureSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			day := shift(businesstime.Today(), -1)
			if tc.channel != "" {
				if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_recharge_daily VALUES($1,$2,'customer',$3::date,$4,$5,1)`, scope.UserID, scope.AdminAccountID, day, tc.channel, tc.amount); err != nil {
					t.Fatal(err)
				}
			}
			if tc.complete {
				if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,recharge_from,recharge_to) VALUES($1,$2,'success',$3::date,$3::date)`, scope.UserID, scope.AdminAccountID, day); err != nil {
					t.Fatal(err)
				}
			}
			if tc.excluded {
				if _, err := pool.Exec(t.Context(), `INSERT INTO sales_settings VALUES($1,$2,ARRAY[$3]::text[])`, scope.UserID, scope.AdminAccountID, tc.channel); err != nil {
					t.Fatal(err)
				}
			}
			got, err := NewService(repo, false).Summary(t.Context(), scope, DayRange{From: day, To: day, Days: 1})
			if err != nil {
				t.Fatal(err)
			}
			check := func(name string, got, want *float64) {
				t.Helper()
				if (got == nil) != (want == nil) || (want != nil && *got != *want) {
					t.Errorf("%s: missing amount category became zero or known amount changed; got %v want %v", name, got, want)
				}
			}
			check("summary credits", got.Totals.Credits, tc.credits)
			check("summary deductions", got.Totals.Deductions, tc.deductions)
			if len(got.Daily) != 1 {
				t.Fatal("missing daily result")
			}
			check("daily credits", got.Daily[0].Credits, tc.credits)
			check("daily deductions", got.Daily[0].Deductions, tc.deductions)
			wantExcluded := tc.credits
			if wantExcluded != nil {
				wantExcluded = reviewAmount(0)
				if tc.excluded {
					wantExcluded = reviewAmount(tc.amount)
				}
			}
			wantExcludedDeductions := tc.deductions
			if wantExcludedDeductions != nil {
				wantExcludedDeductions = reviewAmount(0)
				if tc.excluded {
					wantExcludedDeductions = reviewAmount(-tc.amount)
				}
			}
			check("excluded positive category", got.Exclusions.Amount, wantExcluded)
			check("excluded deductions category", got.Exclusions.Deductions, wantExcludedDeductions)
			entries, err := NewService(repo, false).Entries(t.Context(), scope, DayRange{From: day, To: day, Days: 1}, EntryQuery{Page: 1, PageSize: 20})
			if err != nil {
				t.Fatal(err)
			}
			check("entries excluded positive category", entries.Exclusions.Amount, wantExcluded)
			check("entries excluded deductions category", entries.Exclusions.Deductions, wantExcludedDeductions)
			if tc.channel == "payment" {
				changed, err := NewService(repo, false).ChangeExclusions(t.Context(), scope, []EntryKey{{SiteUserID: "customer", Day: day, Channel: tc.channel}}, false)
				if err != nil || changed.Exclusions == nil {
					t.Fatal("cannot read committed exclusion result", err)
				}
				check("mutation excluded positive category", changed.Exclusions.Amount, reviewAmount(20))
				check("mutation excluded deductions category", changed.Exclusions.Deductions, nil)
			}
			if tc.credits == nil && got.Totals.Payers != nil {
				t.Error("unobserved positive category became a confirmed payer count")
			}
			if tc.credits != nil && (got.Totals.Payers == nil || (tc.excluded && *got.Totals.Payers != 0)) {
				t.Error("known positive category lost its payer count")
			}
		})
	}
}

func reviewAmount(value float64) *float64 { return &value }
