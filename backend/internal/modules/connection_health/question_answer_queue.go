package connection_health

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

// This selector has no side effects. Dispatch and the copied queue use the same
// rotation, including the existing behavior when the previous key was removed.
func nextQuestionAnswerChoice(order []string, lastKey string, eligible func(string) bool) (string, bool) {
	if len(order) == 0 {
		return "", false
	}
	start := 0
	for i, key := range order {
		if key == lastKey {
			start = (i + 1) % len(order)
			break
		}
	}
	for offset := range order {
		key := order[(start+offset)%len(order)]
		if eligible(key) {
			return key, true
		}
	}
	return "", false
}

type QuestionAnswerQueueTarget struct {
	TargetID string `json:"targetId"`
	BatchID  string `json:"batchId"`
}

type QuestionAnswerQueueItem struct {
	QuestionAnswerQueueTarget
	Position     int                        `json:"position,omitempty"`
	RecordID     string                     `json:"recordId"`
	ModelName    string                     `json:"modelName"`
	QuestionName string                     `json:"questionName"`
	RepeatIndex  *int                       `json:"repeatIndex"`
	CreatedAt    time.Time                  `json:"createdAt"`
	Runtime      *QuestionAnswerRuntimeItem `json:"runtime,omitempty"`
}

// Queue finalization deliberately omits recovery: only a database-backed batch
// read can decide whether records exist and which recovery remains possible.
type QuestionAnswerQueueFinalization struct {
	QuestionAnswerQueueTarget
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	HeldCount int    `json:"heldCount"`
}

type QuestionAnswerQueueCounts struct {
	Queued            int `json:"queued"`
	Processing        int `json:"processing"`
	Stopping          int `json:"stopping"`
	PreparingTargets  int `json:"preparingTargets"`
	FinalizingBatches int `json:"finalizingBatches"`
}

type QuestionAnswerQueue struct {
	SnapshotAt        time.Time                         `json:"snapshotAt"`
	Capacity          int                               `json:"capacity"`
	AvailableSlots    int                               `json:"availableSlots"`
	WorkspaceOccupied int                               `json:"workspaceOccupied"`
	HeldCount         int                               `json:"heldCount"`
	PreparingTargets  []QuestionAnswerQueueTarget       `json:"preparingTargets"`
	Queued            []QuestionAnswerQueueItem         `json:"queued"`
	Processing        []QuestionAnswerQueueItem         `json:"processing"`
	Stopping          []QuestionAnswerQueueItem         `json:"stopping"`
	FinalizingBatches []QuestionAnswerQueueFinalization `json:"finalizingBatches"`
	Counts            QuestionAnswerQueueCounts         `json:"counts"`
}

type questionAnswerQueueRun struct {
	userID       string
	targetID     string
	batchID      string
	next         int
	eligible     bool
	stopping     bool
	inFlight     int
	held         int
	records      []QuestionAnswerQueueItem
	finalization *QuestionAnswerQueueFinalization
	finalErr     error
	privateKey   string
}

type questionAnswerQueueSnapshot struct {
	queue   QuestionAnswerQueue
	order   []string
	lastKey string
	runs    map[string]*questionAnswerQueueRun
	starts  []struct {
		userID string
		target QuestionAnswerQueueTarget
	}
}

func queueTargetInScope(userID, workspace, owner, targetID string) bool {
	parsed, ok := parseTargetID(targetID)
	return ok && userID == owner && parsed.platform == "sub2api" && parsed.adminAccountID == workspace &&
		targetID == buildTargetID(parsed.platform, parsed.adminAccountID, parsed.accountID) && strings.TrimSpace(targetID) == targetID &&
		strings.TrimSpace(parsed.accountID) == parsed.accountID && !strings.Contains(parsed.accountID, ":")
}

// One lock acquisition copies only identities, cursors, occupancy and progress.
// Final errors and their private masking input are copied without formatting;
// credentials are never serialized. No SQL, network, sorting or simulation runs here.
func (s *Service) copyQuestionAnswerQueue() questionAnswerQueueSnapshot {
	s.questionAnswerMu.Lock()
	defer s.questionAnswerMu.Unlock()
	snapshot := questionAnswerQueueSnapshot{
		queue: QuestionAnswerQueue{SnapshotAt: time.Now().UTC(), Capacity: s.questionAnswerConcurrency(), Queued: []QuestionAnswerQueueItem{}, Processing: []QuestionAnswerQueueItem{}, Stopping: []QuestionAnswerQueueItem{}, PreparingTargets: []QuestionAnswerQueueTarget{}, FinalizingBatches: []QuestionAnswerQueueFinalization{}},
		order: append([]string(nil), s.questionAnswerOrder...), lastKey: s.questionAnswerLastKey, runs: make(map[string]*questionAnswerQueueRun, len(s.questionAnswerRuns)),
	}
	snapshot.queue.AvailableSlots = max(0, snapshot.queue.Capacity-s.questionAnswerInFlight)
	for key, run := range s.questionAnswerRuns {
		copy := &questionAnswerQueueRun{userID: run.userID, targetID: run.targetID, batchID: run.batchID, next: run.next, eligible: !s.questionAnswerClosed && run.stopReason == "" && run.ctx.Err() == nil, stopping: run.stopReason != "" || run.ctx.Err() != nil, inFlight: run.inFlight, held: run.heldSlots, records: make([]QuestionAnswerQueueItem, len(run.records))}
		for i, record := range run.records {
			item := QuestionAnswerQueueItem{QuestionAnswerQueueTarget: QuestionAnswerQueueTarget{run.targetID, run.batchID}, RecordID: record.ID, ModelName: record.ModelName, QuestionName: record.QuestionName, RepeatIndex: record.RepeatIndex, CreatedAt: record.CreatedAt}
			if progress := run.progress[record.ID]; progress != nil {
				runtime := progress.snapshot(record.ID)
				item.Runtime = &runtime
			}
			copy.records[i] = item
		}
		if run.stopReason != "" || run.finalizing || run.finalErr != nil {
			state := "pending"
			if run.finalErr != nil {
				state = "failed"
			}
			copy.finalization = &QuestionAnswerQueueFinalization{QuestionAnswerQueueTarget: QuestionAnswerQueueTarget{run.targetID, run.batchID}, State: state, Reason: run.stopReason, HeldCount: run.heldSlots}
			copy.finalErr, copy.privateKey = run.finalErr, run.cred.Key
		}
		snapshot.runs[key] = copy
	}
	for _, start := range s.questionAnswerStarts {
		if !start.released && start.ctx.Err() == nil {
			snapshot.starts = append(snapshot.starts, struct {
				userID string
				target QuestionAnswerQueueTarget
			}{start.userID, QuestionAnswerQueueTarget{start.targetID, start.batchID}})
		}
	}
	return snapshot
}

func (snapshot questionAnswerQueueSnapshot) project(userID, workspace string) QuestionAnswerQueue {
	queue := snapshot.queue
	for _, start := range snapshot.starts {
		if queueTargetInScope(userID, workspace, start.userID, start.target.TargetID) {
			queue.PreparingTargets = append(queue.PreparingTargets, start.target)
		}
	}
	for _, key := range snapshot.order {
		run := snapshot.runs[key]
		if run == nil || !queueTargetInScope(userID, workspace, run.userID, run.targetID) {
			continue
		}
		queue.WorkspaceOccupied += run.inFlight + run.held
		queue.HeldCount += run.held
		if len(run.records) == 0 && run.eligible {
			queue.PreparingTargets = append(queue.PreparingTargets, QuestionAnswerQueueTarget{run.targetID, run.batchID})
		}
		if run.finalization != nil {
			finalization := *run.finalization
			finalization.Message = questionAnswerFinalizationMessage(run.finalErr, finalization.Reason, run.privateKey)
			queue.FinalizingBatches = append(queue.FinalizingBatches, finalization)
		}
		for i, record := range run.records {
			if i < run.next && record.Runtime != nil {
				queue.Processing = append(queue.Processing, record)
			}
			if i >= run.next && run.stopping {
				queue.Stopping = append(queue.Stopping, record)
			}
		}
	}
	lastKey := snapshot.lastKey
	cursors := make(map[string]int, len(snapshot.runs))
	for key, run := range snapshot.runs {
		cursors[key] = run.next
	}
	for {
		key, ok := nextQuestionAnswerChoice(snapshot.order, lastKey, func(key string) bool {
			run := snapshot.runs[key]
			return run != nil && run.eligible && cursors[key] < len(run.records)
		})
		if !ok {
			break
		}
		run := snapshot.runs[key]
		item := run.records[cursors[key]]
		cursors[key]++
		lastKey = key
		if queueTargetInScope(userID, workspace, run.userID, run.targetID) {
			item.Position = len(queue.Queued) + 1
			queue.Queued = append(queue.Queued, item)
		}
	}
	queue.Counts = QuestionAnswerQueueCounts{len(queue.Queued), len(queue.Processing), len(queue.Stopping), len(queue.PreparingTargets), len(queue.FinalizingBatches)}
	return queue
}

func questionAnswerFinalizationMessage(finalErr error, reason, key string) string {
	if finalErr != nil {
		return questionAnswerSafeCause(finalErr, key)
	}
	switch reason {
	case string(QuestionAnswerCancelled):
		return "已停止发送，结束结果尚在确认"
	case QuestionAnswerErrorServiceShutdown:
		return "答题服务已停止发送，结束结果尚在确认"
	case QuestionAnswerErrorInternal:
		return "答题处理发生内部错误，结束结果尚在确认"
	case QuestionAnswerErrorStorage:
		return "保存或结束结果尚未确认，请查看每题实际结果"
	default:
		return "批次结束结果尚在确认，请查看每题实际结果"
	}
}

var errQuestionAnswerQueueWorkspaceChanged = errors.New("当前工作区已变化，请重新选择工作区后重新读取；这次只读取状态，没有发题或修改主站")

func (s *Service) GetQuestionAnswerQueue(ctx context.Context, userID, workspace string) (QuestionAnswerQueue, error) {
	current, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return QuestionAnswerQueue{}, err
	}
	if workspace == "" || workspace != current {
		return QuestionAnswerQueue{}, errQuestionAnswerQueueWorkspaceChanged
	}
	return s.copyQuestionAnswerQueue().project(userID, workspace), nil
}

func (h *Handler) getQuestionAnswerQueue(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	queue, err := h.service.GetQuestionAnswerQueue(r.Context(), userID, r.URL.Query().Get("workspace"))
	if errors.Is(err, errQuestionAnswerQueueWorkspaceChanged) {
		detail := "读取排队信息失败：" + err.Error()
		if r.URL.Query().Get("workspace") == "" {
			detail = "读取排队信息失败：本次未提供工作区编号，请重新选择工作区后重新读取；这次只读取状态，没有发题或修改主站。"
		}
		httpjson.Write(w, http.StatusConflict, map[string]string{"message": ErrorProbeTargetNotFound, "detail": detail, "step": "queue", "operationResult": "completed"})
		return
	}
	if err != nil {
		writeQuestionAnswerError(w, err, "queue", "completed", "")
		return
	}
	httpjson.Write(w, http.StatusOK, queue)
}
