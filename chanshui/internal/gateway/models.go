package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	var last string
	status := 0
	for attempt := 0; attempt <= c.retries(); attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		base := apiBase(c.BaseURL)
		req, e := http.NewRequestWithContext(ctx, "GET", base+"/models", nil)
		if e != nil {
			return nil, e
		}
		if c.AnthropicAuth != "" && c.Mode == "anthropic" {
			if c.AnthropicAuth == "bearer" {
				req.Header.Set("Authorization", "Bearer "+c.Key)
			} else {
				req.Header.Set("x-api-key", c.Key)
			}
		} else {
			req.Header.Set("x-api-key", c.Key)
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("User-Agent", c.ua())
		res, e := c.http().Do(req)
		if e != nil {
			last = e.Error()
		} else {
			status = res.StatusCode
			var data struct{ Data []struct{ ID string } }
			if c.PreserveErrorReason {
				raw, readErr := io.ReadAll(io.LimitReader(res.Body, 2<<20))
				_ = res.Body.Close()
				if readErr != nil {
					last = "模型列表响应读取失败：" + readErr.Error()
				} else if status == 200 {
					if err := json.Unmarshal(raw, &data); err != nil {
						last = "模型列表不是有效JSON：" + err.Error()
					} else if data.Data == nil {
						last = "模型列表缺少data数组，请核对上游API兼容性"
					}
				} else {
					var detail struct {
						Message string          `json:"message"`
						Error   json.RawMessage `json:"error"`
					}
					_ = json.Unmarshal(raw, &detail)
					last = detail.Message
					if len(detail.Error) > 0 {
						var inner struct {
							Message string `json:"message"`
							Type    string `json:"type"`
						}
						if json.Unmarshal(detail.Error, &inner) == nil {
							last = inner.Message
							if inner.Type != "" {
								last = inner.Type + ": " + last
							}
						} else {
							var value string
							if json.Unmarshal(detail.Error, &value) == nil {
								last = value
							}
						}
					}
					if strings.TrimSpace(last) == "" {
						last = strings.TrimSpace(string(raw))
					}
					if len(last) > 8192 {
						last = last[:8192] + "（上游提示过长，已截短）"
					}
					if c.Key != "" {
						last = strings.ReplaceAll(last, c.Key, "[密钥已隐藏]")
					}
				}
				if status == 200 && readErr == nil && data.Data != nil {
					ids := make([]string, 0, len(data.Data))
					for _, m := range data.Data {
						ids = append(ids, m.ID)
					}
					return ids, nil
				}
			} else {
				var data struct{ Data []struct{ ID string } }
				e = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&data)
				res.Body.Close()
				if status == 200 && e == nil && data.Data != nil {
					ids := make([]string, 0, len(data.Data))
					for _, m := range data.Data {
						ids = append(ids, m.ID)
					}
					return ids, nil
				}
				last = "invalid model list JSON"
			}
		}
		if attempt < c.retries() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * 250 * time.Millisecond):
			}
		}
	}
	if c.PreserveErrorReason {
		return nil, fmt.Errorf("读取上游模型列表未完成（HTTP %d）：%s；请按此具体提示核查，原因未作猜测", status, last)
	}
	return nil, fmt.Errorf("%s", FriendlyError(status, last, c.retries()+1))
}
