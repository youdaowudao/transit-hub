package connection_health

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptrace"
	"sync"
	"time"
)

const questionAnswerDiagnosticsLimit = 16 << 10

// Diagnostics contain observations of this request, never its content or headers.
type QuestionAnswerDiagnostics struct {
	Version             int        `json:"version"`
	TimeoutMs           *int64     `json:"timeoutMs"`
	SelectedAt          *time.Time `json:"selectedAt"`
	RequestStartedAt    *time.Time `json:"requestStartedAt"`
	RequestFinishedAt   *time.Time `json:"requestFinishedAt"`
	RequestElapsedMs    *int64     `json:"requestElapsedMs"`
	RequestWrittenAt    *time.Time `json:"requestWrittenAt"`
	FirstResponseByteAt *time.Time `json:"firstResponseByteAt"`
	ResponseHeadersAt   *time.Time `json:"responseHeadersAt"`
	LastReceivedAt      *time.Time `json:"lastReceivedAt"`
	ReceivedBytes       int64      `json:"receivedBytes"`
	FailureStep         string     `json:"failureStep,omitempty"`
	LocalError          string     `json:"localError,omitempty"`
}

type QuestionAnswerProgress struct {
	mu                  sync.Mutex
	step                string
	diagnostics         QuestionAnswerDiagnostics
	started             time.Time // retains Go's monotonic clock; never serialized
	storageError        string
	pendingResultStatus *QuestionAnswerStatus
}

type QuestionAnswerRuntimeItem struct {
	RecordID            string                     `json:"recordId"`
	Step                string                     `json:"step"`
	Diagnostics         *QuestionAnswerDiagnostics `json:"diagnostics"`
	StorageError        string                     `json:"storageError"`
	PendingResultStatus *QuestionAnswerStatus      `json:"pendingResultStatus"`
}

type QuestionAnswerRuntime struct {
	SnapshotAt time.Time                   `json:"snapshotAt"`
	Items      []QuestionAnswerRuntimeItem `json:"items"`
}

func newQuestionAnswerProgress(now time.Time) *QuestionAnswerProgress {
	now = now.UTC()
	return &QuestionAnswerProgress{step: "starting", diagnostics: QuestionAnswerDiagnostics{Version: 1, SelectedAt: &now}}
}

func cloneQuestionAnswerDiagnostics(value *QuestionAnswerDiagnostics) *QuestionAnswerDiagnostics {
	if value == nil {
		return nil
	}
	copy := *value
	for _, field := range []**time.Time{&copy.SelectedAt, &copy.RequestStartedAt, &copy.RequestFinishedAt, &copy.RequestWrittenAt, &copy.FirstResponseByteAt, &copy.ResponseHeadersAt, &copy.LastReceivedAt} {
		if *field != nil {
			v := **field
			*field = &v
		}
	}
	for _, field := range []**int64{&copy.TimeoutMs, &copy.RequestElapsedMs} {
		if *field != nil {
			v := **field
			*field = &v
		}
	}
	return &copy
}

func encodeQuestionAnswerDiagnostics(value *QuestionAnswerDiagnostics) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	if value.Version != 1 || value.ReceivedBytes < 0 || (value.TimeoutMs != nil && *value.TimeoutMs < 0) || (value.RequestElapsedMs != nil && *value.RequestElapsedMs < 0) {
		return nil, errors.New("答题过程记录的版本或计时无效")
	}
	switch value.FailureStep {
	case "", "starting", "build", "request", "receive", "parse":
	default:
		return nil, errors.New("答题过程记录的失败步骤无效")
	}
	if len([]rune(value.LocalError)) > 500 {
		return nil, errors.New("答题过程记录的原因超过500字")
	}
	data, err := json.Marshal(value)
	if err == nil && len(data) > questionAnswerDiagnosticsLimit {
		err = errors.New("答题过程记录超过16KiB")
	}
	return data, err
}

func decodeQuestionAnswerDiagnostics(data []byte) (*QuestionAnswerDiagnostics, bool) {
	if len(data) == 0 || string(data) == "null" {
		return nil, false
	}
	if len(data) > questionAnswerDiagnosticsLimit || data[0] != '{' {
		return nil, true
	}
	var value QuestionAnswerDiagnostics
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, true
	}
	if _, err := encodeQuestionAnswerDiagnostics(&value); err != nil {
		return nil, true
	}
	return &value, false
}

func (p *QuestionAnswerProgress) snapshot(recordID string) QuestionAnswerRuntimeItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	item := QuestionAnswerRuntimeItem{RecordID: recordID, Step: p.step, Diagnostics: cloneQuestionAnswerDiagnostics(&p.diagnostics), StorageError: p.storageError}
	if p.pendingResultStatus != nil {
		status := *p.pendingResultStatus
		item.PendingResultStatus = &status
	}
	return item
}

func (p *QuestionAnswerProgress) startRequest(timeout time.Duration, now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = now
	stamp, budget := now.UTC(), timeout.Milliseconds()
	p.diagnostics.RequestStartedAt, p.diagnostics.TimeoutMs = &stamp, &budget
	p.step = "request"
}

func (p *QuestionAnswerProgress) failure(step string, err error, keys ...string) {
	if p == nil || err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// The transparent reader's direct error is more precise than a parser's fallback.
	if p.diagnostics.LocalError == "" {
		p.diagnostics.FailureStep, p.diagnostics.LocalError = step, questionAnswerSafeCause(err, keys...)
	}
}

func (p *QuestionAnswerProgress) finish(result QuestionAnswerAskResult) *QuestionAnswerDiagnostics {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	stamp := now.UTC()
	p.diagnostics.RequestFinishedAt = &stamp
	if !p.started.IsZero() {
		elapsed := now.Sub(p.started).Milliseconds()
		p.diagnostics.RequestElapsedMs = &elapsed
	}
	if result.ErrorType == "" {
		// Transport callbacks can describe an attempt that later recovered. Only
		// the final parser result decides whether this request failed.
		p.diagnostics.FailureStep, p.diagnostics.LocalError = "", ""
	} else if p.diagnostics.FailureStep == "" {
		p.diagnostics.FailureStep = result.failureStep
		if p.diagnostics.FailureStep == "" {
			p.diagnostics.FailureStep = "parse"
		}
		p.diagnostics.LocalError = questionAnswerSafeCause(result.localError)
	}
	p.step = "saving"
	return cloneQuestionAnswerDiagnostics(&p.diagnostics)
}

func (p *QuestionAnswerProgress) trace(key string) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				// A failed write attempt may be retried by the transport. If Do
				// ultimately fails, its returned error supplies the direct cause.
				return
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.diagnostics.RequestWrittenAt == nil {
				now := time.Now().UTC()
				p.diagnostics.RequestWrittenAt = &now
			}
		},
		GotFirstResponseByte: func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.diagnostics.FirstResponseByteAt == nil {
				now := time.Now().UTC()
				p.diagnostics.FirstResponseByteAt = &now
			}
		},
	}
}

type questionAnswerObservedBody struct {
	io.ReadCloser
	progress *QuestionAnswerProgress
	key      string
}

func (body *questionAnswerObservedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 {
		body.progress.mu.Lock()
		now := time.Now().UTC()
		body.progress.diagnostics.LastReceivedAt = &now
		body.progress.diagnostics.ReceivedBytes += int64(n)
		body.progress.mu.Unlock()
	}
	if err != nil && err != io.EOF {
		body.progress.failure("receive", err, body.key)
	}
	return n, err
}

func (s *Service) attachQuestionAnswerRuntime(userID, targetID string, batch *QuestionAnswerBatch) {
	if batch == nil || batch.BatchID == "" {
		return
	}
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	run := s.questionAnswerRuns[questionAnswerRunKey(userID, targetID)]
	if run == nil || run.batchID != batch.BatchID || run.userID != userID || run.targetID != targetID {
		return
	}
	runtime := &QuestionAnswerRuntime{SnapshotAt: time.Now().UTC(), Items: []QuestionAnswerRuntimeItem{}}
	for _, record := range batch.Records {
		if record.Status != QuestionAnswerPending && record.Status != QuestionAnswerRunning {
			continue
		}
		if progress := run.progress[record.ID]; progress != nil {
			runtime.Items = append(runtime.Items, progress.snapshot(record.ID))
		}
	}
	batch.Runtime = runtime
}
