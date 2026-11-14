package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const apiURLFixtureWebsite = "https://www.fixture.invalid"
const apiURLFixturePublished = "https://gateway.fixture.invalid"

// apiURLPlatform answers login and sync at the website address and publishes
// a separate model-call address, like yeclaw (www for login, api for models).
func apiURLPlatform(t *testing.T, calls *[]string, publicStatus int) *PlatformService {
	t.Helper()
	return apiURLPlatformWith(t, calls, publicStatus, apiURLFixturePublished+"/", nil)
}

// apiURLPlatformWith lets a test choose the published value and run a hook
// while the public settings request is in flight.
func apiURLPlatformWith(t *testing.T, calls *[]string, publicStatus int, published string, onPublic func(*http.Request)) *PlatformService {
	t.Helper()
	return failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.URL.Host+r.URL.Path)
		if r.URL.Host != "www.fixture.invalid" {
			t.Errorf("login or sync used %s instead of the website address", r.URL.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/settings/public":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "" || r.URL.User != nil {
				t.Error("public settings read carried credentials")
			}
			if onPublic != nil {
				onPublic(r)
			}
			w.WriteHeader(publicStatus)
			_, _ = io.WriteString(w, `{"code":0,"data":{"site_name":"fixture","api_base_url":"`+published+`"}}`)
		case "/api/v1/auth/refresh", "/api/v1/auth/login":
			_, _ = io.WriteString(w, `{"data":{"access_token":"fixture-access","refresh_token":"fixture-rotated","expires_in":3600}}`)
		case "/api/v1/auth/me":
			_, _ = io.WriteString(w, `{"data":{"id":7,"email":"fixture@example.invalid","balance":10}}`)
		case "/api/v1/groups/available", "/api/v1/keys":
			_, _ = io.WriteString(w, `{"data":[]}`)
		default:
			_, _ = io.WriteString(w, `{"data":{}}`)
		}
	})
}

func apiURLCount(calls []string, path string) int {
	count := 0
	for _, call := range calls {
		if strings.HasSuffix(call, path) {
			count++
		}
	}
	return count
}

func apiURLCreateBody(apiURL any) map[string]any {
	body := map[string]any{"name": "验收-API地址", "siteUrl": apiURLFixtureWebsite, "platform": "sub2api", "authMode": "token", "refreshToken": "fixture-refresh", "rechargeRate": 1}
	if apiURL != nil {
		body["apiUrl"] = apiURL
	}
	return body
}

func TestAPIURLCreateReadsPublishedAddressOnceWhenEmpty(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, cache, repo := failureFixService(t, apiURLPlatform(t, &calls, http.StatusOK))
	h := &Handler{service: s, accounts: s.accounts}
	r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody(""))
	if r.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", r.Code, r.Body.String())
	}
	created := decodePinnedResponse(t, r)
	if created.APIURL != apiURLFixturePublished || created.BaseURL != apiURLFixtureWebsite {
		t.Fatalf("create mixed addresses: api=%q website=%q", created.APIURL, created.BaseURL)
	}
	if repo.saved[len(repo.saved)-1].APIURL != apiURLFixturePublished || cache.sites[created.ID].APIURL != apiURLFixturePublished {
		t.Fatal("published API address was not persisted to storage and cache")
	}
	if cache.sites[created.ID].Session.BaseURL != apiURLFixtureWebsite {
		t.Fatal("login session moved away from the website address")
	}
	if apiURLCount(calls, "/api/v1/settings/public") != 1 {
		t.Fatalf("expected exactly one public settings read, calls=%v", calls)
	}
	if cache.sites[created.ID].ImportBaseURL() != apiURLFixturePublished {
		t.Fatal("import would not use the API address")
	}
}

func TestAPIURLCreateKeepsFilledAddressWithoutReadingUpstream(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, _, repo := failureFixService(t, apiURLPlatform(t, &calls, http.StatusOK))
	h := &Handler{service: s, accounts: s.accounts}
	r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody("  https://manual.fixture.invalid/  "))
	if r.Code != http.StatusCreated {
		t.Fatalf("create status=%d", r.Code)
	}
	if got := decodePinnedResponse(t, r).APIURL; got != "https://manual.fixture.invalid" {
		t.Fatalf("filled API address not kept: %q", got)
	}
	if repo.saved[len(repo.saved)-1].APIURL != "https://manual.fixture.invalid" {
		t.Fatal("filled API address not persisted")
	}
	if apiURLCount(calls, "/api/v1/settings/public") != 0 {
		t.Fatal("filled API address was overwritten by an upstream read")
	}
}

func TestAPIURLPublishedReadFailureStillCreatesAndFallsBack(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, cache, _ := failureFixService(t, apiURLPlatform(t, &calls, http.StatusNotFound))
	h := &Handler{service: s, accounts: s.accounts}
	r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody(nil))
	if r.Code != http.StatusCreated {
		t.Fatalf("public read failure blocked creation: %d", r.Code)
	}
	created := decodePinnedResponse(t, r)
	if created.APIURL != "" || cache.sites[created.ID].ImportBaseURL() != apiURLFixtureWebsite {
		t.Fatal("failed read must leave API empty and import must fall back to the website address")
	}
}

func TestAPIURLUpdateOmitKeepsEmptyReadsAndValueSaves(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, cache, _ := failureFixService(t, apiURLPlatform(t, &calls, http.StatusOK))
	h := &Handler{service: s, accounts: s.accounts}
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{AuthMode: AuthModeToken, Platform: PlatformSub2API, BaseURL: apiURLFixtureWebsite, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
	site.BaseURL, site.Platform, site.RequestedPlatform, site.Account = apiURLFixtureWebsite, PlatformSub2API, PlatformSub2API, "7"
	site.APIURL = "https://saved.fixture.invalid"
	cache.add(site)
	body := websiteUpdate(site)

	r := pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
	if r.Code != http.StatusOK || decodePinnedResponse(t, r).APIURL != "https://saved.fixture.invalid" {
		t.Fatal("old client omitting apiUrl erased the saved API address")
	}
	if len(calls) != 0 {
		t.Fatalf("omitted apiUrl caused upstream requests: %v", calls)
	}

	body["apiUrl"] = "https://other.fixture.invalid/"
	r = pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
	if r.Code != http.StatusOK || decodePinnedResponse(t, r).APIURL != "https://other.fixture.invalid" {
		t.Fatal("filled API address not saved on edit")
	}
	if len(calls) != 0 {
		t.Fatalf("filled apiUrl caused upstream requests: %v", calls)
	}

	body["apiUrl"] = ""
	r = pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
	if r.Code != http.StatusOK || decodePinnedResponse(t, r).APIURL != apiURLFixturePublished {
		t.Fatal("empty API address on edit did not read the published address")
	}
	if len(calls) != 1 || apiURLCount(calls, "/api/v1/settings/public") != 1 {
		t.Fatalf("empty apiUrl must make exactly one public read and no login: %v", calls)
	}
	if cache.sites[site.ID].Session.RefreshToken != "fixture-refresh" || cache.sites[site.ID].BaseURL != apiURLFixtureWebsite {
		t.Fatal("API address edit changed the login session or website address")
	}
}

func TestAPIURLInvalidBeforeAnyLogin(t *testing.T) {
	failureFixCaptureLogs(t)
	cases := []struct{ value, reason string }{
		{"api.fixture.invalid", "完整域名"}, {"ftp://fixture.invalid", "完整域名"}, {"https://fixture.invalid/a b", "空格、换行"},
		{"https://private-user:private-password@fixture.invalid", "用户名或密码"}, {"https:///v1", "完整域名"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			var calls []string
			s, _, repo := failureFixService(t, apiURLPlatform(t, &calls, http.StatusOK))
			h := &Handler{service: s, accounts: s.accounts}
			r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody(tc.value))
			if r.Code != http.StatusBadRequest {
				t.Fatalf("invalid API address status=%d", r.Code)
			}
			text := r.Body.String()
			for _, want := range []string{"apiUrl", "API 地址未保存", tc.reason, "Refresh Token 未使用", "不会修改主站"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing precise cause/result: %s", want)
				}
			}
			if strings.Contains(text, "private-password") {
				t.Fatal("error exposed URL credentials")
			}
			if len(calls) != 0 || len(repo.saved) != 0 {
				t.Fatal("validation ran after login or persistence")
			}
		})
	}
}

func TestAPIURLImportBaseURLFallsBackToWebsite(t *testing.T) {
	site := Site{BaseURL: apiURLFixtureWebsite}
	if site.ImportBaseURL() != apiURLFixtureWebsite {
		t.Fatal("empty API address must fall back to the website address")
	}
	site.APIURL = "  "
	if site.ImportBaseURL() != apiURLFixtureWebsite {
		t.Fatal("blank API address must fall back to the website address")
	}
	site.APIURL = apiURLFixturePublished
	if site.ImportBaseURL() != apiURLFixturePublished {
		t.Fatal("API address must win for imports")
	}
	var nilService *PlatformService
	if nilService.ReadSub2APIPublicAPIURL(context.Background(), apiURLFixtureWebsite) != "" {
		t.Fatal("missing client must read nothing")
	}
}

func TestAPIURLRepositoryAllSavePathsAndRestore(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	site := newTestSite("api-url-site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: apiURLFixtureWebsite, AccessToken: "fixture-access"})
	site.BaseURL = apiURLFixtureWebsite
	site.APIURL = apiURLFixturePublished
	if err := repo.SaveSite(ctx, *site); err != nil {
		t.Fatal(err)
	}
	check := func(want string) Site {
		t.Helper()
		all, err := repo.ListSites(ctx)
		if err != nil || len(all) != 1 {
			t.Fatal("list/restore failed", err)
		}
		own, err := repo.ListSitesForUser(ctx, "user")
		if err != nil || len(own) != 1 {
			t.Fatal("user list failed", err)
		}
		for _, saved := range []Site{all[0], own[0]} {
			if saved.APIURL != want || saved.BaseURL != apiURLFixtureWebsite || saved.Session.BaseURL != apiURLFixtureWebsite {
				t.Fatalf("SQL lost or mixed addresses: api=%q", saved.APIURL)
			}
		}
		return all[0]
	}
	current := check(apiURLFixturePublished)
	current.APIURL = "https://full-save.fixture.invalid"
	if _, err := repo.SaveSiteWithSnapshotVersion(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = check("https://full-save.fixture.invalid")
	candidate := cloneSite(&current)
	candidate.APIURL = "https://conditional.fixture.invalid"
	if ok, err := repo.SaveExistingSiteIfUnchanged(ctx, current, candidate); err != nil || !ok {
		t.Fatal("conditional save", err)
	}
	current = check("https://conditional.fixture.invalid")
	candidate = cloneSite(&current)
	candidate.APIURL = "https://group-save.fixture.invalid"
	if _, _, ok, _, err := repo.SaveExistingSiteWithGroupGuard(ctx, current, candidate, current); err != nil || !ok {
		t.Fatal("group guarded save", err)
	}
	current = check("https://group-save.fixture.invalid")
	stale := current
	stale.APIURL = "https://stale.fixture.invalid"
	if ok, err := repo.SaveExistingSiteIfUnchanged(ctx, stale, stale); err != nil || ok {
		t.Fatal("stale API address accepted", err)
	}
	check("https://group-save.fixture.invalid")
	cache := newFakeSiteCache()
	s := NewService(nil, repo, nil, cache)
	t.Cleanup(s.Close)
	s.SetBackgroundTasksDisabled(true)
	if err := s.RestoreSavedSites(ctx); err != nil {
		t.Fatal(err)
	}
	if cache.sites[site.ID].APIURL != "https://group-save.fixture.invalid" {
		t.Fatal("restore lost API address")
	}
	// Old rows get the empty default and never copy the website address.
	if _, err := pool.Exec(ctx, `ALTER TABLE upstream_sites DROP COLUMN api_url`); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if check("").ImportBaseURL() != apiURLFixtureWebsite {
		t.Fatal("old row must import through the website address")
	}
}

func apiURLEditableSite(cache *fakeSiteCache, apiURL string) *Site {
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{AuthMode: AuthModeToken, Platform: PlatformSub2API, BaseURL: apiURLFixtureWebsite, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
	site.BaseURL, site.Platform, site.RequestedPlatform, site.Account = apiURLFixtureWebsite, PlatformSub2API, PlatformSub2API, "7"
	site.APIURL = apiURL
	cache.add(site)
	return site
}

// Review round 1: a slow published-address read must not overwrite an API
// address that another edit saved meanwhile, whether the read succeeds or fails.
func TestAPIURLConcurrentEditIsNotOverwrittenByPublishedRead(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			failureFixCaptureLogs(t)
			var calls []string
			var cache *fakeSiteCache
			platform := apiURLPlatformWith(t, &calls, status, apiURLFixturePublished, func(*http.Request) {
				cache.sites["fixture-site"].APIURL = "https://manual-b.fixture.invalid"
			})
			s, c, repo := failureFixService(t, platform)
			cache = c
			h := &Handler{service: s, accounts: s.accounts}
			site := apiURLEditableSite(cache, "https://saved.fixture.invalid")
			saves := len(repo.saved)
			body := websiteUpdate(site)
			body["apiUrl"] = ""
			r := pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
			if r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "API 地址在本次保存期间已被其他操作修改") {
				t.Fatalf("stale read was not rejected: status=%d body=%s", r.Code, r.Body.String())
			}
			if cache.sites[site.ID].APIURL != "https://manual-b.fixture.invalid" || len(repo.saved) != saves {
				t.Fatalf("concurrent API address was overwritten: %q", cache.sites[site.ID].APIURL)
			}
		})
	}
}

func TestAPIURLInvalidPortRejectedBeforeLogin(t *testing.T) {
	for _, value := range []string{"https://api.fixture.invalid:65536", "https://api.fixture.invalid:0", "https://api.fixture.invalid:abc"} {
		t.Run(value, func(t *testing.T) {
			failureFixCaptureLogs(t)
			var calls []string
			s, _, repo := failureFixService(t, apiURLPlatform(t, &calls, http.StatusOK))
			h := &Handler{service: s, accounts: s.accounts}
			r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody(value))
			if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "端口必须是 1 到 65535 的数字") {
				t.Fatalf("invalid port status=%d body=%s", r.Code, r.Body.String())
			}
			if len(calls) != 0 || len(repo.saved) != 0 {
				t.Fatal("invalid port reached login or storage")
			}
		})
	}
	if got, err := normalizeImportAPIURL("https://api.fixture.invalid:8443/"); err != nil || got != "https://api.fixture.invalid:8443" {
		t.Fatalf("valid port rejected: %q %v", got, err)
	}
}

func TestAPIURLInvalidPublishedValueFallsBackToWebsite(t *testing.T) {
	for _, published := range []string{"https://api.fixture.invalid:65536", "ftp://api.fixture.invalid", "api.fixture.invalid", "https://user:pass@api.fixture.invalid"} {
		t.Run(published, func(t *testing.T) {
			failureFixCaptureLogs(t)
			var calls []string
			s, cache, _ := failureFixService(t, apiURLPlatformWith(t, &calls, http.StatusOK, published, nil))
			h := &Handler{service: s, accounts: s.accounts}
			r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", apiURLCreateBody(""))
			if r.Code != http.StatusCreated {
				t.Fatalf("invalid published value blocked creation: %d", r.Code)
			}
			created := decodePinnedResponse(t, r)
			if created.APIURL != "" || cache.sites[created.ID].ImportBaseURL() != apiURLFixtureWebsite {
				t.Fatalf("invalid published value was saved: %q", created.APIURL)
			}
		})
	}
}

func TestAPIURLPublishedReadIsBoundedAndCarriesNoCredentials(t *testing.T) {
	failureFixCaptureLogs(t)
	previous := publicAPIURLTimeout
	publicAPIURLTimeout = 50 * time.Millisecond
	t.Cleanup(func() { publicAPIURLTimeout = previous })
	var calls []string
	hasDeadline := false
	platform := apiURLPlatformWith(t, &calls, http.StatusOK, apiURLFixturePublished, func(r *http.Request) {
		deadline, ok := r.Context().Deadline()
		hasDeadline = ok && time.Until(deadline) <= publicAPIURLTimeout
	})
	got := platform.ReadSub2APIPublicAPIURL(context.Background(), "https://private-user:private-pass@www.fixture.invalid/")
	if !hasDeadline {
		t.Fatal("public read had no deadline within the configured timeout")
	}
	if got != apiURLFixturePublished || len(calls) != 1 || calls[0] != "www.fixture.invalid/api/v1/settings/public" {
		t.Fatalf("public read result=%q calls=%v", got, calls)
	}
	// Like net/http's transport, abandon the response once the request deadline passes.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow := NewPlatformService(NewHTTPClient(&http.Client{Transport: apiURLDeadlineTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = io.WriteString(w, `{"data":{"api_base_url":"`+apiURLFixturePublished+`"}}`)
	}}}))
	started := time.Now()
	if got := slow.ReadSub2APIPublicAPIURL(context.Background(), apiURLFixtureWebsite); got != "" || time.Since(started) > 2*time.Second {
		t.Fatalf("slow public read was not cut off: %q after %s", got, time.Since(started))
	}
}

type apiURLDeadlineTransport struct{ handler http.HandlerFunc }

func (f apiURLDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	done := make(chan *http.Response, 1)
	go func() {
		w := httptest.NewRecorder()
		f.handler(w, r)
		done <- w.Result()
	}()
	select {
	case response := <-done:
		return response, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func TestAPIURLSurvivesRedisPayloadRoundTrip(t *testing.T) {
	site := newTestSite("payload-site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: apiURLFixtureWebsite})
	site.BaseURL, site.APIURL = apiURLFixtureWebsite, apiURLFixturePublished
	raw, err := json.Marshal(toPayload(site))
	if err != nil {
		t.Fatal(err)
	}
	var payload sitePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if restored := fromPayload(payload); restored.APIURL != apiURLFixturePublished || restored.BaseURL != apiURLFixtureWebsite {
		t.Fatalf("payload lost or mixed addresses: api=%q", restored.APIURL)
	}
	var old sitePayload
	if err := json.Unmarshal([]byte(`{"id":"payload-site","baseUrl":"`+apiURLFixtureWebsite+`"}`), &old); err != nil {
		t.Fatal(err)
	}
	if restored := fromPayload(old); restored.APIURL != "" || restored.ImportBaseURL() != apiURLFixtureWebsite {
		t.Fatal("old payload must restore an empty API address")
	}
}

const apiURLLifecycleValue = "https://import-api.fixture.invalid"

// Review round 2: every lifecycle path must keep a saved API address that differs
// from the website address, and sync must never read or follow the published one.
func TestAPIURLSyncStreamAndLocalPatchesKeepValue(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, cache, _ := failureFixService(t, websitePlatform(t, &calls))
	h := &Handler{service: s, accounts: s.accounts}
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
	site.BaseURL, site.Platform, site.APIURL = websiteFixtureAPI, PlatformSub2API, apiURLLifecycleValue
	cache.add(site)
	result, err := s.Sync(context.Background(), "fixture-user", site.ID)
	if err != nil || result.APIURL != apiURLLifecycleValue || cache.sites[site.ID].APIURL != apiURLLifecycleValue {
		t.Fatalf("sync lost API address: %q %v", result.APIURL, err)
	}
	streamed := 0
	if err := s.SyncAllStream(context.Background(), "fixture-user", func(event SyncEvent) {
		if event.Site != nil {
			streamed++
			if event.Site.APIURL != apiURLLifecycleValue {
				t.Error("stream result lost API address")
			}
		}
	}); err != nil || streamed == 0 {
		t.Fatal("stream did not publish site", err)
	}
	for _, path := range []string{"/pinned", "/enabled", "/settings"} {
		var patch any = map[string]bool{"pinned": true}
		if path == "/enabled" {
			patch = map[string]bool{"enabled": false}
		}
		if path == "/settings" {
			patch = SiteSettings{}
		}
		r := pinnedRequest(t, h, http.MethodPatch, "/api/upstream-sites/"+site.ID+path, patch)
		if r.Code != http.StatusOK || decodePinnedResponse(t, r).APIURL != apiURLLifecycleValue || cache.sites[site.ID].APIURL != apiURLLifecycleValue {
			t.Fatal("local patch lost API address", path)
		}
	}
	if apiURLCount(calls, "/api/v1/settings/public") != 0 {
		t.Fatalf("sync or local patches read the published address: %v", calls)
	}
}

func TestAPIURLSyncKeepsConcurrentEdit(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	platform := websitePlatform(t, &calls)
	s, cache, _ := failureFixService(t, platform)
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
	site.BaseURL, site.Platform, site.APIURL = websiteFixtureAPI, PlatformSub2API, apiURLLifecycleValue
	cache.add(site)
	baseTransport := platform.httpClient.client.Transport
	updated := false
	platform.httpClient.client.Transport = failureFixTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !updated {
			updated = true
			body := websiteUpdate(cache.sites[site.ID])
			body["apiUrl"] = "https://edited-api.fixture.invalid"
			var dto UpdateRequest
			raw, _ := json.Marshal(body)
			_ = json.Unmarshal(raw, &dto)
			if _, err := s.Update(context.Background(), "fixture-user", site.ID, dto); err != nil {
				t.Error("concurrent API address save failed", err)
			}
		}
		response, err := baseTransport.RoundTrip(r)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})}
	response, err := s.Sync(context.Background(), "fixture-user", site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated || response.APIURL != "https://edited-api.fixture.invalid" || cache.sites[site.ID].APIURL != "https://edited-api.fixture.invalid" {
		t.Fatalf("sync overwrote the concurrent API address edit: %q", response.APIURL)
	}
}

func TestAPIURLReloginAndMigrationPreserveValue(t *testing.T) {
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "relogin", true: "migration"}[migration], func(t *testing.T) {
			f := newSwitchFixture()
			s, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			site.APIURL = apiURLLifecycleValue
			c.add(site)
			r.durable = cloneSite(site)
			dto := switchDTO(site, AuthModeToken)
			if migration {
				dto = migrationConfirmedDTO(t, site, AuthModeToken, true)
			}
			if dto.APIURL != nil {
				t.Fatal("fixture unexpectedly sent apiUrl")
			}
			response, err := s.Update(t.Context(), site.UserID, site.ID, dto)
			if err != nil {
				t.Fatal(err)
			}
			if response.APIURL != apiURLLifecycleValue || r.durable.APIURL != apiURLLifecycleValue || c.sites[site.ID].APIURL != apiURLLifecycleValue || response.BaseURL != dto.SiteURL {
				t.Fatalf("relogin/migration erased or mixed the API address: %q", response.APIURL)
			}
		})
	}
}
