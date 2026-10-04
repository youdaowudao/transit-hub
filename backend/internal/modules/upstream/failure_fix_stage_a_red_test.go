package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

// These explicitly exercised upstream fixtures use httptest recorders through a
// fake transport: no listener, background polling, real credentials or upstream.
type failureFixTransport struct{ handler http.Handler }

func (f failureFixTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func failureFixPlatform(handler http.HandlerFunc) *PlatformService {
	return NewPlatformService(NewHTTPClient(&http.Client{Transport: failureFixTransport{handler}}))
}

func failureFixCaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func failureFixStringField(err error, name string) string {
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		return ""
	}
	field := reflect.ValueOf(requestErr).Elem().FieldByName(name)
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}

func TestFailureFixStageARequestReasonsPreserveCategories(t *testing.T) {
	failureFixCaptureLogs(t)
	cases := []struct {
		name, method, reason, stage, category string
		platform                              Platform
		status                                int
		body                                  string
		cookie                                bool
	}{
		{"R1_newapi_missing_identity", "password", "loginIncomplete", "login", ErrorAuth, PlatformNewAPI, 200, `{"success":true,"data":{"require_2fa":true}}`, true},
		{"R1b_newapi_rejected_envelope", "password", "loginRejected", "login", ErrorRequest, PlatformNewAPI, 200, `{"success":false,"message":"用户名或密码错误"}`, false},
		{"R2_refresh_rejected", "token", "refreshTokenRejected", "refresh", ErrorAuth, PlatformSub2API, 401, `{"message":"刷新失败"}`, false},
		{"R2b_sub2api_missing_access", "password", "loginIncomplete", "login", ErrorAuth, PlatformSub2API, 200, `{"data":{"require_2fa":true}}`, false},
		{"R6a_key_rejected", "user_key", "accessTokenRejected", "verify", ErrorAuth, PlatformNewAPI, 401, `{"message":"令牌无效"}`, false},
		{"R6b_key_rejected_envelope", "user_key", "accessTokenRejected", "verify", ErrorRequest, PlatformNewAPI, 200, `{"success":false,"message":"令牌无效"}`, false},
		{"forbidden", "password", "forbidden", "login", ErrorRequest, PlatformSub2API, 403, `{"message":"拒绝访问"}`, false},
		{"not_found", "password", "notFound", "login", ErrorRequest, PlatformSub2API, 404, `{"message":"接口不存在"}`, false},
		{"rate_limited", "password", "rateLimited", "login", ErrorRequest, PlatformSub2API, 429, `{"message":"请求过快"}`, false},
		{"server_error", "password", "upstreamServerError", "login", ErrorRequest, PlatformSub2API, 502, `{"message":"服务异常"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.cookie {
					w.Header().Set("Set-Cookie", "fixture-session=fixture; Path=/")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			var err error
			switch tc.method {
			case "token":
				_, err = platform.LoginWithToken("http://fixture.invalid", tc.platform, "fixture-account", "", "fixture-refresh", "Bearer")
			case "user_key":
				_, err = platform.LoginWithUserKey("http://fixture.invalid", "42", "fixture-key")
			default:
				_, err = platform.Login("http://fixture.invalid", tc.platform, "fixture-account", "fixture-password")
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) {
				t.Fatal("expected structured request failure")
			}
			if requestErr.MessageKey != tc.category {
				t.Errorf("coarse category changed: %s", requestErr.MessageKey)
			}
			if failureFixStringField(err, "Reason") != "admin.upstream.errors."+tc.reason {
				t.Error("specific failure reason missing or incorrect")
			}
			if failureFixStringField(err, "Stage") != tc.stage {
				t.Error("failure stage missing or incorrect")
			}
			if requestErr.StatusCode != tc.status {
				t.Errorf("HTTP status=%d, want %d", requestErr.StatusCode, tc.status)
			}
		})
	}
}

func failureFixService(t *testing.T, platform *PlatformService) (*Service, *fakeSiteCache, *enabledTestRepository) {
	t.Helper()
	cache := newFakeSiteCache()
	repository := &enabledTestRepository{}
	service := NewService(platform, repository, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
	t.Cleanup(service.Close)
	return service, cache, repository
}

func failureFixRejectedPlatform() *PlatformService {
	return failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"令牌无效"}`)
	})
}

func TestFailureFixStageACreateRejectedDoesNotPersistR5(t *testing.T) {
	failureFixCaptureLogs(t)
	service, cache, repository := failureFixService(t, failureFixRejectedPlatform())
	_, err := service.Create(context.Background(), "fixture-user", CreateRequest{
		Name: "验收-添加失败", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI,
		AuthMode: AuthModeUserKey, UserID: "42", AccessToken: "fixture-key", RechargeRate: 1,
	})
	if err == nil {
		t.Error("failed login must return an error")
	}
	if len(cache.sites) != 0 || len(repository.saved) != 0 {
		t.Error("failed creation must not write cache or repository")
	}
	if len(service.timers) != 0 {
		t.Error("failed creation must not schedule synchronization")
	}
}

func TestFailureFixStageAUpdateRejectedPreservesAllOriginalValuesR4(t *testing.T) {
	failureFixCaptureLogs(t)
	service, cache, repository := failureFixService(t, failureFixRejectedPlatform())
	previous := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 3, &Session{
		Platform: PlatformNewAPI, BaseURL: "http://original.invalid", UserID: "7", AccessToken: "fixture-old-key",
	})
	previous.Name, previous.BaseURL, previous.Account, previous.Remark = "验收-原站点", "http://original.invalid", "7", "原备注"
	previous.Platform, previous.RequestedPlatform = PlatformNewAPI, PlatformNewAPI
	previous.Metrics = defaultMetrics()
	cache.add(previous)
	_, err := service.Update(context.Background(), "fixture-user", previous.ID, UpdateRequest{
		Name: "验收-修改失败", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI,
		AuthMode: AuthModeUserKey, UserID: "42", AccessToken: "fixture-new-key", RechargeRate: 5, Remark: "新备注",
	})
	if err == nil {
		t.Error("failed relogin must return an error")
	}
	after, _ := cache.Get(context.Background(), previous.ID)
	if !reflect.DeepEqual(previous, after) {
		t.Error("failed update changed original fields, metrics, session or status")
	}
	if len(repository.saved) != 0 {
		t.Error("failed update must not persist replacement values")
	}
}

func TestFailureFixStageAHandlerFailuresAndAutomaticAttempts(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, name := range []string{"R5_create", "R3_auto", "invalid_fields"} {
		t.Run(name, func(t *testing.T) {
			platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/user/login" {
					_, _ = io.WriteString(w, `{"success":false,"message":"用户名或密码错误"}`)
				} else {
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"message":"接口不存在"}`)
				}
			})
			service, cache, repository := failureFixService(t, platform)
			dto := CreateRequest{Name: "验收-接口失败", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, Account: "fixture-account", Password: "fixture-password", RechargeRate: 1}
			if name == "R3_auto" {
				dto.Platform = PlatformAuto
			}
			if name == "invalid_fields" {
				dto = CreateRequest{}
			}
			body, _ := json.Marshal(dto)
			r := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(body))
			r = r.WithContext(authctx.WithUserID(r.Context(), "fixture-user"))
			w := httptest.NewRecorder()
			(&Handler{service: service, accounts: service.accounts}).create(w, r)
			wantStatus := 422
			if name == "invalid_fields" {
				wantStatus = 400
			}
			if w.Code != wantStatus {
				t.Errorf("failure status=%d, want %d", w.Code, wantStatus)
			}
			var payload struct {
				Message string `json:"message"`
				Failure struct {
					ErrorKey string   `json:"errorKey"`
					Fields   []string `json:"fields"`
					Attempts []struct {
						Platform   string `json:"platform"`
						ErrorKey   string `json:"errorKey"`
						Stage      string `json:"stage"`
						HTTPStatus int    `json:"httpStatus"`
					} `json:"attempts"`
				} `json:"failure"`
			}
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil {
				t.Fatal("failure response is not JSON")
			}
			switch name {
			case "invalid_fields":
				if payload.Message != "admin.upstream.errors.invalidFields" || len(payload.Failure.Fields) == 0 {
					t.Error("field validation detail missing")
				}
			case "R3_auto":
				if payload.Message != "admin.upstream.errors.autoDetectFailed" || len(payload.Failure.Attempts) != 2 {
					t.Error("automatic detection lost either attempt")
				} else if payload.Failure.Attempts[0].Platform != "newapi" || payload.Failure.Attempts[0].ErrorKey != "admin.upstream.errors.loginRejected" || payload.Failure.Attempts[1].Platform != "sub2api" || payload.Failure.Attempts[1].HTTPStatus != 404 {
					t.Error("automatic detection attempts are inaccurate")
				}
			default:
				if payload.Message != "admin.upstream.errors.loginRejected" || payload.Failure.ErrorKey != payload.Message {
					t.Error("structured login failure missing")
				}
			}
			for _, marker := range []string{"fixture-password", "fixture-account", "fixture-key"} {
				if strings.Contains(w.Body.String(), marker) {
					t.Error("failure response contains sensitive fixture content")
				}
			}
			if len(cache.sites) != 0 || len(repository.saved) != 0 {
				t.Error("handler failure persisted a site")
			}
		})
	}
}

func TestFailureFixStageASyncPreservesClassificationAndSafeLogsR7(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	service, cache, _ := failureFixService(t, failureFixRejectedPlatform())
	site := newTestSite("fixture-sync", "fixture-user", "fixture-workspace", 1, &Session{
		Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", UserID: "42", AccessToken: "fixture-key",
	})
	site.Name, site.BaseURL = "验收-同步失败", "http://fixture.invalid"
	cache.add(site)
	response, err := service.Sync(context.Background(), site.UserID, site.ID)
	if err != nil {
		t.Fatal("business synchronization failure must retain the existing response contract")
	}
	if response.ErrorKey == nil || *response.ErrorKey != "admin.upstream.errors.accessTokenRejected" {
		t.Error("card does not retain specific credential failure")
	}
	result := syncSiteResult(site.ID, response, nil)
	if result.Status != "auth_failed" || result.ErrorKey != "site_sync_auth" {
		t.Error("specific reason no longer maps to authentication category")
	}
	if err := service.SyncAllStream(context.Background(), site.UserID, func(SyncEvent) {}); err != nil {
		t.Fatal("stream synchronization failed unexpectedly")
	}
	for _, required := range []string{"fixture-sync", "验收-同步失败", "host=http://fixture.invalid", "stage=", "reason=", "status=401"} {
		if !strings.Contains(logs.String(), required) {
			t.Error("synchronization diagnostic metadata is missing")
		}
	}
	if strings.Contains(logs.String(), "err=<nil>") || strings.Contains(logs.String(), "fixture-key") {
		t.Error("synchronization log is contradictory or sensitive")
	}
}

func TestFailureFixStageAUpstreamMessageIsBoundedAndNeverLogged(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	marker := strings.Repeat("Q", 28)
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "用户名或密码错误\n" + marker + strings.Repeat("请检查", 80), "debug": "fixture-body-private"})
	})
	_, err := platform.Login("http://fixture.invalid?fixture-query=private", PlatformNewAPI, "fixture-account", "fixture-password")
	message := failureFixStringField(err, "UpstreamMessage")
	if message == "" || !strings.Contains(message, "用户名或密码错误") {
		t.Error("sanitized upstream explanation missing")
	}
	if len([]rune(message)) > 120 || strings.Contains(message, marker) || strings.Contains(message, "\n") {
		t.Error("upstream message sanitization is insufficient")
	}
	for _, forbidden := range []string{marker, "fixture-password", "fixture-query", "fixture-body-private", "用户名或密码错误"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Error("HTTP diagnostic log contains sensitive or upstream response content")
		}
	}
}
