package dashboard

import "time"

const (
	AccountingModeReplace  = "replace_upstream"
	AccountingModeAdditive = "additive_upstream"

	AccountStatusUnactivated = "unactivated"
	AccountStatusActive      = "active"
	AccountStatusExhausted   = "exhausted"
	AccountStatusDead        = "dead"
	AccountStatusClosed      = "closed"

	RecognitionModeImmediate = "immediate"
	RecognitionModeDaily     = "daily"
	RecognitionModeQuota     = "quota"

	StatsModeAutomatic = "automatic"
	StatsModeManual    = "manual"

	AccountEventStatus             = "status"
	AccountEventRestore            = "restore"
	AccountEventRefund             = "refund"
	AccountEventQuotaObservation   = "quota_observation"
	AccountEventManualObservation  = "manual_observation"
	AccountEventLinkChange         = "link_change"
	AccountEventStatsModeChange    = "stats_mode_change"
	AccountEventMetadataCorrection = "metadata_correction"
)

type AccountBatchInput struct {
	IdempotencyKey              string              `json:"-"`
	BatchName                   string              `json:"batchName"`
	Platform                    string              `json:"platform"`
	Channel                     string              `json:"channel"`
	AccountType                 string              `json:"accountType"`
	PurchaseDate                string              `json:"purchaseDate"`
	PurchaseURL                 string              `json:"purchaseUrl"`
	DefaultUpstreamReferenceURL string              `json:"defaultUpstreamReferenceUrl"`
	Quantity                    int                 `json:"quantity"`
	TotalAmountCents            int64               `json:"totalAmountCents"`
	Identifiers                 []string            `json:"identifiers"`
	Accounts                    []AccountAssetInput `json:"accounts"`
	AccountingMode              string              `json:"accountingMode"`
	RecognitionMode             string              `json:"recognitionMode"`
	RecognitionStartDate        string              `json:"recognitionStartDate"`
	RecognitionDays             int                 `json:"recognitionDays"`
	StatsMode                   string              `json:"statsMode"`
	Note                        string              `json:"note"`
}

type AccountAssetInput struct {
	Identifier           string `json:"identifier"`
	QuotaTotalMicros     *int64 `json:"quotaTotalMicros"`
	ConnectionID         string `json:"connectionId"`
	UpstreamReferenceURL string `json:"upstreamReferenceUrl"`
	LinkEffectiveFrom    string `json:"linkEffectiveFrom"`
	ManualSameDaySplit   bool   `json:"manualSameDaySplit"`
}

type AccountLinkInput struct {
	ConnectionID               string `json:"connectionId"`
	UpstreamReferenceURL       string `json:"upstreamReferenceUrl"`
	EffectiveFrom              string `json:"effectiveFrom"`
	ManualSameDaySplit         bool   `json:"manualSameDaySplit"`
	PreviousQuotaUsedMicros    *int64 `json:"previousQuotaUsedMicros"`
	PreviousRevenueCents       *int64 `json:"previousRevenueCents"`
	ReplacementQuotaUsedMicros *int64 `json:"replacementQuotaUsedMicros"`
	ReplacementRevenueCents    *int64 `json:"replacementRevenueCents"`
	Note                       string `json:"note"`
}

type AccountBatch struct {
	ID                          string    `json:"id"`
	UserID                      string    `json:"-"`
	AdminAccountID              string    `json:"-"`
	IdempotencyKey              string    `json:"-"`
	BatchName                   string    `json:"batchName"`
	Platform                    string    `json:"platform"`
	Channel                     string    `json:"channel"`
	AccountType                 string    `json:"accountType"`
	PurchaseDate                string    `json:"purchaseDate"`
	PurchaseURL                 string    `json:"purchaseUrl,omitempty"`
	DefaultUpstreamReferenceURL string    `json:"defaultUpstreamReferenceUrl,omitempty"`
	Quantity                    int       `json:"quantity"`
	TotalAmountCents            int64     `json:"totalAmountCents"`
	AccountingMode              string    `json:"accountingMode"`
	RecognitionMode             string    `json:"recognitionMode"`
	RecognitionStartDate        string    `json:"recognitionStartDate"`
	RecognitionDays             int       `json:"recognitionDays"`
	StatsMode                   string    `json:"statsMode"`
	Note                        string    `json:"note,omitempty"`
	CreatedAt                   time.Time `json:"createdAt"`
}

type AccountAsset struct {
	ID                   string              `json:"id"`
	UserID               string              `json:"-"`
	AdminAccountID       string              `json:"-"`
	BatchID              string              `json:"batchId"`
	Identifier           string              `json:"identifier"`
	Platform             string              `json:"platform"`
	Channel              string              `json:"channel"`
	AccountType          string              `json:"accountType"`
	PurchaseCostCents    int64               `json:"purchaseCostCents"`
	QuotaTotalMicros     *int64              `json:"quotaTotalMicros,omitempty"`
	AccountingMode       string              `json:"accountingMode"`
	RecognitionMode      string              `json:"recognitionMode"`
	RecognitionStartDate string              `json:"recognitionStartDate"`
	RecognitionDays      int                 `json:"recognitionDays"`
	StatsMode            string              `json:"statsMode"`
	CurrentStatus        string              `json:"currentStatus"`
	UpstreamReferenceURL string              `json:"upstreamReferenceUrl,omitempty"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
	QuotaUsedMicros      *int64              `json:"quotaUsedMicros,omitempty"`
	StatsQuality         string              `json:"statsQuality,omitempty"`
	HasActiveLink        bool                `json:"hasActiveLink"`
	Performance          *AccountPerformance `json:"performance,omitempty"`
}

type AccountAssetPage struct {
	Items   []AccountAsset `json:"items"`
	HasMore bool           `json:"hasMore"`
}

type AccountLink struct {
	ID                         string    `json:"id"`
	UserID                     string    `json:"-"`
	AdminAccountID             string    `json:"-"`
	AccountAssetID             string    `json:"accountAssetId"`
	ConnectionID               string    `json:"connectionId"`
	UpstreamSiteID             string    `json:"upstreamSiteId"`
	UpstreamKeyID              string    `json:"upstreamKeyId"`
	ScopeAdminAccountID        string    `json:"scopeAdminAccountId"`
	OwnGroupID                 string    `json:"ownGroupId"`
	ConnectionName             string    `json:"connectionName"`
	SiteName                   string    `json:"siteName"`
	KeyName                    string    `json:"keyName"`
	OwnGroupName               string    `json:"ownGroupName"`
	UpstreamReferenceURL       string    `json:"upstreamReferenceUrl,omitempty"`
	EffectiveFrom              string    `json:"effectiveFrom"`
	EffectiveTo                *string   `json:"effectiveTo,omitempty"`
	ManualSameDaySplit         bool      `json:"manualSameDaySplit"`
	PreviousQuotaUsedMicros    *int64    `json:"-"`
	PreviousRevenueCents       *int64    `json:"-"`
	ReplacementQuotaUsedMicros *int64    `json:"-"`
	ReplacementRevenueCents    *int64    `json:"-"`
	CreatedAt                  time.Time `json:"createdAt"`
}

type AccountBatchResult struct {
	Batch  AccountBatch   `json:"batch"`
	Assets []AccountAsset `json:"assets"`
}

type AccountEvent struct {
	ID                   string    `json:"id"`
	UserID               string    `json:"-"`
	AdminAccountID       string    `json:"-"`
	AccountAssetID       string    `json:"accountAssetId"`
	EventType            string    `json:"eventType"`
	EffectiveDate        string    `json:"effectiveDate"`
	Status               string    `json:"status,omitempty"`
	QuotaUsedMicros      *int64    `json:"quotaUsedMicros,omitempty"`
	RevenueCents         *int64    `json:"revenueCents,omitempty"`
	RefundCents          *int64    `json:"refundCents,omitempty"`
	UpstreamCostCents    *int64    `json:"upstreamCostCents,omitempty"`
	StatsMode            string    `json:"statsMode,omitempty"`
	Identifier           string    `json:"identifier,omitempty"`
	Platform             string    `json:"platform,omitempty"`
	Channel              string    `json:"channel,omitempty"`
	AccountType          string    `json:"accountType,omitempty"`
	PurchaseURL          *string   `json:"purchaseUrl,omitempty"`
	UpstreamReferenceURL *string   `json:"upstreamReferenceUrl,omitempty"`
	Note                 string    `json:"note,omitempty"`
	IdempotencyKey       string    `json:"-"`
	CreatedAt            time.Time `json:"createdAt"`
}

type AccountEventResult struct {
	Event       AccountEvent           `json:"event"`
	Asset       AccountAsset           `json:"asset"`
	CostRecords []AdditionalCostRecord `json:"costRecords,omitempty"`
}

type AccountDailyStat struct {
	ID                        string     `json:"id"`
	UserID                    string     `json:"-"`
	AdminAccountID            string     `json:"-"`
	AccountAssetID            string     `json:"-"`
	BusinessDate              string     `json:"businessDate"`
	Source                    string     `json:"source"`
	Quality                   string     `json:"quality"`
	KeyCostRunID              string     `json:"keyCostRunId,omitempty"`
	RawQuotaUsedMicros        *int64     `json:"rawQuotaUsedMicros,omitempty"`
	RevenueCents              *int64     `json:"revenueCents,omitempty"`
	UpstreamCostCents         *int64     `json:"upstreamCostCents,omitempty"`
	RecognizedCostCents       int64      `json:"recognizedCostCents"`
	ReplacementDeductionCents *int64     `json:"replacementDeductionCents,omitempty"`
	ObservedAt                *time.Time `json:"observedAt,omitempty"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
}

type AccountAssetDetail struct {
	Asset       AccountAsset       `json:"asset"`
	Batch       AccountBatch       `json:"batch"`
	Links       []AccountLink      `json:"links"`
	Events      []AccountEvent     `json:"events"`
	DailyStats  []AccountDailyStat `json:"dailyStats"`
	Performance AccountPerformance `json:"performance"`
}

type AccountAssetFilter struct {
	Platform    string
	Channel     string
	AccountType string
	Status      string
	Search      string
	Page        int
	PageSize    int
}

type AccountCostLedgerFilter struct {
	From           string
	To             string
	Type           string
	Platform       string
	Channel        string
	BatchID        string
	AccountAssetID string
	Page           int
	PageSize       int
}

type AccountCostLedgerPage struct {
	Items   []AdditionalCostRecord `json:"items"`
	HasMore bool                   `json:"hasMore"`
}

type AccountPerformanceInput struct {
	Status                    string
	AccountingMode            string
	PurchaseCostCents         int64
	AdditiveUpstreamCostCents int64
	RevenueCents              int64
	RefundCents               int64
	RawQuotaUsedMicros        int64
	HasAdditiveUpstreamCost   bool
	HasRevenue                bool
	HasRawQuotaUsed           bool
	HasIncompleteDailyStats   bool
}

type AccountPerformance struct {
	AverageSaleMultiplier    *float64 `json:"averageSaleMultiplier,omitempty"`
	CostRecoveryMultiple     *float64 `json:"costRecoveryMultiple,omitempty"`
	BreakevenDifferenceCents *int64   `json:"breakevenDifferenceCents,omitempty"`
	FinalProfitCents         *int64   `json:"finalProfitCents,omitempty"`
	ROI                      *float64 `json:"roi,omitempty"`
	MissingFields            []string `json:"missingFields,omitempty"`
}
