package connection_health

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	ErrorQuestionAnswerScheduleDeleted          = "admin.connectionHealth.errors.questionAnswerScheduleDeleted"
	ErrorQuestionAnswerScheduleInvalid          = "admin.connectionHealth.errors.questionAnswerScheduleInvalid"
	ErrorQuestionAnswerScheduleActive           = "admin.connectionHealth.errors.questionAnswerScheduleActive"
	ErrorQuestionAnswerScheduleNotFound         = "admin.connectionHealth.errors.questionAnswerScheduleNotFound"
	ErrorQuestionAnswerScheduleVersionConflict  = "admin.connectionHealth.errors.questionAnswerScheduleVersionConflict"
	ErrorQuestionAnswerExecutionVersionConflict = "admin.connectionHealth.errors.questionAnswerExecutionVersionConflict"
)

// Config is copied into every accepted execution. It contains no live inventory
// or runtime state, and changing a schedule never changes an accepted copy.
type QuestionAnswerScheduleConfig struct {
	Name                     string   `json:"name"`
	TargetMode               string   `json:"targetMode"`
	SelectedGroupIDs         []string `json:"selectedGroupIds"`
	SelectedAccountTargetIDs []string `json:"selectedAccountTargetIds"`
	Models                   []string `json:"models"`
	QuestionIDs              []string `json:"questionIds"`
	ReasoningEffort          string   `json:"reasoningEffort"`
	RepeatCount              int      `json:"repeatCount"`
	PeakStart                string   `json:"peakStart"`
	PeakEnd                  string   `json:"peakEnd"`
	PeakIntervalMinutes      int      `json:"peakIntervalMinutes"`
	OffPeakIntervalMinutes   int      `json:"offPeakIntervalMinutes"`
}

type QuestionAnswerScheduleInput struct {
	QuestionAnswerScheduleConfig
	Enabled         *bool  `json:"enabled,omitempty"`
	ExpectedVersion *int64 `json:"expectedVersion,omitempty"`
}

type QuestionAnswerScheduleGroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type QuestionAnswerSchedulePreviewTarget struct {
	TargetID      string                           `json:"targetId"`
	AccountID     string                           `json:"accountId"`
	AccountName   string                           `json:"accountName"`
	Platform      string                           `json:"platform"`
	MatchedGroups []QuestionAnswerScheduleGroupRef `json:"matchedGroups"`
	ModelNames    []string                         `json:"modelNames"`
	Missing       bool                             `json:"missing"`
}

type QuestionAnswerScheduleSelectionSnapshot struct {
	TargetPreview                 []QuestionAnswerSchedulePreviewTarget `json:"targetPreview"`
	PreviewedAt                   time.Time                             `json:"previewedAt"`
	EstimatedTargetCount          int                                   `json:"estimatedTargetCount"`
	EstimatedRequestsPerTarget    int                                   `json:"estimatedRequestsPerTarget"`
	EstimatedRequestsPerExecution int                                   `json:"estimatedRequestsPerExecution"`
	EstimatedRequestsPerDay       int                                   `json:"estimatedRequestsPerDay"`
}

type QuestionAnswerSchedule struct {
	QuestionAnswerScheduleConfig
	ID                            string                                  `json:"id"`
	UserID                        string                                  `json:"-"`
	AdminAccountID                string                                  `json:"-"`
	Enabled                       bool                                    `json:"enabled"`
	TargetSelectionSnapshot       QuestionAnswerScheduleSelectionSnapshot `json:"targetSelectionSnapshot"`
	BlockedReason                 string                                  `json:"blockedReason"`
	NextRunAt                     *time.Time                              `json:"nextRunAt"`
	Version                       int64                                   `json:"version"`
	CreatedBy                     string                                  `json:"createdBy"`
	CreatedAt                     time.Time                               `json:"createdAt"`
	UpdatedAt                     time.Time                               `json:"updatedAt"`
	DeletedAt                     *time.Time                              `json:"deletedAt"`
	EstimatedTargetCount          int                                     `json:"estimatedTargetCount"`
	EstimatedRequestsPerTarget    int                                     `json:"estimatedRequestsPerTarget"`
	EstimatedRequestsPerExecution int                                     `json:"estimatedRequestsPerExecution"`
	EstimatedRequestsPerDay       int                                     `json:"estimatedRequestsPerDay"`
	PreviewedAt                   time.Time                               `json:"previewedAt"`
	LastActualTargetCount         int                                     `json:"lastActualTargetCount"`
	LastExecution                 *QuestionAnswerScheduleExecution        `json:"lastExecution"`
}

type QuestionAnswerRuntimeSettings struct {
	QuestionAnswerConcurrency int        `json:"questionAnswerConcurrency"`
	Version                   int64      `json:"version"`
	UpdatedAt                 *time.Time `json:"updatedAt"`
}

type QuestionAnswerScheduleLimits struct {
	MaxEnabledQuestionAnswerSchedules int        `json:"maxEnabledQuestionAnswerSchedules"`
	MaxScheduleTargets                int        `json:"maxScheduleTargets"`
	MaxScheduleRequestsPerExecution   int        `json:"maxScheduleRequestsPerExecution"`
	DailyScheduledRequestLimit        int        `json:"dailyScheduledRequestLimit"`
	MaxActiveScheduleExecutions       int        `json:"maxActiveScheduleExecutions"`
	MaxQueuedScheduledRequests        int        `json:"maxQueuedScheduledRequests"`
	ScheduleExecutionTimeoutMinutes   int        `json:"scheduleExecutionTimeoutMinutes"`
	ScheduleLateGraceMinutes          int        `json:"scheduleLateGraceMinutes"`
	Version                           int64      `json:"version"`
	TodayReservedRequests             int        `json:"todayReservedRequests"`
	UpdatedAt                         *time.Time `json:"updatedAt"`
}

func defaultQuestionAnswerScheduleLimits() QuestionAnswerScheduleLimits {
	return QuestionAnswerScheduleLimits{MaxEnabledQuestionAnswerSchedules: 20, MaxScheduleTargets: 20, MaxScheduleRequestsPerExecution: 500, DailyScheduledRequestLimit: 1000, MaxActiveScheduleExecutions: 3, MaxQueuedScheduledRequests: 1000, ScheduleExecutionTimeoutMinutes: 360, ScheduleLateGraceMinutes: 5}
}

type QuestionAnswerScheduleRequested struct {
	Schedule        QuestionAnswerScheduleConfig            `json:"schedule"`
	ScheduleVersion int64                                   `json:"scheduleVersion"`
	Enabled         bool                                    `json:"enabled"`
	Selection       QuestionAnswerScheduleSelectionSnapshot `json:"selection"`
	Limits          QuestionAnswerScheduleLimits            `json:"limits"`
	Timezone        string                                  `json:"timezone"`
}

type QuestionAnswerScheduleResolved struct {
	Questions        []TestQuestion `json:"questions"`
	ResolvedAt       time.Time      `json:"resolvedAt"`
	MissingTargetIDs []string       `json:"missingTargetIds"`
}

type QuestionAnswerScheduleSnapshot struct {
	Requested QuestionAnswerScheduleRequested `json:"requested"`
	Resolved  *QuestionAnswerScheduleResolved `json:"resolved"`
}

type QuestionAnswerScheduleExecution struct {
	ID                      string                                  `json:"id"`
	UserID                  string                                  `json:"-"`
	AdminAccountID          string                                  `json:"-"`
	ScheduleID              string                                  `json:"scheduleId"`
	Trigger                 string                                  `json:"trigger"`
	RequestID               string                                  `json:"requestId"`
	ScheduledFor            time.Time                               `json:"scheduledFor"`
	Status                  string                                  `json:"status"`
	StatusReason            string                                  `json:"statusReason"`
	ConfigSnapshot          QuestionAnswerScheduleSnapshot          `json:"configSnapshot"`
	PlannedTargetCount      int                                     `json:"plannedTargetCount"`
	ReservedRequestCount    int                                     `json:"reservedRequestCount"`
	MissedCount             int                                     `json:"missedCount"`
	MissedFrom              *time.Time                              `json:"missedFrom"`
	MissedThrough           *time.Time                              `json:"missedThrough"`
	CancelRequestedAt       *time.Time                              `json:"cancelRequestedAt"`
	TerminationCause        string                                  `json:"terminationCause"`
	Version                 int64                                   `json:"version"`
	CreatedAt               time.Time                               `json:"createdAt"`
	StartedAt               *time.Time                              `json:"startedAt"`
	CompletedAt             *time.Time                              `json:"completedAt"`
	UpdatedAt               time.Time                               `json:"updatedAt"`
	BatchCreatedTargetCount int                                     `json:"batchCreatedTargetCount"`
	RequestRecordCount      int                                     `json:"requestRecordCount"`
	Active                  bool                                    `json:"active"`
	Stats                   QuestionAnswerStats                     `json:"stats"`
	Targets                 []QuestionAnswerScheduleExecutionTarget `json:"targets"`
}

func (e QuestionAnswerScheduleExecution) deadline() time.Time {
	return e.CreatedAt.Add(time.Duration(e.ConfigSnapshot.Requested.Limits.ScheduleExecutionTimeoutMinutes) * time.Minute)
}

func (e QuestionAnswerScheduleExecution) startWindowEnd() time.Time {
	anchor := e.ScheduledFor
	if e.Trigger == "run_now" {
		anchor = e.CreatedAt
	}
	return anchor.Add(time.Duration(e.ConfigSnapshot.Requested.Limits.ScheduleLateGraceMinutes) * time.Minute)
}

func questionAnswerScheduleExecutionActive(status string) bool {
	return status == "pending" || status == "active"
}

type QuestionAnswerScheduleAccountSnapshot struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	Platform    string `json:"platform"`
}

type QuestionAnswerScheduleTargetConfiguration struct {
	QuestionAnswerConfigurationSnapshot
	Protocol TestProtocol `json:"protocol,omitempty"`
}

// Pure deletion evidence carries no invented membership or request protocol.
func (c QuestionAnswerScheduleTargetConfiguration) MarshalJSON() ([]byte, error) {
	if c.AdminAccountID == "" && !c.InventoryComplete && len(c.Memberships) == 0 && c.Protocol == "" {
		return []byte("{}"), nil
	}
	type wire QuestionAnswerScheduleTargetConfiguration
	return json.Marshal(wire(c))
}

type QuestionAnswerScheduleUnavailableModel struct {
	ModelName string `json:"modelName"`
	Reason    string `json:"reason"`
}

type QuestionAnswerScheduleExecutionTarget struct {
	ID                        string                                    `json:"id"`
	ExecutionID               string                                    `json:"executionId"`
	TargetID                  string                                    `json:"targetId"`
	AccountSnapshot           QuestionAnswerScheduleAccountSnapshot     `json:"accountSnapshot"`
	MatchedGroupsSnapshot     []QuestionAnswerScheduleGroupRef          `json:"matchedGroupsSnapshot"`
	TestConfigurationSnapshot QuestionAnswerScheduleTargetConfiguration `json:"testConfigurationSnapshot"`
	RequestedModels           []string                                  `json:"requestedModels"`
	AvailableModels           []string                                  `json:"availableModels"`
	UnavailableModels         []QuestionAnswerScheduleUnavailableModel  `json:"unavailableModels"`
	PlannedRequestCount       int                                       `json:"plannedRequestCount"`
	Status                    string                                    `json:"status"`
	StatusReason              string                                    `json:"statusReason"`
	CreatedAt                 time.Time                                 `json:"createdAt"`
	StartedAt                 *time.Time                                `json:"startedAt"`
	CompletedAt               *time.Time                                `json:"completedAt"`
	UpdatedAt                 time.Time                                 `json:"updatedAt"`
	BatchAvailable            bool                                      `json:"batchAvailable"`
	BatchID                   string                                    `json:"batchId"`
	Stats                     QuestionAnswerStats                       `json:"stats"`
}

type QuestionAnswerSchedulePage struct {
	Items      []QuestionAnswerSchedule `json:"items"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"pageSize"`
	Total      int                      `json:"total"`
	TotalPages int                      `json:"totalPages"`
	Status     string                   `json:"status"`
}

type QuestionAnswerScheduleExecutionPage struct {
	Items      []QuestionAnswerScheduleExecution `json:"items"`
	Page       int                               `json:"page"`
	PageSize   int                               `json:"pageSize"`
	Total      int                               `json:"total"`
	TotalPages int                               `json:"totalPages"`
}

type QuestionAnswerSchedulePreviewInput struct {
	TargetMode               string   `json:"targetMode"`
	SelectedGroupIDs         []string `json:"selectedGroupIds"`
	SelectedAccountTargetIDs []string `json:"selectedAccountTargetIds"`
	Models                   []string `json:"models"`
	QuestionIDs              []string `json:"questionIds"`
	ReasoningEffort          string   `json:"reasoningEffort"`
	RepeatCount              int      `json:"repeatCount"`
	ScheduleID               string   `json:"scheduleId,omitempty"`
}

type QuestionAnswerScheduleTargetChanges struct {
	Added   []QuestionAnswerSchedulePreviewTarget `json:"added"`
	Removed []QuestionAnswerSchedulePreviewTarget `json:"removed"`
}

type QuestionAnswerSchedulePreview struct {
	EstimatedTargetCount          int                                   `json:"estimatedTargetCount"`
	EstimatedRequestsPerTarget    int                                   `json:"estimatedRequestsPerTarget"`
	EstimatedRequestsPerExecution int                                   `json:"estimatedRequestsPerExecution"`
	TargetPreview                 []QuestionAnswerSchedulePreviewTarget `json:"targetPreview"`
	TargetChanges                 QuestionAnswerScheduleTargetChanges   `json:"targetChanges"`
	ModelCandidates               []string                              `json:"modelCandidates"`
	Warnings                      []string                              `json:"warnings"`
	PreviewedAt                   time.Time                             `json:"previewedAt"`
	ScheduleVersion               *int64                                `json:"scheduleVersion"`
}

type QuestionAnswerSchedulePreparationOutcome struct {
	Status        string
	Reason        string
	BlockedReason string
	Resolved      *QuestionAnswerScheduleResolved
	Targets       []QuestionAnswerScheduleExecutionTarget
}

type QuestionAnswerScheduleTerminationResult struct {
	Execution QuestionAnswerScheduleExecution
	Accepted  bool
	Conflict  bool
}

type QuestionAnswerScheduleHandleIdentity struct {
	UserID         string
	AdminAccountID string
	ScheduleID     string
	ExecutionID    string
}

type QuestionAnswerScheduleConflictError struct {
	Key               string
	Current           any
	ActiveExecutionID string
}

func (e *QuestionAnswerScheduleConflictError) Error() string { return e.Key }

func questionAnswerScheduleError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*QuestionAnswerScheduleConflictError); ok {
		return err
	}
	if _, ok := err.(requestError); ok {
		return err
	}
	return requestError(ErrorQuestionAnswerStorage)
}

// guard precedes repository access, inventory reads and runtime changes.
func (s *Service) questionAnswerScheduleWriteGuard() error {
	if s.backgroundTasksDisabled {
		return requestError(ErrorQuestionAnswerServiceStopped)
	}
	s.questionAnswerMu.Lock()
	closed := s.questionAnswerClosed
	s.questionAnswerMu.Unlock()
	if closed {
		return requestError(ErrorQuestionAnswerServiceStopped)
	}
	return nil
}

func questionAnswerScheduleOwnedTarget(userID, workspace, targetID string) error {
	parsed, ok := parseTargetID(strings.TrimSpace(targetID))
	if userID == "" || !ok || parsed.adminAccountID != workspace || parsed.platform != string("sub2api") || buildTargetID(parsed.platform, workspace, parsed.accountID) != targetID {
		return requestError(ErrorProbeTargetNotFound)
	}
	return nil
}
