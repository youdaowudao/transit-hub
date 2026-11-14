package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 单独客户赠送：按客户查某天消费，与总营收同一口径（新加坡日期、total_actual_cost）。
func TestFetchSub2APIAdminUserUsageCostUsesUserFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/usage/stats" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		for key, want := range map[string]string{
			"user_id":    "123",
			"start_date": "2026-11-13",
			"end_date":   "2026-11-13",
			"timezone":   "Asia/Singapore",
		} {
			if got := r.URL.Query().Get(key); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 60.25}})
	}))
	defer server.Close()

	cost, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminUserUsageCost(
		Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"}, "123", "2026-11-13", "2026-11-13",
	)
	if err != nil || cost != 60.25 {
		t.Fatalf("cost=%v err=%v, want 60.25", cost, err)
	}
}

// 缺少消费金额或金额为负时报错（带实际状态码和区分原因），不能当成 0。
func TestFetchSub2APIAdminUserUsageCostRejectsMissingOrNegativeCost(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"missing":  {"data": map[string]any{}},
		"negative": {"data": map[string]any{"total_actual_cost": -1}},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, payload)
			}))
			defer server.Close()
			cost, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminUserUsageCost(
				Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"}, "123", "2026-11-13", "2026-11-13",
			)
			var requestErr *RequestError
			if err == nil || cost != 0 || !errors.As(err, &requestErr) {
				t.Fatalf("cost=%v err=%v, want request error", cost, err)
			}
			wantReason := GiftUsageMissingCost
			if name == "negative" {
				wantReason = GiftUsageNegativeCost
			}
			if requestErr.MessageKey != ErrorInvalidResponse || requestErr.StatusCode != http.StatusOK || requestErr.Reason != wantReason {
				t.Fatalf("error = %+v, want invalid response with status 200 and reason %s", requestErr, wantReason)
			}
		})
	}
}

func TestFetchSub2APIAdminUserUsageCostRejectsOtherPlatforms(t *testing.T) {
	_, err := NewPlatformService(NewHTTPClient(http.DefaultClient)).FetchSub2APIAdminUserUsageCost(
		Session{Platform: PlatformNewAPI, BaseURL: "http://127.0.0.1:1", AccessToken: "token"}, "123", "2026-11-13", "2026-11-13",
	)
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.MessageKey != ErrorUnsupported {
		t.Fatalf("err = %v, want unsupported", err)
	}
}

// 单独客户赠送核对用户时比较返回编号：主站以数字或字符串返回编号，解析后都应等于查询的十进制写法（含最大可接受编号）。
func TestFetchSub2APIAdminUserKeepsGiftLedgerUserID(t *testing.T) {
	for name, tc := range map[string]struct {
		id   any
		want string
	}{
		"number":          {123, "123"},
		"string":          {"123", "123"},
		"max safe":        {int64(9007199254740991), "9007199254740991"},
		"max safe as str": {"9007199254740991", "9007199254740991"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"data": map[string]any{"id": tc.id, "email": "a@example.com", "balance": 1.5}})
			}))
			defer server.Close()
			user, err := NewPlatformService(NewHTTPClient(server.Client())).FetchSub2APIAdminUser(
				Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"}, tc.want,
			)
			if err != nil || user.ID != tc.want {
				t.Fatalf("id=%q err=%v, want %q", user.ID, err, tc.want)
			}
		})
	}
}
