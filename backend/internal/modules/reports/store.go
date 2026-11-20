package reports

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrReportStore = errors.New("日报发送记录缓存当前不可用，本次没有开始自动发送；请检查Redis连接和本站服务日志。")
var ErrReportMarker = errors.New("已保存的日报发送记录格式不正确，无法确认上次结果；请先查看Telegram和本站服务日志，避免重复发送。")

type dailyStore struct{ client *redis.Client }

func reportKey(scope Scope, day string) string {
	encoded, _ := json.Marshal([]string{scope.UserID, scope.AdminAccountID, scope.CreatedAt, day})
	return "reports:daily:" + base64.RawURLEncoding.EncodeToString(encoded)
}

func (s *dailyStore) register(ctx context.Context, key, raw string) (bool, error) {
	if s == nil || s.client == nil {
		return false, ErrReportStore
	}
	saved, err := s.client.SetNX(ctx, key, raw, 48*time.Hour).Result()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, ErrReportStore
	}
	return saved, nil
}

const replaceReportMarker = `
    if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end
    local ttl = redis.call('PTTL',KEYS[1])
    if ttl <= 0 then return 0 end
    redis.call('PSETEX',KEYS[1],ttl,ARGV[2])
    return 1
`

// Replacement keeps the original remaining TTL and never creates a deleted/new-attempt key.
func (s *dailyStore) replace(ctx context.Context, key, original string, result ScheduledResult) error {
	if s == nil || s.client == nil {
		return ErrReportStore
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.client.Eval(ctx, replaceReportMarker, []string{key}, original, string(raw)).Int()
	if err != nil {
		return ErrReportStore
	}
	return nil
}

func (s *dailyStore) latest(ctx context.Context, scope Scope, today string) (*ScheduledResult, error) {
	if s == nil || s.client == nil {
		return nil, ErrReportStore
	}
	// At most these three known business-day keys can remain within the original 48-hour TTL.
	keys := []string{reportKey(scope, today), reportKey(scope, shift(today, -1)), reportKey(scope, shift(today, -2))}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, ErrReportStore
	}
	var latest *ScheduledResult
	for _, value := range values {
		if value == nil {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return nil, ErrReportMarker
		}
		var item ScheduledResult
		if json.Unmarshal([]byte(raw), &item) != nil || item.AttemptID == "" || item.At.IsZero() ||
			(item.Status != "sending" && item.Status != "sent" && item.Status != "failed" && item.Status != "unknown") {
			return nil, ErrReportMarker
		}
		if item.Status == "sending" {
			item.Status = "unknown"
			item.Reason = "向" + item.Name + "发送时未确认结果，可能已经收到。为避免重复，本次不自动重发；请先检查 Telegram。"
		}
		if latest == nil || item.At.After(latest.At) {
			copy := item
			latest = &copy
		}
	}
	return latest, nil
}
