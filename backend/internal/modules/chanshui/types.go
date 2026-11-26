package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const MemoryCeiling uint64 = 500000000
const sliceName = "transithub-chanshui.slice"

type Owner struct {
	UserID      string `json:"userId"`
	WorkspaceID string `json:"workspaceId"`
}
type Credential struct {
	BaseURL       string `json:"baseURL"`
	Key           string `json:"key"`
	Protocol      string `json:"protocol"`
	AnthropicAuth string `json:"anthropicAuth,omitempty"`
}
type Deployment struct {
	DisableBankSync     bool `json:"disableBankSync,omitempty"`
	AllowPrivateTargets bool `json:"allowPrivateTargets"`
}
type Envelope struct {
	V          int        `json:"v"`
	Operation  string     `json:"operation"`
	Owner      Owner      `json:"owner"`
	Args       any        `json:"args"`
	Deployment Deployment `json:"deployment"`
}
type Problem struct {
	Message            string `json:"message"`
	Step               string `json:"step"`
	OperationResult    string `json:"operationResult"`
	ReportID           string `json:"reportId,omitempty"`
	Detail             string `json:"detail,omitempty"`
	Retryable          bool   `json:"retryable"`
	CurrentRevision    *int   `json:"currentRevision,omitempty"`
	Status             int    `json:"-"`
	QueryState         string `json:"queryState,omitempty"`
	RetryAfterMs       int64  `json:"retryAfterMs,omitempty"`
	RecoveryDeadlineMs int64  `json:"recoveryDeadlineMs,omitempty"`
}

func (e *Problem) Error() string { return e.Message }
func problem(status int, step, message string) *Problem {
	return &Problem{Status: status, Step: step, Message: message, OperationResult: "not_started", Retryable: true}
}
func asProblem(err error) *Problem {
	if p, ok := err.(*Problem); ok {
		return p
	}
	return problem(503, "runtime", fmt.Sprintf("模型检测处理未完成：%s。请核查运行状态后重试", err))
}

type QueryResponse struct {
	Status      int
	ContentType string
	Length      int64
	Body        io.ReadCloser
}
type ExitInfo struct {
	Exited              bool   `json:"processExited"`
	Result              string `json:"result,omitempty"`
	MemoryPeak          uint64 `json:"memoryPeakBytes,omitempty"`
	OOM                 bool   `json:"oom"`
	TaskMemoryMaxBytes  uint64 `json:"taskMemoryMaxBytes,omitempty"`
	SliceMemoryMaxBytes uint64 `json:"sliceMemoryMaxBytes,omitempty"`
	SwapMaxBytes        uint64 `json:"swapMaxBytes"`
}
type Job struct {
	ID                  string
	ExitConfirmDeadline time.Time
	Done                <-chan struct{}
	stop                func(context.Context) error
	info                func() ExitInfo
}

func (j *Job) Stop(ctx context.Context) error { return j.stop(ctx) }
func (j *Job) Info() ExitInfo                 { return j.info() }

type Executor interface {
	Query(context.Context, Envelope, *Credential, bool) (*QueryResponse, error)
	Start(context.Context, Envelope, Credential, int, int) (*Job, error)
	Recover(context.Context) error
	Available(context.Context) error
	Shutdown(context.Context) error
}
type wireHeader struct {
	V           int             `json:"v"`
	OK          bool            `json:"ok"`
	Status      int             `json:"status"`
	ContentType string          `json:"contentType"`
	Length      int64           `json:"length"`
	Error       json.RawMessage `json:"error,omitempty"`
}
