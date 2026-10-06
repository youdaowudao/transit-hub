package connection_health

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// Protocol defaults are also used for future protocols missing a preset entry.
func defaultProtocolDelayLineMs(protocol TestProtocol) int {
	if protocol == TestProtocolResponses {
		return 10000
	}
	return 5000
}

type chatProbeAccumulator struct {
	content        string
	reasoning      string
	contentParts   []json.RawMessage
	reasoningParts []json.RawMessage
	hasContent     bool
	hasReasoning   bool
}

func (a *chatProbeAccumulator) add(raw json.RawMessage, reasoning bool) (visible bool) {
	if !validCompletionContent(raw) {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if reasoning {
			a.hasReasoning = true
			a.reasoning += text
		} else {
			a.hasContent = true
			a.content += text
		}
		return text != ""
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	if reasoning {
		a.hasReasoning = true
		a.reasoningParts = append(a.reasoningParts, parts...)
	} else {
		a.hasContent = true
		a.contentParts = append(a.contentParts, parts...)
	}
	for _, part := range parts {
		var value struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &value) == nil && value.Text != "" {
			visible = true
		}
	}
	return visible
}

func (a *chatProbeAccumulator) valid() bool {
	message := map[string]any{}
	if a.hasContent {
		if len(a.contentParts) > 0 {
			message["content"] = a.contentParts
		} else {
			message["content"] = a.content
		}
	}
	if a.hasReasoning {
		if len(a.reasoningParts) > 0 {
			message["reasoning_content"] = a.reasoningParts
		} else {
			message["reasoning_content"] = a.reasoning
		}
	}
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message}}})
	return validChatCompletionResponse(body)
}

func streamErrorResult(body []byte) ResultKey {
	var value any
	_ = json.Unmarshal(body, &value)
	var limited func(any) bool
	limited = func(v any) bool {
		switch item := v.(type) {
		case map[string]any:
			for key, child := range item {
				if key == "code" || key == "type" {
					if text, ok := child.(string); ok {
						text = strings.ToLower(text)
						if strings.Contains(text, "rate_limit") || text == "too_many_requests" || text == "429" {
							return true
						}
					}
				}
				if key == "status" || key == "status_code" {
					if number, ok := child.(float64); ok && number == 429 {
						return true
					}
				}
				if limited(child) {
					return true
				}
			}
		case []any:
			for _, child := range item {
				if limited(child) {
					return true
				}
			}
		}
		return false
	}
	if limited(value) {
		return ResultRateLimited
	}
	return ResultServerError
}

func readStreamingProbeResponse(body io.Reader, protocol TestProtocol, key string, started time.Time, now func() time.Time) (out ProbeOutcome) {
	defer func() {
		out.LatencyMs = int(now().Sub(started).Milliseconds())
		if out.FirstTokenMs == nil {
			out.FirstTokenMs = intPtr(out.LatencyMs)
		}
	}()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxProbeResponseBytes+1)
	total := 0
	data := []string{}
	eventName := ""
	accumulator := chatProbeAccumulator{}
	complete := false
	terminalInvalid := false
	// Events may have multiple data lines. Parse only once their SSE frame ends.
	consume := func() bool {
		if len(data) == 0 {
			return false
		}
		event := strings.Join(data, "\n")
		data = nil
		if event == "[DONE]" && protocol == TestProtocolChatCompletions {
			complete = true
			if accumulator.valid() && !terminalInvalid {
				out.Result = ResultOK
			} else {
				out.Result = ResultInvalidResponse
			}
			return true
		}
		var payload struct {
			Type     string          `json:"type"`
			Delta    json.RawMessage `json:"delta"`
			Response json.RawMessage `json:"response"`
			Error    json.RawMessage `json:"error"`
			Choices  []struct {
				Delta struct {
					Content   json.RawMessage `json:"content"`
					Reasoning json.RawMessage `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(event), &payload) != nil {
			terminalInvalid = true
			return false
		}
		eventType := payload.Type
		if eventType == "" {
			eventType = eventName
		}
		eventName = ""
		elapsed := int(now().Sub(started).Milliseconds())
		metadata := eventType == "response.created" || eventType == "response.in_progress" || eventType == "keepalive"
		if protocol == TestProtocolChatCompletions {
			metadata = len(payload.Choices) == 0 && len(payload.Error) == 0
		}
		if !metadata && out.FirstEventMs == nil {
			out.FirstEventMs = intPtr(elapsed)
		}
		if eventType == "error" || eventType == "response.failed" || (len(payload.Error) > 0 && string(payload.Error) != "null") {
			out.Result = streamErrorResult([]byte(event))
			out.Detail = truncate(redact(event, key), 500)
			return true
		}
		if protocol == TestProtocolResponses {
			if eventType == "response.output_text.delta" {
				var delta string
				if json.Unmarshal(payload.Delta, &delta) == nil && delta != "" && out.FirstTokenMs == nil {
					out.FirstTokenMs = intPtr(elapsed)
				}
			}
			if eventType == "response.incomplete" {
				out.Result = ResultInvalidResponse
				out.Detail = "响应未完成"
				return true
			}
			if eventType == "response.completed" {
				complete = true
				if _, valid := decodeTestResponse(protocol, payload.Response, false); valid && !terminalInvalid {
					out.Result = ResultOK
				} else {
					out.Result = ResultInvalidResponse
				}
				return true
			}
		} else {
			for choiceIndex, choice := range payload.Choices {
				if choiceIndex > 0 {
					break
				}
				visible := accumulator.add(choice.Delta.Content, false)
				if accumulator.add(choice.Delta.Reasoning, true) {
					visible = true
				}
				if visible && out.FirstTokenMs == nil {
					out.FirstTokenMs = intPtr(elapsed)
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					complete = true
				}
			}
			if complete {
				if accumulator.valid() && !terminalInvalid {
					out.Result = ResultOK
				} else {
					out.Result = ResultInvalidResponse
				}
				return true
			}
		}
		return false
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxProbeResponseBytes {
			out.Result = ResultInvalidResponse
			out.Detail = "probe response exceeds 1 MiB limit"
			return
		}
		if line == "" {
			if consume() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if consume() {
		return
	}
	out.Result = ResultNetworkFluctuation
	out.RequestPhase = "reading_body"
	out.Detail = "流式回答中途断开或未结束"
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			out.Result = ResultInvalidResponse
			out.Detail = "probe response exceeds 1 MiB limit"
			return
		}
		out.Detail = truncate(redact(err.Error(), key), 500)
	}
	return
}
