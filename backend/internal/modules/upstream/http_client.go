package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
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
	Payload any
	Header  http.Header
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
		return jsonResponse{}, newRequestError(ErrorInvalidURL, "")
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

	response, err := c.client.Do(req)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return jsonResponse{}, contextErr
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return jsonResponse{}, &RequestError{MessageKey: ErrorNetwork, Timeout: true}
		}
		log.Printf("[http-client] 请求失败 url=%s err=%v", reqURL, err)
		return jsonResponse{}, newRequestError(ErrorNetwork, "")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.Printf("[http-client] 非 2xx 响应 url=%s status=%d", reqURL, response.StatusCode)
		// 错误响应不要求是 JSON；先按状态码分类，确保非 JSON 的 401/403
		// 也能进入认证退避，而不会被误报成响应格式错误。
		if response.StatusCode == http.StatusUnauthorized {
			return jsonResponse{}, newRequestErrorWithStatus(ErrorAuth, "", response.StatusCode)
		}
		return jsonResponse{}, newRequestErrorWithStatus(ErrorRequest, "", response.StatusCode)
	}

	payload, err := parseJSONWithLimit(response.Body, reqURL, maxResponseBytes)
	if err != nil {
		return jsonResponse{}, err
	}
	// new-api commonly reports authentication/authorization failures as HTTP 200
	// with {"success": false}. Treat that envelope as an error so Root/Admin
	// writes cannot be mistaken for successful no-op operations.
	if record, ok := payload.(map[string]any); ok {
		if success, exists := record["success"].(bool); exists && !success {
			return jsonResponse{}, newRequestError(ErrorRequest, "")
		}
	}
	return jsonResponse{Payload: payload, Header: response.Header}, nil
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
		log.Printf("[http-client] 读取响应体失败 url=%s err=%v", reqURL, err)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		log.Printf("[http-client] 响应体超过限制 url=%s limit=%d", reqURL, maxBytes)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		preview := string(data)
		if len(preview) > 500 {
			preview = preview[:500] + "...(truncated)"
		}
		log.Printf("[http-client] JSON 解析失败 url=%s len=%d preview=%s", reqURL, len(data), preview)
		return nil, newRequestError(ErrorInvalidResponse, "")
	}
	return payload, nil
}
