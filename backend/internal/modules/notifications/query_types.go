package notifications

import "time"

type Warning struct {
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Next    string `json:"next"`
}

type SummaryQuery struct {
	DisplayedIDs    []string
	AttentionCursor string
	ImportantCursor string
	CriticalCursor  string
}

type ActiveBucket struct {
	Items      []Notice `json:"items"`
	TotalCount int64    `json:"totalCount"`
	NextCursor *string  `json:"nextCursor"`
}

type ActiveNotifications struct {
	Attention ActiveBucket `json:"attention"`
	Important ActiveBucket `json:"important"`
	Critical  ActiveBucket `json:"critical"`
}

type SamplingSite struct {
	SiteName   string     `json:"siteName"`
	ObservedAt *time.Time `json:"observedAt"`
	Reason     string     `json:"reason"`
}

type SamplingState struct {
	AutoSyncEnabled      *bool          `json:"autoSyncEnabled"`
	UnconfirmedSiteCount *int64         `json:"unconfirmedSiteCount"`
	Sites                []SamplingSite `json:"sites"`
	Warning              *Warning       `json:"warning,omitempty"`
}

type DisplayedState struct {
	ID                     string     `json:"id"`
	SubjectActive          bool       `json:"subjectActive"`
	SubjectInactiveMessage string     `json:"subjectInactiveMessage"`
	ReadAt                 *time.Time `json:"readAt"`
	ReminderCompletedAt    *time.Time `json:"reminderCompletedAt"`
}

type SummaryResponse struct {
	SignupRisk              *SignupRiskSummary  `json:"signupRisk,omitempty"`
	DisplayedStates         []DisplayedState    `json:"displayedStates"`
	ServerTime              time.Time           `json:"serverTime"`
	WorkspaceAdminAccountID string              `json:"workspaceAdminAccountId"`
	IntervalSeconds         *int                `json:"intervalSeconds"`
	PreferenceWarning       *Warning            `json:"preferenceWarning,omitempty"`
	UnreadCount             int64               `json:"unreadCount"`
	PendingCriticalCount    int64               `json:"pendingCriticalCount"`
	Active                  ActiveNotifications `json:"active"`
	SamplingState           SamplingState       `json:"samplingState"`
}

type HistoryQuery struct {
	Limit  int
	Cursor string
	Filter string
}

type HistoryResponse struct {
	WorkspaceAdminAccountID string   `json:"workspaceAdminAccountId"`
	Items                   []Notice `json:"items"`
	NextCursor              *string  `json:"nextCursor"`
}

type CheckAction struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

type DetailResponse struct {
	WorkspaceAdminAccountID string       `json:"workspaceAdminAccountId"`
	Item                    Notice       `json:"item"`
	CheckAction             *CheckAction `json:"checkAction,omitempty"`
}

type Progress struct {
	ID                  string     `json:"id"`
	ReadAt              *time.Time `json:"readAt"`
	ReminderCompletedAt *time.Time `json:"reminderCompletedAt"`
}

type ProgressResponse struct {
	WorkspaceAdminAccountID string     `json:"workspaceAdminAccountId"`
	Items                   []Progress `json:"items"`
}

type PreferencesResponse struct {
	WorkspaceAdminAccountID string     `json:"workspaceAdminAccountId"`
	IntervalSeconds         int        `json:"intervalSeconds"`
	UpdatedAt               *time.Time `json:"updatedAt"`
}
