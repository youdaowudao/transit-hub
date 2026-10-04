package upstream

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

type RequestError struct {
	MessageKey string
	Platform   Platform
	// Failure details are ephemeral: upstream hints never enter storage or logs.
	Reason          string
	Stage           string
	UpstreamMessage string
	Attempts        []FailureAttempt
	Timeout         bool
	// StatusCode 是失败步骤返回的 HTTP 状态码，包括 2xx 业务失败；未收到响应时为 0。
	// 现有调用方只读 MessageKey，新增此字段向后兼容；需要区分 403/401 等细分场景的调用方
	// （如 new-api channel key 获取的安全验证判定）可读取它。
	StatusCode int
	// Only allowlisted response metadata is retained; never keep a raw body.
	RemoteReason    string
	RemoteMessage   string
	RemoteCode      *int
	MutationOutcome string
	Cause           error
}

func (e *RequestError) Error() string {
	return e.MessageKey
}

func (e *RequestError) Unwrap() error { return e.Cause }

const (
	MutationNotSent           = "not_sent"
	MutationConfirmedRejected = "confirmed_rejected"
	MutationConfirmedApplied  = "confirmed_applied"
	MutationUncertain         = "uncertain"
)

// RemoteMutationOutcome is conservative when evidence is absent.
func RemoteMutationOutcome(err error) string {
	if err == nil {
		return MutationConfirmedApplied
	}
	var requestErr *RequestError
	if errors.As(err, &requestErr) && requestErr.MutationOutcome != "" {
		return requestErr.MutationOutcome
	}
	return MutationUncertain
}

func localMutationError(key string) *RequestError {
	return &RequestError{MessageKey: key, Platform: PlatformSub2API, MutationOutcome: MutationNotSent}
}

func newRequestError(messageKey string, platform Platform) *RequestError {
	return &RequestError{MessageKey: messageKey, Platform: platform}
}

func newRequestErrorWithStatus(messageKey string, platform Platform, statusCode int) *RequestError {
	return &RequestError{MessageKey: messageKey, Platform: platform, StatusCode: statusCode}
}

func errorKey(err error) string {
	if requestErr, ok := err.(*RequestError); ok {
		return requestErr.MessageKey
	}
	return ErrorUnknown
}

func requestTimedOut(err error) bool {
	var requestErr *RequestError
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &requestErr) && requestErr.Timeout)
}

const (
	ErrorLoginRejected        = "admin.upstream.errors.loginRejected"
	ErrorLoginIncomplete      = "admin.upstream.errors.loginIncomplete"
	ErrorRefreshTokenRejected = "admin.upstream.errors.refreshTokenRejected"
	ErrorAccessTokenRejected  = "admin.upstream.errors.accessTokenRejected"
	ErrorForbidden            = "admin.upstream.errors.forbidden"
	ErrorRateLimited          = "admin.upstream.errors.rateLimited"
	ErrorUpstreamServer       = "admin.upstream.errors.upstreamServerError"
	ErrorNetworkTimeout       = "admin.upstream.errors.networkTimeout"
	ErrorNetworkUnreachable   = "admin.upstream.errors.networkUnreachable"
	ErrorTLSFailed            = "admin.upstream.errors.tlsFailed"
	ErrorAutoDetectFailed     = "admin.upstream.errors.autoDetectFailed"
	ErrorInvalidFields        = "admin.upstream.errors.invalidFields"
)

type FailureAttempt struct {
	Platform        Platform `json:"platform"`
	ErrorKey        string   `json:"errorKey"`
	Stage           string   `json:"stage,omitempty"`
	HTTPStatus      int      `json:"httpStatus"`
	UpstreamMessage string   `json:"upstreamMessage,omitempty"`
}

type FieldValidationError struct{ Fields []string }

func (e *FieldValidationError) Error() string { return ErrorInvalidFields }

func siteErrorKey(err error) string {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		if requestErr.Reason != "" {
			return requestErr.Reason
		}
		return requestErr.MessageKey
	}
	var validationErr *FieldValidationError
	if errors.As(err, &validationErr) {
		return ErrorInvalidFields
	}
	return ErrorUnknown
}

func errorCategory(key string) string {
	switch key {
	case ErrorLoginRejected, ErrorLoginIncomplete, ErrorRefreshTokenRejected, ErrorAccessTokenRejected:
		return ErrorAuth
	case ErrorNetworkTimeout, ErrorNetworkUnreachable, ErrorTLSFailed:
		return ErrorNetwork
	case ErrorForbidden, ErrorNotFound, ErrorRateLimited, ErrorUpstreamServer, ErrorAutoDetectFailed:
		return ErrorRequest
	default:
		return key
	}
}

func withFailureStage(err error, stage string, platform Platform) error {
	if err == nil {
		return nil
	}
	var original *RequestError
	if !errors.As(err, &original) {
		return err
	}
	detail := *original
	detail.Stage, detail.Platform = stage, platform
	if stage == "refresh" && detail.StatusCode >= 300 {
		detail.Reason = ErrorRefreshTokenRejected
	} else if detail.StatusCode == 401 || (detail.StatusCode >= 200 && detail.StatusCode < 300 && detail.MessageKey == ErrorRequest) {
		if stage == "login" {
			detail.Reason = ErrorLoginRejected
		}
		if stage == "verify" {
			detail.Reason = ErrorAccessTokenRejected
		}
	}
	return &detail
}

func incompleteLogin(platform Platform, stage string, response jsonResponse) *RequestError {
	err := newRequestErrorWithStatus(ErrorAuth, platform, response.StatusCode)
	err.Reason, err.Stage = ErrorLoginIncomplete, stage
	return err
}

func failureAttempt(platform Platform, err error) FailureAttempt {
	result := FailureAttempt{Platform: platform, ErrorKey: siteErrorKey(err)}
	var detail *RequestError
	if errors.As(err, &detail) {
		result.Stage, result.HTTPStatus, result.UpstreamMessage = detail.Stage, detail.StatusCode, detail.UpstreamMessage
	}
	return result
}

var longMessageToken = regexp.MustCompile(`[A-Za-z0-9]{24,}`)
var messageURL = regexp.MustCompile(`(?i)https?://[^\s<>"'，。；]+`)
var messageQuery = regexp.MustCompile(`\?[^\s<>"'，。；]+`)
var labelledSecret = regexp.MustCompile(`(?i)(?:password|passwd|token|key|secret|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|cookie|密码|密钥|令牌)\s*[:=]\s*[^\s,;，。；]+`)

func safeUpstreamMessage(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[已遮盖]")
		}
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = messageURL.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "[地址已遮盖]"
		}
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		return parsed.String()
	})
	value = messageQuery.ReplaceAllString(value, "[查询已遮盖]")
	value = labelledSecret.ReplaceAllString(value, "[凭据已遮盖]")
	value = longMessageToken.ReplaceAllString(value, "[已遮盖]")
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}

func safeHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "-"
	}
	return parsed.Scheme + "://" + parsed.Host
}
