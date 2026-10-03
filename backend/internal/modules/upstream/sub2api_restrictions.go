package upstream

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InventoryResponseTime belongs to one actual inventory response. Keeping the
// whole page set lets a workspace invalidate deadlines across all its groups.
type InventoryResponseTime struct {
	HTTPDate   string
	ReceivedAt time.Time
}

// InventoryTimeEvidence is shared only by groups from one complete list read.
// Account responses (including empty pages) append evidence to the same read.
type InventoryTimeEvidence struct {
	mu        sync.Mutex
	responses []InventoryResponseTime
}

func (e *InventoryTimeEvidence) add(response InventoryResponseTime) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.responses = append(e.responses, response)
}

func (e *InventoryTimeEvidence) Responses() []InventoryResponseTime {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]InventoryResponseTime(nil), e.responses...)
}

type Sub2APIModelRateLimit struct {
	Model   string
	ResetAt *time.Time
	Known   bool
}

// Sub2APIDeadlineExpired also accepts an explicit null as no restriction.
// The strict comparison deliberately retains the two-second safety margin.
func Sub2APIDeadlineExpired(deadline *time.Time, known bool, now time.Time) bool {
	return known && (deadline == nil || now.After(deadline.Add(2*time.Second)))
}

func Sub2APIInventoryTimeTrusted(baseURL string, responses []InventoryResponseTime) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || len(responses) == 0 {
		return false
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	for _, response := range responses {
		remote, err := http.ParseTime(response.HTTPDate)
		if err != nil || response.ReceivedAt.IsZero() {
			return false
		}
		skew := response.ReceivedAt.Sub(remote)
		if skew > 2*time.Second || skew < -2*time.Second {
			return false
		}
	}
	return true
}

func GuardSub2APIAccountTimes(baseURL string, accounts []AdminGroupAccountInfo, responses []InventoryResponseTime) {
	trusted := Sub2APIInventoryTimeTrusted(baseURL, responses)
	for index := range accounts {
		accounts[index].InventoryResponseTimes = responses
		if !trusted {
			InvalidateSub2APIAccountTimes(&accounts[index])
		}
	}
}

// Null stays known and unlimited. A parsed deadline loses its known flag when
// any response in the complete inventory fails the clock guard.
func InvalidateSub2APIAccountTimes(account *AdminGroupAccountInfo) {
	if account.TempUnschedulableUntil != nil {
		account.TempUnschedulableKnown = false
	}
	if account.RateLimitResetAt != nil {
		account.RateLimitKnown = false
	}
	if account.OverloadUntil != nil {
		account.OverloadKnown = false
	}
	if account.ExpiresAt != nil {
		account.ExpiresAtKnown = false
	}
	for index := range account.ModelRateLimits {
		if account.ModelRateLimits[index].ResetAt != nil {
			account.ModelRateLimits[index].Known = false
			account.ModelRateLimitsKnown = false
		}
	}
}

func strictSub2APITime(record map[string]any, key string) (*time.Time, bool) {
	raw, exists := record[key]
	if !exists {
		return nil, false
	}
	if raw == nil {
		return nil, true
	}
	value, ok := raw.(string)
	if !ok {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}

func strictSub2APIUnixTime(record map[string]any, key string) (*time.Time, bool) {
	raw, exists := record[key]
	if !exists {
		return nil, false
	}
	if raw == nil {
		return nil, true
	}
	var seconds int64
	switch value := raw.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < 0 || value > 253402300799 {
			return nil, false
		}
		seconds = int64(value)
	case int64:
		seconds = value
	case int:
		seconds = int64(value)
	case json.Number:
		var err error
		seconds, err = strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return nil, false
		}
	default:
		return nil, false
	}
	if seconds < 0 || seconds > 253402300799 {
		return nil, false
	}
	parsed := time.Unix(seconds, 0).UTC()
	return &parsed, true
}

func parseSub2APIRestrictions(record map[string]any, account *AdminGroupAccountInfo) {
	account.TempUnschedulableUntil, account.TempUnschedulableKnown = strictSub2APITime(record, "temp_unschedulable_until")
	account.TempUnschedulableReason = sanitizeSub2APIErrorMessage(safeString(record, "temp_unschedulable_reason"))
	account.RateLimitResetAt, account.RateLimitKnown = strictSub2APITime(record, "rate_limit_reset_at")
	account.OverloadUntil, account.OverloadKnown = strictSub2APITime(record, "overload_until")
	account.ExpiresAt, account.ExpiresAtKnown = strictSub2APIUnixTime(record, "expires_at")
	if value, ok := record["auto_pause_on_expired"].(bool); ok {
		account.AutoPauseOnExpired = &value
	}
	account.ModelRateLimitsKnown, account.QuotaKnown = true, true
	extraRaw, exists := record["extra"]
	if !exists || extraRaw == nil {
		return
	}
	extra, ok := extraRaw.(map[string]any)
	if !ok {
		account.ModelRateLimitsKnown, account.QuotaKnown = false, false
		return
	}
	if raw, exists := extra["model_rate_limits"]; exists {
		limits, ok := raw.(map[string]any)
		if !ok {
			account.ModelRateLimitsKnown = false
		} else {
			for model, rawLimit := range limits {
				limit, ok := rawLimit.(map[string]any)
				entry := Sub2APIModelRateLimit{Model: model}
				if ok {
					entry.ResetAt, entry.Known = strictSub2APITime(limit, "rate_limit_reset_at")
				}
				if !entry.Known {
					account.ModelRateLimitsKnown = false
				}
				account.ModelRateLimits = append(account.ModelRateLimits, entry)
			}
		}
	}
	for _, field := range []struct {
		key    string
		target **float64
	}{
		{"quota_limit", &account.QuotaLimit}, {"quota_used", &account.QuotaUsed},
		{"quota_daily_limit", &account.QuotaDailyLimit}, {"quota_daily_used", &account.QuotaDailyUsed},
		{"quota_weekly_limit", &account.QuotaWeeklyLimit}, {"quota_weekly_used", &account.QuotaWeeklyUsed},
	} {
		raw, exists := extra[field.key]
		if !exists || raw == nil {
			continue
		}
		value, ok := raw.(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			account.QuotaKnown = false
			continue
		}
		*field.target = &value
	}
}
