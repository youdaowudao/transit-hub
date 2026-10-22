package connection_health

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

type QuestionAnswerRunner struct {
	client *http.Client
}

type QuestionAnswerAskResult struct {
	Diagnostics     *QuestionAnswerDiagnostics
	failureStep     string
	localError      error
	Answer          string
	ErrorType       string
	UpstreamStatus  int
	UpstreamExcerpt string
}

func NewQuestionAnswerRunner() *QuestionAnswerRunner {
	return &QuestionAnswerRunner{client: &http.Client{}}
}

func (r *QuestionAnswerRunner) Ask(ctx context.Context, cred upstream.ProbeCredential, model string, question string, reasoningEffort QuestionAnswerReasoningEffort, protocols ...TestProtocol) (string, string) {
	result := r.AskDetailed(ctx, cred, model, question, reasoningEffort, protocols...)
	return result.Answer, result.ErrorType
}

func (r *QuestionAnswerRunner) AskDetailed(ctx context.Context, cred upstream.ProbeCredential, model string, question string, reasoningEffort QuestionAnswerReasoningEffort, protocols ...TestProtocol) QuestionAnswerAskResult {
	return r.askDetailed(ctx, cred, model, question, reasoningEffort, nil, protocols...)
}

func (r *QuestionAnswerRunner) AskDetailedWithProgress(ctx context.Context, cred upstream.ProbeCredential, model string, question string, reasoningEffort QuestionAnswerReasoningEffort, progress *QuestionAnswerProgress, protocols ...TestProtocol) QuestionAnswerAskResult {
	return r.askDetailed(ctx, cred, model, question, reasoningEffort, progress, protocols...)
}

func (r *QuestionAnswerRunner) askDetailed(ctx context.Context, cred upstream.ProbeCredential, model string, question string, reasoningEffort QuestionAnswerReasoningEffort, progress *QuestionAnswerProgress, protocols ...TestProtocol) (out QuestionAnswerAskResult) {
	defer func() { out.Diagnostics = progress.finish(out) }()
	if progress != nil {
		ctx = httptrace.WithClientTrace(ctx, progress.trace(cred.Key))
	}
	protocol := TestProtocolChatCompletions
	if len(protocols) > 0 {
		protocol = protocols[0]
	}
	request, err := buildTestRequest(ctx, testRequestInput{Protocol: protocol, BaseURL: cred.BaseURL, Key: cred.Key, Model: model, Prompt: question, ReasoningEffort: reasoningEffort, QuestionAnswer: true, AnthropicAuth: cred.AnthropicAuth})
	if err != nil {
		progress.failure("build", err, cred.Key)
		if errors.Is(err, errMessagesAccountTypeUnsupported) {
			return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorAccountTypeUnsupported}
		}
		return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorInvalidResponse}
	}
	response, err := r.client.Do(request)
	if err != nil {
		progress.failure("request", err, cred.Key)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorTimeout}
		}
		return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorNetwork}
	}
	if progress != nil {
		progress.mu.Lock()
		now := time.Now().UTC()
		progress.diagnostics.ResponseHeadersAt = &now
		progress.step = "receiving"
		progress.mu.Unlock()
		response.Body = &questionAnswerObservedBody{ReadCloser: response.Body, progress: progress, key: cred.Key}
	}
	defer response.Body.Close()
	result := QuestionAnswerAskResult{}
	if response.StatusCode >= 100 && response.StatusCode <= 999 {
		result.UpstreamStatus = response.StatusCode
	}
	if protocol == TestProtocolResponses {
		return readResponsesQuestionAnswer(ctx, response.Body, cred.Key, result)
	}
	if protocol == TestProtocolMessages && response.StatusCode >= 200 && response.StatusCode < 300 && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return readMessagesQuestionAnswerStream(ctx, response.Body, cred.Key, result)
	}
	body, oversized, err := readProbeResponseBody(response.Body)
	if err != nil {
		progress.failure("receive", err, cred.Key)
		result.ErrorType = QuestionAnswerErrorNetwork
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ErrorType = QuestionAnswerErrorTimeout
		}
		return result
	}
	failure := func(errorType string) QuestionAnswerAskResult {
		result.ErrorType = errorType
		result.UpstreamExcerpt = questionAnswerUpstreamExcerpt(body, cred.Key)
		return result
	}
	if oversized {
		progress.failure("receive", errors.New("完整响应超过1MiB读取上限"), cred.Key)
		return failure(QuestionAnswerErrorResponseTooLarge)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.failureStep = "receive"
		outcome := classifyTestHTTPResponse(protocol, response.StatusCode, body, cred.Key, 0)
		if outcome.Result == ResultInvalidResponse {
			return failure(QuestionAnswerErrorInvalidResponse)
		}
		if outcome.Result == ResultModelNotFound {
			return failure(QuestionAnswerErrorModelNotFound)
		}
		if outcome.Result == ResultRateLimited {
			return failure(QuestionAnswerErrorRateLimited)
		}
		return failure(questionAnswerHTTPError(response.StatusCode))
	}
	if protocol == TestProtocolMessages {
		answer, errorType := extractMessagesQuestionAnswer(body)
		if errorType != "" {
			var payload any
			if err := json.Unmarshal(body, &payload); err != nil {
				progress.failure("parse", err, cred.Key)
			} else if errorType == QuestionAnswerErrorResponseIncomplete {
				progress.failure("parse", errors.New("接口以max_tokens结束，已达到本次输出上限，回答未完整结束"), cred.Key)
			} else {
				progress.failure("parse", errors.New("接口返回缺少完整Messages回答或有效结束标记"), cred.Key)
			}
			return failure(errorType)
		}
		return QuestionAnswerAskResult{Answer: answer}
	}
	answer, ok := decodeTestResponse(protocol, body, true)
	if !ok {
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			progress.failure("parse", err, cred.Key)
		} else {
			progress.failure("parse", errors.New("接口返回不包含可判题的完整回答，或明确报告了失败"), cred.Key)
		}
		return failure(QuestionAnswerErrorInvalidResponse)
	}
	return QuestionAnswerAskResult{Answer: answer}
}

const messagesQuestionAnswerStreamLimit = 16 << 20

func readMessagesQuestionAnswerStream(ctx context.Context, body io.Reader, key string, result QuestionAnswerAskResult) QuestionAnswerAskResult {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxProbeResponseBytes+2)
	total := 0
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		total += advance
		return advance, token, err
	})
	texts := map[int]*strings.Builder{}
	data := []string{}
	eventName, stopReason := "", ""
	fail := func(errorType, excerpt string) QuestionAnswerAskResult {
		result.Answer = ""
		result.ErrorType = errorType
		result.UpstreamExcerpt = excerpt
		return result
	}
	finish := func() QuestionAnswerAskResult {
		if stopReason == "max_tokens" {
			return fail(QuestionAnswerErrorResponseIncomplete, questionAnswerUpstreamExcerpt([]byte(stopReason), key))
		}
		if stopReason == "end_turn" || stopReason == "stop_sequence" {
			indices := make([]int, 0, len(texts))
			for index := range texts {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			parts := []string{}
			for _, index := range indices {
				if text := strings.TrimSpace(texts[index].String()); text != "" {
					parts = append(parts, text)
				}
			}
			if answer := strings.Join(parts, "\n"); answer != "" {
				result.Answer = answer
				return result
			}
		}
		return fail(QuestionAnswerErrorInvalidResponse, questionAnswerUpstreamExcerpt([]byte(stopReason), key))
	}
	consume := func() (QuestionAnswerAskResult, bool) {
		name := eventName
		eventName = ""
		if len(data) == 0 {
			return result, false
		}
		event := strings.Join(data, "\n")
		data = nil
		if event == "[DONE]" {
			return finish(), true
		}
		var payload struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error json.RawMessage `json:"error"`
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			result.failureStep, result.localError = "parse", err
			return fail(QuestionAnswerErrorInvalidResponse, ""), true
		}
		if err := json.Unmarshal([]byte(event), &object); err != nil {
			result.failureStep, result.localError = "parse", err
			return fail(QuestionAnswerErrorInvalidResponse, ""), true
		}
		eventType := payload.Type
		if eventType == "" {
			eventType = name
		}
		if eventType == "error" || eventType == "response.failed" || (len(payload.Error) > 0 && string(payload.Error) != "null") {
			errorType := QuestionAnswerErrorServer
			var streamError struct {
				Type string `json:"type"`
			}
			if streamErrorResult([]byte(event)) == ResultRateLimited || (json.Unmarshal(payload.Error, &streamError) == nil && strings.Contains(strings.ToLower(streamError.Type), "overloaded")) {
				errorType = QuestionAnswerErrorRateLimited
			}
			return fail(errorType, messagesStreamErrorExcerpt(object, key)), true
		}
		appendText := func(text string) {
			if texts[payload.Index] == nil {
				texts[payload.Index] = &strings.Builder{}
			}
			texts[payload.Index].WriteString(text)
		}
		switch eventType {
		case "content_block_start":
			if payload.ContentBlock.Type == "text" {
				appendText(payload.ContentBlock.Text)
			}
		case "content_block_delta":
			if payload.Delta.Type == "text_delta" {
				appendText(payload.Delta.Text)
			}
		case "message_delta":
			if payload.Delta.StopReason != "" {
				stopReason = payload.Delta.StopReason
			}
		case "message_stop":
			return finish(), true
		}
		return result, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if total > messagesQuestionAnswerStreamLimit || len(line) > maxProbeResponseBytes {
			result.failureStep, result.localError = "receive", errors.New("Messages流式响应超过16MiB总上限或1MiB单行上限")
			return fail(QuestionAnswerErrorResponseTooLarge, "")
		}
		if line == "" {
			if out, done := consume(); done {
				return out
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		result.failureStep, result.localError = "receive", scanner.Err()
		return fail(QuestionAnswerErrorResponseTooLarge, "")
	}
	if scanner.Err() == nil {
		if out, done := consume(); done {
			return out
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.failureStep, result.localError = "receive", ctx.Err()
		return fail(QuestionAnswerErrorTimeout, "")
	}
	result.failureStep, result.localError = "receive", scanner.Err()
	if result.localError == nil {
		result.localError = errors.New("响应已结束，但未收到完整Messages回答和结束标记")
	}
	return fail(QuestionAnswerErrorNetwork, "")
}

func messagesStreamErrorExcerpt(event map[string]any, key string) string {
	errorObject, _ := event["error"].(map[string]any)
	response, _ := event["response"].(map[string]any)
	responseError, _ := response["error"].(map[string]any)
	for _, candidate := range []any{errorObject["message"], responseError["message"], event["message"], event["error"], response["error"]} {
		if text, ok := candidate.(string); ok {
			if excerpt := questionAnswerUpstreamExcerpt([]byte(text), key); excerpt != "" {
				return excerpt
			}
		}
	}
	return ""
}

func extractMessagesQuestionAnswer(body []byte) (string, string) {
	var response struct {
		Type       string `json:"type"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &response) != nil || response.Type != "message" {
		return "", QuestionAnswerErrorInvalidResponse
	}
	if response.StopReason == "max_tokens" {
		return "", QuestionAnswerErrorResponseIncomplete
	}
	if response.StopReason != "end_turn" && response.StopReason != "stop_sequence" {
		return "", QuestionAnswerErrorInvalidResponse
	}
	texts := []string{}
	for _, part := range response.Content {
		if part.Type == "text" {
			if text := strings.TrimSpace(part.Text); text != "" {
				texts = append(texts, text)
			}
		}
	}
	answer := strings.Join(texts, "\n")
	if answer == "" {
		return "", QuestionAnswerErrorInvalidResponse
	}
	return answer, ""
}

func questionAnswerUpstreamExcerpt(body []byte, key string) string {
	text := strings.ReplaceAll(strings.ToValidUTF8(string(body), ""), "\x00", "")
	if key != "" {
		text = strings.ReplaceAll(text, key, "***")
		encodedKey, _ := json.Marshal(key)
		escapedKey := string(encodedKey[1 : len(encodedKey)-1])
		if escapedKey != key {
			text = strings.ReplaceAll(text, escapedKey, "***")
		}
	}
	text = questionAnswerHideTokens(text)
	if key != "" && !questionAnswerExcerptKeySafe(text, key) {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 500 {
		runes = runes[:500]
	}
	excerpt := string(runes)
	if key != "" && !questionAnswerExcerptKeySafe(excerpt, key) {
		return ""
	}
	return excerpt
}

// Only token bytes are replaced; separators and the Bearer prefix are retained.
func questionAnswerHideTokens(text string) string {
	var masked strings.Builder
	last := 0
	for i := 0; i < len(text); i++ {
		if i > 0 && questionAnswerTokenIdentifier(text[i-1]) {
			continue
		}
		tokenStart, tokenEnd := i, i
		if strings.HasPrefix(text[i:], "sk-") {
			tokenEnd = i + 3
			for tokenEnd < len(text) && questionAnswerTokenIdentifier(text[tokenEnd]) {
				tokenEnd++
			}
			if tokenEnd-(i+3) < 16 {
				continue
			}
		} else if len(text)-i >= 6 && strings.EqualFold(text[i:i+6], "Bearer") {
			tokenStart = i + 6
			for tokenStart < len(text) {
				char, size := utf8.DecodeRuneInString(text[tokenStart:])
				if !unicode.IsSpace(char) {
					break
				}
				tokenStart += size
			}
			if tokenStart == i+6 {
				continue
			}
			tokenEnd = tokenStart
			for tokenEnd < len(text) && (questionAnswerTokenIdentifier(text[tokenEnd]) || strings.ContainsRune(".~+/=", rune(text[tokenEnd]))) {
				tokenEnd++
			}
			if tokenEnd-tokenStart < 16 {
				continue
			}
		} else {
			continue
		}
		masked.WriteString(text[last:tokenStart])
		masked.WriteString("***")
		last = tokenEnd
		i = tokenEnd - 1
	}
	if last == 0 {
		return text
	}
	masked.WriteString(text[last:])
	return masked.String()
}

func questionAnswerTokenIdentifier(char byte) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-'
}

func questionAnswerExcerptKeySafe(text, key string) bool {
	if strings.Contains(text, key) {
		return false
	}
	for layer := 0; layer < 8; layer++ {
		decoded := questionAnswerDecodeJSONEscapes(text)
		if decoded == text {
			return true
		}
		if strings.Contains(decoded, key) {
			return false
		}
		text = decoded
	}
	return false
}

// Decode escapes across the entire excerpt, including plain text and JSON
// fragments. Invalid escapes and unpaired UTF-16 surrogates stay unchanged.
func questionAnswerDecodeJSONEscapes(text string) string {
	var decoded strings.Builder
	decoded.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			switch text[i+1] {
			case '"', '\\', '/':
				decoded.WriteByte(text[i+1])
				i++
				continue
			case 'b', 'f', 'n', 'r', 't':
				decoded.WriteByte(map[byte]byte{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[text[i+1]])
				i++
				continue
			case 'u':
				if i+6 <= len(text) {
					char, ok := questionAnswerJSONHexRune(text[i+2 : i+6])
					if ok && char >= 0xd800 && char <= 0xdbff {
						if i+12 <= len(text) && text[i+6:i+8] == `\u` {
							low, lowOK := questionAnswerJSONHexRune(text[i+8 : i+12])
							if lowOK && low >= 0xdc00 && low <= 0xdfff {
								decoded.WriteRune(utf16.DecodeRune(char, low))
								i += 11
								continue
							}
						}
					} else if ok && !utf16.IsSurrogate(char) {
						decoded.WriteRune(char)
						i += 5
						continue
					}
				}
			}
		}
		decoded.WriteByte(text[i])
	}
	return decoded.String()
}

func questionAnswerJSONHexRune(hex string) (rune, bool) {
	var value rune
	for i := 0; i < len(hex); i++ {
		value <<= 4
		switch char := hex[i]; {
		case char >= '0' && char <= '9':
			value += rune(char - '0')
		case char >= 'a' && char <= 'f':
			value += rune(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value += rune(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
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
