package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestAccountGroupsOnlyWritesGroupFields(t *testing.T) {
	calls := 0
	s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Path != "/api/v1/admin/accounts/bulk-update" {
			t.Fatalf("unexpected route: %s %s", req.Method, req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"account_ids": []any{float64(15)}, "group_ids": []any{float64(2), float64(3)}}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("payload=%v want=%v", body, want)
		}
		return stageAMutationResponse(req, 200, map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 15, "success": true}}}})
	})
	err := s.UpdateSub2APIAdminAccountGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "fixture"}, "15", []string{"2", "3"})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestAccountGroupsInvalidIDsNeverSend(t *testing.T) {
	for _, tc := range []struct {
		account string
		groups  []string
	}{{"15", nil}, {"15", []string{}}, {"0", []string{"2"}}, {"oops", []string{"2"}}, {"15", []string{"0"}}, {"15", []string{"-1"}}, {"15", []string{"no"}}} {
		t.Run(tc.account+strings.Join(tc.groups, ","), func(t *testing.T) {
			calls := 0
			s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected transport")
			})
			err := s.UpdateSub2APIAdminAccountGroupsContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "fixture"}, tc.account, tc.groups)
			if err == nil || RemoteMutationOutcome(err) != MutationNotSent || calls != 0 {
				t.Fatalf("calls=%d outcome=%s", calls, RemoteMutationOutcome(err))
			}
		})
	}
}

func TestAccountGroupsPreservesSanitizedFailureAndLegacyError(t *testing.T) {
	session := Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Cookie: "synthetic-cookie", AdminAPIKey: "synthetic-admin"}
	s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		return stageAMutationResponse(req, 200, map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 15, "success": false, "error": "主站拒绝 synthetic-access synthetic-refresh synthetic-cookie synthetic-admin " + strings.Repeat("长", 400)}}}})
	})
	err := s.UpdateSub2APIAdminAccountGroupsContext(t.Context(), session, "15", []string{"2"})
	var remote *RequestError
	if !errors.As(err, &remote) || !strings.Contains(remote.UpstreamMessage, "主站拒绝") || len([]rune(remote.UpstreamMessage)) > 300 || RemoteMutationOutcome(err) != MutationUncertain {
		t.Fatalf("missing safe per-account reason: %v", err)
	}
	for _, secret := range []string{session.AccessToken, session.RefreshToken, session.Cookie, session.AdminAPIKey} {
		if strings.Contains(remote.UpstreamMessage, secret) {
			t.Fatal("credential was not masked")
		}
	}
	oldErr := s.UpdateSub2APIAdminAccountStatusContext(t.Context(), session, "15", "active")
	var legacy *RequestError
	if !errors.As(oldErr, &legacy) || legacy.MessageKey != ErrorInvalidResponse || legacy.UpstreamMessage != "" {
		t.Fatalf("legacy failure changed: %v", oldErr)
	}
}

func TestAccountGroupsMixedChannelHint(t *testing.T) {
	s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		return stageAMutationResponse(req, 409, map[string]any{"code": "mixed_channel_warning", "message": "Anthropic 与 Antigravity 不能混合 synthetic-access"})
	})
	err := s.UpdateSub2APIAdminAccountGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "synthetic-access"}, "15", []string{"2"})
	var remote *RequestError
	if !errors.As(err, &remote) || !strings.Contains(remote.UpstreamMessage, "Anthropic") || strings.Contains(remote.UpstreamMessage, "synthetic-access") {
		t.Fatalf("mixed channel hint missing or unsafe: %v", err)
	}
}
