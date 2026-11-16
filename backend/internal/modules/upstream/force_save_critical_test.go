package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

const forceSaveUnverifiedKey = "admin.upstream.errors.savedWithoutVerification"

// Main-agent regression expectations: fake RoundTripper only, no listening port,
// polling, real credentials, database, or upstream. Implementers must not relax these.
func forceSaveRawCreate(t *testing.T, svc *Service, force bool) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"name": "验收-强存", "siteUrl": "http://fixture.invalid/", "platform": "auto", "authMode": "token", "refreshToken": "fixture-fresh-refresh", "tokenType": "Bearer", "rechargeRate": 1, "pinned": true}
	if force {
		body["forceSave"] = true
	}
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/upstream-sites", bytes.NewReader(encoded))
	req = req.WithContext(authctx.WithUserID(req.Context(), "fixture-user"))
	w := httptest.NewRecorder()
	(&Handler{service: svc, accounts: svc.accounts}).create(w, req)
	return w
}

func forceSaveUnverifiedSite() *Site {
	s := newTestSite("force-fixture", "fixture-user", "fixture-workspace", 1, &Session{AuthMode: AuthModeToken, Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", RefreshToken: "fixture-original-refresh", TokenType: "Bearer"})
	s.BaseURL = "http://fixture.invalid"
	s.Platform, s.RequestedPlatform = PlatformSub2API, PlatformAuto
	s.Status, s.Enabled = StatusError, boolPointer(false)
	key := forceSaveUnverifiedKey
	s.ErrorKey, s.LastSyncedAt = &key, nil
	s.Metrics = defaultMetrics()
	return s
}

func forceSaveValidPlatform(t *testing.T, calls *int) *PlatformService {
	t.Helper()
	return failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_, _ = io.WriteString(w, `{"data":{"access_token":"fixture-verified-access","refresh_token":"fixture-verified-refresh","expires_in":3600}}`)
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer fixture-verified-access" {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"message":"旧资料尚未验证"}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"id":7,"balance":0,"total_recharged":0}}`)
		case "/api/v1/usage/dashboard/stats", "/api/v1/usage/stats":
			_, _ = io.WriteString(w, `{"data":{"total_actual_cost":0,"today_actual_cost":0}}`)
		default:
			_, _ = io.WriteString(w, `{"data":[]}`)
		}
	})
}

func TestForceSaveCriticalExplicitCreateSkipsUpstreamAndPersistsUnknown(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, cache, repo := failureFixService(t, failureFixPlatform(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
	w := forceSaveRawCreate(t, svc, true)
	if w.Code != 201 {
		t.Fatalf("explicit save must return 201, got %d: %s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("save-only consumed credentials: %d requests", calls)
	}
	var got Response
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Platform != PlatformSub2API || got.IsEnabled() || got.Status != StatusError || got.ErrorKey == nil || *got.ErrorKey != forceSaveUnverifiedKey || got.LastSyncedAt != nil || got.Metrics.Balance.Value != nil || len(got.Metrics.Groups) != 0 || !got.Pinned {
		t.Fatalf("false connection, enabled, time, balance or platform: %+v", got)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("must persist exactly once, got %d", len(repo.saved))
	}
	saved, _ := cache.Get(t.Context(), got.ID)
	if saved == nil || saved.Session == nil || saved.Session.RefreshToken != "fixture-fresh-refresh" || saved.Session.UserIDConfirmed || saved.Session.UserID != "" || saved.Session.ExpiresAt != nil || saved.APIURL != "http://fixture.invalid" {
		t.Fatalf("saved login input changed or fabricated: %+v", saved)
	}
	if !reflect.DeepEqual(saved.Session, repo.saved[0].Session) {
		t.Fatal("cache and durable credentials differ")
	}
	for _, absent := range []string{"fixture-fresh-refresh", "refreshToken", "RefreshToken", "Session"} {
		if strings.Contains(w.Body.String(), absent) {
			t.Fatalf("response exposes credentials: %s", absent)
		}
	}
	if len(svc.timers) != 0 {
		t.Fatal("save-only scheduled automatic access")
	}
}

func TestForceSaveCriticalNormal401ReasonIsLoggedWithoutCredentialEcho(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	secret := "fixture-fresh-refresh"
	calls := 0
	svc, cache, repo := failureFixService(t, failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "上游拒绝：测试来源限制 token=" + secret, "code": secret})
	}))
	w := forceSaveRawCreate(t, svc, false)
	if w.Code != 422 || calls != 1 || len(repo.saved) != 0 || len(cache.sites) != 0 {
		t.Fatalf("normal failure must remain unpersisted: status=%d calls=%d", w.Code, calls)
	}
	if !strings.Contains(w.Body.String(), "测试来源限制") || !strings.Contains(w.Body.String(), `"httpStatus":401`) {
		t.Fatal("HTTP failure lost the direct upstream reason")
	}
	if !strings.Contains(logs.String(), "测试来源限制") {
		t.Fatal("login log omitted the direct refusal reason")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(w.Body.String(), secret) {
		t.Fatal("upstream echoed credential leaked into log/response")
	}
}

func TestForceSaveCriticalZeroBalanceNormalCreateStillConnects(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, _, _ := failureFixService(t, forceSaveValidPlatform(t, &calls))
	w := forceSaveRawCreate(t, svc, false)
	var got Response
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 201 || got.Status != StatusConnected || got.Metrics.Balance.Value == nil || *got.Metrics.Balance.Value != 0 {
		t.Fatalf("a real zero balance must connect normally: status=%d %+v", w.Code, got)
	}
}

func TestForceSaveCriticalUnverifiedCannotBeEnabledOrRead(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, cache, repo := failureFixService(t, forceSaveValidPlatform(t, &calls))
	original := forceSaveUnverifiedSite()
	cache.add(original)
	if SiteReadAuthError(original) == nil {
		t.Fatal("unverified record is missing the local read guard")
	}
	if _, err := svc.UpdateEnabled(t.Context(), original.UserID, original.ID, true); err == nil {
		t.Fatal("unverified record was enabled")
	}
	if _, err := svc.prepareSiteReadSession(t.Context(), *original, 0, true); err == nil {
		t.Fatal("unverified record attempted login during a read")
	}
	after, _ := cache.Get(t.Context(), original.ID)
	if calls != 0 || len(repo.saved) != 0 || !reflect.DeepEqual(original, after) {
		t.Fatal("read/enable changed unverified data or accessed upstream")
	}
}

func TestForceSaveCriticalMetadataClearAPIAddressUsesNoUpstream(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, cache, _ := failureFixService(t, forceSaveValidPlatform(t, &calls))
	original := forceSaveUnverifiedSite()
	original.APIURL = "https://old-api.invalid"
	cache.add(original)
	empty := ""
	_, err := svc.Update(t.Context(), original.UserID, original.ID, UpdateRequest{Name: "新备注站点", SiteURL: original.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RechargeRate: 1, APIURL: &empty})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := cache.Get(t.Context(), original.ID)
	if calls != 0 || after.APIURL != original.BaseURL || !reflect.DeepEqual(after.Session, original.Session) || after.ErrorKey == nil || *after.ErrorKey != forceSaveUnverifiedKey {
		t.Fatalf("metadata edit accessed upstream or reset verification: calls=%d %+v", calls, after)
	}
}

func TestForceSaveCriticalFirstVerificationEstablishesIdentityOnce(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	svc, cache, _ := failureFixService(t, forceSaveValidPlatform(t, &calls))
	original := forceSaveUnverifiedSite()
	cache.add(original)
	got, err := svc.Update(t.Context(), original.UserID, original.ID, UpdateRequest{Name: original.Name, SiteURL: original.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
	if err != nil {
		t.Fatalf("first validation must not require an old confirmed identity: %v", err)
	}
	after, _ := cache.Get(t.Context(), original.ID)
	if got.Status != StatusConnected || got.IsEnabled() || got.ErrorKey != nil || after.Session.UserID != "7" || !after.Session.UserIDConfirmed || calls == 0 {
		t.Fatalf("identity not established without auto-enabling: %+v", after)
	}
}

func TestForceSaveCriticalFirstVerificationDBFailureKeepsUnverifiedCache(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	original := forceSaveUnverifiedSite()
	repo := &singaporeCostCriticalRepository{durable: cloneSite(original), saveError: errors.New("fixture database unavailable")}
	cache := newFakeSiteCache()
	cache.add(original)
	svc := NewService(forceSaveValidPlatform(t, &calls), repo, nil, cache)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
	t.Cleanup(svc.Close)
	_, err := svc.Update(context.Background(), original.UserID, original.ID, UpdateRequest{Name: original.Name, SiteURL: original.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
	if err == nil || calls < 2 || repo.checks != 1 {
		t.Fatalf("must reach first validation and report database failure: err=%v calls=%d writes=%d", err, calls, repo.checks)
	}
	after, _ := cache.Get(t.Context(), original.ID)
	if !reflect.DeepEqual(original, after) || SiteReadAuthError(after) == nil {
		t.Fatal("failed persistence replaced raw login and bypassed unverified guard")
	}
	beforeRead := calls
	if _, err := svc.UpdateEnabled(t.Context(), original.UserID, original.ID, true); err == nil {
		t.Fatal("DB failure allowed enable")
	}
	if _, err := svc.prepareSiteReadSession(t.Context(), *after, 0, true); err == nil || calls != beforeRead {
		t.Fatal("DB failure allowed read to access upstream")
	}
}

type forceSaveCancelAfterReadCache struct {
	*fakeSiteCache
	cancel    context.CancelFunc
	cancelled bool
}

func (c *forceSaveCancelAfterReadCache) Get(ctx context.Context, id string) (*Site, error) {
	site, err := c.fakeSiteCache.Get(ctx, id)
	if !c.cancelled {
		c.cancelled = true
		c.cancel()
	}
	return site, err
}

func TestForceSaveCriticalCancelledBeforeFirstVerificationNeverConsumesNewToken(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache := &forceSaveCancelAfterReadCache{fakeSiteCache: newFakeSiteCache(), cancel: cancel}
	original := forceSaveUnverifiedSite()
	cache.add(original)
	repo := &enabledTestRepository{}
	svc := NewService(forceSaveValidPlatform(t, &calls), repo, nil, cache)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"fixture-user": "fixture-workspace"}})
	t.Cleanup(svc.Close)
	_, err := svc.Update(ctx, original.UserID, original.ID, UpdateRequest{Name: original.Name, SiteURL: original.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
	after, _ := cache.Get(t.Context(), original.ID)
	if !errors.Is(err, context.Canceled) || calls != 0 || len(repo.saved) != 0 || !reflect.DeepEqual(after, original) {
		t.Fatalf("cancelled request consumed token or mutated data: err=%v upstream=%d writes=%d", err, calls, len(repo.saved))
	}
}
