package user_activity

import (
	"context"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type absorbRechargePlatform struct {
	Platform
	p                             *pacedReader
	redeemCalls, affiliateCalls   int
	brokenOrder, missingAffiliate bool
}

func (f *absorbRechargePlatform) FetchSub2APIUsedRedeemRecordsContext(_ context.Context, _ upstream.Session, kind string, page, size int) (upstream.Sub2APIRedeemRecordsPage, error) {
	f.redeemCalls++
	f.p.last = time.Time{}
	if size != 500 {
		panic("wrong redeem request budget")
	}
	at := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	old := at.AddDate(0, 0, -10)
	rows := []upstream.Sub2APIRedeemRecord{}
	if kind == "balance" {
		rows = []upstream.Sub2APIRedeemRecord{{ID: "1", Code: "PAY-test", UsedBy: "1", Type: kind, UsedAt: &at, Value: 20}, {ID: "2", Code: "PAY-test2", UsedBy: "1", Type: kind, UsedAt: &at, Value: 10}, {ID: "3", Code: "other", UsedBy: "1", Type: kind, UsedAt: &old, Value: 5}}
	} else {
		rows = []upstream.Sub2APIRedeemRecord{{ID: "4", Code: "admin", UsedBy: "1", Type: kind, UsedAt: &at, Value: -3}}
	}
	if f.brokenOrder && len(rows) > 1 {
		rows[0], rows[len(rows)-1] = rows[len(rows)-1], rows[0]
	}
	return upstream.Sub2APIRedeemRecordsPage{Items: rows, Total: len(rows), TotalKnown: true}, nil
}
func (f *absorbRechargePlatform) FetchSub2APIAffiliateTransfersContext(_ context.Context, _ upstream.Session, _, _ string, _, size int) (upstream.Sub2APIAffiliateTransfersPage, error) {
	f.affiliateCalls++
	f.p.last = time.Time{}
	if size != 100 {
		panic("wrong affiliate request budget")
	}
	if f.missingAffiliate {
		return upstream.Sub2APIAffiliateTransfersPage{}, &upstream.RequestError{StatusCode: 404}
	}
	return upstream.Sub2APIAffiliateTransfersPage{Items: []upstream.Sub2APIAffiliateTransfer{}, TotalKnown: true}, nil
}
func TestAbsorbIncomeWindowsShareFetchedPagesAndBudgets(t *testing.T) {
	p := &pacedReader{}
	f := &absorbRechargePlatform{p: p}
	s := NewService(nil, nil, f, false)
	defer s.cancel()
	c := newRechargeCollector(s, p, upstream.Session{})
	rows, channels, all, warning, err := c.window(t.Context(), "2026-11-18", "2026-11-20")
	if err != nil || !all || warning != "" || len(channels) != 5 || len(rows) != 2 {
		t.Fatal("complete channel window did not publish its known sources")
	}
	payment, deduction := 0.0, 0.0
	for _, row := range rows {
		if row.Channel == "payment" {
			payment = row.Amount
			if row.Events != 2 {
				t.Fatal("same-day payment records lost their event count")
			}
		}
		if row.Channel == "admin_deduct" {
			deduction = row.Amount
		}
	}
	if payment != 30 || deduction != -3 {
		t.Fatal("payment or deduction channel classification changed")
	}
	older, err := c.redeems(t.Context(), "balance", "2026-11-08", "2026-11-18")
	if err != nil || len(older) != 1 || older[0].Channel != "redeem" || f.redeemCalls != 2 {
		t.Fatal("older window reread already fetched income or lost unrequested older rows")
	}
	c.streams["balance"].done = false
	c.streams["balance"].last = nil
	c.streams["balance"].requests = 400
	if _, err := c.redeems(t.Context(), "balance", "2026-11-01", "2026-11-08"); err == nil || f.redeemCalls != 2 {
		t.Fatal("new window reset the shared 400-request limit")
	}
	c.affiliateRequests = 200
	if _, err := c.affiliates(t.Context(), "2026-11-01", "2026-11-08"); err == nil || f.affiliateCalls != 1 {
		t.Fatal("new window reset the shared affiliate request limit")
	}
}
func TestAbsorbIncomeMissingSourceIsPartialAndUnsortedIsRejected(t *testing.T) {
	p := &pacedReader{}
	f := &absorbRechargePlatform{p: p, missingAffiliate: true}
	s := NewService(nil, nil, f, false)
	defer s.cancel()
	c := newRechargeCollector(s, p, upstream.Session{})
	rows, _, all, warning, err := c.window(t.Context(), "2026-11-18", "2026-11-20")
	if err != nil || all || warning == "" || len(rows) != 2 {
		t.Fatal("unavailable affiliate source was treated as complete or erased other valid sources")
	}
	f.brokenOrder = true
	c = newRechargeCollector(s, p, upstream.Session{})
	if _, err := c.redeems(t.Context(), "balance", "2026-11-01", "2026-11-20"); err == nil {
		t.Fatal("unsorted upstream pages were accepted as complete")
	}
}
