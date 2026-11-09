package my_sites

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type siteReadLookupFixture struct {
	mu      sync.Mutex
	site    *upstream.Site
	readErr error
}

func (f *siteReadLookupFixture) GetSite(context.Context, string) (*upstream.Site, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneUpstreamSite(f.site), f.readErr
}

func siteReadKeyFixture(t *testing.T, change func(*siteReadLookupFixture)) (*Service, *siteReadLookupFixture, *atomic.Int32) {
	t.Helper()
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture-access"}
	lookup := &siteReadLookupFixture{site: &upstream.Site{ID: "site", UserID: "user", AdminAccountID: "workspace", Platform: upstream.PlatformSub2API, Status: upstream.StatusConnected, Session: &session}}
	calls := &atomic.Int32{}
	transport := scopedRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if change != nil {
			lookup.mu.Lock()
			change(lookup)
			lookup.mu.Unlock()
		}
		item := map[string]any{"id": 7, "name": "fixture-key", "group": map[string]any{"id": 1, "name": "fixture-group"}}
		var payload any = []any{item}
		if r.URL.Path == "/api/v1/keys/7" {
			payload = item
		}
		return c5SafeResponse(r, 200, map[string]any{"data": payload}), nil
	})
	svc := NewService(&testStateRepo{}, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
	return svc, lookup, calls
}

func siteReadKeyCall(t *testing.T, svc *Service, entry string) (int, error) {
	t.Helper()
	switch entry {
	case "detail":
		item, err := svc.GetUpstreamKeyForWorkspace(t.Context(), "user", "workspace", "site", "7")
		if item.ID != "" {
			return 1, err
		}
		return 0, err
	case "until":
		keys, err := svc.ListUpstreamKeysForWorkspaceUntil(t.Context(), "user", "workspace", "site", []string{"7"})
		return len(keys), err
	default:
		keys, err := svc.ListUpstreamKeysForWorkspace(t.Context(), "user", "workspace", "site")
		return len(keys), err
	}
}

func TestSiteReadKeyPauseStopsBeforeAnyRequest(t *testing.T) {
	for _, entry := range []string{"list", "until", "detail"} {
		t.Run(entry, func(t *testing.T) {
			svc, lookup, calls := siteReadKeyFixture(t, nil)
			key := upstream.ErrorRefreshTokenRejected
			lookup.site.Status, lookup.site.ErrorKey = upstream.StatusError, &key
			count, err := siteReadKeyCall(t, svc, entry)
			var detail *upstream.RequestError
			if count != 0 || calls.Load() != 0 || !errors.As(err, &detail) || detail.Reason != key || detail.StatusCode != 0 || !strings.Contains(detail.UpstreamMessage, "本次没有请求上游") {
				t.Fatal("saved authentication refusal sent traffic or hid the local cause")
			}
		})
	}
}

func TestSiteReadKeyLateSuccessCannotEscapeCurrentRejection(t *testing.T) {
	for _, entry := range []string{"list", "until", "detail"} {
		t.Run(entry, func(t *testing.T) {
			svc, _, calls := siteReadKeyFixture(t, func(f *siteReadLookupFixture) {
				key := upstream.ErrorRefreshTokenRejected
				f.site.Status, f.site.ErrorKey = upstream.StatusError, &key
			})
			count, err := siteReadKeyCall(t, svc, entry)
			var detail *upstream.RequestError
			if calls.Load() != 1 || count != 0 || !errors.As(err, &detail) || detail.Reason != upstream.ErrorRefreshTokenRejected || strings.Contains(detail.UpstreamMessage, "本次没有请求上游") || !strings.Contains(detail.UpstreamMessage, "未采用") {
				t.Fatal("a pre-refusal Key request returned old success after the saved refusal")
			}
		})
	}
}

func TestSiteReadKeyCompletionChecksScopeAndLogin(t *testing.T) {
	for _, change := range []string{"deleted", "workspace", "platform", "login", "enabled", "read_failed"} {
		for _, entry := range []string{"list", "until", "detail"} {
			t.Run(change+"/"+entry, func(t *testing.T) {
				svc, _, calls := siteReadKeyFixture(t, func(f *siteReadLookupFixture) {
					switch change {
					case "deleted":
						f.site = nil
					case "workspace":
						f.site.AdminAccountID = "other"
					case "platform":
						f.site.Platform = upstream.PlatformNewAPI
					case "login":
						session := *f.site.Session
						session.AccessToken = "fixture-new-login"
						f.site.Session = &session
					case "enabled":
						enabled := false
						f.site.Enabled = &enabled
					case "read_failed":
						f.readErr = errors.New("fixture lookup unavailable")
					}
				})
				count, err := siteReadKeyCall(t, svc, entry)
				if calls.Load() != 1 || count != 0 || err == nil {
					t.Fatal("changed/unknown current site published an old successful Key result")
				}
			})
		}
	}
}

func TestSiteReadKeyOtherFailuresAndStableDisabledReadsKeepOldBehavior(t *testing.T) {
	for _, entry := range []string{"list", "until", "detail"} {
		t.Run(entry, func(t *testing.T) {
			svc, lookup, calls := siteReadKeyFixture(t, nil)
			key, enabled := upstream.ErrorNetworkTimeout, false
			lookup.site.Status, lookup.site.ErrorKey, lookup.site.Enabled = upstream.StatusError, &key, &enabled
			count, err := siteReadKeyCall(t, svc, entry)
			if err != nil || count != 1 || calls.Load() != 1 {
				t.Fatal("new authentication guard changed an unrelated existing Key read")
			}
		})
	}
}
