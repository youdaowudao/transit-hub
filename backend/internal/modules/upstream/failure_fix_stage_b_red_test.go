package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func failureFixAuthMode(t *testing.T, value any) string {
	t.Helper()
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	field := v.FieldByName("AuthMode")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}

func TestFailureFixStageBLoginModesPersistWithoutExposingCredentials(t *testing.T) {
	failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			w.Header().Set("Set-Cookie", "fixture-session=fixture-cookie; Path=/")
			io.WriteString(w, `{"success":true,"data":{"id":42}}`)
		case "/api/v1/auth/login", "/api/v1/auth/refresh":
			io.WriteString(w, `{"data":{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}}`)
		case "/api/status":
			io.WriteString(w, `{"data":{"quota_per_unit":500000}}`)
		case "/api/v1/groups/available":
			io.WriteString(w, `{"data":[]}`)
		default:
			io.WriteString(w, `{"success":true,"data":{}}`)
		}
	})
	tests := []struct {
		name  string
		login func() (LoginResult, error)
		mode  string
	}{
		{"newapi_password", func() (LoginResult, error) {
			return platform.Login("http://fixture.invalid", PlatformNewAPI, "fixture-account", "fixture-password")
		}, "password"},
		{"sub2api_password", func() (LoginResult, error) {
			return platform.Login("http://fixture.invalid", PlatformSub2API, "fixture-account", "fixture-password")
		}, "password"},
		{"token_with_refresh", func() (LoginResult, error) {
			return platform.LoginWithToken("http://fixture.invalid", PlatformSub2API, "fixture-account", "fixture-access", "fixture-refresh", "Bearer")
		}, "token"},
		{"user_key", func() (LoginResult, error) {
			return platform.LoginWithUserKey("http://fixture.invalid", "42", "fixture-key")
		}, "user_key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.login()
			if err != nil {
				t.Fatal("fixture login failed")
			}
			if failureFixAuthMode(t, result.Session) != tc.mode {
				t.Error("successful session omitted login mode")
			}
			encoded, err := json.Marshal(result.Session)
			if err != nil {
				t.Fatal("session serialization failed")
			}
			var restored Session
			if json.Unmarshal(encoded, &restored) != nil {
				t.Fatal("session restoration failed")
			}
			if failureFixAuthMode(t, restored) != tc.mode {
				t.Error("login mode not preserved in existing JSON session")
			}
			response := toResponse(&Site{Platform: result.Platform, Session: &restored})
			if failureFixAuthMode(t, response) != tc.mode {
				t.Error("list response omitted login mode")
			}
			safe, _ := json.Marshal(response)
			for _, secret := range []string{"fixture-access", "fixture-refresh", "fixture-key", "fixture-cookie", "fixture-password"} {
				if strings.Contains(string(safe), secret) {
					t.Error("response exposed a fixture credential")
				}
			}
			var fields map[string]any
			json.Unmarshal(safe, &fields)
			for _, field := range []string{"session", "password", "accessToken", "refreshToken", "cookie", "adminApiKey"} {
				if _, exists := fields[field]; exists {
					t.Error("response exposed a credential field")
				}
			}
		})
	}
}

func TestFailureFixStageBLegacySessionsInferOriginalCompatibleModes(t *testing.T) {
	tests := []struct {
		name     string
		platform Platform
		session  *Session
		mode     string
	}{
		{"newapi_cookie", PlatformNewAPI, &Session{Platform: PlatformNewAPI, Cookie: "fixture-cookie"}, "password"},
		{"newapi_key", PlatformNewAPI, &Session{Platform: PlatformNewAPI, UserID: "42", AccessToken: "fixture-key"}, "user_key"},
		{"sub2api_token_legacy", PlatformSub2API, &Session{Platform: PlatformSub2API, AccessToken: "fixture-access"}, "password"},
		{"missing_session", PlatformSub2API, nil, "password"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := toResponse(&Site{Platform: tc.platform, Session: tc.session})
			if failureFixAuthMode(t, response) != tc.mode {
				t.Error("legacy session login mode inference missing")
			}
		})
	}
}
