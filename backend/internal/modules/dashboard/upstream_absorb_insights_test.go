package dashboard

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

func TestAbsorbInsightsOriginalTotalsAndQuality(t *testing.T) {
	span := OperatingInsightsRange{From: "2026-11-13", To: "2026-11-19", Days: 7}
	rows := []DailySnapshot{}
	for day := span.From; day <= span.To; day = operatingShift(day, 1) {
		date, _, _ := businesstime.Bounds(day)
		rows = append(rows, DailySnapshot{Date: date, SettlementStatus: "final", CostQualityMode: "retained", TodayProfit: ptrFloat64(100), TodayPurchase: ptrFloat64(30), OperatingCost: ptrFloat64(5), AdjustedNetProfit: ptrFloat64(63)})
	}
	totals, missing, _ := operatingSnapshotTotals(rows, span)
	if totals == nil || len(missing) != 0 || totals.TodayProfit != 700 || totals.TodayPurchase != 210 || totals.OperatingCost != 35 || totals.AdjustedNetProfit != 441 || totals.ProfitMargin == nil || *totals.ProfitMargin != .63 {
		t.Fatal("original stored amounts were recalculated or merged incorrectly")
	}
	for _, bad := range []string{"absent", "duplicate", "partial", "provisional", "nil", "nonfinite"} {
		t.Run(bad, func(t *testing.T) {
			copy := append([]DailySnapshot(nil), rows...)
			switch bad {
			case "absent":
				copy = copy[1:]
			case "duplicate":
				copy = append(copy, copy[0])
			case "partial":
				copy[0].CostQualityMode = "partial"
			case "provisional":
				copy[0].SettlementStatus = "provisional"
			case "nil":
				copy[0].TodayProfit = nil
			case "nonfinite":
				copy[0].TodayPurchase = ptrFloat64(math.Inf(1))
			}
			totals, missing, warnings := operatingSnapshotTotals(copy, span)
			if totals != nil || len(missing) != 1 || len(warnings) != 1 {
				t.Fatal("incomplete original records produced a partial seven-day total")
			}
		})
	}
}

func TestAbsorbInsightsBalanceEstimateDoesNotInventMissingCost(t *testing.T) {
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	stamp := now.UnixMilli()
	span := OperatingInsightsRange{From: "2026-11-13", To: "2026-11-19", Days: 7}
	costs := map[string]map[string][]SiteDailyCost{}
	for day := span.From; day <= span.To; day = operatingShift(day, 1) {
		date, _, _ := businesstime.Bounds(day)
		costs[day] = map[string][]SiteDailyCost{"site": {{Date: date, RawCost: ptrFloat64(10), Status: "ok", Source: "dated_query", ObservedAt: &now}}}
	}
	site := upstream.BalanceSnapshot{SiteID: "site", Enabled: true, Status: upstream.StatusConnected, RawBalance: ptrFloat64(15), LastSyncedAt: &stamp}
	valid := operatingSiteEstimate(site, costs, span, now)
	if valid.DaysLeft == nil || *valid.DaysLeft != 1.5 || valid.SuggestedTopUp == nil || *valid.SuggestedTopUp != 55 {
		t.Fatal("complete original balance/cost estimate was wrong")
	}
	for _, bad := range []string{"missing cost", "stale", "future", "connecting", "sync error", "unknown balance", "negative cost", "zero cost", "disabled"} {
		t.Run(bad, func(t *testing.T) {
			copy := site
			rows := map[string]map[string][]SiteDailyCost{}
			for day, items := range costs {
				rows[day] = map[string][]SiteDailyCost{"site": append([]SiteDailyCost(nil), items["site"]...)}
			}
			switch bad {
			case "missing cost":
				delete(rows, span.From)
			case "stale":
				v := now.Add(-3 * time.Hour).UnixMilli()
				copy.LastSyncedAt = &v
			case "future":
				v := now.Add(time.Hour).UnixMilli()
				copy.LastSyncedAt = &v
			case "connecting":
				copy.Status = upstream.StatusConnecting
			case "sync error":
				copy.Status = upstream.StatusError
			case "unknown balance":
				copy.RawBalance = nil
			case "negative cost":
				rows[span.From]["site"][0].RawCost = ptrFloat64(-1)
			case "zero cost":
				for _, items := range rows {
					items["site"][0].RawCost = ptrFloat64(0)
				}
			case "disabled":
				copy.Enabled = false
			}
			got := operatingSiteEstimate(copy, rows, span, now)
			if got.SuggestedTopUp != nil || len(got.Reasons) == 0 {
				t.Fatal("unsafe or disabled data produced a top-up suggestion")
			}
			if bad != "disabled" && got.DaysLeft != nil {
				t.Fatal("missing evidence was converted into a usable day estimate")
			}
		})
	}
}

type absorbInsightsRepo struct {
	*fakeMetricsRepository
	checks, costReads int
	err               error
	changed           bool
}

func (r *absorbInsightsRepo) verifyOperatingScope(context.Context, workspacescope.Scope) error {
	r.checks++
	if r.changed && r.checks == 2 {
		return workspacescope.ErrChanged
	}
	return nil
}
func (r *absorbInsightsRepo) ListSiteCosts(context.Context, string, string, string) ([]SiteDailyCost, error) {
	r.costReads++
	return nil, r.err
}

type absorbInsightsUpstreams struct {
	*fakeUpstreamLister
	reads int
	err   error
}

func (u *absorbInsightsUpstreams) BalanceSnapshotForWorkspace(context.Context, string, string) ([]upstream.BalanceSnapshot, error) {
	u.reads++
	return nil, u.err
}
func TestAbsorbInsightsOnlyReadsOriginalStores(t *testing.T) {
	for _, kind := range []string{"ok", "cost unavailable", "cache unavailable", "scope changed"} {
		t.Run(kind, func(t *testing.T) {
			repo := &absorbInsightsRepo{fakeMetricsRepository: &fakeMetricsRepository{}}
			sites := &absorbInsightsUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}}
			switch kind {
			case "cost unavailable":
				repo.err = errors.New("fixture read unavailable")
			case "cache unavailable":
				sites.err = errors.New("fixture cache unavailable")
			case "scope changed":
				repo.changed = true
			}
			s := NewMetricsService(nil, nil, sites, repo, nil)
			got, err := s.OperatingInsightsForWorkspace(t.Context(), workspacescope.Scope{UserID: "fixture", AdminAccountID: "workspace", CreatedAt: "generation"})
			if kind == "ok" {
				if err != nil || repo.costReads != 7 || repo.checks != 2 || sites.reads != 1 || got.SnapshotTotals != nil {
					t.Fatal("readonly result did not keep seven original closed-day reads")
				}
			} else if err == nil {
				t.Fatal("read failure was swallowed")
			}
			if sites.keyUsageCalls != 0 {
				t.Fatal("operating hints triggered upstream collection")
			}
		})
	}
}
