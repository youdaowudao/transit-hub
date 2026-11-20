package reports

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/sales"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
)

func TestAbsorbReview2ReportAmountCategoriesStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, channel      string
		amount             float64
		complete           bool
		positive, negative string
	}{
		{"only positive", "payment", 20, false, "20.00", "未取得"},
		{"only negative", "admin_deduct", -8, false, "未取得", "8.00"},
		{"confirmed zero", "", 0, true, "0.00", "0.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope := absorbStore(t)
			repo := sales.NewRepository(pool)
			if err := repo.EnsureSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			day := shift(businesstime.DateAt(now), -1)
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
			notifier := settings.NewService(&http.Client{Transport: absorbReportTransport(func(*http.Request) (*http.Response, error) {
				t.Error("preview attempted notification")
				return nil, context.Canceled
			})}, settings.NewRepository(pool))
			s := NewService(NewRepository(pool), &absorbReportMetrics{}, sales.NewService(repo, false), notifier, nil, false)
			defer s.cancel()
			document, err := s.build(t.Context(), scope, now)
			if err != nil {
				t.Fatal(err)
			}
			want := "正向有效入账：" + tc.positive + "；扣款单列：" + tc.negative
			if !strings.Contains(document.Text, want) {
				t.Errorf("report invented zero for a missing amount category: want %q; got %s", want, document.Sections[1].Text)
			}
			if document.Sections[1].Coverage.Complete != tc.complete {
				t.Error("amount nullability changed existing coverage")
			}
		})
	}
}
