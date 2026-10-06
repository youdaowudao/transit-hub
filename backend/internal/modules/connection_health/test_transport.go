package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type testRequestInput struct {
	Protocol                    TestProtocol
	BaseURL, Key, Model, Prompt string
	MaxTokens                   int
	ReasoningEffort             QuestionAnswerReasoningEffort
	QuestionAnswer              bool
	Stream                      bool
}

func buildTestRequest(ctx context.Context, input testRequestInput) (*http.Request, error) {
	if !validTestProtocol(input.Protocol) {
		return nil, fmt.Errorf("unknown test protocol")
	}
	path := "/v1/chat/completions"
	payload := map[string]any{"model": input.Model}
	if input.Protocol == TestProtocolResponses {
		path = "/v1/responses"
		payload["input"] = []map[string]any{{"role": "user", "content": input.Prompt}}
		payload["store"] = false
		if input.QuestionAnswer {
			payload["stream"] = false
		} else {
			payload["stream"] = input.Stream
		}
		if input.QuestionAnswer {
			payload["reasoning"] = map[string]any{"effort": input.ReasoningEffort}
		} else {
			payload["max_output_tokens"] = maxInt(input.MaxTokens, 128)
		}
	} else {
		payload["messages"] = []map[string]any{{"role": "user", "content": input.Prompt}}
		if input.QuestionAnswer {
			payload["reasoning_effort"] = input.ReasoningEffort
		} else {
			payload["max_tokens"] = input.MaxTokens
			if input.Stream {
				payload["stream"] = true
			}
		}
	}
	return newJSONRequest(ctx, http.MethodPost, strings.TrimRight(input.BaseURL, "/")+path, payload, map[string]string{"Authorization": "Bearer " + input.Key})
}

func decodeTestResponse(protocol TestProtocol, body []byte, questionAnswer bool) (string, bool) {
	if protocol == TestProtocolChatCompletions {
		if questionAnswer {
			return extractQuestionAnswer(body)
		}
		return "", validChatCompletionResponse(body)
	}
	if protocol != TestProtocolResponses {
		return "", false
	}
	var response struct {
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "completed" || (len(response.Error) > 0 && string(response.Error) != "null") {
		return "", false
	}
	texts := []string{}
	for _, item := range response.Output {
		if item.Type == "refusal" {
			return "", false
		}
		if item.Type != "message" {
			continue
		}
		if item.Status != "completed" || item.Role != "assistant" {
			return "", false
		}
		for _, part := range item.Content {
			if part.Type == "refusal" {
				return "", false
			}
			if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				texts = append(texts, part.Text)
			}
		}
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	return text, text != ""
}

func classifyTestHTTPResponse(protocol TestProtocol, status int, body []byte, key string, latency int) ProbeOutcome {
	detail := truncate(redact(string(body), key), 500)
	if status == http.StatusOK || status == http.StatusCreated {
		if _, ok := decodeTestResponse(protocol, body, false); !ok {
			return ProbeOutcome{Result: ResultInvalidResponse, LatencyMs: latency, Detail: detail}
		}
		result := ResultOK
		if latency > defaultProtocolDelayLineMs(protocol) {
			result = ResultSlowResponse
		}
		return ProbeOutcome{Result: result, LatencyMs: latency}
	}
	if status == http.StatusNotFound {
		var response struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &response) == nil && (response.Error.Code == "model_not_found" || response.Error.Type == "model_not_found") {
			return ProbeOutcome{Result: ResultModelNotFound, LatencyMs: latency, Detail: detail}
		}
		if response.Error.Code == "unsupported_endpoint" || response.Error.Code == "endpoint_not_supported" || response.Error.Code == "unsupported_protocol" || response.Error.Type == "unsupported_endpoint" {
			return ProbeOutcome{Result: ResultInvalidResponse, LatencyMs: latency, Detail: "所选测试协议与上游接口不兼容"}
		}
		return ProbeOutcome{Result: ResultInvalidResponse, LatencyMs: latency, Detail: "接口或模型不可用，原因未确认"}
	}
	return classifyHTTPResponse(status, body, key, latency)
}
