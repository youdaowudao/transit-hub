package upstream

import (
	"testing"
	"time"
)

func TestStageAReadOnlyExtrasAndUnixExpiry(t *testing.T) {
	account := parseSub2APIAccount(map[string]any{
		"id": 1, "expires_at": float64(1800000000), "auto_pause_on_expired": true,
		"extra": map[string]any{
			"model_rate_limits": map[string]any{"model": map[string]any{"rate_limit_reset_at": "2027-01-01T00:00:00Z"}},
			"quota_limit":       float64(30), "quota_used": float64(10), "quota_daily_limit": float64(20), "quota_daily_used": float64(20), "quota_weekly_limit": float64(100), "quota_weekly_used": float64(100),
		},
	})
	if !account.ExpiresAtKnown || account.ExpiresAt == nil || account.ExpiresAt.Unix() != 1800000000 || account.AutoPauseOnExpired == nil || !*account.AutoPauseOnExpired {
		t.Fatal("expiry/automatic-expiry data not read as Unix seconds and strict bool")
	}
	if !account.ModelRateLimitsKnown || len(account.ModelRateLimits) != 1 || !account.ModelRateLimits[0].Known || account.ModelRateLimits[0].ResetAt == nil {
		t.Fatal("model limit deadline lost")
	}
	for _, sample := range []struct {
		value *float64
		want  float64
	}{{account.QuotaLimit, 30}, {account.QuotaUsed, 10}, {account.QuotaDailyLimit, 20}, {account.QuotaDailyUsed, 20}, {account.QuotaWeeklyLimit, 100}, {account.QuotaWeeklyUsed, 100}} {
		if sample.value == nil || *sample.value != sample.want {
			t.Fatal("read-only quota data lost")
		}
	}
	if !account.QuotaKnown {
		t.Fatal("valid quota fields became unknown")
	}
	for _, value := range []any{float64(1800000000.25), "1800000000", float64(-1)} {
		invalid := parseSub2APIAccount(map[string]any{"expires_at": value, "auto_pause_on_expired": "true"})
		if invalid.ExpiresAtKnown || invalid.ExpiresAt != nil || invalid.AutoPauseOnExpired != nil {
			t.Fatal("invalid expiry/bool data accepted")
		}
	}
}

func TestStageAExtraRestrictionsPreserveMissingNullAndMalformed(t *testing.T) {
	missing := parseSub2APIAccount(map[string]any{})
	if !missing.ModelRateLimitsKnown || len(missing.ModelRateLimits) != 0 || !missing.QuotaKnown {
		t.Fatal("absent optional extra restrictions became an invented limit")
	}
	for _, extra := range []any{"malformed", map[string]any{"model_rate_limits": true, "quota_limit": "30"}, map[string]any{"model_rate_limits": map[string]any{"model": true}, "quota_used": float64(-1)}} {
		invalid := parseSub2APIAccount(map[string]any{"extra": extra})
		if invalid.ModelRateLimitsKnown || invalid.QuotaKnown {
			t.Fatal("malformed extras accepted")
		}
	}
	account := parseSub2APIAccount(map[string]any{"temp_unschedulable_until": nil, "rate_limit_reset_at": nil, "overload_until": nil, "expires_at": nil, "extra": map[string]any{"model_rate_limits": map[string]any{"model": map[string]any{"rate_limit_reset_at": nil}}}})
	accounts := []AdminGroupAccountInfo{account}
	GuardSub2APIAccountTimes("https://external.test", accounts, []InventoryResponseTime{{HTTPDate: "", ReceivedAt: time.Now()}})
	guarded := accounts[0]
	if !guarded.TempUnschedulableKnown || !guarded.RateLimitKnown || !guarded.OverloadKnown || !guarded.ExpiresAtKnown || !guarded.ModelRateLimitsKnown || !guarded.ModelRateLimits[0].Known {
		t.Fatal("explicit null restrictions did not stay unlimited when guard failed")
	}
}
