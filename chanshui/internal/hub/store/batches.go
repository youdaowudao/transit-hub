package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/api"
)

// BatchRequest stores the frozen choices, not credentials or generated content.
type BatchExpectedContext struct {
	WorkspaceID string `json:"workspaceId"`
	TargetID    string `json:"targetId"`
	GroupID     string `json:"groupId"`
	BaseURL     string `json:"baseURL"`
	Protocol    string `json:"protocol"`
}
type BatchTask struct {
	ReportID        string               `json:"reportId"`
	TargetID        string               `json:"targetId"`
	GroupID         string               `json:"groupId"`
	Model           string               `json:"model"`
	ExpectedContext BatchExpectedContext `json:"expectedContext"`
}
type BatchRequest struct {
	BatchID             string      `json:"batchId"`
	SettingsRevision    int         `json:"settingsRevision"`
	SelectedSectionIDs  []string    `json:"selectedSectionIds"`
	SelectedQuestionIDs []string    `json:"selectedQuestionIds"`
	Tasks               []BatchTask `json:"tasks"`
}

// Only the parent executor supplies target metadata, derived from the
// authenticated workspace and validated account/group context.
type BatchTarget struct {
	TargetID string `json:"targetId"`
	GroupID  string `json:"groupId"`
	Target   Target `json:"target"`
}
type BatchAccepted struct {
	BatchID             string             `json:"batchId"`
	ReportIDs           []string           `json:"reportIds"`
	SettingsRevision    int                `json:"settingsRevision"`
	MemoryLimitMiB      int                `json:"memoryLimitMiB"`
	TotalTimeoutSeconds int                `json:"totalTimeoutSeconds"`
	FirstFinishedAtMs   int64              `json:"firstFinishedAtMs,omitempty"`
	MemberStates        []BatchMemberState `json:"memberStates,omitempty"`
}
type BatchMemberState struct {
	ReportID            string      `json:"reportId"`
	Status              string      `json:"status"`
	RetryID             string      `json:"retryId,omitempty"`
	RetryAcceptedAtMs   int64       `json:"retryAcceptedAtMs,omitempty"`
	Timing              *api.Timing `json:"timing,omitempty"`
	TotalTimeoutSeconds int         `json:"totalTimeoutSeconds,omitempty"`
	EtaKey              string      `json:"etaKey,omitempty"`
	SummaryComplete     bool        `json:"summaryComplete"`
}

var batchSectionNames = map[string]string{
	"iq": "推理题", "vision": "识图", "knowledge": "知识事件", "tools": "工具调用",
	"cache": "缓存读写", "injection": "注入大小", "web": "联网", "thinking": "思维摘要",
	"fingerprint": "模型指纹", "fingerprint_numeric": "数字指纹", "fingerprint_behavior": "行为指纹",
}

func normalizeBatchRequest(r BatchRequest) (BatchRequest, []byte, error) {
	fail := func(message string) (BatchRequest, []byte, error) {
		return r, nil, Fail(422, message+"；请重新确认整批清单，本次未创建报告或发送请求", "create-batch")
	}
	if !reportPattern.MatchString(r.BatchID) || r.SettingsRevision < 1 {
		return fail("批次编号或检测设置版本无效")
	}
	if len(r.Tasks) < 1 || len(r.Tasks) > 32 {
		return fail("一批必须选择1～32份报告")
	}
	r.SelectedSectionIDs = sortedBatchIDs(r.SelectedSectionIDs)
	r.SelectedQuestionIDs = sortedBatchIDs(r.SelectedQuestionIDs)
	if len(r.SelectedSectionIDs) == 0 && len(r.SelectedQuestionIDs) == 0 {
		return fail("请至少选择一个检测分类或题目")
	}
	for _, section := range r.SelectedSectionIDs {
		if _, ok := batchSectionNames[section]; !ok {
			return fail("检测分类不存在")
		}
	}
	for _, id := range r.SelectedQuestionIDs {
		if strings.TrimSpace(id) == "" || len(id) > 200 {
			return fail("检测题目编号无效")
		}
	}
	seen := map[string]bool{}
	combination := map[[2]string]string{}
	r.Tasks = append([]BatchTask(nil), r.Tasks...)
	for i := range r.Tasks {
		t := &r.Tasks[i]
		t.Model = strings.TrimSpace(t.Model)
		if !reportPattern.MatchString(t.ReportID) || seen[t.ReportID] ||
			t.TargetID == "" || t.GroupID == "" || t.Model == "" || len(t.Model) > 256 {
			return fail("报告编号重复或账号、分组、模型、报告编号无效")
		}
		seen[t.ReportID] = true
		key := [2]string{t.TargetID, t.Model}
		if group, exists := combination[key]; exists {
			if t.GroupID != group {
				return fail("相同账号与模型不能同时使用多个分组")
			}
			return fail("相同账号与模型的任务重复")
		}
		combination[key] = t.GroupID
	}
	b, err := json.Marshal(r)
	if err != nil {
		return r, nil, err
	}
	if len(b) > 128<<10 {
		return fail("批次清单超过128KiB")
	}
	return r, b, nil
}
func sortedBatchIDs(in []string) []string {
	unique := make(map[string]struct{}, len(in))
	out := []string{}
	for _, id := range in {
		if _, ok := unique[id]; !ok {
			unique[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

type batchMeta struct {
	ID                         string
	CreatedAt, FirstFinishedAt int64
	Request                    BatchRequest
	canonical                  string
}

func readBatchMeta(ctx context.Context, reader reportReader, owner Owner, id string) (batchMeta, error) {
	var m batchMeta
	if err := owner.Validate(); err != nil {
		return m, err
	}
	if !reportPattern.MatchString(id) {
		return m, Fail(422, "批次编号格式无效，请输入32位小写十六进制编号", "batch")
	}
	err := reader.QueryRowContext(ctx, `SELECT batch_id,created_at_ms,first_finished_at_ms,request_json FROM hub_batches WHERE user_id=? AND workspace_id=? AND batch_id=?`, owner.UserID, owner.WorkspaceID, id).Scan(&m.ID, &m.CreatedAt, &m.FirstFinishedAt, &m.canonical)
	if err == sql.ErrNoRows {
		return m, Fail(404, "批次不存在或不属于当前工作区", "batch")
	}
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal([]byte(m.canonical), &m.Request); err != nil {
		return m, err
	}
	if len(m.Request.Tasks) < 1 || len(m.Request.Tasks) > 32 {
		return m, Fail(503, "保存的批次任务数量无效，请核查检测数据库；已有报告不受影响", "batch")
	}
	return m, nil
}

func (s *Store) CreateBatch(ctx context.Context, o Owner, request BatchRequest, targets []BatchTarget) (BatchAccepted, error) {
	var out BatchAccepted
	if err := o.Validate(); err != nil {
		return out, err
	}
	r, canonical, err := normalizeBatchRequest(request)
	if err != nil {
		return out, err
	}
	// The idempotency check precedes new settings and target checks. Existing
	// batches are not changed because names or settings subsequently changed.
	if existing, err := s.CheckBatchRequest(ctx, o, r.BatchID, r); err == nil {
		return existing, nil
	} else if typed, ok := err.(*Error); !ok || typed.Status != 404 {
		return out, err
	}
	envelope, err := json.Marshal(struct {
		Request BatchRequest  `json:"request"`
		Targets []BatchTarget `json:"targets"`
	}{r, targets})
	if err != nil {
		return out, err
	}
	if len(envelope) > 128<<10 {
		return out, Fail(422, "整批任务及账号资料超过128KiB，请减少组合；没有创建报告或发送请求", "create-batch")
	}
	targetByKey := map[[2]string]Target{}
	for _, t := range targets {
		key := [2]string{t.TargetID, t.GroupID}
		if _, exists := targetByKey[key]; exists {
			return out, Fail(422, "批次账号与分组资料重复，请重新确认清单；未发送请求", "create-batch")
		}
		if t.TargetID == "" || t.GroupID == "" || t.Target.AccountID == "" || t.Target.BaseURL == "" || t.Target.Protocol == "" {
			return out, Fail(422, "批次账号与分组资料不完整，请重新读取账号；没有创建报告或发送请求", "create-batch")
		}
		targetByKey[key] = t.Target
	}
	for _, task := range r.Tasks {
		if _, ok := targetByKey[[2]string{task.TargetID, task.GroupID}]; !ok {
			return out, Fail(422, "批次缺少所选账号与分组的资料，请重新读取；没有创建报告或发送请求", "create-batch")
		}
	}
	// A batch must use the settings and question bank the user already
	// read and confirmed; unlike a first-use single report, do not seed a
	// new scope outside the atomic batch transaction.
	if err = s.writable(); err != nil {
		return out, err
	}
	err = retryWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// The second check shares the write transaction, covering a concurrent
		// creator even when not called through the serialized parent helper.
		m, err := readBatchMeta(ctx, tx, o, r.BatchID)
		if err == nil {
			if string(canonical) != m.canonical {
				return Fail(409, "相同批次编号的检测清单已改变，请使用原清单或明确新建批次", "create-batch")
			}
			out, err = readBatchAccepted(ctx, tx, o, m)
			return err
		}
		if typed, ok := err.(*Error); !ok || typed.Status != 404 {
			return err
		}
		// A different owner's ID cannot be overwritten. Detect the collision
		// before writing or generating any papers.
		var found int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM hub_batches WHERE batch_id=?`, r.BatchID).Scan(&found)
		if err == nil {
			return Fail(404, "批次不存在或不属于当前工作区", "batch")
		}
		if err != sql.ErrNoRows {
			return err
		}

		template := CreateRequest{SelectedSectionIDs: r.SelectedSectionIDs, SelectedQuestionIDs: r.SelectedQuestionIDs}
		settings, defs, err := prepareReport(ctx, tx, o, template, r.SettingsRevision)
		if err != nil {
			return err
		}
		createdAt := Now().UnixMilli()
		if _, err = tx.ExecContext(ctx, `INSERT INTO hub_batches(batch_id,user_id,workspace_id,created_at_ms,request_json) VALUES(?,?,?,?,?)`, r.BatchID, o.UserID, o.WorkspaceID, createdAt, string(canonical)); err != nil {
			return err
		}
		// Check all report IDs once, before generating any paper. The
		// existing report primary key also guards concurrent insertions.
		var collisions int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hub_reports WHERE report_id IN (SELECT json_extract(value,'$.reportId') FROM json_each(?,'$.tasks'))`, string(canonical)).Scan(&collisions); err != nil {
			return err
		}
		if collisions != 0 {
			return Fail(409, "批次中的报告编号已被使用，请重新生成新批次编号与清单；本次未创建报告或发送请求", "create-batch")
		}
		accepted := BatchAccepted{BatchID: r.BatchID, ReportIDs: make([]string, 0, len(r.Tasks)), SettingsRevision: settings.Revision,
			MemoryLimitMiB: settings.MemoryLimitMiB, TotalTimeoutSeconds: settings.TotalTimeoutSeconds}
		for _, t := range r.Tasks {
			report := template
			report.ReportID, report.TargetID, report.GroupID, report.Model = t.ReportID, t.TargetID, t.GroupID, t.Model
			report.Target = targetByKey[[2]string{t.TargetID, t.GroupID}]
			report.ExpectedContext, _ = json.Marshal(t.ExpectedContext)
			// The existing single-report binding and frozen selections remain
			// available through the original report and retry entry points.
			report.Request, _ = json.Marshal(struct {
				BatchTask
				SelectedSectionIDs  []string `json:"selectedSectionIds"`
				SelectedQuestionIDs []string `json:"selectedQuestionIds"`
			}{t, r.SelectedSectionIDs, r.SelectedQuestionIDs})
			if err = createReportTx(ctx, tx, o, report, canonicalRequest(report), settings, defs, r.BatchID, createdAt); err != nil {
				return err
			}
			accepted.ReportIDs = append(accepted.ReportIDs, t.ReportID)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		out = accepted
		return nil
	})
	return out, errorSQL(err)
}

// CheckBatchRequest only checks the frozen request; it never creates or queues.
func (s *Store) CheckBatchRequest(ctx context.Context, o Owner, id string, r BatchRequest) (BatchAccepted, error) {
	var out BatchAccepted
	r, canonical, err := normalizeBatchRequest(r)
	if err != nil {
		return out, err
	}
	if r.BatchID != id {
		return out, Fail(409, "查询的批次编号与原受理清单不一致，请核查原编号", "batch")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	meta, err := readBatchMeta(ctx, tx, o, id)
	if err != nil {
		return out, err
	}
	if string(canonical) != meta.canonical {
		return out, Fail(409, "相同批次编号的检测清单已改变，请使用原清单或明确新建批次", "create-batch")
	}
	out, err = readBatchAccepted(ctx, tx, o, meta)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// readBatchAccepted deliberately omits all score, question, and display data.
func readBatchAccepted(ctx context.Context, reader reportReader, o Owner, m batchMeta) (BatchAccepted, error) {
	out := BatchAccepted{BatchID: m.ID, SettingsRevision: m.Request.SettingsRevision, FirstFinishedAtMs: m.FirstFinishedAt}
	for _, t := range m.Request.Tasks {
		out.ReportIDs = append(out.ReportIDs, t.ReportID)
	}
	rows, err := reader.QueryContext(ctx, `SELECT json_extract(t.value,'$.reportId'),COALESCE(r.status,'missing'),
		COALESCE(json_extract(r.snapshot_json,'$.retry.id'),''),
		COALESCE(json_extract(r.snapshot_json,'$.settings.memoryLimitMiB'),0),
		COALESCE(json_extract(r.snapshot_json,'$.settings.totalTimeoutSeconds'),0)
		FROM hub_batches b JOIN json_each(b.request_json,'$.tasks') t
		LEFT JOIN hub_reports r ON r.report_id=json_extract(t.value,'$.reportId') AND r.batch_id=b.batch_id AND r.user_id=b.user_id AND r.workspace_id=b.workspace_id
		WHERE b.user_id=? AND b.workspace_id=? AND b.batch_id=? ORDER BY cast(t.key as integer)`, o.UserID, o.WorkspaceID, m.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var state BatchMemberState
		var memory, timeout int
		if err = rows.Scan(&state.ReportID, &state.Status, &state.RetryID, &memory, &timeout); err != nil {
			return out, err
		}
		if len(out.MemberStates) == 0 {
			out.MemoryLimitMiB, out.TotalTimeoutSeconds = memory, timeout
		}
		out.MemberStates = append(out.MemberStates, state)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	raw, err := json.Marshal(out)
	if err == nil && len(raw) > 8<<10 {
		return out, Fail(503, "批次受理摘要超过8KiB，请按批次编号查看已保存任务", "batch")
	}
	return out, err
}

const batchCanFinish = `first_finished_at_ms=0 AND json_array_length(request_json,'$.tasks') BETWEEN 1 AND 32 AND
	(SELECT count(*) FROM hub_reports r WHERE r.batch_id=hub_batches.batch_id AND r.user_id=hub_batches.user_id AND r.workspace_id=hub_batches.workspace_id)=json_array_length(request_json,'$.tasks')
	AND NOT EXISTS(SELECT 1 FROM json_each(hub_batches.request_json,'$.tasks') t
		WHERE NOT EXISTS(SELECT 1 FROM hub_reports r WHERE r.batch_id=hub_batches.batch_id AND r.user_id=hub_batches.user_id AND r.workspace_id=hub_batches.workspace_id
		AND r.report_id=json_extract(t.value,'$.reportId') AND r.status IN('done','partial','cancelled','interrupted')))`

// The parent executor must attest that no other member of this batch is held
// by an active process or unresolved recovery. SQLite also checks every frozen
// member is terminal and updates the first completion timestamp only once.
func FinalizeBatchFirstRun(ctx context.Context, tx *sql.Tx, o Owner, batchID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE hub_batches SET first_finished_at_ms=? WHERE user_id=? AND workspace_id=? AND batch_id=? AND `+batchCanFinish, Now().UnixMilli(), o.UserID, o.WorkspaceID, batchID)
	return err
}

// InterruptBatchUnstarted handles an accepted batch that the parent's FIFO
// never registered. It is not an execution or retry command.
func (s *Store) InterruptBatchUnstarted(ctx context.Context, o Owner, id string, mayFinalizeBatch bool) (BatchAccepted, error) {
	var out BatchAccepted
	err := retryWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		m, err := readBatchMeta(ctx, tx, o, id)
		if err != nil {
			return err
		}
		// A prior write may have committed even when its pipe response was
		// lost. Repeating the same recovery must be a read, not a new error.
		if m.FirstFinishedAt != 0 {
			out, err = readBatchAccepted(ctx, tx, o, m)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
		var ineligible int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hub_reports WHERE batch_id=? AND user_id=? AND workspace_id=? AND (status='running' OR json_extract(snapshot_json,'$.retry.id') IS NOT NULL)`, id, o.UserID, o.WorkspaceID).Scan(&ineligible); err != nil {
			return err
		}
		if ineligible != 0 {
			return Fail(409, "这批任务已开始执行或补测，不属于尚未开始的批次；没有取消或重复发送任何任务", "interrupt-batch")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hub_reports SET status='interrupted',updated_at_ms=?,summary_json=json_set(summary_json,'$.complete',json('false')),error_json='{"message":"整批已保存但尚未登记等待任务，本次没有发送检测请求；请查看原报告后补测","step":"create-batch","operationResult":"not_started"}' WHERE user_id=? AND workspace_id=? AND batch_id=? AND status='queued'`, Now().UnixMilli(), o.UserID, o.WorkspaceID, id); err != nil {
			return err
		}
		if mayFinalizeBatch {
			if err = FinalizeBatchFirstRun(ctx, tx, o, id); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT first_finished_at_ms FROM hub_batches WHERE batch_id=? AND user_id=? AND workspace_id=?`, id, o.UserID, o.WorkspaceID).Scan(&m.FirstFinishedAt); err != nil {
				return err
			}
		}
		out, err = readBatchAccepted(ctx, tx, o, m)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return out, err
}

type BatchStateHint struct {
	RetryID string `json:"retryId"`
	Phase   string `json:"phase"`
}
type BatchFilters struct {
	BatchID    string                    `json:"batchId"`
	Request    *BatchRequest             `json:"request,omitempty"`
	Offset     int                       `json:"offset"`
	Limit      int                       `json:"limit"`
	TargetID   string                    `json:"targetId"`
	Model      string                    `json:"model"`
	View       string                    `json:"view"`
	Status     string                    `json:"status"`
	StateHints map[string]BatchStateHint `json:"stateHints"`
}
type BatchSection struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type BatchRow struct {
	ReportID    string          `json:"reportId"`
	TargetID    string          `json:"targetId"`
	AccountID   string          `json:"accountId"`
	AccountName string          `json:"accountName"`
	GroupID     string          `json:"groupId"`
	GroupName   string          `json:"groupName"`
	Model       string          `json:"model"`
	CreatedAtMs int64           `json:"createdAtMs"`
	Status      string          `json:"status"`
	RetryID     string          `json:"retryId,omitempty"`
	Summary     json.RawMessage `json:"summary"`
	Error       json.RawMessage `json:"error"`
}
type BatchSummary struct {
	BatchID           string             `json:"batchId"`
	CreatedAtMs       int64              `json:"createdAtMs"`
	FirstFinishedAtMs int64              `json:"firstFinishedAtMs"`
	MemberStates      []BatchMemberState `json:"memberStates"`
}
type BatchDetail struct {
	BatchSummary
	Sections   []BatchSection `json:"sections"`
	Items      []BatchRow     `json:"items"`
	NextOffset *int           `json:"nextOffset"`
}

type definitionSignature struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind,omitempty"`
}

func batchEtaKey(model, protocol string, settings Settings, defs []definitionSignature) string {
	if protocol == "" || settings.Revision < 1 || len(defs) == 0 {
		return ""
	}
	if settings.Protocol == "" {
		settings.Protocol = protocol // Same fallback as original RunOne.
	}
	if settings.Validate() != nil {
		return ""
	}
	encodedSettings, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	var signedSettings map[string]json.RawMessage
	if json.Unmarshal(encodedSettings, &signedSettings) != nil {
		return ""
	}
	delete(signedSettings, "revision")
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	for i, d := range defs {
		if d.ID == "" || d.Revision < 1 || i > 0 && defs[i-1].ID == d.ID {
			return ""
		}
		d.Kind = ""
		defs[i] = d
	}
	b, err := json.Marshal(struct {
		Model       string
		Protocol    string
		Settings    map[string]json.RawMessage
		Definitions []definitionSignature
	}{model, protocol, signedSettings, defs})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func selectedBatchPhase(state BatchMemberState, hints map[string]BatchStateHint) string {
	hint, exists := hints[state.ReportID]
	if !exists {
		// An unregistered batch may have one batch-wide recovery intent
		// rather than individual blocked members.
		hint, exists = hints["*"]
	}
	if exists && hint.RetryID != state.RetryID {
		return "unknown"
	}
	if state.Status == "missing" {
		return "unknown"
	}
	if exists {
		switch hint.Phase {
		case "preparing", "running", "stopping", "saving_end", "saving_cancel", "paused", "unknown":
			return hint.Phase
		case "queued":
			if state.Status == "queued" {
				return "queued"
			}
			return "unknown"
		}
	}
	if terminalStatus(state.Status) {
		return "ended"
	}
	return "unknown"
}

func (s *Store) ReadBatch(ctx context.Context, o Owner, f BatchFilters) (BatchDetail, error) {
	var out BatchDetail
	limit := f.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 || f.Offset < 0 || f.Offset > 32 || (f.View != "" && f.View != "progress" && f.View != "results") {
		return out, Fail(422, "批次分页或展示方式无效，请从第一页重新读取", "batch")
	}
	switch f.Status {
	case "", "queued", "preparing", "running", "stopping", "saving_end", "saving_cancel", "paused", "unknown", "ended":
	default:
		return out, Fail(422, "批次状态筛选无效，请重新选择", "batch")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	m, err := readBatchMeta(ctx, tx, o, f.BatchID)
	if err != nil {
		return out, err
	}
	out.BatchSummary = BatchSummary{m.ID, m.CreatedAt, m.FirstFinishedAt, []BatchMemberState{}}
	out.Sections, out.Items = []BatchSection{}, []BatchRow{}
	// Only projected metadata is read. Neither the question bodies nor the
	// nested answer/evidence/knowledge rows of a report cross this SQL boundary.
	projection := `'{}'`
	if f.View == "results" {
		projection = batchResultProjection
	}
	rows, err := tx.QueryContext(ctx, `SELECT cast(t.key AS integer),json_extract(t.value,'$.reportId'),
		COALESCE(r.target_id,json_extract(t.value,'$.targetId')),COALESCE(r.account_id,''),substr(COALESCE(r.account_name,''),1,96),
		COALESCE(r.group_id,json_extract(t.value,'$.groupId')),substr(COALESCE(r.group_name,''),1,96),
		COALESCE(r.model,json_extract(t.value,'$.model')),COALESCE(r.created_at_ms,b.created_at_ms),COALESCE(r.status,'missing'),
		COALESCE(json_extract(r.snapshot_json,'$.retry.id'),''),COALESCE(json_extract(r.snapshot_json,'$.retry.acceptedAtMs'),0),
		COALESCE(json_extract(r.snapshot_json,'$.settings'),'{}'),COALESCE(r.protocol,''),
		(SELECT json_group_array(json_object('id',json_extract(value,'$.definition.id'),'revision',json_extract(value,'$.definition.revision'),'kind',json_extract(value,'$.definition.kind'))) FROM json_each(r.snapshot_json,'$.questions')),
		`+batchSummaryComplete+`,
		`+batchTimingProjection+`,`+projection+`,
		json_object('message',substr(json_extract(r.error_json,'$.message'),1,256),'step',substr(json_extract(r.error_json,'$.step'),1,64),'operationResult',substr(json_extract(r.error_json,'$.operationResult'),1,64))
		FROM hub_batches b JOIN json_each(b.request_json,'$.tasks') t
		LEFT JOIN hub_reports r ON r.report_id=json_extract(t.value,'$.reportId') AND r.batch_id=b.batch_id AND r.user_id=b.user_id AND r.workspace_id=b.workspace_id
		WHERE b.user_id=? AND b.workspace_id=? AND b.batch_id=? ORDER BY cast(t.key AS integer)`, o.UserID, o.WorkspaceID, m.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	pageBytes := 2 // JSON array brackets
	for rows.Next() {
		var index int
		var row BatchRow
		var state BatchMemberState
		var settingsRaw, signatureRaw, timingRaw, summaryRaw, errorRaw, protocol string
		if err = rows.Scan(&index, &row.ReportID, &row.TargetID, &row.AccountID, &row.AccountName, &row.GroupID, &row.GroupName, &row.Model, &row.CreatedAtMs, &row.Status,
			&row.RetryID, &state.RetryAcceptedAtMs, &settingsRaw, &protocol, &signatureRaw, &state.SummaryComplete, &timingRaw, &summaryRaw, &errorRaw); err != nil {
			return out, err
		}
		state.ReportID, state.Status, state.RetryID = row.ReportID, row.Status, row.RetryID
		var settings Settings
		var defs []definitionSignature
		if err = json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(signatureRaw), &defs); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(timingRaw), &state.Timing); err != nil {
			return out, err
		}
		state.TotalTimeoutSeconds = settings.TotalTimeoutSeconds
		if len(out.Sections) == 0 && len(defs) > 0 {
			counts := map[string]int{}
			for _, d := range defs {
				counts[d.Kind]++
			}
			for _, id := range []string{"iq", "vision", "knowledge", "tools", "cache", "injection", "web", "thinking", "fingerprint_numeric", "fingerprint_behavior"} {
				if n := counts[id]; n > 0 {
					out.Sections = append(out.Sections, BatchSection{id, batchSectionNames[id], n})
				}
			}
		}
		state.EtaKey = batchEtaKey(row.Model, protocol, settings, defs)
		compact, err := json.Marshal(state)
		if err != nil {
			return out, err
		}
		if len(compact) > 512 {
			return out, Fail(503, "批次中有一份报告的状态摘要超过512字节，请按报告编号核查原记录", "batch")
		}
		out.MemberStates = append(out.MemberStates, state)
		if out.NextOffset != nil || index < f.Offset {
			continue
		}
		if f.TargetID != "" && f.TargetID != row.TargetID || f.Model != "" && f.Model != row.Model ||
			f.Status != "" && f.Status != selectedBatchPhase(state, f.StateHints) {
			continue
		}
		row.Summary, row.Error = json.RawMessage(summaryRaw), json.RawMessage(errorRaw)
		raw, err := json.Marshal(row)
		if err != nil {
			return out, err
		}
		if len(raw)+3 > 36<<10 {
			return out, Fail(503, "批次中有一份报告的摘要超过36KiB，请按报告编号查看完整报告；已有结果未删除", "batch")
		}
		if len(out.Items) == limit || pageBytes+len(raw)+1 > 36<<10 {
			next := index // This matching row has not been consumed.
			out.NextOffset = &next
			continue
		}
		out.Items = append(out.Items, row)
		pageBytes += len(raw) + 1
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	base := out
	base.Items = []BatchRow{}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return out, err
	}
	if len(baseJSON) > 24<<10 || len(baseJSON)+pageBytes+(4<<10) > 64<<10 {
		return out, Fail(503, "批次状态基础摘要或当前页面超过读取上限，请按报告编号查看已保存结果", "batch")
	}
	return out, tx.Commit()
}

const batchSummaryComplete = `CASE WHEN json_type(r.summary_json,'$.complete')='true' AND COALESCE(json_type(r.summary_json,'$.total.complete'),'null')!='false' THEN 1 ELSE 0 END`
const batchTimingProjection = `CASE WHEN json_type(r.summary_json,'$.verdict.timing')='object' THEN json_object(
	'started_at',substr(json_extract(r.summary_json,'$.verdict.timing.started_at'),1,64),
	'finished_at',substr(json_extract(r.summary_json,'$.verdict.timing.finished_at'),1,64),
	'total_ms',json_extract(r.summary_json,'$.verdict.timing.total_ms'),
	'last_run_ms',json_extract(r.summary_json,'$.verdict.timing.last_run_ms'),
	'runs',json_extract(r.summary_json,'$.verdict.timing.runs')) ELSE 'null' END`
const batchResultProjection = `json_object(
	'iqScore',json_extract(r.summary_json,'$.iqScore'),'iqMax',json_extract(r.summary_json,'$.iqMax'),'totalScore',json_extract(r.summary_json,'$.totalScore'),
	'total',CASE WHEN json_type(r.summary_json,'$.total')='object' THEN json_object(
		'complete',r.summary_json->'$.total.complete','reason',substr(json_extract(r.summary_json,'$.total.reason'),1,256),
		'parts',CASE WHEN json_type(r.summary_json,'$.total.parts')='array' THEN
			(SELECT json_group_array(json_object('key',json_extract(value,'$.key'),'applicable',value->'$.applicable',
			'known',value->'$.known','points',json_extract(value,'$.points'),'max_points',json_extract(value,'$.max_points'),
			'contribution',json_extract(value,'$.contribution'))) FROM json_each(r.summary_json,'$.total.parts')) ELSE NULL END
	) ELSE NULL END,
	'knowledge',CASE WHEN json_type(r.summary_json,'$.knowledge')='object' THEN json_object(
		'status',substr(json_extract(r.summary_json,'$.knowledge.status'),1,64),'label',substr(json_extract(r.summary_json,'$.knowledge.label'),1,96),
		'correct',json_extract(r.summary_json,'$.knowledge.correct'),'total',json_extract(r.summary_json,'$.knowledge.total')) ELSE NULL END
)`

// Listing uses the original Singapore date and cursor semantics, but reads
// only lifecycle fields. The parent computes counts from these same members.
func (s *Store) ListBatches(ctx context.Context, o Owner, f Filters) (Page[BatchSummary], error) {
	out := Page[BatchSummary]{Items: []BatchSummary{}}
	if err := o.Validate(); err != nil {
		return out, err
	}
	c, err := decodeCursor(f.Cursor)
	if err != nil {
		return out, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return out, Fail(422, "每页批次数必须为1～50", "pagination")
	}
	from, to, err := historyRange(f)
	if err != nil {
		return out, err
	}
	q := `WITH selected AS (SELECT batch_id,user_id,workspace_id,created_at_ms,first_finished_at_ms,request_json FROM hub_batches
		WHERE user_id=? AND workspace_id=? AND created_at_ms>=? AND created_at_ms<?`
	args := []any{o.UserID, o.WorkspaceID, from, to}
	if f.Cursor != "" {
		q += ` AND (created_at_ms<? OR (created_at_ms=? AND batch_id<?))`
		args = append(args, c.N, c.N, c.ID)
	}
	q += ` ORDER BY created_at_ms DESC,batch_id DESC LIMIT ?)
		SELECT b.batch_id,b.created_at_ms,b.first_finished_at_ms,json_extract(t.value,'$.reportId'),COALESCE(r.status,'missing'),
		COALESCE(json_extract(r.snapshot_json,'$.retry.id'),''),` + batchSummaryComplete + `
		FROM selected b JOIN json_each(b.request_json,'$.tasks') t
		LEFT JOIN hub_reports r ON r.report_id=json_extract(t.value,'$.reportId') AND r.batch_id=b.batch_id AND r.user_id=b.user_id AND r.workspace_id=b.workspace_id
		ORDER BY b.created_at_ms DESC,b.batch_id DESC,cast(t.key as integer)`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var current BatchSummary
	bytesUsed := len(`{"items":[],"nextCursor":""}`)
	appendCurrent := func() (bool, error) {
		if current.BatchID == "" {
			return true, nil
		}
		if len(current.MemberStates) < 1 || len(current.MemberStates) > 32 {
			return false, Fail(503, "批次任务数量不正确，请核查检测数据库；已有结果未删除", "batch")
		}
		raw, err := json.Marshal(current)
		if err != nil {
			return false, err
		}
		if len(raw)+len(`{"items":[],"nextCursor":""}`)+2048 > 64<<10 {
			return false, Fail(503, "这条批次摘要超过读取上限，请按批次编号查看；已有结果未删除", "batch")
		}
		if len(out.Items) == limit || bytesUsed+len(raw)+2048 > 64<<10 {
			last := out.Items[len(out.Items)-1]
			out.NextCursor = encodeCursor(last.CreatedAtMs, last.BatchID)
			return false, nil
		}
		out.Items = append(out.Items, current)
		bytesUsed += len(raw) + 1
		return true, nil
	}
	for rows.Next() {
		var b BatchSummary
		var member BatchMemberState
		if err = rows.Scan(&b.BatchID, &b.CreatedAtMs, &b.FirstFinishedAtMs, &member.ReportID, &member.Status, &member.RetryID, &member.SummaryComplete); err != nil {
			return out, err
		}
		if b.BatchID != current.BatchID {
			ok, err := appendCurrent()
			if err != nil || !ok {
				return out, err
			}
			current = b
		}
		raw, err := json.Marshal(member)
		if err != nil {
			return out, err
		}
		if len(raw) > 512 {
			return out, Fail(503, "批次中一份报告状态摘要超过512字节，请按报告编号核查原记录", "batch")
		}
		current.MemberStates = append(current.MemberStates, member)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	_, err = appendCurrent()
	return out, err
}
