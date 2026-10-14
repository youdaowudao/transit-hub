package notifications

import "time"

type Level int16

const (
	LevelNormal    Level = 1
	LevelAttention Level = 2
	LevelImportant Level = 3
	LevelUrgent    Level = 4
)

type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Action describes a fixed application destination, never a producer supplied URL.
type Action struct {
	Kind         string `json:"kind"`
	SiteID       string `json:"siteId,omitempty"`
	FocusGroupID string `json:"focusGroupId,omitempty"`
	FocusGroup   string `json:"focusGroup,omitempty"`
}

type Content struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Details []Detail `json:"details"`
	Action  *Action  `json:"action,omitempty"`
}

// NoticeInput is a complete immutable business fact. Retries retain EventKey.
type NoticeInput struct {
	UserID         string
	AdminAccountID string
	Kind           string
	EventKey       string
	SubjectType    string
	SubjectID      string
	Level          Level
	DiscoveredAt   time.Time
	Content        Content
}

type Notice struct {
	ID                  string     `json:"id"`
	UserID              string     `json:"-"`
	AdminAccountID      string     `json:"adminAccountId"`
	Kind                string     `json:"kind"`
	SubjectType         string     `json:"subjectType"`
	SubjectID           string     `json:"subjectId"`
	Level               Level      `json:"level"`
	DiscoveredAt        time.Time  `json:"discoveredAt"`
	CreatedAt           time.Time  `json:"createdAt"`
	Content             Content    `json:"content"`
	ReadAt              *time.Time `json:"readAt"`
	ReminderCompletedAt *time.Time `json:"reminderCompletedAt"`
}
