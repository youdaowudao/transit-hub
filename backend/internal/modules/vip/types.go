package vip

import (
	"time"

	"transithub/backend/internal/shared/workspacescope"
)

type Scope = workspacescope.Scope

type Settings struct {
	DailyTop          int      `json:"dailyTop"`
	LifetimeTop       int      `json:"lifetimeTop"`
	AlertDays         float64  `json:"alertDays"`
	AlertBalance      float64  `json:"alertBalance"`
	AlertsEnabled     bool     `json:"alertsEnabled"`
	BotIDs            []string `json:"botIds"`
	ReminderText      string   `json:"reminderText"`
	EmptyReminderText string   `json:"emptyReminderText"`
}

func DefaultSettings() Settings {
	return Settings{DailyTop: 20, LifetimeTop: 20, AlertDays: 2, AlertBalance: 20, BotIDs: []string{}}
}

type Source struct {
	Manual       bool   `json:"manual"`
	Note         string `json:"note"`
	DailyRank    int    `json:"dailyRank,omitempty"`
	LifetimeRank int    `json:"lifetimeRank,omitempty"`
}

type Row struct {
	SiteUserID          string     `json:"siteUserId"`
	Email               string     `json:"email"`
	Username            string     `json:"username"`
	Role                string     `json:"role"`
	UserStatus          string     `json:"userStatus"`
	Status              string     `json:"status"`
	Source              Source     `json:"source"`
	Reminder            string     `json:"reminder"`
	Balance             *float64   `json:"balance"`
	DailyAverage        *float64   `json:"dailyAverage"`
	PreviousAverage     *float64   `json:"previousAverage"`
	DaysLeft            *float64   `json:"daysLeft"`
	Trend               *float64   `json:"trend"`
	RecordedConsumption *float64   `json:"recordedConsumption"`
	RecordedFrom        string     `json:"recordedFrom"`
	LastUsedAt          *time.Time `json:"lastUsedAt"`
	ObservedAt          *time.Time `json:"observedAt"`
	LastAlertStatus     string     `json:"lastAlertStatus,omitempty"`
	LastAlertAt         *time.Time `json:"lastAlertAt,omitempty"`
	LastAlertState      string     `json:"lastAlertState,omitempty"`
	LastAlertRecovered  bool       `json:"lastAlertRecovered"`
	Requests7           int64      `json:"-"`
	Cost7               float64    `json:"-"`
	CostPrevious7       float64    `json:"-"`
	ProfilePresent      bool       `json:"-"`
}

type Counts struct {
	All       int `json:"all"`
	Normal    int `json:"normal"`
	Low       int `json:"low"`
	Empty     int `json:"empty"`
	Declining int `json:"declining"`
	Disabled  int `json:"disabled"`
}

type Coverage struct {
	From         string   `json:"from"`
	To           string   `json:"to"`
	SevenDays    bool     `json:"sevenDays"`
	FourteenDays bool     `json:"fourteenDays"`
	Warnings     []string `json:"warnings"`
}

type BotOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type Summary struct {
	Settings       Settings    `json:"settings"`
	Counts         Counts      `json:"counts"`
	Items          []Row       `json:"items"`
	LastSyncedAt   *time.Time  `json:"lastSyncedAt"`
	Coverage       Coverage    `json:"coverage"`
	DismissedCount int         `json:"dismissedCount"`
	Bots           []BotOption `json:"bots"`
	PauseReason    string      `json:"pauseReason"`
}

type Candidate struct {
	SiteUserID string   `json:"siteUserId"`
	Email      string   `json:"email"`
	Username   string   `json:"username"`
	Balance    *float64 `json:"balance"`
	Watched    bool     `json:"watched"`
	Dismissed  bool     `json:"dismissed"`
}

type syncSnapshot struct {
	Status    string
	From      string
	To        string
	StartedAt *time.Time
	SyncedAt  *time.Time
	Warning   string
}

type alertState struct {
	State     string
	At        time.Time
	Recovered bool
	Delivery  string
}
