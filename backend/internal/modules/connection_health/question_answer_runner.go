package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

type QuestionAnswerRunner struct {
	client *http.Client
}

type QuestionAnswerAskResult struct {
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
	protocol := TestProtocolChatCompletions
	if len(protocols) > 0 {
		protocol = protocols[0]
	}
	request, err := buildTestRequest(ctx, testRequestInput{Protocol: protocol, BaseURL: cred.BaseURL, Key: cred.Key, Model: model, Prompt: question, ReasoningEffort: reasoningEffort, QuestionAnswer: true})
	if err != nil {
		return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorInvalidResponse}
	}
	response, err := r.client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorTimeout}
		}
		return QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorNetwork}
	}
	defer response.Body.Close()
	result := QuestionAnswerAskResult{}
	if response.StatusCode >= 100 && response.StatusCode <= 999 {
		result.UpstreamStatus = response.StatusCode
	}
	body, oversized, err := readProbeResponseBody(response.Body)
	if err != nil {
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
		return failure(QuestionAnswerErrorResponseTooLarge)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		outcome := classifyTestHTTPResponse(protocol, response.StatusCode, body, cred.Key, 0)
		if outcome.Result == ResultInvalidResponse {
			return failure(QuestionAnswerErrorInvalidResponse)
		}
		if outcome.Result == ResultModelNotFound {
			return failure(QuestionAnswerErrorModelNotFound)
		}
		return failure(questionAnswerHTTPError(response.StatusCode))
	}
	answer, ok := decodeTestResponse(protocol, body, true)
	if !ok {
		return failure(QuestionAnswerErrorInvalidResponse)
	}
	return QuestionAnswerAskResult{Answer: answer}
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
