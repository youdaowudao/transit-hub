package dashboard

import "time"

type OperatingInsightsRange struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

// Snapshot totals preserve the four original persisted accounting fields.
type OperatingSnapshotTotals struct {
	TodayProfit       float64  `json:"todayProfit"`
	TodayPurchase     float64  `json:"todayPurchase"`
	OperatingCost     float64  `json:"operatingCost"`
	AdjustedNetProfit float64  `json:"adjustedNetProfit"`
	ProfitMargin      *float64 `json:"profitMargin"`
}

type OperatingSiteEstimate struct {
	SiteID         string     `json:"siteId"`
	Name           string     `json:"name"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"`
	RawBalance     *float64   `json:"rawBalance"`
	AverageRawCost *float64   `json:"averageRawCost"`
	DaysLeft       *float64   `json:"daysLeft"`
	SuggestedTopUp *float64   `json:"suggestedTopUp"`
	LastSyncedAt   *time.Time `json:"lastSyncedAt"`
	MissingDates   []string   `json:"missingDates"`
	Reasons        []string   `json:"reasons"`
}

type OperatingInsights struct {
	WorkspaceAdminAccountID string                   `json:"workspaceAdminAccountId"`
	WorkspaceCreatedAt      string                   `json:"workspaceCreatedAt"`
	Range                   OperatingInsightsRange   `json:"range"`
	SnapshotTotals          *OperatingSnapshotTotals `json:"snapshotTotals"`
	MissingDates            []string                 `json:"missingDates"`
	SiteEstimates           []OperatingSiteEstimate  `json:"siteEstimates"`
	Warnings                []string                 `json:"warnings"`
	ObservedAt              time.Time                `json:"observedAt"`
}
