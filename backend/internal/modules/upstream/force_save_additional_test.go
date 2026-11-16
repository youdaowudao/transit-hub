package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

func forceSaveBody() map[string]any {
	return map[string]any{"name": "验收-只存资料", "siteUrl": "http://fixture.invalid", "platform": "auto", "authMode": "token", "refreshToken": "fixture-raw-refresh", "rechargeRate": 1, "forceSave": true}
}
func forceSaveCall(t *testing.T, svc *Service, body map[string]any, user string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(encoded))
	req = req.WithContext(authctx.WithUserID(req.Context(), user))
	w := httptest.NewRecorder()
	(&Handler{service: svc, accounts: svc.accounts}).create(w, req)
	return w
}

func TestForceSaveAdditionalLocalValidationNeverUsesCredentials(t *testing.T) {
	failureFixCaptureLogs(t)
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"empty-name", func(b map[string]any) { b["name"] = "" }},
		{"empty-token", func(b map[string]any) { b["refreshToken"] = "" }},
		{"new-api", func(b map[string]any) { b["platform"] = "newapi" }},
		{"password-mode", func(b map[string]any) {
			b["platform"] = "sub2api"
			b["authMode"] = "password"
			b["account"] = "fixture"
			b["password"] = "fixture-password"
		}},
		{"invalid-url", func(b map[string]any) { b["siteUrl"] = "file:///fixture" }},
		{"invalid-api-url", func(b map[string]any) { b["apiUrl"] = "file:///fixture" }},
		{"invalid-rate", func(b map[string]any) { b["rechargeRate"] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc, cache, repo := failureFixService(t, failureFixPlatform(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
			b := forceSaveBody()
			tc.change(b)
			w := forceSaveCall(t, svc, b, "fixture-user")
			if w.Code != 400 || calls != 0 || len(cache.sites) != 0 || len(repo.saved) != 0 {
				t.Fatalf("invalid save bypassed local validation: status=%d requests=%d cached=%d durable=%d", w.Code, calls, len(cache.sites), len(repo.saved))
			}
		})
	}
}

func TestForceSaveAdditionalCannotCrossWorkspace(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, cache, repo := failureFixService(t, failureFixPlatform(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
	w := forceSaveCall(t, svc, forceSaveBody(), "another-user")
	if w.Code < 400 || calls != 0 || len(cache.sites) != 0 || len(repo.saved) != 0 {
		t.Fatal("save-only bypassed workspace ownership")
	}
}

type forceSaveFailRepo struct {
	enabledTestRepository
	failure error
	writes  int
}

func (r *forceSaveFailRepo) SaveSite(_ context.Context, _ Site) error { r.writes++; return r.failure }

type forceSaveFailCache struct {
	*fakeSiteCache
	setFailure, deleteFailure error
}

func (c *forceSaveFailCache) Set(ctx context.Context, site *Site) error {
	if c.setFailure != nil {
		return c.setFailure
	}
	return c.fakeSiteCache.Set(ctx, site)
}
func (c *forceSaveFailCache) Delete(ctx context.Context, id, user string) error {
	if c.deleteFailure != nil {
		return c.deleteFailure
	}
	return c.fakeSiteCache.Delete(ctx, id, user)
}

func TestForceSaveAdditionalPersistenceFailuresReportActualSaveStage(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, tc := range []struct {
		name                     string
		setErr, deleteErr, dbErr error
		want                     string
		cached                   int
	}{
		{"cache-refused", errors.New("fixture cache unavailable"), nil, nil, "fixture cache unavailable", 0},
		{"database-refused", nil, nil, errors.New("fixture database unavailable"), "fixture database unavailable", 0},
		{"commit-unknown", nil, nil, &siteCommitError{err: errors.New("fixture lost commit reply")}, "未确认", 0},
		{"cleanup-refused", nil, errors.New("fixture cleanup unavailable"), errors.New("fixture database unavailable"), "fixture cleanup unavailable", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			cache := &forceSaveFailCache{fakeSiteCache: newFakeSiteCache(), setFailure: tc.setErr, deleteFailure: tc.deleteErr}
			repo := &forceSaveFailRepo{failure: tc.dbErr}
			svc := NewService(failureFixPlatform(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(401) }), repo, nil, cache)
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
			t.Cleanup(svc.Close)
			w := forceSaveCall(t, svc, forceSaveBody(), "fixture-user")
			var payload struct {
				Failure struct{ Stage, UpstreamMessage string } `json:"failure"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &payload)
			if w.Code != 503 || payload.Failure.Stage != "save" || !strings.Contains(payload.Failure.UpstreamMessage, tc.want) {
				t.Fatalf("save outcome not explained: status=%d %s", w.Code, w.Body.String())
			}
			if requests != 0 || len(cache.sites) != tc.cached || len(svc.timers) != 0 {
				t.Fatal("failed save used upstream, scheduled access or left unexplained cache")
			}
			if tc.setErr != nil && repo.writes != 0 {
				t.Fatal("failed cache write still wrote database")
			}
			if strings.Contains(w.Body.String(), "fixture-raw-refresh") {
				t.Fatal("save failure exposed credentials")
			}
		})
	}
}
