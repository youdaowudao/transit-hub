package user_activity

import (
	"time"

	"transithub/backend/internal/shared/workspacescope"
)

type Scope = workspacescope.Scope

const DateLayout = "2006-01-02"

type SyncState struct {
	TodayUsageObservedAt *time.Time `json:"todayUsageObservedAt"`
	Status               string     `json:"status"`
	Error                string     `json:"error,omitempty"`
	Warnings             []string   `json:"warnings"`
	UsageFrom            string     `json:"usageFrom"`
	UsageTo              string     `json:"usageTo"`
	RechargeFrom         string     `json:"rechargeFrom"`
	RechargeTo           string     `json:"rechargeTo"`
	UserCount            int        `json:"userCount"`
	StartedAt            *time.Time `json:"startedAt"`
	LastSyncedAt         *time.Time `json:"lastSyncedAt"`
}
type Coverage struct {
	UsageFrom    string   `json:"usageFrom"`
	UsageTo      string   `json:"usageTo"`
	RechargeFrom string   `json:"rechargeFrom"`
	RechargeTo   string   `json:"rechargeTo"`
	Warnings     []string `json:"warnings"`
}

func coverage(s SyncState) Coverage {
	return Coverage{s.UsageFrom, s.UsageTo, s.RechargeFrom, s.RechargeTo, s.Warnings}
}

type Profile struct {
	SiteUserID      string     `json:"siteUserId"`
	Email           string     `json:"email"`
	Username        string     `json:"username"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	Balance         *float64   `json:"balance"`
	CreatedAt       *time.Time `json:"createdAt"`
	LastUsedAt      *time.Time `json:"lastUsedAt"`
	FirstObservedAt time.Time  `json:"firstObservedAt"`
	ObservedAt      time.Time  `json:"observedAt"`
}
type UserRow struct {
	TodayUsageObservedAt *time.Time `json:"todayUsageObservedAt"`
	Profile
	Cost7d         *float64 `json:"cost7d"`
	Cost30d        *float64 `json:"cost30d"`
	CostPrev7d     *float64 `json:"costPrev7d"`
	CostPrev30d    *float64 `json:"costPrev30d"`
	Requests7d     *int64   `json:"requests7d"`
	RequestsPrev7d *int64   `json:"requestsPrev7d"`
	RequestsToday  *int64   `json:"requestsToday"`
	CostToday      *float64 `json:"costToday"`
}
type DailyUsage struct {
	SiteUserID       string
	Day              string
	Requests, Tokens int64
	Cost             float64
}
type Recharge struct {
	SiteUserID, Day, Channel string
	Amount                   float64
	Events                   int
}
type DailyPoint struct {
	ObservedAt  *time.Time `json:"observedAt,omitempty"`
	Day         string     `json:"day"`
	Requests    *int64     `json:"requests"`
	Tokens      *int64     `json:"tokens"`
	Cost        *float64   `json:"cost"`
	ActiveUsers *int       `json:"activeUsers"`
	Recharge    *float64   `json:"recharge"`
	Deduction   *float64   `json:"deduction"`
	Temporary   bool       `json:"temporary"`
}
type Metrics struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Requests    int64   `json:"requests"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	ActiveUsers int     `json:"activeUsers"`
}
type Period struct {
	Days      int        `json:"days"`
	Current   *Metrics   `json:"current"`
	Previous  *Metrics   `json:"previous"`
	Retention *Retention `json:"retention"`
}
type Retention struct {
	Retained int `json:"retained"`
	Churned  int `json:"churned"`
	Gained   int `json:"gained"`
}
type Money struct {
	TotalBalance       *float64        `json:"totalBalance"`
	ObservedAt         *time.Time      `json:"observedAt"`
	TodayRecharge      *float64        `json:"todayRecharge"`
	TodayDeduction     *float64        `json:"todayDeduction"`
	YesterdayRecharge  *float64        `json:"yesterdayRecharge"`
	YesterdayDeduction *float64        `json:"yesterdayDeduction"`
	Channels           []ChannelAmount `json:"channels"`
	Complete           bool            `json:"complete"`
}
type ChannelAmount struct {
	Channel string  `json:"channel"`
	Amount  float64 `json:"amount"`
	Events  int     `json:"events"`
}
type ListQuery struct {
	Page, PageSize, InactiveDays               int
	Segment, Search, Role, Status, Sort, Order string
}
type Summary struct {
	Sync     SyncState         `json:"sync"`
	Today    DailyPoint        `json:"today"`
	Periods  map[string]Period `json:"periods"`
	Daily    []DailyPoint      `json:"daily"`
	Money    Money             `json:"money"`
	Coverage Coverage          `json:"coverage"`
	Warnings []string          `json:"warnings"`
	Timezone string            `json:"timezone"`
}
type UserList struct {
	Items    []UserRow `json:"items"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
	Sync     SyncState `json:"sync"`
	Coverage Coverage  `json:"coverage"`
}
type Detail struct {
	Profile  UserRow           `json:"profile"`
	Daily    []DailyPoint      `json:"daily"`
	Periods  map[string]Period `json:"periods"`
	Coverage Coverage          `json:"coverage"`
}
