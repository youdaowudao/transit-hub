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

func TestFailureFixStageAHandlerUpdateRejectedAndInvalidFields(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, invalid := range []bool{false, true} {
		service, cache, repository := failureFixService(t, failureFixRejectedPlatform())
		original := newTestSite("fixture-edit", "fixture-user", "fixture-workspace", 2, &Session{Platform: PlatformNewAPI, BaseURL: "http://original.invalid", UserID: "7", AccessToken: "fixture-old"})
		cache.add(original)
		dto := UpdateRequest{Name: "验收-新名称", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "42", AccessToken: "fixture-new", RechargeRate: 3}
		want := http.StatusUnprocessableEntity
		if invalid {
			dto.Name = ""
			want = http.StatusBadRequest
		}
		body, _ := json.Marshal(dto)
		request := httptest.NewRequest(http.MethodPut, "/api/upstream-sites/fixture-edit", bytes.NewReader(body))
		request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
		response := httptest.NewRecorder()
		(&Handler{service: service, accounts: service.accounts}).update(response, request)
		if response.Code != want {
			t.Errorf("update status=%d want=%d", response.Code, want)
		}
		var result struct {
			Message string `json:"message"`
			Failure struct {
				ErrorKey   string   `json:"errorKey"`
				Fields     []string `json:"fields"`
				HTTPStatus int      `json:"httpStatus"`
				Stage      string   `json:"stage"`
			} `json:"failure"`
		}
		if json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatal("update response must be JSON")
		}
		if invalid {
			if result.Message != "admin.upstream.errors.invalidFields" || !reflect.DeepEqual(result.Failure.Fields, []string{"name"}) {
				t.Error("update field validation missing")
			}
		} else if result.Message != "admin.upstream.errors.accessTokenRejected" || result.Failure.ErrorKey != result.Message || result.Failure.HTTPStatus != 401 || result.Failure.Stage != "verify" {
			t.Error("update structured failure missing")
		}
		after, _ := cache.Get(context.Background(), original.ID)
		if !reflect.DeepEqual(original, after) || len(repository.saved) != 0 {
			t.Error("rejected handler update changed the original site")
		}
	}
}

func TestFailureFixStageAHandlerSuccessfulCreateAndUpdateRetainStatus(t *testing.T) {
	failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/login" {
			w.Header().Set("Set-Cookie", "fixture-session=fixture; Path=/")
			_, _ = io.WriteString(w, `{"success":true,"data":{"id":7}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
	})
	service, _, _ := failureFixService(t, platform)
	handler := &Handler{service: service, accounts: service.accounts}
	dto := CreateRequest{Name: "验收-成功", SiteURL: "http://fixture.invalid", Platform: PlatformNewAPI, Account: "fixture-account", Password: "fixture-password", RechargeRate: 1}
	body, _ := json.Marshal(dto)
	request := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(body))
	request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
	response := httptest.NewRecorder()
	handler.create(response, request)
	if response.Code != http.StatusCreated {
		t.Errorf("successful create status=%d", response.Code)
	}
	var created Response
	if json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID == "" || created.Status != StatusConnected {
		t.Fatal("successful create result missing")
	}
	update := UpdateRequest{Name: "验收-修改成功", SiteURL: dto.SiteURL, Platform: dto.Platform, Account: dto.Account, Password: dto.Password, RechargeRate: 2}
	body, _ = json.Marshal(update)
	request = httptest.NewRequest(http.MethodPut, "/api/upstream-sites/"+created.ID, bytes.NewReader(body))
	request = request.WithContext(authctx.WithUserID(request.Context(), "fixture-user"))
	response = httptest.NewRecorder()
	handler.update(response, request)
	if response.Code != http.StatusOK {
		t.Errorf("successful update status=%d", response.Code)
	}
	var updated Response
	if json.Unmarshal(response.Body.Bytes(), &updated) != nil || updated.Name != update.Name || updated.Status != StatusConnected {
		t.Error("successful update result missing")
	}
}

func TestFailureFixStageAHTTPDiagnosticsHideUnapprovedAndEncodedPaths(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	client := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }).httpClient
	for _, sample := range []struct {
		path    string
		allowed bool
	}{
		{"/api/user/self", true}, {"/api/v1/auth/login", true},
		{"/api/private-fixture", false}, {"/api/user/self/private-fixture", false}, {"/api/user/%73elf", false},
	} {
		logs.Reset()
		_, _ = client.requestJSON("http://fixture-account:fixture-password@fixture.invalid"+sample.path+"?private-query=fixture-key", requestOptions{AccessToken: "fixture-key"})
		entry := logs.String()
		if sample.allowed {
			if !strings.Contains(entry, "host=http://fixture.invalid path="+sample.path) {
				t.Error("fixed API diagnostic missing origin or path")
			}
		} else if !strings.Contains(entry, "host=- path=-") || strings.Contains(entry, "fixture.invalid") || strings.Contains(entry, "private-fixture") {
			t.Error("unapproved API diagnostic exposed request information")
		}
		for _, forbidden := range []string{"fixture-password", "fixture-account", "fixture-key", "private-query", "%73"} {
			if strings.Contains(entry, forbidden) {
				t.Error("HTTP diagnostic contains a sensitive request marker")
			}
		}
	}
}

func TestFailureFixStageASanitizerRedactsEchoedCredentialsAndQueries(t *testing.T) {
	hint := safeRequestMessage("连接失败 HTTPS://fixture-account:fixture-password@fixture.invalid/path?private-query=fixture-key password=short key=brief cookie-value", requestOptions{AccessToken: "fixture-key", Cookie: "sid=cookie-value", Body: map[string]string{"password": "fixture-password", "username": "fixture-account"}})
	for _, forbidden := range []string{"fixture-account", "fixture-password", "fixture-key", "private-query", "short", "brief", "cookie-value"} {
		if strings.Contains(hint, forbidden) {
			t.Error("sanitized hint contains a sensitive request marker")
		}
	}
	if !strings.Contains(hint, "连接失败") {
		t.Error("sanitized hint lost its useful explanation")
	}
}
