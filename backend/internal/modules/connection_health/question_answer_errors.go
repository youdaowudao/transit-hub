package connection_health

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/httpjson"
)

var (
	questionAnswerPrivateURL           = regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|redis)://[^\s<>"']+`)
	questionAnswerPrivateAssignment    = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|token|secret|api[_-]?key|authorization|dsn|connectionstring)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	questionAnswerPrivateBearer        = regexp.MustCompile(`(?i)(?:Bearer|Basic)\s+[^\s,;"'<>]+`)
	questionAnswerSQLQuoted            = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	questionAnswerAuthenticationHeader = regexp.MustCompile(`(?i)\b(?:proxy[-_]?authorization|authorization|x[-_]?api[-_]?key|api[-_]?key|set-cookie|cookie)\b`)
)

// No URL wrapper, SQL Detail, SQL text, parameters or connection strings are
// allowed into the UI. The same function is used by runtime and API failures.
func questionAnswerSafeCause(err error, keys ...string) string {
	if err == nil {
		return ""
	}
	var credentialError *upstream.ProbeCredentialError
	if errors.As(err, &credentialError) {
		return questionAnswerKnownOperationCause(reasonToErrorKey(credentialError.Reason))
	}
	var upstreamError *upstream.RequestError
	if errors.As(err, &upstreamError) {
		if key := upstream.KnownUpstreamCodeErrorKey(upstreamError.UpstreamCode); key != "" {
			return questionAnswerKnownOperationCause(key)
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := []string{}
		for _, child := range joined.Unwrap() {
			if _, isKey := child.(requestError); isKey {
				continue
			}
			if part := questionAnswerSafeCause(child, keys...); part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			return questionAnswerUpstreamExcerpt([]byte(strings.Join(parts, "；")), "")
		}
	}
	for {
		var wrapped *url.Error
		if !errors.As(err, &wrapped) || wrapped.Err == nil {
			break
		}
		err = wrapped.Err
	}
	text := err.Error()
	if known := questionAnswerKnownOperationCause(text); known != "" {
		return known
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		text = "SQLSTATE " + pgErr.Code + "：" + questionAnswerSQLQuoted.ReplaceAllString(pgErr.Message, "***")
	}
	// A header error may contain an unrelated credential and partially escaped
	// quotes, so token-by-token replacement cannot prove the remainder safe.
	// Discard the entire header-bearing explanation before other replacements.
	decoded := text
	for layer := 0; layer < 8; layer++ {
		if questionAnswerAuthenticationHeader.MatchString(decoded) {
			return fmt.Sprintf("%T：错误中包含认证头或Cookie，完整内容已隐藏；请按批次和记录编号核查后台日志", err)
		}
		next := questionAnswerDecodeJSONEscapes(decoded)
		if next == decoded {
			break
		}
		decoded = next
	}
	if decoded != questionAnswerDecodeJSONEscapes(decoded) {
		return fmt.Sprintf("%T：错误文本含多层转义，无法确认安全，已隐藏；请按批次和记录编号核查后台日志", err)
	}
	// Apply every replacement to the same fully decoded text. Otherwise an
	// escaped credential label/value could bypass a successful plain-text mask.
	text = decoded
	if strings.HasPrefix(strings.TrimSpace(text), "admin.") || strings.HasPrefix(strings.TrimSpace(text), "auth.") {
		if _, isKey := err.(requestError); !isKey {
			return "准备批次时失败，接口只提供了错误分类，未提供更具体原因；请核查后台日志"
		}
	}
	if pgErr != nil {
		text = questionAnswerSQLQuoted.ReplaceAllString(text, "***")
	} else {
		lower := strings.ToLower(text)
		for _, private := range []string{"select ", "insert ", "update ", "delete ", "sql=", "parameters:", "arguments:", "postgres://", "postgresql://", "host=", "dbname="} {
			if strings.Contains(lower, private) {
				return fmt.Sprintf("存储操作返回 %T；具体原因含连接或查询信息，已隐藏，请按批次和记录编号核查后台日志", err)
			}
		}
	}
	text = questionAnswerPrivateURL.ReplaceAllString(text, "[连接地址已隐藏]")
	text = questionAnswerPrivateAssignment.ReplaceAllString(text, "[认证信息已隐藏]")
	text = questionAnswerPrivateBearer.ReplaceAllString(text, "[认证信息已隐藏]")
	for _, key := range keys {
		if key != "" {
			text = questionAnswerUpstreamExcerpt([]byte(text), key)
		}
	}
	return questionAnswerUpstreamExcerpt([]byte(text), "")
}

type QuestionAnswerOperationError struct {
	Err             error
	Step            string
	Cause           string
	OperationResult string
	BatchID         string
}

func (err *QuestionAnswerOperationError) Error() string { return err.Err.Error() }
func (err *QuestionAnswerOperationError) Unwrap() error { return err.Err }

func questionAnswerKnownOperationCause(key string) string {
	return map[string]string{
		ErrorRequest:                              "请求内容无法识别或字段无效，请检查输入后重试",
		ErrorNoCurrentAccount:                     "尚未选择可用工作区，请先选择工作区再操作",
		ErrorQuestionAnswerActive:                 "这个账号已有正在进行的答题批次，请查看最近批次",
		ErrorQuestionAnswerSelection:              "请先选择模型和题目",
		ErrorQuestionAnswerReasoningEffort:        "思考力度无效，请重新选择",
		ErrorQuestionAnswerRepeatCount:            "重复次数无效，请在1到10之间重新选择",
		ErrorQuestionAnswerBatchLimit:             "本批次超过50条限制，请减少模型、题目或重复次数",
		ErrorQuestionAnswerBatchNotFound:          "没有找到这个账号的指定批次，请核对账号和批次编号",
		ErrorQuestionAnswerHistoryPage:            "历史页码必须是大于0的一个整数，请重新选择历史页码",
		ErrorQuestionAnswerHistoryScope:           "历史范围只能选择今日或全部，请重新选择",
		ErrorQuestionAnswerStorage:                "答题记录操作未能取得确认，接口未提供更具体原因，请核查后台日志",
		ErrorQuestionAnswerJudgmentConflict:       "这条记录已被其他操作修改，请重新读取后再判断",
		ErrorQuestionAnswerJudgmentForbidden:      "这条记录尚未成功回答，不能保存人工判断",
		ErrorQuestionAnswerServiceStopped:         "答题服务正在停止，暂时不能接受新的操作",
		ErrorTestQuestionInvalid:                  "题目名称或正文无效，请检查题目后重新选择",
		ErrorTestQuestionNotFound:                 "所选题目已经不存在，请重新读取题目列表",
		ErrorTestQuestionDisabled:                 "所选题目已停用，请重新选择题目",
		ErrorModelUnavailable:                     "所选模型当前不可用，请重新读取模型列表",
		ErrorProbeTargetNotFound:                  "当前工作区没有这个账号，请重新选择工作区和账号",
		ErrorAccountsFetch:                        "读取账号列表时失败，尚未确认这个账号的当前信息，请刷新分组和账号列表后重新读取",
		"admin.mySites.errors.authRequired":       "当前工作区的上游登录已经失效或尚未登录，请先到工作区账号页面重新登录，再重新读取",
		"admin.mySites.errors.adminOnly":          "当前工作区的上游登录没有管理员身份，请在工作区账号页面检查登录身份后重新读取",
		ErrorCredentialUnavailable:                "无法安全取得这个账号的答题凭据，请在账号对应的上游页面检查凭据后重新读取",
		ErrorSecureVerificationRequired:           "读取这个账号凭据需要额外安全验证，请先在账号对应的上游页面完成验证，再重新读取",
		ErrorBaseURLUnavailable:                   "这个账号缺少可用的接口地址，请在账号对应的上游页面补全地址后重新读取",
		ErrorExportUnavailable:                    "无法取得这个账号的凭据导出内容，请在账号对应的上游页面检查导出功能后重新读取",
		ErrorCredentialsRedacted:                  "上游返回的账号凭据已被隐藏，无法用它答题，请在账号对应的上游页面检查凭据导出后重新读取",
		ErrorModelListUnavailable:                 "无法读取这个账号的模型列表，接口没有提供更具体的链路原因，请核查该账号上游同一时刻的模型列表请求后重新读取",
		ErrorModelListInvalid:                     "这个账号的模型列表不是可识别的格式，请核查上游模型列表返回内容后重新读取",
		ErrorTestProtocolNotAllowed:               "这个账号的测试请求协议不支持，请到分组设置选择支持的协议后重试",
		ErrorTestConfigurationConflict:            "这个账号所属分组的测试协议或超时设置互相冲突，请到分组设置统一配置后重新读取",
		ErrorTestConfigurationUnavailable:         "无法确认这个账号的完整分组或测试配置，请先刷新分组列表，检查分组设置后重新读取",
		upstream.ErrorAnnouncementAckRequired:     "上游明确要求先确认公告，请先在账号对应的上游网页确认新公告，再重新读取",
		upstream.ErrorUpstreamInsufficientBalance: "上游明确报告余额不足，请在账号对应的上游页面检查余额并处理后重新读取",
		upstream.ErrorUpstreamKeyQuotaExhausted:   "上游明确报告这个Key的额度已用完，请在账号对应的上游页面检查额度并处理后重新读取",
		upstream.ErrorUpstreamKeyExpired:          "上游明确报告这个Key已过期，请在账号对应的上游页面检查并更换有效凭据后重新读取",
	}[key]
}

func questionAnswerOperationError(err error, step, result, batchID string, keys ...string) error {
	if err == nil {
		return nil
	}
	var existing *QuestionAnswerOperationError
	if errors.As(err, &existing) {
		return err
	}
	return &QuestionAnswerOperationError{Err: err, Step: step, Cause: questionAnswerSafeCause(err, keys...), OperationResult: result, BatchID: batchID}
}

func questionAnswerOperationDetail(err *QuestionAnswerOperationError) string {
	action := map[string]string{"preparing": "准备答题批次", "creating": "创建答题批次", "reading": "读取答题记录", "history": "读取答题历史", "cancelling": "停止答题并确认结束结果", "judging": "保存这题的人工判断", "queue": "读取排队信息"}[err.Step]
	if action == "" {
		action = "处理答题操作"
	}
	cause := err.Cause
	var key requestError
	if errors.As(err.Err, &key) {
		// A key alone contains no direct cause. Keep the original key in message
		// and explain this limitation without translating other modules here.
		if cause == key.Error() {
			cause = questionAnswerKnownOperationCause(key.Error())
			if cause == "" {
				cause = "接口未提供更具体原因，请结合页面提示和批次编号核查后台日志"
			}
		}
	}
	text := action + "失败：" + cause + "。"
	if err.BatchID != "" {
		text += "影响批次 #" + err.BatchID + "。"
	}
	if err.Step == "reading" || err.Step == "history" || err.Step == "queue" {
		return text + "这次只读取状态，没有重新发题；可以重新读取。"
	}
	if err.OperationResult == "not_started" {
		if err.Step == "judging" {
			return text + "本次没有保存或修改人工判断，请处理上述原因后再操作。"
		}
		if err.Step == "cancelling" {
			return text + "本次没有执行停止操作，请处理上述原因后再操作。"
		}
		return text + "本次尚未创建题目；处理上述原因后可重新操作。"
	}
	if err.Step == "judging" {
		return text + "目前无法确认人工判断是否已保存，请先重新读取这条记录，再决定是否重试。"
	}
	if err.Step == "cancelling" {
		return text + "目前无法确认结束结果，请先重新读取该批次，不要把本次操作当作已完成。"
	}
	return text + "目前无法确认后台是否已接收，请先查看账号最近批次和时间核对，本次不会自动重发。"
}

// Reuse writeError's exact status/key mapping, while limiting the extra error
// contract to question-answer handlers. No global error behavior is changed.
func writeQuestionAnswerError(w http.ResponseWriter, err error, step, result, batchID string) {
	wrapped := questionAnswerOperationError(err, step, result, batchID)
	var operation *QuestionAnswerOperationError
	_ = errors.As(wrapped, &operation)
	recorder := httptest.NewRecorder()
	writeError(recorder, operation.Err)
	// writeError always writes a JSON message. Preserve its original body fields
	// for specialized conflicts as well as the ordinary status and error key.
	var payload map[string]any
	if jsonErr := json.Unmarshal(recorder.Body.Bytes(), &payload); jsonErr != nil {
		payload = map[string]any{"message": ErrorUnknown}
	}
	payload["detail"] = questionAnswerOperationDetail(operation)
	payload["step"], payload["operationResult"] = operation.Step, operation.OperationResult
	if operation.BatchID != "" {
		payload["batchId"] = operation.BatchID
	}
	httpjson.Write(w, recorder.Code, payload)
}
