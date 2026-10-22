package connection_health

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	responsesQuestionAnswerStreamLimit    = 16 << 20
	responsesQuestionAnswerDeadlinePrefix = "本地等待到期；以下为此前收到的信息。"
)

// Evidence is local to one response. Deltas, reasoning and response headers are
// deliberately never retained here.
type responsesQuestionAnswerEvidence struct {
	message, code, responseID, lastEvent, status, reason string
}

func readResponsesQuestionAnswer(ctx context.Context, body io.Reader, key string, result QuestionAnswerAskResult) QuestionAnswerAskResult {
	if result.UpstreamStatus < 200 || result.UpstreamStatus >= 300 {
		return readResponsesQuestionAnswerJSON(ctx, body, 0, key, result, false)
	}
	reader := bufio.NewReader(body)
	prefixBytes := 0
	leadingLineBytes, leadingCR := 0, false
	oversizedLeadingLine := false
	// Leading whitespace/BOM is ignored for detection, but is charged to the
	// selected format's byte budget without retaining an unbounded prefix.
	if prefix, _ := reader.Peek(3); string(prefix) == "\xef\xbb\xbf" {
		_, _ = reader.Discard(3)
		prefixBytes = 3
		leadingLineBytes = 3
	}
	for {
		first, err := reader.ReadByte()
		if err != nil {
			if err != io.EOF {
				return responsesQuestionAnswerReadFailure(ctx, key, result, responsesQuestionAnswerEvidence{})
			}
			if prefixBytes > maxProbeResponseBytes {
				return responsesQuestionAnswerSizeFailure(ctx, key, result, responsesQuestionAnswerEvidence{}, "完整JSON或无法识别的正文超过1MiB")
			}
			return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, responsesQuestionAnswerEvidence{}, "上游返回中没有可判题的完整正文，本题不判对错。可稍后重测。")
		}
		prefixBytes++
		if prefixBytes > responsesQuestionAnswerStreamLimit {
			return responsesQuestionAnswerSizeFailure(ctx, key, result, responsesQuestionAnswerEvidence{}, "整条响应超过16MiB")
		}
		if first == ' ' || first == '\t' || first == '\r' || first == '\n' || first == '\v' || first == '\f' {
			if first == '\n' {
				lineBytes := leadingLineBytes
				if leadingCR {
					lineBytes--
				}
				oversizedLeadingLine = oversizedLeadingLine || lineBytes > maxProbeResponseBytes
				leadingLineBytes = 0
			} else {
				leadingLineBytes++
			}
			leadingCR = first == '\r'
			continue
		}
		_ = reader.UnreadByte()
		prefixBytes--
		if first == '{' {
			return readResponsesQuestionAnswerJSON(ctx, reader, prefixBytes, key, result, true)
		}
		isSSE := first == ':'
		for _, field := range []string{"event:", "data:", "id:", "retry:"} {
			if first == field[0] {
				prefix, _ := reader.Peek(len(field))
				isSSE = string(prefix) == field
				break
			}
		}
		if isSSE {
			if oversizedLeadingLine || leadingLineBytes > maxProbeResponseBytes {
				return responsesQuestionAnswerSizeFailure(ctx, key, result, responsesQuestionAnswerEvidence{}, "单行超过1MiB")
			}
			return readResponsesQuestionAnswerStream(ctx, reader, prefixBytes, leadingLineBytes, key, result)
		}
		return readResponsesQuestionAnswerJSON(ctx, reader, prefixBytes, key, result, false)
	}
}

func readResponsesQuestionAnswerJSON(ctx context.Context, body io.Reader, prefixBytes int, key string, result QuestionAnswerAskResult, allowSuccess bool) QuestionAnswerAskResult {
	if prefixBytes > maxProbeResponseBytes {
		return responsesQuestionAnswerSizeFailure(ctx, key, result, responsesQuestionAnswerEvidence{}, "完整JSON超过1MiB")
	}
	data, err := io.ReadAll(io.LimitReader(body, int64(maxProbeResponseBytes-prefixBytes+1)))
	if len(data)+prefixBytes > maxProbeResponseBytes {
		return responsesQuestionAnswerSizeFailure(ctx, key, result, responsesQuestionAnswerEvidence{}, "完整JSON或非流式响应超过1MiB")
	}
	if err != nil {
		return responsesQuestionAnswerReadFailure(ctx, key, result, responsesQuestionAnswerEvidence{})
	}
	var object map[string]any
	parseErr := json.Unmarshal(data, &object)
	parsed := parseErr == nil && object != nil
	if parseErr != nil {
		result.failureStep, result.localError = "parse", parseErr
	}
	evidence := responsesQuestionAnswerEvidence{}
	if parsed {
		evidence = responsesQuestionAnswerExtractEvidence(object, key, true, evidence)
	}
	if result.UpstreamStatus < 200 || result.UpstreamStatus >= 300 {
		outcome := classifyTestHTTPResponse(TestProtocolResponses, result.UpstreamStatus, data, key, 0)
		errorType := questionAnswerHTTPError(result.UpstreamStatus)
		switch outcome.Result {
		case ResultInvalidResponse:
			errorType = QuestionAnswerErrorInvalidResponse
		case ResultModelNotFound:
			errorType = QuestionAnswerErrorModelNotFound
		case ResultRateLimited:
			errorType = QuestionAnswerErrorRateLimited
		}
		if !parsed {
			// Preserve the old safe prefix only for non-JSON HTTP errors.
			trimmed := strings.TrimLeft(string(data), " \t\r\n\v\f")
			trimmed = strings.TrimLeft(strings.TrimPrefix(trimmed, "\xef\xbb\xbf"), " \t\r\n\v\f")
			looksLikeSSE := false
			for _, prefix := range []string{":", "event:", "data:", "id:", "retry:"} {
				looksLikeSSE = looksLikeSSE || strings.HasPrefix(trimmed, prefix)
			}
			if looksLikeSSE {
				// HTTP failures retain their original 1MiB read limit and HTTP
				// classification. Reuse the event parser only for safe details.
				failure := readResponsesQuestionAnswerStream(ctx, bufio.NewReader(strings.NewReader(trimmed)), len(data)-len(trimmed), 0, key, result)
				failure.ErrorType = errorType
				return failure
			}
			if !looksLikeSSE && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
				result.ErrorType = errorType
				result.UpstreamExcerpt = questionAnswerUpstreamExcerpt(data, key)
				return result
			}
		}
		return responsesQuestionAnswerFailure(ctx, key, result, errorType, evidence, "")
	}
	if allowSuccess && parsed {
		if answer, ok := decodeTestResponse(TestProtocolResponses, data, true); ok {
			return QuestionAnswerAskResult{Answer: answer}
		}
		details, _ := object["incomplete_details"].(map[string]any)
		if object["status"] == "incomplete" && details["reason"] == "max_output_tokens" {
			return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorResponseIncomplete, evidence, "")
		}
	}
	return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, evidence, "")
}

func readResponsesQuestionAnswerStream(ctx context.Context, reader *bufio.Reader, total, firstLineBytes int, key string, result QuestionAnswerAskResult) QuestionAnswerAskResult {
	evidence := responsesQuestionAnswerEvidence{}
	var data strings.Builder
	eventName := ""
	dataPresent := false
	consume := func() (QuestionAnswerAskResult, bool) {
		name, payload := eventName, data.String()
		eventName = ""
		data.Reset()
		present := dataPresent
		dataPresent = false
		if !present {
			return result, false
		}
		if payload == "[DONE]" {
			return responsesQuestionAnswerReadFailure(ctx, key, result, evidence), true
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(payload), &object); err != nil || object == nil {
			result.failureStep, result.localError = "parse", err
			return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, evidence, "接收回答时收到无法解析的事件，本题不判对错。可稍后重测，重复出现请上游核查返回内容。"), true
		}
		typeName, _ := object["type"].(string)
		if rawType, exists := object["type"]; exists && rawType != nil {
			if _, ok := rawType.(string); !ok {
				return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, evidence, "接收回答时收到不符合格式的事件类型，本题不判对错。可稍后重测。"), true
			}
		}
		if typeName != "" && name != "" && typeName != name {
			return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, evidence, "接收回答时事件名称互相矛盾，无法确认完整结果，本题不判对错。可稍后重测。"), true
		}
		if typeName == "" {
			typeName = name
		}
		response, _ := object["response"].(map[string]any)
		hasError := typeName == "error" || typeName == "response.failed" || responsesQuestionAnswerHasError(object["error"]) || responsesQuestionAnswerHasError(response["error"])
		evidence.lastEvent = responsesQuestionAnswerEventName(typeName, key)
		evidence = responsesQuestionAnswerExtractEvidence(object, key, false, evidence)
		if !hasError && typeName != "response.incomplete" && typeName != "response.completed" {
			// Only failure/terminal payloads may supply explanatory text.
			evidence.message, evidence.code, evidence.reason = "", "", ""
		}
		if hasError {
			errorType := QuestionAnswerErrorServer
			if responsesQuestionAnswerRateLimited(object, typeName) {
				errorType = QuestionAnswerErrorRateLimited
			}
			return responsesQuestionAnswerFailure(ctx, key, result, errorType, evidence, ""), true
		}
		switch typeName {
		case "response.incomplete":
			errorType := QuestionAnswerErrorInvalidResponse
			details, _ := response["incomplete_details"].(map[string]any)
			if details["reason"] == "max_output_tokens" {
				errorType = QuestionAnswerErrorResponseIncomplete
			}
			return responsesQuestionAnswerFailure(ctx, key, result, errorType, evidence, ""), true
		case "response.completed":
			if result.UpstreamStatus < 200 || result.UpstreamStatus >= 300 {
				return responsesQuestionAnswerFailure(ctx, key, result, questionAnswerHTTPError(result.UpstreamStatus), evidence, ""), true
			}
			snapshot, err := json.Marshal(response)
			if err == nil {
				if answer, ok := decodeTestResponse(TestProtocolResponses, snapshot, true); ok {
					return QuestionAnswerAskResult{Answer: answer}, true
				}
			}
			return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorInvalidResponse, evidence, ""), true
		}
		return result, false
	}
	for {
		line := []byte{}
		var readErr error
		for {
			part, err := reader.ReadSlice('\n')
			total += len(part)
			if total > responsesQuestionAnswerStreamLimit {
				return responsesQuestionAnswerSizeFailure(ctx, key, result, evidence, "整条响应超过16MiB")
			}
			if len(line)+len(part) > maxProbeResponseBytes+2 {
				return responsesQuestionAnswerSizeFailure(ctx, key, result, evidence, "单行超过1MiB")
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			readErr = err
			break
		}
		// A read error never turns the unterminated residual frame into a
		// completed answer. EOF alone is a normal end-of-frame opportunity.
		if readErr != nil && readErr != io.EOF {
			return responsesQuestionAnswerReadFailure(ctx, key, result, evidence)
		}
		line = stringsTrimLineEnding(line)
		if len(line)+firstLineBytes > maxProbeResponseBytes {
			return responsesQuestionAnswerSizeFailure(ctx, key, result, evidence, "单行超过1MiB")
		}
		firstLineBytes = 0
		text := string(line)
		if text == "" {
			if out, done := consume(); done {
				return out
			}
		} else if !strings.HasPrefix(text, ":") {
			field, value, _ := strings.Cut(text, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				eventName = value
			case "data":
				additional := len(value)
				if dataPresent {
					additional++
				}
				if data.Len()+additional > maxProbeResponseBytes {
					return responsesQuestionAnswerSizeFailure(ctx, key, result, evidence, "单个事件的data合计超过1MiB")
				}
				if dataPresent {
					data.WriteByte('\n')
				}
				data.WriteString(value)
				dataPresent = true
			}
		}
		if readErr == io.EOF {
			if out, done := consume(); done {
				return out
			}
			return responsesQuestionAnswerReadFailure(ctx, key, result, evidence)
		}
	}
}

func stringsTrimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	return line
}

func responsesQuestionAnswerExtractEvidence(object map[string]any, key string, fullJSON bool, previous responsesQuestionAnswerEvidence) responsesQuestionAnswerEvidence {
	response, _ := object["response"].(map[string]any)
	metadataSource := response
	if fullJSON {
		metadataSource = object
	}
	if id, _ := metadataSource["id"].(string); responsesQuestionAnswerValidID(id) && questionAnswerUpstreamExcerpt([]byte(id), key) == id {
		previous.responseID = id
	}
	previous.message, previous.code, previous.reason, previous.status = "", "", "", ""
	errorObject, _ := object["error"].(map[string]any)
	responseError, _ := response["error"].(map[string]any)
	for _, candidate := range []any{errorObject["message"], responseError["message"], object["message"], object["error"], response["error"]} {
		if text, ok := candidate.(string); ok {
			if safe := questionAnswerUpstreamExcerpt([]byte(text), key); safe != "" {
				previous.message = safe
				break
			}
		}
	}
	for _, candidate := range []any{errorObject["code"], errorObject["type"], responseError["code"], responseError["type"], object["code"]} {
		if code, ok := candidate.(string); ok && responsesQuestionAnswerValidCode(code) && questionAnswerUpstreamExcerpt([]byte(code), key) == code {
			previous.code = code
			break
		}
	}
	details, _ := metadataSource["incomplete_details"].(map[string]any)
	if reason, ok := details["reason"].(string); ok {
		previous.reason = questionAnswerUpstreamExcerpt([]byte(reason), key)
	}
	if status, _ := metadataSource["status"].(string); questionAnswerUpstreamExcerpt([]byte(status), key) == status {
		switch status {
		case "completed", "failed", "incomplete", "cancelled", "queued", "in_progress":
			previous.status = status
		case "":
		default:
			previous.status = "无法识别的结束状态"
		}
	}
	return previous
}

func responsesQuestionAnswerHasError(value any) bool {
	switch item := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(item) != ""
	case map[string]any:
		return len(item) != 0
	default:
		return true
	}
}

func responsesQuestionAnswerRateLimited(object map[string]any, eventName string) bool {
	response, _ := object["response"].(map[string]any)
	outerError, _ := object["error"].(map[string]any)
	innerError, _ := response["error"].(map[string]any)
	errors := []map[string]any{outerError, innerError}
	if eventName == "error" {
		errors = append(errors, object)
	}
	for _, actualError := range errors {
		for _, field := range []string{"code", "type"} {
			switch actualError[field] {
			case "rate_limit_exceeded", "rate_limit_error", "too_many_requests":
				return true
			}
		}
		for _, field := range []string{"status", "status_code"} {
			if actualError[field] == float64(http.StatusTooManyRequests) {
				return true
			}
		}
	}
	return false
}

func responsesQuestionAnswerValidID(id string) bool {
	if !strings.HasPrefix(id, "resp_") || len(id) > 80 {
		return false
	}
	for _, char := range id[len("resp_"):] {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func responsesQuestionAnswerValidCode(code string) bool {
	if code == "" || len(code) > 40 {
		return false
	}
	for _, char := range code {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._:-", char)) {
			return false
		}
	}
	return true
}

func responsesQuestionAnswerEventName(name, key string) string {
	name = questionAnswerUpstreamExcerpt([]byte(name), key)
	switch name {
	case "error", "response.created", "response.queued", "response.in_progress", "response.completed", "response.failed", "response.incomplete", "response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done", "response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done", "response.reasoning_text.delta", "response.reasoning_text.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return name
	default:
		return "无法识别的事件"
	}
}

func responsesQuestionAnswerReadFailure(ctx context.Context, key string, result QuestionAnswerAskResult, evidence responsesQuestionAnswerEvidence) QuestionAnswerAskResult {
	result.failureStep = "receive"
	errorType := QuestionAnswerErrorNetwork
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		errorType = QuestionAnswerErrorTimeout
		result.localError = ctx.Err()
	}
	return responsesQuestionAnswerFailure(ctx, key, result, errorType, evidence, "")
}

func responsesQuestionAnswerSizeFailure(ctx context.Context, key string, result QuestionAnswerAskResult, evidence responsesQuestionAnswerEvidence, limit string) QuestionAnswerAskResult {
	result.failureStep, result.localError = "receive", errors.New(limit)
	return responsesQuestionAnswerFailure(ctx, key, result, QuestionAnswerErrorResponseTooLarge, evidence, "接收回答时"+limit+"，本题不判对错。可缩短回答或降低思考强度后重测。")
}

func responsesQuestionAnswerFailure(ctx context.Context, key string, result QuestionAnswerAskResult, errorType string, evidence responsesQuestionAnswerEvidence, explanation string) QuestionAnswerAskResult {
	if result.failureStep == "" {
		result.failureStep = "parse"
		if result.UpstreamStatus < 200 || result.UpstreamStatus >= 300 {
			result.failureStep = "receive"
		}
	}
	if explanation == "" {
		switch errorType {
		case QuestionAnswerErrorTimeout:
			explanation = "本地已等满这题的最长等待时间，仍未取得可判题的完整答案。本题不判对错，可稍后重测。"
		case QuestionAnswerErrorNetwork:
			explanation = "接收回答时连接已经结束，但没有收到回答完成的确认。本题不判对错。可稍后重测，重复出现请上游核查这次请求。"
		case QuestionAnswerErrorResponseIncomplete:
			explanation = "上游明确报告这道题已达到输出上限，本题不判对错。可以降低思考强度后重测；Responses的实际输出上限以上游为准。"
		default:
			switch {
			case evidence.message == "upstream_error: upstream timeout" && !errors.Is(ctx.Err(), context.DeadlineExceeded):
				explanation = "上游明确返回处理超时，尚未到本地10分钟上限。本题不判对错，可稍后重测；重复出现请上游核查处理时限。"
			case evidence.message == "upstream_error: socket hang up":
				explanation = "上游报告连接提前关闭，具体位置和原因尚不能确认。本题不判对错，可稍后重测；重复出现请上游按时间和响应编号核查。"
			case evidence.reason != "":
				explanation = "上游没有正常完成回答，本题不判对错。可稍后重测，重复出现请上游核查返回内容。"
			case evidence.message != "":
				explanation = "接收回答时上游返回错误，本题不判对错。可稍后重测，重复出现请上游按时间和响应编号核查。"
			case errorType == QuestionAnswerErrorInvalidResponse:
				explanation = "接收回答后未能取得可判题的完整正文，本题不判对错。上游未说明具体原因，可稍后重测，重复出现请上游核查返回内容。"
			default:
				explanation = "上游没有说明具体失败原因，本题不判对错。请把时间、模型、返回码及可用的响应编号交给上游核查。"
			}
		}
	}
	metadata := ""
	for _, item := range [][2]string{{"错误码", evidence.code}, {"最后事件", evidence.lastEvent}, {"结束状态", evidence.status}, {"响应编号", evidence.responseID}} {
		if item[1] != "" {
			metadata += "；" + item[0] + "：" + item[1]
		}
	}
	original := ""
	if evidence.reason != "" {
		original = "未完成原因：" + evidence.reason
	}
	if evidence.message != "" {
		if original != "" {
			original += "；"
		}
		original += evidence.message
	}
	if original != "" {
		label := "原话："
		// The dispatcher may prepend this exact text when its deadline wins.
		// Reserve that space now so validated metadata is never cut in half.
		budget := 500 - len([]rune(responsesQuestionAnswerDeadlinePrefix)) - len([]rune(explanation+metadata+label))
		runes := []rune(original)
		if len(runes) > budget {
			omitted := "（说明较长，已省略后文）"
			budget -= len([]rune(omitted))
			if budget < 0 {
				budget = 0
			}
			original = string(runes[:budget]) + omitted
		}
		explanation += label + original
	}
	result.Answer = ""
	result.ErrorType = errorType
	result.UpstreamExcerpt = questionAnswerUpstreamExcerpt([]byte(explanation+metadata), key)
	return result
}
