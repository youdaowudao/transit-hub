package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

// BrowserUserAgent 是所有上游 HTTP 请求统一使用的浏览器 User-Agent。
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

type HTTPClient struct {
	client *http.Client
}

type requestOptions struct {
	Method      string
	Body        any
	Cookie      string
	UserID      string
	AccessToken string
	TokenType   string
	AdminAPIKey string
}

type jsonResponse struct {
	Payload    any
	Header     http.Header
	ReceivedAt time.Time
	StatusCode int
}

func NewHTTPClient(client *http.Client) *HTTPClient {
	return &HTTPClient{client: client}
}

func (c *HTTPClient) requestJSON(reqURL string, options requestOptions) (jsonResponse, error) {
	return c.requestJSONWithContext(context.Background(), reqURL, options)
}

// requestJSONWithTimeout 为少量对延迟敏感的只读请求设置更短的调用时限，
// 不改变共享 HTTP client 的全局超时配置。
func (c *HTTPClient) requestJSONWithTimeout(reqURL string, options requestOptions, timeout time.Duration) (jsonResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.requestJSONWithContext(ctx, reqURL, options)
}

func (c *HTTPClient) requestJSONWithContext(ctx context.Context, reqURL string, options requestOptions) (jsonResponse, error) {
	return c.requestJSONWithContextLimit(ctx, reqURL, options, 0)
}

// requestJSONWithContextLimit keeps one bounded read isolated to callers that
// fetch potentially large paginated payloads. A zero limit preserves the
// existing response handling used by the rest of the upstream client.
func (c *HTTPClient) requestJSONWithContextLimit(ctx context.Context, reqURL string, options requestOptions, maxResponseBytes int64) (jsonResponse, error) {
	method := options.Method
	if method == "" {
		method = http.MethodGet
	}

	body, err := encodeBody(options.Body)
	if err != nil {
		return jsonResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return jsonResponse{}, &RequestError{MessageKey: ErrorInvalidURL, MutationOutcome: MutationNotSent}
	}
	req.Header.Set("Accept", "application/json")
	if options.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if options.AccessToken != "" {
		tokenType := options.TokenType
		if tokenType == "" {
			tokenType = "Bearer"
		}
		req.Header.Set("Authorization", tokenType+" "+options.AccessToken)
	}
	if options.AdminAPIKey != "" {
		req.Header.Set("x-api-key", options.AdminAPIKey)
	}

	req.Header.Set("User-Agent", BrowserUserAgent)
	if options.Cookie != "" {
		req.Header.Set("Cookie", options.Cookie)
	}

	if options.UserID != "" {
		req.Header.Set("New-Api-User", options.UserID)
	}

	if err := ctx.Err(); err != nil {
		if method == http.MethodGet || method == http.MethodHead {
			return jsonResponse{}, err
		}
		return jsonResponse{}, &RequestError{MessageKey: ErrorNetwork, MutationOutcome: MutationNotSent, Cause: err}
	}
	var traceMu sync.Mutex
	wroteRequest, dialFailed := false, false
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { traceMu.Lock(); wroteRequest = true; traceMu.Unlock() },
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				traceMu.Lock()
				dialFailed = true
				traceMu.Unlock()
			}
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err != nil {
				traceMu.Lock()
				dialFailed = true
				traceMu.Unlock()
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	response, err := c.client.Do(req)
	receivedAt := time.Now().UTC()
	if err != nil {
		traceMu.Lock()
		outcome := MutationUncertain
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if !wroteRequest && dialFailed && ((errors.As(err, &opErr) && opErr.Op == "dial") || errors.As(err, &dnsErr)) {
			outcome = MutationNotSent
		}
		traceMu.Unlock()
		var netErr net.Error
		requestErr := &RequestError{MessageKey: ErrorNetwork, MutationOutcome: outcome}
		requestErr.Timeout = errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
		requestErr.Reason = ErrorNetworkUnreachable
		var certificateErr *tls.CertificateVerificationError
		var unknownAuthority x509.UnknownAuthorityError
		var hostnameErr x509.HostnameError
		if requestErr.Timeout {
			requestErr.Reason = ErrorNetworkTimeout
		} else if errors.As(err, &certificateErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) || strings.Contains(strings.ToLower(err.Error()), "tls:") {
			requestErr.Reason = ErrorTLSFailed
		}
		if ctx.Err() != nil {
			requestErr.Cause = ctx.Err()
		}
		log.Printf("[http-client] 请求失败 method=%s %s category=%s reason=%s outcome=%s timeout=%t", method, safeHTTPDiagnostic(reqURL), requestErr.MessageKey, siteErrorKey(requestErr), outcome, requestErr.Timeout)
		// Read callers retain the shared client's original cancellation contract;
		// write callers require the dispatch evidence even after cancellation.
		if requestErr.Cause != nil && (method == http.MethodGet || method == http.MethodHead) {
			return jsonResponse{}, requestErr.Cause
		}
		return jsonResponse{}, requestErr
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.Printf("[http-client] 非 2xx 响应 method=%s %s status=%d", method, safeHTTPDiagnostic(reqURL), response.StatusCode)
		key := ErrorRequest
		if response.StatusCode == http.StatusUnauthorized {
			key = ErrorAuth
		}
		requestErr := newRequestErrorWithStatus(key, "", response.StatusCode)
		requestErr.MutationOutcome = MutationUncertain
		switch response.StatusCode {
		case http.StatusForbidden:
			requestErr.Reason = ErrorForbidden
		case http.StatusNotFound:
			requestErr.Reason = ErrorNotFound
		case http.StatusTooManyRequests:
			requestErr.Reason = ErrorRateLimited
		default:
			if response.StatusCode >= 500 {
				requestErr.Reason = ErrorUpstreamServer
			}
		}
		// A bounded, structured allowlist prevents response bodies or metadata
		// containing credentials from reaching callers, audit events, or logs.
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		if readErr == nil && len(data) <= 4096 {
			var record map[string]any
			if json.Unmarshal(data, &record) == nil {
				requestErr.UpstreamCode = ParseUpstreamCode(record)
				if code, ok := record["code"].(float64); ok && code == float64(int(code)) {
					value := int(code)
					requestErr.RemoteCode = &value
				}
				if reason, ok := record["reason"].(string); ok && safeRemoteReason(reason) {
					requestErr.RemoteReason = reason
				}
				if code, ok := record["code"].(string); ok && safeRemoteReason(code) && requestErr.RemoteReason == "" {
					requestErr.RemoteReason = code
				}
				if message, ok := record["message"].(string); ok {
					requestErr.RemoteMessage = safeRemoteMutationMessage(message)
					requestErr.UpstreamMessage = safeRequestMessage(message, options)
				}
			}
		}
		applyKnownUpstreamCodeReason(requestErr)
		return jsonResponse{Header: response.Header, ReceivedAt: receivedAt, StatusCode: response.StatusCode}, requestErr
	}
	payload, err := parseJSONWithLimit(response.Body, reqURL, maxResponseBytes)
	if err != nil {
		var requestErr *RequestError
		if errors.As(err, &requestErr) {
			requestErr.StatusCode = response.StatusCode
		}
		return jsonResponse{}, err
	}
	// new-api commonly reports authentication/authorization failures as HTTP 200
	// with {"success": false}. Treat that envelope as an error so Root/Admin
	// writes cannot be mistaken for successful no-op operations.
	if record, ok := payload.(map[string]any); ok {
		if success, exists := record["success"].(bool); exists && !success {
			requestErr := newRequestErrorWithStatus(ErrorRequest, "", response.StatusCode)
			requestErr.UpstreamCode = ParseUpstreamCode(record)
			applyKnownUpstreamCodeReason(requestErr)
			if message, ok := record["message"].(string); ok {
				requestErr.UpstreamMessage = safeRequestMessage(message, options)
			}
			log.Printf("[http-client] 上游业务拒绝 method=%s %s status=%d category=%s", method, safeHTTPDiagnostic(reqURL), response.StatusCode, requestErr.MessageKey)
			return jsonResponse{}, requestErr
		}
	}
	return jsonResponse{Payload: payload, Header: response.Header, ReceivedAt: receivedAt, StatusCode: response.StatusCode}, nil
}

func encodeBody(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	return bytes.NewReader(encoded), nil
}

func parseJSON(reader io.Reader, reqURL string) (any, error) {
	return parseJSONWithLimit(reader, reqURL, 0)
}

func parseJSONWithLimit(reader io.Reader, reqURL string, maxBytes int64) (any, error) {
	if maxBytes > 0 {
		reader = io.LimitReader(reader, maxBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("[http-client] 读取响应体失败 %s category=%s", safeHTTPDiagnostic(reqURL), ErrorInvalidResponse)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		log.Printf("[http-client] 响应体超过限制 %s limit=%d", safeHTTPDiagnostic(reqURL), maxBytes)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Printf("[http-client] JSON 解析失败 %s category=%s", safeHTTPDiagnostic(reqURL), ErrorInvalidResponse)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	return payload, nil
}

func safeRemoteReason(value string) bool {
	switch value {
	case "UNAUTHORIZED", "INVALID_ADMIN_KEY", "TOKEN_EXPIRED", "INVALID_TOKEN", "USER_NOT_FOUND", "USER_INACTIVE", "TOKEN_REVOKED", "FORBIDDEN", "ACCOUNT_NOT_FOUND", "INTERNAL_ERROR":
		return true
	}
	return false
}

// Preserve just the handler's safe validation category, never binding details.
func safeRemoteMutationMessage(value string) string {
	if strings.HasPrefix(value, "Invalid request: ") {
		return "Invalid request"
	}
	switch value {
	case "Invalid account ID", "No updates provided", "account_ids or filters is required":
		return value
	}
	return ""
}

func safeRequestMessage(message string, options requestOptions) string {
	secrets := []string{options.AccessToken, options.Cookie, options.AdminAPIKey}
	for _, cookie := range strings.Split(options.Cookie, ";") {
		if _, value, ok := strings.Cut(strings.TrimSpace(cookie), "="); ok {
			secrets = append(secrets, value)
		}
	}
	if body, ok := options.Body.(map[string]string); ok {
		for key, value := range body {
			if key == "password" || key == "refresh_token" || key == "username" || key == "email" {
				secrets = append(secrets, value)
			}
		}
	}
	return safeUpstreamMessage(message, secrets...)
}

// Only fixed known API paths may identify the origin. Arbitrary paths can carry
// secrets; a prefix match or a dynamic resource ID must never be logged.
func safeHTTPDiagnostic(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.RawPath != "" {
		return "host=- path=-"
	}
	switch parsed.Path {
	case "/api/user/login", "/api/user/self", "/api/status", "/api/log/self/stat", "/api/user/self/groups", "/api/user/groups", "/api/pricing",
		"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/me", "/api/v1/usage/dashboard/stats", "/api/v1/groups/available", "/api/v1/groups/rates":
		return "host=" + safeHost(rawURL) + " path=" + parsed.Path
	default:
		return "host=- path=-"
	}
}
