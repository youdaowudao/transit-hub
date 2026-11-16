package store

import (
	"encoding/json"
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

type Owner struct {
	UserID      string `json:"userId"`
	WorkspaceID string `json:"workspaceId"`
}

func (o Owner) Validate() error {
	if o.UserID == "" || o.WorkspaceID == "" || len(o.UserID) > 200 || len(o.WorkspaceID) > 200 {
		return Fail(401, "登录用户或工作区无效，请重新选择工作区", "identity")
	}
	return nil
}

type Settings struct {
	TimeoutSeconds      int `json:"timeoutSeconds"`
	TotalTimeoutSeconds int `json:"totalTimeoutSeconds"`
	Concurrency         int `json:"concurrency"`
	MaxOutputTokens     int `json:"maxOutputTokens"`
	Retries             int `json:"retries"`
	MaxAttempts         int `json:"maxAttempts"`
	MemoryLimitMiB      int `json:"memoryLimitMiB"`
	Revision            int `json:"revision"`
}

func DefaultSettings() Settings { return Settings{120, 600, 2, 4096, 0, 128, 256, 1} }
func (s Settings) Validate() error {
	if s.TimeoutSeconds < 10 || s.TimeoutSeconds > 600 {
		return fmt.Errorf("单请求超时必须为10～600秒")
	}
	if s.TotalTimeoutSeconds < 60 || s.TotalTimeoutSeconds > 1800 || s.TimeoutSeconds > s.TotalTimeoutSeconds {
		return fmt.Errorf("任务总时长必须为60～1800秒，且不少于单请求超时")
	}
	if s.Concurrency < 1 || s.Concurrency > 2 {
		return fmt.Errorf("上游并发仅支持1或2")
	}
	if s.MaxOutputTokens < 128 || s.MaxOutputTokens > 16384 {
		return fmt.Errorf("普通推理输出预算必须为128～16384")
	}
	if s.Retries < 0 || s.Retries > 2 || s.MaxAttempts < 16 || s.MaxAttempts > 256 {
		return fmt.Errorf("重试次数为0～2，尝试总数为16～256")
	}
	if s.MemoryLimitMiB < 128 || s.MemoryLimitMiB > 384 {
		return fmt.Errorf("检测任务内存上限必须为128～384MiB")
	}
	return nil
}

type Error struct {
	Status          int    `json:"-"`
	Message         string `json:"message"`
	Step            string `json:"step"`
	OperationResult string `json:"operationResult"`
	Retryable       bool   `json:"retryable"`
	CurrentRevision *int   `json:"currentRevision,omitempty"`
}

func (e *Error) Error() string { return e.Message }
func Fail(status int, message, step string) *Error {
	return &Error{Status: status, Message: message, Step: step, OperationResult: "not_started", Retryable: status >= 500 || status == 409 || status == 429}
}

// Conflict reports only a revision read from the same owner scope; zero means
// that the row is absent. It never authorizes an automatic retry or overwrite.
func Conflict(message, step string, revision int) *Error {
	e := Fail(409, message, step)
	e.CurrentRevision = &revision
	return e
}

type Target struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	GroupName   string `json:"groupName"`
	BaseURL     string `json:"baseURL"`
	Protocol    string `json:"protocol"`
}
type CreateRequest struct {
	Request             json.RawMessage `json:"request,omitempty"`
	ReportID            string          `json:"reportId"`
	TargetID            string          `json:"targetId"`
	GroupID             any             `json:"groupId"`
	Model               string          `json:"model"`
	SelectedSectionIDs  []string        `json:"selectedSectionIds"`
	SelectedQuestionIDs []string        `json:"selectedQuestionIds"`
	ExpectedContext     json.RawMessage `json:"expectedContext"`
	Target              Target          `json:"target"`
	AccountID           string          `json:"accountId,omitempty"`
	AccountName         string          `json:"accountName,omitempty"`
	GroupName           string          `json:"groupName,omitempty"`
	BaseURL             string          `json:"baseURL,omitempty"`
	Protocol            string          `json:"protocol,omitempty"`
}
type Snapshot struct {
	Settings        Settings              `json:"settings"`
	Questions       []questions.Generated `json:"questions"`
	UpstreamVersion string                `json:"upstreamVersion"`
}
type Summary struct {
	ReportID    string          `json:"reportId"`
	TargetID    string          `json:"targetId"`
	AccountID   string          `json:"accountId"`
	AccountName string          `json:"accountName"`
	GroupID     string          `json:"groupId"`
	GroupName   string          `json:"groupName"`
	BaseURL     string          `json:"baseURL"`
	Protocol    string          `json:"protocol"`
	Model       string          `json:"model"`
	CreatedAtMs int64           `json:"createdAtMs"`
	UpdatedAtMs int64           `json:"updatedAtMs"`
	Status      string          `json:"status"`
	Summary     json.RawMessage `json:"summary"`
	Error       json.RawMessage `json:"error"`
}
type Report struct {
	Summary
	Request             json.RawMessage `json:"request"`
	Settings            Settings        `json:"settings"`
	ItemCounts          map[string]int  `json:"itemCounts"`
	MemoryLimitMiB      int             `json:"memoryLimitMiB"`
	TotalTimeoutSeconds int             `json:"totalTimeoutSeconds"`
}
type Item struct {
	ItemID         string               `json:"itemId"`
	SectionID      string               `json:"sectionId"`
	Definition     questions.Definition `json:"definition"`
	Seed           int64                `json:"seed"`
	Prompt         string               `json:"prompt"`
	Expected       string               `json:"expected"`
	Tags           []string             `json:"tags,omitempty"`
	Status         string               `json:"status"`
	Attempts       int                  `json:"attempts"`
	Result         json.RawMessage      `json:"result"`
	ImageAvailable bool                 `json:"imageAvailable"`
	StartedAtMs    int64                `json:"startedAtMs"`
	CompletedAtMs  int64                `json:"completedAtMs"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor"`
}
type QuestionSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Enabled  bool   `json:"enabled"`
	Order    int    `json:"order"`
	Revision int    `json:"revision"`
}
