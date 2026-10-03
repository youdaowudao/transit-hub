package upstream

import (
	"context"
	"errors"
)

type RequestError struct {
	MessageKey string
	Platform   Platform
	Timeout    bool
	// StatusCode 是上游返回的 HTTP 状态码（仅非 2xx 响应错误会填充，其它错误为 0）。
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
