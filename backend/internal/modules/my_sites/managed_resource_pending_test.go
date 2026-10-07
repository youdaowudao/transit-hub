package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func TestStageANewAPIAdminCreateFailureKeepsOriginalKeyRollback(t *testing.T) {
	for _, outcome := range []string{"rejected", "server_error", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			deletedKeys, createdChannels := 0, 0
			transport := stageAResourceTransport(func(req *http.Request) (*http.Response, error) {
				payload, status := `{"data":{"role":10}}`, http.StatusOK
				switch req.URL.Path {
				case "/api/user/self":
				case "/api/group/":
					payload = `{"data":["vip"]}`
				case "/api/user/self/groups":
					payload = `{"data":{"vip":1}}`
				case "/api/pricing":
					payload = `{}`
				case "/api/v1/keys":
					payload = `{"data":{"id":11,"key":"synthetic-test-key"}}`
				case "/api/channel/":
					if req.Method != http.MethodPost {
						t.Fatal("unexpected channel readback")
					}
					createdChannels++
					if outcome == "unknown" {
						return nil, io.EOF
					}
					status = http.StatusInternalServerError
					if outcome == "rejected" {
						status = http.StatusUnauthorized
					}
					payload = `{"code":"UNAUTHORIZED"}`
				case "/api/v1/keys/11":
					if req.Method != http.MethodDelete {
						t.Fatal("unexpected key read")
					}
					deletedKeys++
					payload = `{}`
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
			})
			admin := platformTestSession(upstream.PlatformNewAPI, "http://127.0.0.1:8080")
			source := platformTestSession(upstream.PlatformSub2API, admin.BaseURL)
			states := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: admin}}
			repo := &testConnRepo{stateRepo: states}
			lookup := testUpstreamLookup{sites: map[string]*upstream.Site{"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "source", BaseURL: source.BaseURL, Platform: source.Platform, Session: &source, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "7", Name: "vip", Platform: stringPointer("openai")}}}}}}
			service := NewService(states, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
			service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
			service.connRepository = repo
			_, err := service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"vip"}})
			var requestErr *upstream.RequestError
			var pending *ManagedResourcePendingError
			if !errors.As(err, &requestErr) || errors.As(err, &pending) || deletedKeys != 1 || createdChannels != 1 || repo.connection != nil {
				t.Fatalf("NewAPI create must return original error and roll back key: pending=%t key_deletes=%d creates=%d local=%t", errors.As(err, &pending), deletedKeys, createdChannels, repo.connection != nil)
			}
		})
	}
}

func TestStageAManagedPendingResponseDoesNotRepeatFallbackOrClaimSavedCompensation(t *testing.T) {
	for _, source := range []string{"full_delete", "compensation"} {
		t.Run(source, func(t *testing.T) {
			service, repo, _ := stageAResourceService(t)
			service.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error {
				return errors.New("result unknown")
			}})
			var err error
			message := "admin.mySites.errors.resourcesPendingVerification"
			if source == "full_delete" {
				repo.connection = &RealConnection{ID: "connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", AdminAccountID: "22", UpstreamKeyID: "11", Status: ConnectionStatusActive, ProvisioningMode: ProvisioningModeManaged}
				err = service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: "connection", Mode: "full"})
			} else {
				repo.saveErr = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: errors.New("persist failed")}
				_, err = service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}})
				message = "admin.mySites.errors.compensationPendingVerification"
			}
			response := httptest.NewRecorder()
			writeError(response, err)
			var payload map[string]any
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
				t.Fatal("invalid error response")
			}
			if response.Code != http.StatusConflict || payload["message"] != message || payload["reason"] != "" || payload["adminResourceId"] != "22" || payload["upstreamKeyId"] != "11" {
				t.Fatalf("inaccurate pending response: %#v", payload)
			}
		})
	}
}

func TestStageASentDeletePendingResponseNeverClaimsOperationNotSent(t *testing.T) {
	for _, source := range []string{"full_delete", "compensation"} {
		for _, observation := range []string{"still-visible", "inventory-incomplete", "pre-send-pending"} {
			t.Run(source+"/"+observation, func(t *testing.T) {
				service, repo, deletes := stageAResourceService(t)
				cause := error(errors.New("remote action requires confirmation"))
				if observation != "pre-send-pending" {
					// The safe deletion contract reports a confirmed DELETE followed
					// by an observation that cannot yet settle the persistent claim.
					cause = &upstream.RequestError{MessageKey: cause.Error(), Platform: upstream.PlatformSub2API, MutationOutcome: upstream.MutationConfirmedApplied, Cause: errors.New(observation)}
				}
				safe := &stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error { return cause }}
				service.SetSafeAdminAccountDeletion(safe)
				var err error
				message := "admin.mySites.errors.resourcesPendingVerification"
				if source == "full_delete" {
					repo.connection = &RealConnection{ID: "connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", AdminAccountID: "22", UpstreamKeyID: "11", Status: ConnectionStatusActive, ProvisioningMode: ProvisioningModeManaged}
					err = service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: "connection", Mode: "full"})
				} else {
					repo.saveErr = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: errors.New("persist failed")}
					_, err = service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}})
					message = "admin.mySites.errors.compensationPendingVerification"
				}
				response := httptest.NewRecorder()
				writeError(response, err)
				var payload map[string]any
				if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
					t.Fatal("invalid error response")
				}
				wantReason := ""
				if observation == "pre-send-pending" {
					wantReason = "admin.connectionHealth.errors.remoteActionPending"
				}
				if response.Code != http.StatusConflict || payload["message"] != message || payload["reason"] != wantReason || payload["adminResourceId"] != "22" || payload["upstreamKeyId"] != "11" {
					t.Fatalf("delete pending response misreported sending state: %#v", payload)
				}
				if *deletes != 0 || repo.deleteCalls != 0 || safe.calls != 1 {
					t.Fatal("pending deletion removed key or local connection")
				}
				if (source == "full_delete") != (repo.connection != nil) {
					t.Fatal("pending response misrepresented local persistence")
				}
			})
		}
	}
}

func TestStageAPersistFailureAfterConfirmedAccountDeleteReportsOnlyKeyCleanupUnknown(t *testing.T) {
	service, repo, _ := stageAResourceService(t)
	repo.saveErr = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: errors.New("persist failed")}
	safe := &stageATestSafeDeletion{}
	service.SetSafeAdminAccountDeletion(safe)
	keyDeletes := 0
	service.platformService = upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: stageAResourceTransport(func(req *http.Request) (*http.Response, error) {
		status, payload := http.StatusOK, `{"data":{"role":"admin"}}`
		switch req.URL.Path {
		case "/api/v1/auth/me":
		case "/api/v1/admin/groups":
			payload = `{"data":[{"id":7,"name":"vip","platform":"openai","status":"active"}]}`
		case "/api/v1/keys":
			payload = `{"code":0,"data":{"id":11,"key":"synthetic-test-key"}}`
		case "/api/v1/admin/accounts/models/sync-upstream-preview":
			payload = `{"code":0,"data":{"models":["live-a"]}}`
		case "/api/v1/admin/accounts":
			payload = `{"code":0,"data":{"id":22}}`
		case "/api/v1/admin/accounts/22":
			payload = c5SingleAccountFixture("safe-account")
		case "/api/v1/keys/11":
			if req.Method != http.MethodDelete {
				t.Fatal("unexpected key read")
			}
			keyDeletes++
			status, payload = http.StatusInternalServerError, `{}`
		default:
			t.Fatalf("unexpected fixture request %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	})}))
	_, err := service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}})
	response := httptest.NewRecorder()
	writeError(response, err)
	var payload map[string]any
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatal("invalid error response")
	}
	if keyDeletes != 1 || safe.calls != 1 || repo.connection != nil || response.Code != http.StatusConflict || payload["message"] != "admin.mySites.errors.upstreamKeyCleanupPendingVerification" || payload["reason"] != "" || payload["adminResourceId"] != "22" || payload["upstreamKeyId"] != "11" {
		t.Fatalf("post-delete key cleanup failure reported inaccurate resources: key_deletes=%d admin_deletes=%d local=%t payload=%#v", keyDeletes, safe.calls, repo.connection != nil, payload)
	}
}
