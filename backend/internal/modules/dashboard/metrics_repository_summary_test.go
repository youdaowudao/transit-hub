package dashboard

import "testing"

func TestSummarizeConfirmedSiteCostsUsesCurrentTargetsAndRetainsSameDayConfirmation(t *testing.T) {
	confirmed := 30.0
	fresh := 12.0
	staleDisabled := 99.0
	runID := "run-current"
	summary := summarizeConfirmedSiteCosts(
		[]SiteDailyCost{
			{
				SiteID: "site-retained", AdjustedCost: &confirmed, Status: "ok", Source: "dated_query",
				LastAttemptStatus: "failed", LastAttemptRunID: runID,
			},
			{
				SiteID: "site-fresh", AdjustedCost: &fresh, Status: "ok", Source: "dated_query",
				LastAttemptStatus: "ok", LastAttemptRunID: runID,
			},
			{SiteID: "removed-site", AdjustedCost: &staleDisabled, Status: "ok", Source: "dated_query"},
		},
		[]SiteDailyCost{{SiteID: "site-retained"}, {SiteID: "site-fresh"}},
		runID,
	)
	if summary.expected != 2 || summary.collected != 2 || summary.fresh != 1 || summary.retained != 1 || summary.missing != 0 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if summary.total != 42 || summary.mode != "retained" || !summary.allAccountLevel {
		t.Fatalf("unexpected retained summary: %+v", summary)
	}
}

func TestSummarizeConfirmedSiteCostsDoesNotTreatFailedAttemptAsMissingWhenConfirmedValueExists(t *testing.T) {
	confirmed := 8.5
	summary := summarizeConfirmedSiteCosts(
		[]SiteDailyCost{{
			SiteID: "site-1", AdjustedCost: &confirmed, Status: "ok", Source: "dated_query",
			LastAttemptStatus: "failed", LastAttemptRunID: "new-run",
		}},
		[]SiteDailyCost{{SiteID: "site-1"}},
		"new-run",
	)
	if summary.collected != 1 || summary.retained != 1 || summary.missing != 0 || summary.total != confirmed {
		t.Fatalf("failed retry erased confirmed cost semantics: %+v", summary)
	}
}

func TestMergeDailyCostTargetCandidatesPreservesHistoricalCostsAndAddsOnlyToday(t *testing.T) {
	existing := []SiteDailyCost{{SiteID: "disabled-after-cost"}}
	attempts := []SiteDailyCost{{SiteID: "currently-enabled"}}

	historical := mergeDailyCostTargetCandidates(nil, existing, attempts, false)
	if len(historical) != 1 || historical["disabled-after-cost"].SiteID == "" {
		t.Fatalf("historical target must be restored from saved same-day costs: %+v", historical)
	}

	today := mergeDailyCostTargetCandidates([]SiteDailyCost{{SiteID: "old-target"}}, nil, attempts, true)
	if len(today) != 2 || today["old-target"].SiteID == "" || today["currently-enabled"].SiteID == "" {
		t.Fatalf("current business day must retain prior targets and append new sites: %+v", today)
	}
}

func TestMergeDailyCostTargetCandidatesDoesNotAddNewAttemptsToHistoricalDate(t *testing.T) {
	targets := mergeDailyCostTargetCandidates(
		[]SiteDailyCost{{SiteID: "historical-site"}},
		nil,
		[]SiteDailyCost{{SiteID: "newly-added-site"}},
		false,
	)
	if len(targets) != 1 || targets["historical-site"].SiteID == "" || targets["newly-added-site"].SiteID != "" {
		t.Fatalf("historical targets must not absorb current attempts: %+v", targets)
	}
}

func TestMergeDailyCostTargetCandidatesBuildsFirstHistoricalTargetSetFromAttempts(t *testing.T) {
	attempts := []SiteDailyCost{{SiteID: "site-from-first-historical-attempt"}}

	targets := mergeDailyCostTargetCandidates(nil, nil, attempts, false)
	if len(targets) != 1 || targets["site-from-first-historical-attempt"].SiteID == "" {
		t.Fatalf("first historical settlement must freeze targets from its attempts: %+v", targets)
	}
}
