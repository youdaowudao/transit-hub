package connection_health

import (
	"context"
	"time"
	"transithub/backend/internal/modules/upstream"
)

const modelControlErrorPrefix = "admin.connectionHealth.errors.modelControl"

func modelControlError(reason string) string { return modelControlErrorPrefix + reason }

type ModelControlActioner interface {
	ReadSub2APIModelControlAccountContext(context.Context, upstream.Session, string) (upstream.Sub2APIModelControlAccount, error)
	UpdateSub2APIAdminAccountModelMappingContext(context.Context, upstream.Session, string, map[string]string) error
}
type ModelControlSettings struct {
	MinAccuracyPercent int   `json:"minAccuracyPercent"`
	MinJudgedAnswers   int   `json:"minJudgedAnswers"`
	Version            int64 `json:"version"`
}
type ModelControlRule struct {
	ID                 string    `json:"-"`
	UserID             string    `json:"-"`
	AdminAccountID     string    `json:"-"`
	ModelName          string    `json:"modelName"`
	MinAccuracyPercent int       `json:"minAccuracyPercent"`
	MinJudgedAnswers   int       `json:"minJudgedAnswers"`
	IncludeManual      bool      `json:"includeManual"`
	IncludeScheduled   bool      `json:"includeScheduled"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
type modelControlRound struct {
	BatchID         string     `json:"batchId"`
	Source          string     `json:"source"`
	ScheduleName    string     `json:"scheduleName"`
	CreatedAt       time.Time  `json:"createdAt"`
	CompletedAt     *time.Time `json:"completedAt"`
	Running         bool       `json:"running"`
	Correct         int        `json:"correct"`
	Incorrect       int        `json:"incorrect"`
	Unreviewed      int        `json:"unreviewed"`
	Failed          int        `json:"failed"`
	Cancelled       int        `json:"cancelled"`
	AccuracyPercent *float64   `json:"accuracyPercent"`
}
type modelControlBasis struct {
	BusinessDay            string   `json:"businessDay"`
	BatchID                string   `json:"batchId"`
	RuleVersion            int64    `json:"ruleVersion"`
	Decision               string   `json:"decision"`
	AccuracyPercent        *float64 `json:"accuracyPercent,omitempty"`
	ConfirmWithoutEvidence bool     `json:"confirmWithoutEvidence,omitempty"`
}
type modelControlPending struct {
	ID                string            `json:"id"`
	Operation         string            `json:"operation"`
	Basis             modelControlBasis `json:"basis"`
	BeforeMappingHash string            `json:"beforeMappingHash"`
	AfterMapping      map[string]string `json:"afterMapping"`
	Entries           map[string]string `json:"entries"`
	Phase             string            `json:"phase"`
	Receipt           string            `json:"receipt"`
	SendStartedAt     *time.Time        `json:"sendStartedAt"`
	StartedAt         time.Time         `json:"startedAt"`
	ActorUserID       string            `json:"actorUserId"`
}
type modelControlUnconfirmed struct {
	Entries     map[string]string `json:"entries"`
	Basis       modelControlBasis `json:"basis"`
	SentAt      time.Time         `json:"sentAt"`
	ActorUserID string            `json:"actorUserId"`
}
type ModelControlEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	State string `json:"state"`
}
type modelSourceCount struct {
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	Key       string `json:"key"`
	Count     *int   `json:"count"`
	OK        bool   `json:"ok"`
}
type modelControlObservation struct {
	State              string             `json:"state"`
	ReasonKey          string             `json:"reasonKey"`
	Sources            []modelSourceCount `json:"sources"`
	AccountStatus      string             `json:"accountStatus"`
	AccountSchedulable *bool              `json:"accountSchedulable"`
	CheckedAt          *time.Time         `json:"checkedAt"`
}
type modelControlAttempt struct {
	Operation string              `json:"operation"`
	Outcome   string              `json:"outcome"`
	ReasonKey string              `json:"reasonKey"`
	Entries   []ModelControlEntry `json:"entries"`
	Groups    []modelSourceCount  `json:"groups"`
	At        time.Time           `json:"at"`
}
type modelControlTarget struct {
	observationAt                                                *time.Time
	updateAttempt                                                bool
	deleted                                                      bool
	ID, UserID, AdminAccountID, TargetID, ModelName, AccountName string
	ClosedEntries                                                map[string]string
	ClosedAt                                                     *time.Time
	ClosedBy, ClosedBatchID                                      *string
	ClosedAccuracyPercent                                        *float64
	ConflictReason                                               string
	Pending                                                      *modelControlPending
	LastPendingID                                                string
	UnconfirmedClose                                             *modelControlUnconfirmed
	Observation                                                  modelControlObservation
	LastAttempt                                                  *modelControlAttempt
	AttemptAt                                                    *time.Time
	Version                                                      int64
	CreatedAt, UpdatedAt                                         time.Time
}
type ModelControlSummaryModel struct {
	ModelName string     `json:"modelName"`
	Status    string     `json:"status"`
	Decision  string     `json:"decision"`
	Attention bool       `json:"attention"`
	CheckedAt *time.Time `json:"checkedAt"`
}
type ModelControlCounts struct {
	Total     int `json:"total"`
	Open      int `json:"open"`
	Closed    int `json:"closed"`
	Attention int `json:"attention"`
	Untested  int `json:"untested"`
}
type ModelControlAccountSummary struct {
	Open      int                        `json:"open"`
	Models    []ModelControlSummaryModel `json:"models"`
	Closed    int                        `json:"closed"`
	Attention int                        `json:"attention"`
}
type modelControlPendingView struct {
	Operation     string     `json:"operation"`
	Phase         string     `json:"phase"`
	Receipt       string     `json:"receipt"`
	State         string     `json:"state"`
	StartedAt     time.Time  `json:"startedAt"`
	SendStartedAt *time.Time `json:"sendStartedAt"`
}
type modelControlAccountPending struct {
	ModelName string `json:"modelName"`
	Operation string `json:"operation"`
	State     string `json:"state"`
}
type modelControlUnconfirmedView struct {
	Entries   map[string]string `json:"entries"`
	SentAt    time.Time         `json:"sentAt"`
	ExpiresAt time.Time         `json:"expiresAt"`
}
type modelControlControl struct {
	ClosedEntries         map[string]string            `json:"closedEntries"`
	ClosedAt              *time.Time                   `json:"closedAt"`
	ClosedAccuracyPercent *float64                     `json:"closedAccuracyPercent"`
	Pending               *modelControlPendingView     `json:"pending"`
	UnconfirmedClose      *modelControlUnconfirmedView `json:"unconfirmedClose"`
	AccountPending        *modelControlAccountPending  `json:"accountPending"`
	ConflictReason        string                       `json:"conflictReason"`
	Observation           modelControlObservation      `json:"observation"`
	LastAttempt           *modelControlAttempt         `json:"lastAttempt"`
}
type modelControlScheduleCoverage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type modelControlCoverage struct {
	Schedules []modelControlScheduleCoverage `json:"schedules"`
}
type modelControlHealth struct {
	RecentlyProbed bool    `json:"recentlyProbed"`
	State          *string `json:"state"`
}
type ModelControlItem struct {
	Attention      bool                 `json:"attention"`
	TargetID       string               `json:"targetId"`
	AccountName    string               `json:"accountName"`
	ModelName      string               `json:"modelName"`
	Version        int64                `json:"version"`
	Rule           ModelControlRule     `json:"rule"`
	Round          *modelControlRound   `json:"round"`
	PreviousRound  *modelControlRound   `json:"previousRound"`
	Decision       string               `json:"decision"`
	DecisionReason string               `json:"decisionReason"`
	Basis          modelControlBasis    `json:"basis"`
	Control        modelControlControl  `json:"control"`
	Coverage       modelControlCoverage `json:"coverage"`
	Health         modelControlHealth   `json:"health"`
	VerifyErrorKey string               `json:"verifyErrorKey,omitempty"`
}
type modelControlRequestHealth struct {
	Checked   bool   `json:"checked"`
	Allowed   *bool  `json:"allowed"`
	ReasonKey string `json:"reasonKey"`
}
type ModelControlPreview struct {
	Item               ModelControlItem          `json:"item"`
	Entries            []ModelControlEntry       `json:"entries"`
	Groups             []modelSourceCount        `json:"groups"`
	BlockReasonKey     string                    `json:"blockReasonKey"`
	RequestHealth      modelControlRequestHealth `json:"requestHealth"`
	AccountStatus      string                    `json:"accountStatus"`
	AccountSchedulable *bool                     `json:"accountSchedulable"`
	PlanFingerprint    string                    `json:"planFingerprint"`
	NoRemoteWrite      bool                      `json:"noRemoteWrite"`
}
type ModelControlResult struct {
	Item              ModelControlItem               `json:"item"`
	Outcome           string                         `json:"outcome"`
	ReasonKey         string                         `json:"reasonKey"`
	Entries           []ModelControlEntry            `json:"entries"`
	Groups            []modelSourceCount             `json:"groups"`
	HintKeys          []string                       `json:"hintKeys"`
	SchedulableResult *TargetSchedulableActionResult `json:"schedulableResult,omitempty"`
	ErrorKey          string                         `json:"errorKey,omitempty"`
}
type ModelControlEvent struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	AdminAccountID string    `json:"-"`
	TargetID       string    `json:"targetId"`
	ModelName      string    `json:"modelName"`
	EventType      string    `json:"eventType"`
	ActorUserID    string    `json:"actorUserId"`
	Basis          any       `json:"basis"`
	Detail         any       `json:"detail"`
	CreatedAt      time.Time `json:"createdAt"`
}
type ModelControlConflictError struct {
	Key     string
	Current any
}

func (e *ModelControlConflictError) Error() string { return e.Key }
func modelControlConflict(reason string, current any) error {
	return &ModelControlConflictError{modelControlError(reason), current}
}
func modelControlPendingState(p *modelControlPending, now time.Time) string {
	if p.Phase == "prepared" {
		if now.Sub(p.StartedAt) <= 60*time.Second {
			return "in_progress"
		}
		return "not_completed"
	}
	if p.Receipt != "" {
		return "not_completed"
	}
	if p.SendStartedAt != nil && now.Sub(*p.SendStartedAt) < 30*time.Second {
		return "in_progress"
	}
	return "unknown"
}
func modelControlNeedsAttention(i ModelControlItem) bool {
	c := i.Control
	if c.Pending != nil {
		return c.Pending.State != "in_progress"
	}
	if c.ConflictReason != "" || c.Observation.State == "account_missing" || c.LastAttempt != nil {
		return true
	}
	if len(c.ClosedEntries) > 0 && i.Decision == "usable" {
		return true
	}
	if i.Decision == "close_recommended" {
		switch c.Observation.State {
		case "serving", "partially_closed", "not_isolatable":
			return true
		case "last_model":
			return c.Observation.AccountSchedulable != nil && *c.Observation.AccountSchedulable
		}
	}
	return c.Observation.State == "last_model" && c.Observation.AccountSchedulable != nil && !*c.Observation.AccountSchedulable && i.Decision == "usable"
}
