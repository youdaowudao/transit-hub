package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

// These business expectations were fixed by the primary agent before C5 implementation.
// The transport never starts a listener or contacts an external site.
type c5RedTransport struct {
	t                                                                         *testing.T
	platform                                                                  string
	previewFailure                                                            bool
	configurationMismatch                                                     bool
	secondGroupPlatform                                                       string
	keyCreates, accountCreates, previews, reads, keyDeletes, protectedDeletes int
	created                                                                   map[string]any
}

// Review regression: a failed HTTP receipt with an observed resource ID is
// evidence of an uncertain mutation, not proof that nothing was created.
// Primary-agent expectations: preserve IDs, no deletion/new attempt, no success.
func TestC5REDFailedCreationReceiptPreservesObservedIDsWithoutCleanup(t *testing.T) {
	for _, target := range []string{"sub2api-key", "newapi-key", "main-account"} {
		for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusOK} {
			t.Run(fmt.Sprintf("%s/http-%d", target, status), func(t *testing.T) {
				remote := &c5RedTransport{platform: "openai"}
				s, repo := c5RedService(t, remote)
				path, id, stage := "/api/v1/keys", 11, "upstream_key"
				if target == "newapi-key" {
					path = "/api/token/"
					lookup := s.upstreamLookup.(testUpstreamLookup)
					site := lookup.sites["site-1"]
					session := platformTestSession(upstream.PlatformNewAPI, site.BaseURL)
					site.Platform, site.Session = upstream.PlatformNewAPI, &session
				} else if target == "main-account" {
					path, id, stage = "/api/v1/admin/accounts", 22, "account_create"
				}
				creates, deletes, otherAfterFailure := 0, 0, 0
				failed := false
				c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
					if r.Method == http.MethodDelete {
						deletes++
					}
					if failed {
						otherAfterFailure++
					}
					if r.Method == http.MethodPost && r.URL.Path == path {
						creates++
						failed = true
						return c5SafeResponse(r, status, map[string]any{
							"code": "FORBIDDEN", "success": false,
							"data": map[string]any{"id": id, "key": "synthetic-sensitive-key-must-not-leak"},
						}), nil, true
					}
					return nil, nil, false
				})
				_, err := s.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
				var failure *ImportFailure
				if !errors.As(err, &failure) {
					t.Fatal("failed creation was not represented by the real safe import contract")
				}
				if failure.UpstreamKeyID != "11" || target == "main-account" && failure.AdminResourceID != "22" {
					t.Errorf("observed resource ID lost: key=%q account=%q", failure.UpstreamKeyID, failure.AdminResourceID)
				}
				if failure.Stage != stage || failure.StatusCode != 409 || failure.Cleanup != "pending" || failure.RetryAllowed {
					t.Errorf("contradictory receipt was trusted as no mutation: stage=%s status=%d cleanup=%s retry=%t", failure.Stage, failure.StatusCode, failure.Cleanup, failure.RetryAllowed)
				}
				if creates != 1 || deletes != 0 || otherAfterFailure != 0 || remote.protectedDeletes != 0 || repo.saveCalls != 0 {
					t.Errorf("uncertain creation caused deletion, retry, readback or persistence: creates=%d deletes=%d subsequent=%d protected=%d saves=%d", creates, deletes, otherAfterFailure, remote.protectedDeletes, repo.saveCalls)
				}
				encoded, _ := json.Marshal(failure)
				if strings.Contains(string(encoded), "synthetic-sensitive-key-must-not-leak") {
					t.Fatal("safe import failure exposed the upstream credential")
				}
			})
		}
	}
}

func (f *c5RedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	status := http.StatusOK
	var value any
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
		value = map[string]any{"code": 0, "data": map[string]any{"role": "admin"}}
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/groups":
		secondPlatform := f.platform
		if f.secondGroupPlatform != "" {
			secondPlatform = f.secondGroupPlatform
		}
		value = map[string]any{"code": 0, "data": []map[string]any{
			{"id": 7, "name": "first-live-group", "platform": f.platform, "status": "active"},
			{"id": 8, "name": "second-live-group", "platform": secondPlatform, "status": "active"},
		}}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys":
		f.keyCreates++
		value = map[string]any{"code": 0, "data": map[string]any{"id": 11, "key": "synthetic-c5-key"}}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts/models/sync-upstream-preview":
		f.previews++
		if f.accountCreates != 0 {
			f.t.Error("model preview must precede account creation")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		if body["platform"] != f.platform || body["type"] != "apikey" || body["base_url"] != "https://c5-source.invalid" || body["api_key"] != "synthetic-c5-key" {
			f.t.Errorf("preview did not use the captured upstream credential and platform")
		}
		if _, exists := body["model_mapping"]; exists {
			f.t.Error("preview must not reuse a static mapping")
		}
		if f.previewFailure {
			status = http.StatusForbidden
			value = map[string]any{"code": 403, "message": "forbidden"}
		} else {
			value = map[string]any{"code": 0, "data": map[string]any{"models": []string{"live-z", " live-a ", "live-z"}, "warnings": []string{"capability metadata incomplete"}}}
		}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts":
		f.accountCreates++
		if err := json.NewDecoder(r.Body).Decode(&f.created); err != nil {
			f.t.Fatal(err)
		}
		value = map[string]any{"code": 0, "data": map[string]any{"id": 22}}
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/22":
		f.reads++
		data := make(map[string]any)
		for k, v := range f.created {
			data[k] = v
		}
		data["id"] = 22
		data["groups"] = []map[string]any{{"id": 7, "name": "readback-first"}, {"id": 8, "name": "readback-second"}}
		extra, _ := data["extra"].(map[string]any)
		if extra == nil {
			extra = map[string]any{}
		}
		extra["upstream_billing_probe_enabled"] = data["upstream_billing_probe_enabled"]
		extra["upstream_billing_rate_sync_enabled"] = false
		data["extra"] = extra
		if f.configurationMismatch {
			data["priority"] = 999
		}
		value = map[string]any{"code": 0, "data": data}
	case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/keys/11":
		f.keyDeletes++
		value = map[string]any{"code": 0, "data": map[string]any{"message": "deleted"}}
	default:
		f.t.Errorf("unexpected C5 remote call: %s %s", r.Method, r.URL.Path)
		status = http.StatusNotFound
		value = map[string]any{"code": 404}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
}

func c5RedService(t *testing.T, transport *c5RedTransport) (*Service, *testConnRepo) {
	t.Helper()
	transport.t = t
	session := platformTestSession(upstream.PlatformSub2API, "https://c5-main.invalid")
	sourceSession := platformTestSession(upstream.PlatformSub2API, "https://c5-source.invalid")
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: session}}
	repo := &testConnRepo{stateRepo: stateRepo}
	lookup := testUpstreamLookup{sites: map[string]*upstream.Site{"site-1": {
		ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "C5-source", BaseURL: sourceSession.BaseURL,
		Platform: upstream.PlatformSub2API, Session: &sourceSession,
		Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "70", Name: "upstream-only", Platform: stringPointer(transport.platform)}}},
	}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
	service.connRepository = repo
	service.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(ctx context.Context, user, workspace string, session upstream.Session, id, source string) error {
		if user != "user-1" || workspace != "admin-1" || id != "22" || source != "compensate_delete" {
			t.Fatal("compensation bypassed the captured identity or original protection")
		}
		transport.protectedDeletes++
		return nil
	}})
	return service, repo
}

func c5RedRequest(t *testing.T, settings string) RealConnectRequest {
	t.Helper()
	var req RealConnectRequest
	body := `{"upstreamSiteId":"site-1","upstreamGroupId":"70","upstreamGroupName":"upstream-only","ownGroupIds":["7","8","7"],"operationId":"c5-fixed-red","addToPricingMapping":false`
	if settings != "" {
		body += `,"accountSettings":` + settings
	}
	if err := json.Unmarshal([]byte(body+`}`), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestC5REDDefaultsAndAllGroupsSavedAtMainSite(t *testing.T) {
	for _, platform := range []string{"openai", "anthropic", "gemini", "antigravity"} {
		t.Run(platform, func(t *testing.T) {
			remote := &c5RedTransport{platform: platform}
			service, repo := c5RedService(t, remote)
			response, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
			if err != nil {
				t.Fatalf("default import failed: %v", err)
			}
			if remote.created["concurrency"] != float64(50) {
				t.Errorf("new %s account concurrency must be 50, got %v", platform, remote.created["concurrency"])
			}
			if remote.created["priority"] != float64(100) {
				t.Errorf("automatic initial priority must be 100")
			}
			credentials, _ := remote.created["credentials"].(map[string]any)
			if credentials["pool_mode"] != true || remote.created["upstream_billing_probe_enabled"] != true {
				t.Error("pool and top-level declared billing probe must default on")
			}
			extra, _ := remote.created["extra"].(map[string]any)
			if platform == "openai" || platform == "anthropic" {
				if extra[platform+"_passthrough"] != false {
					t.Error("passthrough must explicitly default off")
				}
			}
			mapping, _ := credentials["model_mapping"].(map[string]any)
			if !reflect.DeepEqual(mapping, map[string]any{"live-a": "live-a", "live-z": "live-z"}) {
				t.Errorf("nonempty exact live identity mapping must be saved at main site, got %v", mapping)
			}
			if !reflect.DeepEqual(remote.created["group_ids"], []any{float64(7), float64(8)}) {
				t.Error("all selected group IDs must reach the single account")
			}
			if remote.keyCreates != 1 || remote.accountCreates != 1 || remote.previews != 1 || remote.reads != 1 {
				t.Errorf("expected one Key/preview/account/readback: %d/%d/%d/%d", remote.keyCreates, remote.previews, remote.accountCreates, remote.reads)
			}
			if repo.connection == nil || !reflect.DeepEqual(repo.connection.OwnGroupIDs, []string{"7", "8"}) {
				t.Error("local binding must retain every group")
			}
			encoded, _ := json.Marshal(response)
			var public map[string]any
			_ = json.Unmarshal(encoded, &public)
			if public["configurationStatus"] != "confirmed" || public["workspaceAdminAccountId"] != "admin-1" {
				t.Error("success must include confirmed configuration and captured workspace")
			}
		})
	}
}

func TestC5REDExplicitFalseAndEditableValuesReachMainSite(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	service, _ := c5RedService(t, remote)
	_, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, `{"priorityMode":"manual","priority":4,"concurrency":23,"passthrough":true,"poolMode":false,"upstreamBillingProbeEnabled":false}`))
	if err != nil {
		t.Fatalf("edited import failed: %v", err)
	}
	credentials, _ := remote.created["credentials"].(map[string]any)
	extra, _ := remote.created["extra"].(map[string]any)
	if remote.created["priority"] != float64(4) || remote.created["concurrency"] != float64(23) || credentials["pool_mode"] != false || remote.created["upstream_billing_probe_enabled"] != false || extra["openai_passthrough"] != true {
		t.Error("edited numbers and explicit false values must be saved without default substitution")
	}
	if remote.previews != 0 {
		t.Error("passthrough on must skip model preview")
	}
	if _, exists := credentials["model_mapping"]; exists {
		t.Error("passthrough on must not manufacture a whitelist")
	}
}

func TestC5REDModelFailureCreatesZeroAccountsAndReportsActualCleanup(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", previewFailure: true}
	service, repo := c5RedService(t, remote)
	_, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
	if err == nil {
		t.Error("model sync failure must fail import")
	}
	if remote.accountCreates != 0 || repo.saveCalls != 0 {
		t.Error("model failure must create zero main accounts and no successful local binding")
	}
	if remote.keyCreates != 1 || remote.keyDeletes != 1 {
		t.Error("failed preview must clean only the Key created by this operation")
	}
	if err != nil {
		encoded, _ := json.Marshal(err)
		var failure map[string]any
		_ = json.Unmarshal(encoded, &failure)
		if failure["stage"] != "model_sync" || failure["cleanup"] != "confirmed" || failure["retryAllowed"] != true {
			t.Errorf("failure must report trusted model stage and confirmed cleanup: %s", encoded)
		}
	}
}

func TestC5REDWrongGroupTypeRejectedBeforeRemoteCreation(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", secondGroupPlatform: "anthropic"}
	service, repo := c5RedService(t, remote)
	_, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
	if err == nil || remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
		t.Error("mixed-type target IDs must be rejected before creating any resource")
	}
}

func TestC5REDReadbackMismatchCannotPersistSuccess(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", configurationMismatch: true}
	service, repo := c5RedService(t, remote)
	_, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
	if err == nil || repo.saveCalls != 0 {
		t.Error("main-site configuration mismatch must never persist or return successful binding")
	}
	if remote.reads != 1 || remote.protectedDeletes != 1 || remote.keyDeletes != 1 {
		t.Error("mismatch must use readback and existing protected account-then-Key compensation")
	}
}
