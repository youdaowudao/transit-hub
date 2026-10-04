package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestFailureFixStageBRefreshRetainsTokenAndPasswordModes(t *testing.T) {
	failureFixCaptureLogs(t)
	platform := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login", "/api/v1/auth/refresh":
			_, _ = io.WriteString(w, `{"data":{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}}`)
		case "/api/v1/groups/available":
			_, _ = io.WriteString(w, `{"data":[]}`)
		default:
			_, _ = io.WriteString(w, `{"data":{}}`)
		}
	})
	for _, mode := range []string{"password", "token"} {
		var result LoginResult
		var err error
		if mode == "password" {
			result, err = platform.Login("http://fixture.invalid", PlatformSub2API, "fixture-account", "fixture-password")
		} else {
			result, err = platform.LoginWithToken("http://fixture.invalid", PlatformSub2API, "fixture-account", "fixture-access", "fixture-refresh", "Bearer")
		}
		if err != nil {
			t.Fatal("fixture login failed")
		}
		result.Session.ExpiresAt = nil
		refreshed, err := platform.RefreshSession(result.Session)
		if err != nil {
			t.Fatal("fixture refresh failed")
		}
		if failureFixAuthMode(t, refreshed) != mode {
			t.Error("refresh changed the original login mode")
		}
	}
}

func TestFailureFixStageBLegacyJSONOmitsEmptyAuthMode(t *testing.T) {
	encoded, err := json.Marshal(Session{})
	if err != nil {
		t.Fatal("legacy session serialization failed")
	}
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil {
		t.Fatal("legacy session is not JSON")
	}
	if _, exists := fields["AuthMode"]; exists {
		t.Error("empty login mode must remain absent from legacy JSON")
	}
}

func TestFailureFixStageBNewAndPartialCredentialsStillRequireSelectedMode(t *testing.T) {
	for _, mode := range []AuthMode{AuthModePassword, AuthModeToken, AuthModeUserKey} {
		platform := PlatformNewAPI
		if mode == AuthModeToken {
			platform = PlatformSub2API
		}
		create := CreateRequest{Name: "验收-空凭据", SiteURL: "http://fixture.invalid", Platform: platform, AuthMode: mode, Account: "fixture-account", UserID: "42", RechargeRate: 1}
		if validateCreate(create) == nil {
			t.Error("creation accepted empty credentials")
		}
		update := UpdateRequest{Name: create.Name, SiteURL: create.SiteURL, Platform: platform, AuthMode: mode, Account: create.Account, UserID: create.UserID, RechargeRate: 1}
		// A credential for a different mode must not turn metadata editing into an
		// accepted login with the selected mode's mandatory credential missing.
		if mode == AuthModePassword {
			update.AccessToken = "fixture-new"
		} else {
			update.Password = "fixture-new"
		}
		if validateUpdate(update) == nil {
			t.Error("new credentials bypassed selected login mode requirements")
		}
	}
}
