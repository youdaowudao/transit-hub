package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

type review3WriteCountingCache struct {
	*fakeSiteCache
	writes int
}

func (c *review3WriteCountingCache) Set(ctx context.Context, site *Site) error {
	c.writes++
	return c.fakeSiteCache.Set(ctx, site)
}

func TestFailureFixReview3SyncKeepsBaselineCategory(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, tc := range []struct {
		name, category, reason string
		status                 int
		platform               Platform
		cookie                 bool
	}{
		{"key_401", ErrorAuth, ErrorAccessTokenRejected, 401, PlatformNewAPI, false},
		{"key_business_rejection", ErrorRequest, "", 200, PlatformNewAPI, false},
		{"cookie_401", ErrorAuth, ErrorAuth, 401, PlatformNewAPI, true},
		{"refresh_401", ErrorAuth, ErrorRefreshTokenRejected, 401, PlatformSub2API, false},
		{"refresh_403", ErrorRequest, ErrorForbidden, 403, PlatformSub2API, false},
		{"refresh_404", ErrorRequest, ErrorNotFound, 404, PlatformSub2API, false},
		{"refresh_429", ErrorRequest, ErrorRateLimited, 429, PlatformSub2API, false},
		{"refresh_502", ErrorRequest, ErrorUpstreamServer, 502, PlatformSub2API, false},
		{"refresh_business_rejection", ErrorRequest, "", 200, PlatformSub2API, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/api/user/self"
				if tc.platform == PlatformSub2API {
					wantPath = "/api/v1/auth/refresh"
				}
				if r.URL.Path != wantPath {
					t.Errorf("unexpected request path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"success":false,"message":"fixture rejection"}`)
			})
			service, cache, _ := failureFixService(t, platform)
			session := &Session{Platform: tc.platform, BaseURL: "http://fixture.invalid", AuthMode: AuthModeUserKey, UserID: "42", AccessToken: "fixture-access"}
			if tc.cookie {
				session.Cookie, session.AccessToken, session.AuthMode = "fixture-session=fixture", "", AuthModePassword
			}
			if tc.platform == PlatformSub2API {
				session.RefreshToken, session.AuthMode = "fixture-refresh", AuthModeToken
			}
			site := newTestSite("fixture-sync", "fixture-user", "fixture-workspace", 1, session)
			site.Platform = tc.platform
			cache.add(site)
			response, err := service.Sync(context.Background(), site.UserID, site.ID)
			if err != nil || response.ErrorKey == nil {
				t.Fatalf("missing synchronization response: %v", err)
			}
			if errorCategory(*response.ErrorKey) != tc.category {
				t.Errorf("card reason %s maps to %s, want baseline %s", *response.ErrorKey, errorCategory(*response.ErrorKey), tc.category)
			}
			if tc.reason != "" && *response.ErrorKey != tc.reason {
				t.Errorf("card reason=%s want=%s", *response.ErrorKey, tc.reason)
			}
			want := syncSiteResult(site.ID, Response{Status: StatusError, ErrorKey: &tc.category}, nil)
			for _, result := range []SyncSiteResult{syncSiteResult(site.ID, response, nil), service.syncSiteForWorkspace(context.Background(), site.UserID, site.AdminAccountID, site.ID, false)} {
				if result != want {
					t.Errorf("sync classification=%+v, want baseline %+v", result, want)
				}
			}
		})
	}
}

func TestFailureFixReview3ModeSwitchRequiresCredentialsWithoutWrites(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, tc := range []struct {
		platform         Platform
		oldMode, newMode AuthMode
		legacy           bool
	}{
		{PlatformNewAPI, AuthModePassword, AuthModeUserKey, false},
		{PlatformNewAPI, AuthModeUserKey, AuthModePassword, false},
		{PlatformSub2API, AuthModePassword, AuthModeToken, false},
		{PlatformSub2API, AuthModeToken, AuthModePassword, false},
		{PlatformNewAPI, AuthModePassword, AuthModeUserKey, true},
		{PlatformSub2API, AuthModePassword, AuthModeToken, true},
	} {
		t.Run(string(tc.platform)+"_"+string(tc.oldMode)+"_"+string(tc.newMode)+map[bool]string{true: "_legacy"}[tc.legacy], func(t *testing.T) {
			calls := 0
			platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				calls++
				t.Error("empty credentials must not send a login request")
			})
			service, cache, repository := failureFixService(t, platform)
			countingCache := &review3WriteCountingCache{fakeSiteCache: cache}
			service.cache = countingCache
			session := &Session{Platform: tc.platform, AuthMode: tc.oldMode, BaseURL: "http://fixture.invalid", AccessToken: "fixture-original"}
			if tc.oldMode == AuthModePassword && tc.platform == PlatformNewAPI {
				session.Cookie = "fixture-session=fixture"
			}
			if tc.legacy {
				session.AuthMode = ""
			}
			original := newTestSite("fixture-edit", "fixture-user", "fixture-workspace", 1, session)
			original.Platform, original.Account = tc.platform, "fixture-account"
			before, _ := json.Marshal(original)
			cache.add(original)
			dto := UpdateRequest{Name: "验收-切换方式", SiteURL: session.BaseURL, Platform: tc.platform, AuthMode: tc.newMode, Account: "fixture-account", UserID: "42", RechargeRate: 1, Password: " ", AccessToken: " ", RefreshToken: " "}
			body, _ := json.Marshal(dto)
			request := httptest.NewRequest(http.MethodPut, "/api/upstream-sites/fixture-edit", bytes.NewReader(body))
			request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
			response := httptest.NewRecorder()
			(&Handler{service: service, accounts: service.accounts}).update(response, request)
			if response.Code != http.StatusBadRequest {
				t.Errorf("mode switch status=%d, want 400", response.Code)
			}
			var result struct {
				Message string `json:"message"`
				Failure struct {
					Fields []string `json:"fields"`
				} `json:"failure"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &result)
			field := "accessToken"
			if tc.newMode == AuthModePassword {
				field = "password"
			}
			if result.Message != ErrorInvalidFields || !reflect.DeepEqual(result.Failure.Fields, []string{field}) {
				t.Errorf("missing credential validation: %+v", result)
			}
			after, _ := cache.Get(context.Background(), original.ID)
			afterJSON, _ := json.Marshal(after)
			if !bytes.Equal(before, afterJSON) || countingCache.writes != 0 || len(repository.saved) != 0 || len(service.timers) != 0 || calls != 0 {
				t.Error("rejected mode switch changed original fields/session/status or scheduled work")
			}
		})
	}
}

func TestFailureFixReview3KeyUsageBusinessRejectionRetainsRetry(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, platformType := range []Platform{PlatformNewAPI, PlatformSub2API} {
		for _, recover := range []bool{true, false} {
			t.Run(string(platformType)+map[bool]string{true: "_recover", false: "_budget"}[recover], func(t *testing.T) {
				calls := 0
				platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "stats") || strings.Contains(r.URL.Path, "/stat") {
						calls++
						if calls == 1 || !recover {
							_, _ = io.WriteString(w, `{"success":false,"message":"fixture temporary rejection"}`)
							return
						}
						if platformType == PlatformNewAPI {
							_, _ = io.WriteString(w, `{"success":true,"data":{"quota":250000}}`)
						} else {
							_, _ = io.WriteString(w, `{"data":{"total_actual_cost":2.5}}`)
						}
						return
					}
					if r.URL.Path != "/api/token/" && r.URL.Path != "/api/v1/keys" {
						t.Errorf("unexpected usage path %s", r.URL.Path)
					}
					_, _ = io.WriteString(w, `{"data":[{"id":1,"name":"fixture-key","group":"vip"}],"total":1}`)
				})
				stats, err := platform.FetchKeyUsageToday(Session{Platform: platformType, BaseURL: "http://fixture.invalid", AccessToken: "fixture-access", UserID: "42", QuotaPerUnit: 100000}, nil)
				if calls != keyUsageRequestAttempts {
					t.Errorf("business rejection attempts=%d, want %d", calls, keyUsageRequestAttempts)
				}
				if recover && (err != nil || len(stats) != 1 || stats[0].TodayAmount != 2.5) {
					t.Errorf("recovered usage=%+v err=%v", stats, err)
				}
				if !recover && err == nil {
					t.Error("persistent business rejection must keep unknown cost/failure")
				}
			})
		}
	}
}

func TestFailureFixReview3ModeSwitchSuccessReplacesSession(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, tc := range []struct {
		platform         Platform
		oldMode, newMode AuthMode
		refreshOnly      bool
	}{
		{PlatformNewAPI, AuthModePassword, AuthModeUserKey, false},
		{PlatformNewAPI, AuthModeUserKey, AuthModePassword, false},
		{PlatformSub2API, AuthModePassword, AuthModeToken, false},
		{PlatformSub2API, AuthModePassword, AuthModeToken, true},
		{PlatformSub2API, AuthModeToken, AuthModePassword, false},
	} {
		t.Run(string(tc.platform)+"_"+string(tc.newMode)+map[bool]string{true: "_refresh_only"}[tc.refreshOnly], func(t *testing.T) {
			platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/user/login":
					w.Header().Set("Set-Cookie", "fixture-new-session=fixture; Path=/")
					_, _ = io.WriteString(w, `{"success":true,"data":{"id":42}}`)
				case "/api/v1/auth/login", "/api/v1/auth/refresh":
					_, _ = io.WriteString(w, `{"data":{"access_token":"fixture-new-access","refresh_token":"fixture-new-refresh","expires_in":3600}}`)
				case "/api/user/self/groups", "/api/v1/groups/available":
					_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
				default:
					_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
				}
			})
			service, cache, repository := failureFixService(t, platform)
			original := newTestSite("fixture-success", "fixture-user", "fixture-workspace", 1, &Session{Platform: tc.platform, AuthMode: tc.oldMode, AccessToken: "fixture-old-access", Cookie: "fixture-old-cookie"})
			original.Platform, original.Account = tc.platform, "fixture-account"
			cache.add(original)
			dto := UpdateRequest{Name: "验收-切换成功", SiteURL: "http://fixture.invalid", Platform: tc.platform, AuthMode: tc.newMode, Account: "fixture-account", UserID: "42", RechargeRate: 1}
			switch tc.newMode {
			case AuthModePassword:
				dto.Password = "fixture-new-password"
			case AuthModeUserKey:
				dto.AccessToken = "fixture-new-key"
			case AuthModeToken:
				if tc.refreshOnly {
					dto.RefreshToken = "fixture-refresh"
				} else {
					dto.AccessToken = "fixture-new-access"
				}
			}
			response, err := service.Update(context.Background(), original.UserID, original.ID, dto)
			if err != nil {
				t.Fatalf("valid mode switch failed: %v", err)
			}
			after, _ := cache.Get(context.Background(), original.ID)
			if response.AuthMode != tc.newMode || after.Session.AuthMode != tc.newMode || after.Status != StatusConnected || len(repository.saved) != 1 {
				t.Error("successful mode switch did not replace and persist the session with selected mode")
			}
			if after.Session.AccessToken == "fixture-old-access" || after.Session.Cookie == "fixture-old-cookie" {
				t.Error("mode switch retained original authentication credentials")
			}
			account := dto.Account
			if tc.newMode == AuthModeUserKey {
				account = dto.UserID
			}
			if after.Account != account || response.Account != account {
				t.Error("display account differs from selected login identity")
			}
		})
	}
}
