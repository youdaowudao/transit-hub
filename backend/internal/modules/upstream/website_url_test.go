package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const websiteFixtureAPI = "https://api.fixture.invalid"
const websiteFixtureURL = "https://www.fixture.invalid:8443/console/?view=keys#login"

// Use the public JSON contract so these regressions compile against the old
// code and fail on missing behavior, without starting a server or using tokens.
func websiteJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func websiteSet(t *testing.T, site *Site, value string) {
	t.Helper()
	fields := websiteJSON(t, site)
	fields["websiteUrl"] = value
	raw, _ := json.Marshal(fields)
	if err := json.Unmarshal(raw, site); err != nil {
		t.Fatal(err)
	}
}

func websitePlatform(t *testing.T, calls *[]string) *PlatformService {
	t.Helper()
	return failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.URL.Host+r.URL.Path)
		if r.URL.Host != "api.fixture.invalid" {
			t.Error("system requested the website instead of API")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
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

func websiteUpdate(site *Site) map[string]any {
	return map[string]any{"name": site.Name, "siteUrl": site.BaseURL, "platform": site.Platform, "authMode": siteAuthMode(site), "account": site.Account, "userId": site.Account, "rechargeRate": site.RechargeRate, "remark": site.Remark}
}

func TestWebsiteURLCreateRefreshAndSyncKeepAPI(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	s, cache, repo := failureFixService(t, websitePlatform(t, &calls))
	h := &Handler{service: s, accounts: s.accounts}
	body := map[string]any{"name": "验收-双地址", "siteUrl": websiteFixtureAPI, "websiteUrl": "  " + websiteFixtureURL + "  ", "platform": "sub2api", "authMode": "token", "refreshToken": "fixture-refresh", "rechargeRate": 1}
	r := pinnedRequest(t, h, http.MethodPost, "/api/upstream-sites", body)
	if r.Code != http.StatusCreated {
		t.Fatalf("create status=%d", r.Code)
	}
	created := decodePinnedResponse(t, r)
	if websiteJSON(t, created)["websiteUrl"] != websiteFixtureURL || created.BaseURL != websiteFixtureAPI {
		t.Fatal("creation mixed or lost the two addresses")
	}
	site := cache.sites[created.ID]
	if websiteJSON(t, repo.saved[len(repo.saved)-1])["websiteUrl"] != websiteFixtureURL {
		t.Fatal("website did not persist")
	}
	if site.Session.BaseURL != websiteFixtureAPI || site.Session.RefreshToken != "fixture-rotated" {
		t.Fatal("refresh changed API or lost rotation")
	}
	site.Session.ExpiresAt = nil
	cache.add(site)
	result, err := s.Sync(context.Background(), "fixture-user", site.ID)
	if err != nil {
		t.Fatal("sync failed", err)
	}
	if websiteJSON(t, result)["websiteUrl"] != websiteFixtureURL || result.BaseURL != websiteFixtureAPI {
		t.Fatal("sync lost website or changed API")
	}
	refreshes := 0
	for _, call := range calls {
		if call == "api.fixture.invalid/api/v1/auth/refresh" {
			refreshes++
		}
	}
	if refreshes != 2 {
		t.Fatalf("expected initial and later refresh at API, got %d", refreshes)
	}
	options, err := s.ListOptions(context.Background(), "fixture-user", "fixture-workspace")
	if err != nil || len(options.Items) != 1 || websiteJSON(t, options.Items[0])["websiteUrl"] != websiteFixtureURL || options.Items[0].BaseURL != websiteFixtureAPI {
		t.Fatal("read-only options mixed addresses")
	}
	if len(s.List(context.Background(), "fixture-user")) != 1 {
		t.Fatal("created site missing from list")
	}
	if websiteJSON(t, s.List(context.Background(), "fixture-user")[0])["websiteUrl"] != websiteFixtureURL {
		t.Fatal("list lost website")
	}
	streamSites := 0
	if err := s.SyncAllStream(context.Background(), "fixture-user", func(event SyncEvent) {
		if event.Site != nil {
			streamSites++
			if websiteJSON(t, *event.Site)["websiteUrl"] != websiteFixtureURL || event.Site.BaseURL != websiteFixtureAPI {
				t.Error("stream result mixed addresses")
			}
		}
	}); err != nil || streamSites == 0 {
		t.Fatal("stream did not publish site", err)
	}
}

func TestWebsiteURLMetadataSaveOmitNullClearAndLifecycle(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, mode := range []AuthMode{AuthModeToken, AuthModePassword, AuthModeUserKey} {
		t.Run(string(mode), func(t *testing.T) {
			calls := 0
			s, cache, _ := failureFixService(t, failureFixPlatform(func(http.ResponseWriter, *http.Request) { calls++ }))
			h := &Handler{service: s, accounts: s.accounts}
			site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{AuthMode: mode, Platform: PlatformSub2API, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
			site.BaseURL = websiteFixtureAPI
			site.Account = "7"
			site.Platform = PlatformSub2API
			if mode == AuthModeUserKey {
				site.Platform = PlatformNewAPI
				site.Session.Platform = PlatformNewAPI
				site.Session.UserID = "7"
			}
			site.RequestedPlatform = site.Platform
			cache.add(site)
			original := cloneSite(site)
			for _, value := range []any{websiteFixtureURL, nil, "", "https://other.fixture.invalid/"} {
				body := websiteUpdate(cache.sites[site.ID])
				body["websiteUrl"] = value
				r := pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
				if r.Code != 200 {
					t.Fatalf("metadata status=%d", r.Code)
				}
				want := value
				if value == nil {
					want = websiteFixtureURL
				}
				if websiteJSON(t, decodePinnedResponse(t, r))["websiteUrl"] != want {
					t.Fatal("explicit update/null/clear not respected")
				}
				if !reflect.DeepEqual(cache.sites[site.ID].Session, original.Session) || cache.sites[site.ID].BaseURL != websiteFixtureAPI {
					t.Fatal("metadata changed API login")
				}
			}
			body := websiteUpdate(cache.sites[site.ID])
			r := pinnedRequest(t, h, http.MethodPut, "/api/upstream-sites/"+site.ID, body)
			if r.Code != 200 || websiteJSON(t, decodePinnedResponse(t, r))["websiteUrl"] != "https://other.fixture.invalid/" {
				t.Fatal("old client omitted field and erased website")
			}
			for _, path := range []string{"/pinned", "/enabled", "/settings"} {
				var patch any = map[string]bool{"pinned": true}
				if path == "/enabled" {
					patch = map[string]bool{"enabled": false}
				}
				if path == "/settings" {
					patch = SiteSettings{}
				}
				r = pinnedRequest(t, h, http.MethodPatch, "/api/upstream-sites/"+site.ID+path, patch)
				if r.Code != 200 || websiteJSON(t, decodePinnedResponse(t, r))["websiteUrl"] != "https://other.fixture.invalid/" {
					t.Fatal("local patch lost website", path)
				}
			}
			if calls != 0 {
				t.Fatalf("website-only edits consumed login credentials: %d requests", calls)
			}
		})
	}
}

func TestWebsiteURLInvalidBeforeAnyLoginAndMigration(t *testing.T) {
	failureFixCaptureLogs(t)
	cases := []struct{ value, reason string }{
		{"www.fixture.invalid", "完整域名"}, {"https:///path", "完整域名"}, {"javascript:alert(1)", "只支持"}, {"ftp://fixture.invalid", "只支持"},
		{"https://private-user:private-password@fixture.invalid", "用户名或密码"}, {"https://fixture.invalid/a b", "空格、换行"}, {"https://fixture.invalid/a\nb", "空格、换行"},
		{"https://fixture.invalid:65536", "端口必须"}, {"https://fixture.invalid:abc", "端口必须"},
		{"https://[not-an-ip]/", "完整域名"}, {"https://[127.0.0.1]/", "完整域名"},
		{"https://not-an-ip]/", "完整域名"}, {"https://not[an-ip/", "完整域名"},
		{"https://[::1.]/", "完整域名"}, {"https://[::ffff:127.0.0.1.]/", "完整域名"},
		{"https://[2001:db8:::1]/", "完整域名"}, {"https://256.1.2.3/", "完整域名"},
		{"https://1.2.3.4.5/", "完整域名"},
		{"https://999.1/", "完整域名"}, {"https://host.123/", "完整域名"}, {"https://0x100000000/", "完整域名"},
	}
	for _, tc := range cases {
		t.Run(tc.reason+tc.value[:min(len(tc.value), 8)], func(t *testing.T) {
			calls := 0
			s, cache, repo := failureFixService(t, failureFixPlatform(func(http.ResponseWriter, *http.Request) { calls++ }))
			h := &Handler{service: s, accounts: s.accounts}
			site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
			site.BaseURL = websiteFixtureAPI
			site.Platform = PlatformSub2API
			websiteSet(t, site, websiteFixtureURL)
			cache.add(site)
			original := cloneSite(site)
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				body := websiteUpdate(site)
				body["websiteUrl"] = tc.value
				body["refreshToken"] = "fixture-new-refresh"
				body["siteUrl"] = "https://new-api.fixture.invalid"
				body["confirmDomainMigration"] = true
				if method == http.MethodPost {
					delete(body, "confirmDomainMigration")
				}
				r := pinnedRequest(t, h, method, "/api/upstream-sites/fixture-site", body)
				if r.Code != 400 {
					t.Fatalf("invalid website status=%d", r.Code)
				}
				text := r.Body.String()
				for _, want := range []string{"websiteUrl", `"stage":"check"`, tc.reason, "Refresh Token 未使用", "不会修改主站"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing precise cause/result: %s", want)
					}
				}
				if strings.Contains(text, "private-user") || strings.Contains(text, "private-password") {
					t.Fatal("error exposed URL credentials")
				}
			}
			if calls != 0 || len(repo.saved) != 0 {
				t.Fatal("validation ran after login or persistence")
			}
			if !reflect.DeepEqual(*cache.sites[site.ID], original) {
				t.Fatal("invalid website changed the original site")
			}
		})
	}
}

func TestWebsiteURLPayloadGuardMergeAndLegacy(t *testing.T) {
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: websiteFixtureAPI})
	websiteSet(t, site, websiteFixtureURL)
	if websiteJSON(t, fromPayload(toPayload(site)))["websiteUrl"] != websiteFixtureURL || websiteJSON(t, cloneSite(site))["websiteUrl"] != websiteFixtureURL {
		t.Fatal("cache/clone lost website")
	}
	expected := cloneSite(site)
	changed := cloneSite(site)
	websiteSet(t, &changed, "https://changed.fixture.invalid")
	if unchangedSiteConfiguration(changed, expected) {
		t.Fatal("guard ignored website configuration change")
	}
	merged := mergeExistingSiteChanges(expected, expected, changed)
	if websiteJSON(t, merged)["websiteUrl"] != "https://changed.fixture.invalid" {
		t.Fatal("merge lost website")
	}
	var legacy sitePayload
	if err := json.Unmarshal([]byte(`{"id":"old","baseUrl":"https://api.fixture.invalid"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if websiteJSON(t, toResponse(fromPayload(legacy)))["websiteUrl"] != "" {
		t.Fatal("legacy website must be an explicit empty string")
	}
}

func TestWebsiteURLSyncKeepsConcurrentMetadata(t *testing.T) {
	failureFixCaptureLogs(t)
	var calls []string
	platform := websitePlatform(t, &calls)
	s, cache, _ := failureFixService(t, platform)
	site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
	site.BaseURL = websiteFixtureAPI
	site.Platform = PlatformSub2API
	cache.add(site)
	baseTransport := platform.httpClient.client.Transport
	// Interleave a complete metadata save exactly while the refresh HTTP response
	// is in flight, without goroutines, polling or a listener.
	updated := false
	platform.httpClient.client.Transport = failureFixTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !updated {
			updated = true
			body := websiteUpdate(cache.sites[site.ID])
			body["websiteUrl"] = websiteFixtureURL
			var dto UpdateRequest
			raw, _ := json.Marshal(body)
			_ = json.Unmarshal(raw, &dto)
			if _, err := s.Update(context.Background(), "fixture-user", site.ID, dto); err != nil {
				t.Error("concurrent metadata save failed", err)
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
	if websiteJSON(t, response)["websiteUrl"] != websiteFixtureURL || cache.sites[site.ID].Session.RefreshToken != "fixture-rotated" {
		t.Fatal("sync overwrote website edit or rotated login")
	}
}

func TestWebsiteURLPersistenceFailureKeepsOutcomeAndOriginalValue(t *testing.T) {
	for _, kind := range []string{"database", "commit", "cache"} {
		t.Run(kind, func(t *testing.T) {
			f := newSwitchFixture()
			s, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			websiteSet(t, site, websiteFixtureURL)
			c.add(site)
			r.durable = cloneSite(site)
			dto := switchDTO(site, AuthModeToken)
			dto.RefreshToken = ""
			dto.WebsiteURL = new(string)
			*dto.WebsiteURL = "https://changed.fixture.invalid/"
			want := "本站资料未保存，原数据库资料保留"
			switch kind {
			case "database":
				r.saveError = errors.New("fixture database unavailable")
			case "commit":
				r.saveError = &siteCommitError{err: errors.New("fixture commit response lost")}
				want = "数据库保存结果尚未确认"
			case "cache":
				s.cache = migrationFailingCache{SiteCache: c}
				want = "本站资料已保存，当前运行数据未更新"
			}
			_, err := s.Update(t.Context(), site.UserID, site.ID, dto)
			var detail *RequestError
			if !errors.As(err, &detail) || detail.Stage != "save" || !strings.Contains(detail.UpstreamMessage, want) {
				t.Fatal("failed save falsely reported success/outcome")
			}
			if c.sites[site.ID].WebsiteURL != websiteFixtureURL || c.sites[site.ID].Session.BaseURL != site.BaseURL {
				t.Fatal("failed save published a new browser or API target")
			}
			if kind == "cache" {
				if r.durable.WebsiteURL != *dto.WebsiteURL {
					t.Fatal("confirmed database result lost")
				}
			} else if r.durable.WebsiteURL != websiteFixtureURL {
				t.Fatal("failed database save changed website")
			}
			if f.logins != 0 || len(f.paths) != 0 {
				t.Fatal("website save failure consumed a credential")
			}
		})
	}
}

func TestWebsiteURLValidDestinationKeepsFullValue(t *testing.T) {
	for _, value := range []string{"", "   ", websiteFixtureURL, "http://localhost:0/path/", "http://127.0.0.1/path/", "https://192.168.1.2:8443/path/", "https://[::1]:65535/path/?a=b#c", "https://[::ffff:127.0.0.1]/", "HTTPS://www.fixture.invalid/a/", "http://127.1/", "http://0x7f000001/", "http://0177.0.0.1/", "http://4294967295/", "http://127.0.0.1./"} {
		got, err := normalizeWebsiteURL(value)
		if err != nil || got != strings.TrimSpace(value) {
			t.Errorf("valid browser destination changed or rejected: %q", value)
		}
	}
}

func TestWebsiteURLReloginAndAPIMigrationPreserveWebsite(t *testing.T) {
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "relogin", true: "migration"}[migration], func(t *testing.T) {
			f := newSwitchFixture()
			s, c, r, site := switchSetup(t, f, PlatformSub2API, AuthModeToken, true)
			websiteSet(t, site, websiteFixtureURL)
			c.add(site)
			r.durable = cloneSite(site)
			dto := switchDTO(site, AuthModeToken)
			if migration {
				dto = migrationConfirmedDTO(t, site, AuthModeToken, true)
			}
			response, err := s.Update(t.Context(), site.UserID, site.ID, dto)
			if err != nil {
				t.Fatal(err)
			}
			if response.WebsiteURL != websiteFixtureURL || r.durable.WebsiteURL != websiteFixtureURL || response.BaseURL != dto.SiteURL || r.durable.Session.BaseURL != dto.SiteURL {
				t.Fatal("relogin/API migration mixed or erased website")
			}
		})
	}
}
