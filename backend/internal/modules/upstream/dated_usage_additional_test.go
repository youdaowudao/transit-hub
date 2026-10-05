package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTodayRateLimitDoesNotCreateHistoricalPause(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls atomic.Int32
	platform := failureFixPlatform(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	for i := 0; i < 2; i++ {
		_, err := platform.requestKeyUsageJSONWithContext(context.Background(), "https://today.test/api/v1/usage/stats?period=today", requestOptions{})
		datedUsageREDError(t, err)
	}
	if calls.Load() != 4 {
		t.Fatalf("today 429 must retain two attempts per call: requests=%d", calls.Load())
	}
	_, err := platform.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), "site"), "https://today.test/api/v1/usage/stats?start_date=2031-02-03", requestOptions{})
	datedUsageREDError(t, err)
	if calls.Load() != 6 {
		t.Fatalf("today 429 must not pause the later historical call: requests=%d", calls.Load())
	}
	_, err = platform.requestKeyUsageJSONWithContext(context.Background(), "https://today.test/api/v1/usage/stats?period=today", requestOptions{})
	datedUsageREDError(t, err)
	if calls.Load() != 8 {
		t.Fatalf("historical pause must not block today's 429 retries: requests=%d", calls.Load())
	}
}

func TestNewAPIAccountDateCostIgnoresAndDoesNotCreatePause(t *testing.T) {
	failureFixCaptureLogs(t)
	var accountCalls, tokenCalls atomic.Int32
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/log/self/stat" {
			accountCalls.Add(1)
		} else {
			tokenCalls.Add(1)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	})
	session := Session{Platform: PlatformNewAPI, BaseURL: "https://newapi-date.test", AccessToken: "fixture-only", UserID: "42"}
	_, _, err := platform.fetchCostForDateForSite("site", session, "2031-02-03")
	datedUsageREDError(t, err)
	_, err = platform.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), "site"), session.BaseURL+"/api/token/", newAPIAuthOptions(session))
	datedUsageREDError(t, err)
	if accountCalls.Load() != 1 || tokenCalls.Load() != 2 {
		t.Fatalf("NewAPI account 429 must stay single-attempt and not create a pause: account=%d tokens=%d", accountCalls.Load(), tokenCalls.Load())
	}
	_, _, err = platform.fetchCostForDateForSite("site", session, "2031-02-03")
	datedUsageREDError(t, err)
	if accountCalls.Load() != 2 {
		t.Fatalf("NewAPI account cost must ignore active historical pause: account=%d", accountCalls.Load())
	}
	_, err = platform.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), "site"), session.BaseURL+"/api/token/", newAPIAuthOptions(session))
	datedUsageREDError(t, err)
	if tokenCalls.Load() != 2 {
		t.Fatalf("token calls must remain blocked while account cost remains allowed: tokens=%d", tokenCalls.Load())
	}
}

func TestSub2APICostFallbackMemoryCoversMissingFieldAndSiteIdentity(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	var fallbackCalls atomic.Int32
	accountPayload := "failure"
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/admin/keys" {
			fallbackCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if accountPayload == "failure" {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			fmt.Fprint(w, accountPayload)
		}
	})
	session := Session{Platform: PlatformSub2API, BaseURL: "https://shared-fallback.test", AccessToken: "fixture-only"}
	_, _, err := platform.fetchCostForDateForSite("site-a", session, "2031-02-03")
	if err == nil || err.Error() != ErrorRequest || fallbackCalls.Load() != 1 {
		t.Fatalf("first unsupported fallback must preserve request error: calls=%d err=%v", fallbackCalls.Load(), err)
	}
	accountPayload = `{"data":{}}`
	session.AccessToken = "rotated-fixture-only"
	_, _, err = platform.fetchCostForDateForSite("site-a", session, "2031-02-03")
	if err == nil || err.Error() != ErrorRequest || fallbackCalls.Load() != 1 {
		t.Fatalf("missing account field after token rotation must respect fallback memory: calls=%d err=%v", fallbackCalls.Load(), err)
	}
	var detail *RequestError
	if errors.As(err, &detail) {
		t.Errorf("remembered fallback must retain the original plain request error: %+v", detail)
	}
	_, _, err = platform.fetchCostForDateForSite("site-b", session, "2031-02-03")
	if err == nil || err.Error() != ErrorRequest || fallbackCalls.Load() != 2 {
		t.Fatalf("same-host different station must have independent fallback memory: calls=%d err=%v", fallbackCalls.Load(), err)
	}
	accountPayload = `{"data":{"total_actual_cost":7}}`
	cost, meta, err := platform.fetchCostForDateForSite("site-a", session, "2031-02-03")
	if err != nil || cost != 7 || meta.Source != "account_level" || fallbackCalls.Load() != 2 {
		t.Fatalf("fallback memory must not block a successful account response: cost=%v meta=%+v calls=%d err=%v", cost, meta, fallbackCalls.Load(), err)
	}
	if strings.Count(logs.String(), "站点不支持管理员 Key 列表回退") != 2 || !strings.Contains(logs.String(), "host=https://shared-fallback.test status=404") {
		t.Errorf("unsupported fallback must log once per site record: %s", logs.String())
	}
}

func TestDatedUsageFailureLogReportsStageAndSafeSite(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	service, cache, _ := failureFixService(t, platform)
	site := newTestSite("log-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "https://safe-date.test", AccessToken: "fixture-only"})
	site.Name = "fixture token=must-never-log"
	cache.add(site)
	_, err := service.KeyUsageForDate(context.Background(), site.UserID, site.AdminAccountID, "2031-02-03")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "日期用量读取失败 site=log-site") || !strings.Contains(logs.String(), "date=2031-02-03 stage=usage reason="+ErrorRateLimited+" status=429") || strings.Contains(logs.String(), "must-never-log") {
		t.Errorf("dated usage failure needs safe station/date/stage/status: %s", logs.String())
	}
}
