package sales

import (
	"time"

	"transithub/backend/internal/shared/workspacescope"
)

type Scope = workspacescope.Scope

type RangeQuery struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

type DayRange struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

type Settings struct {
	ExcludedChannels []string `json:"excludedChannels"`
}

type Coverage struct {
	From             string     `json:"from"`
	To               string     `json:"to"`
	Complete         bool       `json:"complete"`
	PreviousComplete bool       `json:"previousComplete"`
	HasKnownData     bool       `json:"hasKnownData"`
	LastSyncedAt     *time.Time `json:"lastSyncedAt"`
	Warnings         []string   `json:"warnings"`
}

type Totals struct {
	Credits         *float64 `json:"credits"`
	Deductions      *float64 `json:"deductions"`
	Payers          *int64   `json:"payers"`
	RepeatPayers    *int64   `json:"repeatPayers"`
	NewPayers       *int64   `json:"newPayers"`
	Registered      *int64   `json:"registered"`
	RegisteredPaid  *int64   `json:"registeredPaid"`
	Conversion      *float64 `json:"conversion"`
	AveragePerPayer *float64 `json:"averagePerPayer"`
}

type ChannelTotal struct {
	Channel         string   `json:"channel"`
	RawAmount       *float64 `json:"rawAmount"`
	ExcludedAmount  *float64 `json:"excludedAmount"`
	EffectiveAmount *float64 `json:"effectiveAmount"`
	ChannelExcluded bool     `json:"channelExcluded"`
}

type DailyPoint struct {
	Day        string   `json:"day"`
	Credits    *float64 `json:"credits"`
	Deductions *float64 `json:"deductions"`
	Complete   bool     `json:"complete"`
}

type Payer struct {
	SiteUserID string  `json:"siteUserId"`
	Email      string  `json:"email"`
	Username   string  `json:"username"`
	Credits    float64 `json:"credits"`
	PaidDays   int64   `json:"paidDays"`
	LastDay    string  `json:"lastDay"`
}

type Concentration struct {
	Top1Share  *float64 `json:"top1Share"`
	Top10Share *float64 `json:"top10Share"`
}

type ExclusionSummary struct {
	Channels   []string `json:"channels"`
	Entries    int64    `json:"entries"`
	Amount     *float64 `json:"amount"`
	Deductions *float64 `json:"deductions"`
}

type Summary struct {
	Range         DayRange         `json:"range"`
	PreviousRange DayRange         `json:"previousRange"`
	Coverage      Coverage         `json:"coverage"`
	Settings      Settings         `json:"settings"`
	Totals        Totals           `json:"totals"`
	Previous      *Totals          `json:"previous"`
	Channels      []ChannelTotal   `json:"channels"`
	Daily         []DailyPoint     `json:"daily"`
	TopPayers     []Payer          `json:"topPayers"`
	Concentration Concentration    `json:"concentration"`
	Exclusions    ExclusionSummary `json:"exclusions"`
}

type EntryKey struct {
	SiteUserID string `json:"siteUserId"`
	Day        string `json:"day"`
	Channel    string `json:"channel"`
}

type Entry struct {
	EntryKey
	Email           string  `json:"email"`
	Username        string  `json:"username"`
	RawAmount       float64 `json:"rawAmount"`
	Events          int64   `json:"events"`
	ExcludedAmount  float64 `json:"excludedAmount"`
	ExcludedEvents  int64   `json:"excludedEvents"`
	EffectiveAmount float64 `json:"effectiveAmount"`
	HasExclusion    bool    `json:"hasExclusion"`
	ChannelExcluded bool    `json:"channelExcluded"`
	ExclusionStatus string  `json:"exclusionStatus"`
}

type EntryQuery struct {
	RangeQuery
	Page         int
	PageSize     int
	Channel      string
	Search       string
	ExcludedOnly bool
}

type EntriesResponse struct {
	Range      DayRange         `json:"range"`
	Coverage   Coverage         `json:"coverage"`
	Settings   Settings         `json:"settings"`
	Items      []Entry          `json:"items"`
	Total      int64            `json:"total"`
	Page       int              `json:"page"`
	PageSize   int              `json:"pageSize"`
	Exclusions ExclusionSummary `json:"exclusions"`
}

type MutationResult struct {
	Requested      int               `json:"requested"`
	Changed        int               `json:"changed"`
	Unchanged      int               `json:"unchanged"`
	Range          DayRange          `json:"range"`
	Exclusions     *ExclusionSummary `json:"exclusionSummary"`
	RefreshMessage string            `json:"refreshMessage,omitempty"`
}

type syncState struct {
	From         string
	To           string
	LastSyncedAt *time.Time
	Warning      string
}
