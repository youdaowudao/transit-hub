package user_activity

import (
	"testing"
	"transithub/backend/internal/shared/businesstime"
)

func TestAbsorbReview2ActivityAmountCategoriesStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, channel      string
		amount             float64
		complete           bool
		positive, negative *float64
	}{
		{"only positive", "payment", 20, false, pointer(float64(20)), nil},
		{"only negative", "admin_deduct", -8, false, nil, pointer(float64(8))},
		{"confirmed zero", "", 0, true, pointer(float64(0)), pointer(float64(0))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope := absorbStore(t)
			today := businesstime.Today()
			yesterday := shift(today, -1)
			if tc.channel != "" {
				for _, day := range []string{yesterday, today} {
					if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_recharge_daily VALUES($1,$2,'customer',$3::date,$4,$5,1)`, scope.UserID, scope.AdminAccountID, day, tc.channel, tc.amount); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.complete {
				if _, err := pool.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,recharge_from,recharge_to) VALUES($1,$2,'success',$3::date,$3::date)`, scope.UserID, scope.AdminAccountID, yesterday); err != nil {
					t.Fatal(err)
				}
			}
			s := NewService(NewRepository(pool), nil, nil, false)
			defer s.cancel()
			got, err := s.Summary(t.Context(), scope, 2)
			if err != nil {
				t.Fatal(err)
			}
			check := func(name string, got, want *float64) {
				t.Helper()
				if (got == nil) != (want == nil) || (want != nil && *got != *want) {
					t.Errorf("%s: amount category unknown/zero was mixed: got %v want %v", name, got, want)
				}
			}
			check("yesterday recharge", got.Money.YesterdayRecharge, tc.positive)
			check("yesterday deduction", got.Money.YesterdayDeduction, tc.negative)
			check("daily recharge", got.Daily[0].Recharge, tc.positive)
			check("daily deduction", got.Daily[0].Deduction, tc.negative)
			if !tc.complete {
				check("today recharge", got.Money.TodayRecharge, tc.positive)
				check("today deduction", got.Money.TodayDeduction, tc.negative)
				check("today point recharge", got.Today.Recharge, tc.positive)
				check("today point deduction", got.Today.Deduction, tc.negative)
			} else if got.Money.TodayRecharge != nil || got.Money.TodayDeduction != nil {
				t.Error("closed-day coverage was reused as proof of current-day zero")
			}
		})
	}
}
