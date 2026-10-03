package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

type QuestionAnswerRunner struct {
	client *http.Client
}

func NewQuestionAnswerRunner() *QuestionAnswerRunner {
	return &QuestionAnswerRunner{client: &http.Client{}}
}

func (r *QuestionAnswerRunner) Ask(ctx context.Context, cred upstream.ProbeCredential, model string, question string, reasoningEffort QuestionAnswerReasoningEffort, protocols ...TestProtocol) (string, string) {
	protocol := TestProtocolChatCompletions
	if len(protocols) > 0 {
		protocol = protocols[0]
	}
	request, err := buildTestRequest(ctx, testRequestInput{Protocol: protocol, BaseURL: cred.BaseURL, Key: cred.Key, Model: model, Prompt: question, ReasoningEffort: reasoningEffort, QuestionAnswer: true})
	if err != nil {
		return "", QuestionAnswerErrorInvalidResponse
	}
	response, err := r.client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", QuestionAnswerErrorTimeout
		}
		return "", QuestionAnswerErrorNetwork
	}
	defer response.Body.Close()
	body, oversized, err := readProbeResponseBody(response.Body)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", QuestionAnswerErrorTimeout
		}
		return "", QuestionAnswerErrorNetwork
	}
	if oversized {
		return "", QuestionAnswerErrorResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		outcome := classifyTestHTTPResponse(protocol, response.StatusCode, body, cred.Key, 0)
		if outcome.Result == ResultInvalidResponse {
			return "", QuestionAnswerErrorInvalidResponse
		}
		if outcome.Result == ResultModelNotFound {
			return "", QuestionAnswerErrorModelNotFound
		}
		return "", questionAnswerHTTPError(response.StatusCode)
	}
	answer, ok := decodeTestResponse(protocol, body, true)
	if !ok {
		return "", QuestionAnswerErrorInvalidResponse
	}
	return answer, ""
}

func questionAnswerHTTPError(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return QuestionAnswerErrorRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return QuestionAnswerErrorAuth
	case status == http.StatusNotFound:
		return QuestionAnswerErrorModelNotFound
	case status >= 500:
		return QuestionAnswerErrorServer
	default:
		return QuestionAnswerErrorInvalidResponse
	}
}

func extractQuestionAnswer(body []byte) (string, bool) {
	var response struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Choices) == 0 {
		return "", false
	}
	raw := response.Choices[0].Message.Content
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		return text, text != ""
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		var direct string
		if json.Unmarshal(part, &direct) == nil {
			if direct = strings.TrimSpace(direct); direct != "" {
				texts = append(texts, direct)
			}
			continue
		}
		var item struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &item) == nil {
			if item.Text = strings.TrimSpace(item.Text); item.Text != "" {
				texts = append(texts, item.Text)
			}
		}
	}
	text = strings.Join(texts, "\n")
	return text, text != ""
}
