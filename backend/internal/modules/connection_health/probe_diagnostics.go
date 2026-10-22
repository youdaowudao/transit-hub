package connection_health

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Only the health probe uses this exception. Question-answer keeps its shared
// streamErrorResult contract, and HTTP failures retain their HTTP classification.
func classifyProbeStreamError(eventType string, raw []byte) ResultKey {
	original := streamErrorResult(raw)
	if original == ResultRateLimited {
		return original
	}
	if probeCompatibilityCode(eventType, raw) != "" {
		return ResultInvalidResponse
	}
	return original
}

// A single exact code must be corroborated by every known error/status field.
// Missing evidence is not inferred from the message or from an HTTP status.
func probeCompatibilityCode(eventType string, raw []byte) string {
	if streamErrorResult(raw) == ResultRateLimited {
		return ""
	}
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil || event == nil {
		return ""
	}
	if !probeEnvelopeType(event["type"]) || !probeEnvelopeType(eventType) {
		return ""
	}
	response := map[string]any{}
	if value := event["response"]; value != nil {
		var ok bool
		response, ok = value.(map[string]any)
		if !ok {
			return ""
		}
	}
	objects := []map[string]any{event, response}
	codes := []string{}
	collectCode := func(object map[string]any) bool {
		value, exists := object["code"]
		if !exists {
			return true
		}
		code, ok := value.(string)
		if !ok {
			return false
		}
		if code != "" {
			codes = append(codes, code)
		}
		return true
	}
	// A top-level code is evidence only for an explicit error event.
	if eventType == "error" && !collectCode(event) {
		return ""
	}
	if value, exists := event["code"]; eventType != "error" && exists && value != "" {
		// It cannot prove compatibility here, but must not hide conflicting or
		// duplicate error evidence alongside a nested compatibility code.
		return ""
	}
	for _, parent := range []map[string]any{event, response} {
		value := parent["error"]
		if value == nil || value == "" {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		if typ, exists := object["type"]; exists && typ != "" && typ != "invalid_request_error" {
			return ""
		}
		if !collectCode(object) {
			return ""
		}
		objects = append(objects, object)
	}
	for index, object := range objects {
		for _, field := range []string{"status", "status_code"} {
			value := object[field]
			if value == nil || (index == 1 && field == "status" && value == "failed") {
				continue
			}
			if value != float64(400) && value != float64(404) {
				return ""
			}
		}
	}
	if len(codes) != 1 {
		return ""
	}
	switch codes[0] {
	case "unsupported_parameter", "unsupported_endpoint", "endpoint_not_supported", "unsupported_protocol":
		return codes[0]
	default:
		return ""
	}
}

func probeEnvelopeType(value any) bool {
	return value == nil || value == "" || value == "error" || value == "response.failed"
}

func probeFailureExcerpt(raw []byte, key string) string {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		switch object := value.(type) {
		case map[string]any:
			return messagesStreamErrorExcerpt(object, key)
		case string:
			return questionAnswerUpstreamExcerpt([]byte(object), key)
		default:
			return ""
		}
	}
	return questionAnswerUpstreamExcerpt(raw, key)
}

// stage is local display context, not a stored state or classification input.
// Call once while a complete frame/body and its actual HTTP status are available.
func formatProbeFailureDetail(req ProbeRequest, status int, stage, eventType string, result ResultKey, raw []byte, fallback string) string {
	model := questionAnswerUpstreamExcerpt([]byte(req.ModelName), req.UpstreamKey)
	if model == "" {
		model = "当前模型"
	}
	protocol := "Chat Completions"
	switch req.Protocol {
	case TestProtocolResponses:
		protocol = "Responses"
	case TestProtocolMessages:
		protocol = "Messages"
	}
	prefix := fmt.Sprintf("模型 %s 的 %s 测试", model, protocol)
	httpStatus := ""
	if status > 0 {
		httpStatus = fmt.Sprintf("（HTTP %d）", status)
	}
	excerpt := probeFailureExcerpt(raw, req.UpstreamKey)
	var detail string
	switch stage {
	case "response_validation":
		detail = prefix + "已收到回复" + httpStatus + "，但未能确认有效回答。已知原因：" + fallback + "。"
		if excerpt != "" {
			detail += "上游提示：" + excerpt + "。"
		}
	case "reading_body":
		detail = prefix + "在读取接口回复时中断" + httpStatus + "。已知原因：" + questionAnswerUpstreamExcerpt([]byte(fallback), req.UpstreamKey) + "。"
	default:
		step := "在等待回答时收到接口错误"
		if stage == "stream_error" {
			step = "在开始接收回答时收到错误"
		}
		if excerpt == "" {
			excerpt = "上游没有提供可安全显示的具体原因"
		}
		detail = prefix + step + httpStatus + "。上游提示：" + excerpt + "。"
	}
	code := ""
	if result == ResultInvalidResponse {
		if stage != "stream_error" {
			var event struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &event)
			eventType = event.Type
		}
		code = probeCompatibilityCode(eventType, raw)
	}
	if code != "" {
		kind := "接口"
		if code == "unsupported_parameter" {
			kind = "参数"
		} else if code == "unsupported_protocol" {
			kind = "协议"
		}
		return detail + "上游不接受这次测试使用的" + kind + "，本次不增加健康失败次数。请检查分组测试配置和该模型的请求要求，再用‘一次性测试’重试。"
	}
	return strings.TrimSpace(detail) + "尚未确认本次能正常回答；这条错误不足以确定是上游服务还是转发网关的问题。请按记录时间核查对应日志，处理后用‘一次性测试’重试。"
}
