package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type c5OverrideTransport struct {
	base     *c5RedTransport
	override func(*http.Request) (*http.Response, error, bool)
}

func (f c5OverrideTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if response, err, handled := f.override(r); handled {
		return response, err
	}
	return f.base.RoundTrip(r)
}
func c5SafeResponse(r *http.Request, status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}
}
func c5UseOverride(s *Service, base *c5RedTransport, override func(*http.Request) (*http.Response, error, bool)) {
	s.platformService = upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: c5OverrideTransport{base, override}}))
}

type c5ThreeGroupRepository struct {
	*testConnRepo
	tx *c5Transaction
}

func (r *c5ThreeGroupRepository) SaveRealConnectionWithPricingMapping(ctx context.Context, conn RealConnection) error {
	if err := saveRealConnectionWithPricingMapping(ctx, conn, r.tx.Begin); err != nil {
		return err
	}
	return r.testConnRepo.SaveRealConnection(ctx, conn)
}

func TestC5ImportThreeGroupsCompleteMainSitePipeline(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, base := c5RedService(t, remote)
	tx := &c5Transaction{t: t}
	s.connRepository = &c5ThreeGroupRepository{testConnRepo: base, tx: tx}
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/groups" {
			return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": []map[string]any{{"id": 7, "name": "live-first", "platform": "openai", "status": "active"}, {"id": 8, "name": "live-second", "platform": "openai", "status": "active"}, {"id": 9, "name": "live-third", "platform": "openai", "status": "active"}}}), nil, true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/22" {
			remote.reads++
			data := map[string]any{}
			for key, value := range remote.created {
				data[key] = value
			}
			data["id"] = 22
			data["groups"] = []map[string]any{{"id": 7, "name": "readback-first"}, {"id": 8, "name": "readback-second"}, {"id": 9, "name": "readback-third"}}
			extra, _ := data["extra"].(map[string]any)
			extra["upstream_billing_probe_enabled"] = data["upstream_billing_probe_enabled"]
			extra["upstream_billing_rate_sync_enabled"] = false
			return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": data}), nil, true
		}
		return nil, nil, false
	})
	req := c5RedRequest(t, "")
	req.OwnGroupIDs = []string{"7", "8", "9", "9"}
	enabled := true
	req.AddToPricingMapping = &enabled
	response, err := s.RealConnect(t.Context(), "user-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || base.saveCalls != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
		t.Fatal("three target groups did not remain one Key/preview/account/readback/binding")
	}
	if !reflect.DeepEqual(remote.created["group_ids"], []any{float64(7), float64(8), float64(9)}) || base.connection == nil || !reflect.DeepEqual(base.connection.OwnGroupIDs, []string{"7", "8", "9"}) || !base.connection.PricingMappingEnabled {
		t.Fatal("three-group request lost full main/local group IDs or pricing selection")
	}
	if response.ConfigurationStatus != "confirmed" || response.Configuration == nil || !reflect.DeepEqual(response.Configuration.OwnGroups, []ImportOwnGroup{{ID: "7", Name: "readback-first"}, {ID: "8", Name: "readback-second"}, {ID: "9", Name: "readback-third"}}) {
		t.Fatal("success summary did not use all three main-site readback group names")
	}
	if tx.commits != 1 || tx.queries != 1 || tx.rollbacks != 0 || len(tx.statements) != 2 || tx.insertArgs[10] != `["7","8","9"]` || tx.insertArgs[17] != true {
		t.Fatal("three-group binding and enabled pricing were not written in one transaction")
	}
	var mappings []GroupMapping
	if json.Unmarshal(tx.mappingJSON, &mappings) != nil || len(mappings) != 3 {
		t.Fatal("three-group import lost atomic pricing mappings")
	}
	seen := map[string]bool{}
	for _, mapping := range mappings {
		seen[mapping.OwnGroup] = true
		if len(mapping.UpstreamTargets) != 1 || mapping.UpstreamTargets[0].SiteID != "site-1" || mapping.UpstreamTargets[0].GroupName != "upstream-only" {
			t.Fatal("three-group atomic pricing mapping lost upstream target")
		}
	}
	if !seen["live-first"] || !seen["live-second"] || !seen["live-third"] {
		t.Fatal("three-group mapping used incomplete selection or readback summary names as local group truth")
	}
}

func TestC5ImportSettingsValidationBeforeResources(t *testing.T) {
	for _, settings := range []string{
		`{"priorityMode":"unknown"}`, `{"priorityMode":"automatic","priority":9}`, `{"priorityMode":"automatic","priority":2147483648}`, `{"priorityMode":"manual","priority":0}`, `{"priorityMode":"manual","priority":10}`, `{"concurrency":0}`, `{"concurrency":1001}`,
	} {
		t.Run(settings, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, settings))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "validation" || !failure.RetryAllowed || remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("invalid settings reached resources or lost safe validation")
			}
		})
	}
	for _, platform := range []string{"", "unknown", "gemini", "antigravity"} {
		t.Run(platform, func(t *testing.T) {
			remote := &c5RedTransport{platform: platform}
			s, repo := c5RedService(t, remote)
			req := c5RedRequest(t, `{"passthrough":true}`)
			_, err := s.RealConnect(t.Context(), "user-1", req)
			if err == nil || remote.keyCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("unknown/unsupported passthrough platform reached resources")
			}
		})
	}
	for _, ids := range [][]string{nil, {"999"}, {"7", "0"}, {"7", "1.5"}, {"7", "9223372036854775808"}} {
		remote := &c5RedTransport{platform: "openai"}
		s, _ := c5RedService(t, remote)
		req := c5RedRequest(t, "")
		req.OwnGroupIDs = ids
		if _, err := s.RealConnect(t.Context(), "user-1", req); err == nil || remote.keyCreates != 0 {
			t.Fatal("invalid group ID reached Key create")
		}
	}
	remote := &c5RedTransport{platform: "openai"}
	s, _ := c5RedService(t, remote)
	req := c5RedRequest(t, "")
	req.GroupType = "anthropic"
	if _, err := s.RealConnect(t.Context(), "user-1", req); err == nil || remote.keyCreates != 0 {
		t.Fatal("cached/requested platform conflict was accepted")
	}
	for _, raw := range []string{`{"priority":1.5}`, `{"concurrency":"50"}`, `{"passthrough":"false"}`} {
		var request RealConnectRequest
		if err := json.Unmarshal([]byte(`{"accountSettings":`+raw+`}`), &request); err == nil {
			t.Fatal("invalid JSON field type bypassed integer/bool decoding")
		}
	}
	settings, err := normalizeImportAccountSettings(nil, "openai")
	if err != nil || settings.Priority != 100 || settings.Concurrency != 50 || settings.Passthrough || !settings.PoolMode || !settings.UpstreamBillingProbeEnabled {
		t.Fatal("default normalization changed")
	}
	if _, err := buildAccountPayload("openai", "x", "x", []int{7}, "n", settings, nil); err == nil {
		t.Fatal("empty non-passthrough mapping was accepted")
	}
}

func TestC5ImportModelTimeoutStopsAccountAndReportsCleanup(t *testing.T) {
	for _, cleanup := range []string{"confirmed", "ambiguous"} {
		t.Run(cleanup, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			previewCalls, cleanupCalls := 0, 0
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts/models/sync-upstream-preview" {
					previewCalls++
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
						t.Fatal("import model preview did not retain the bounded deadline")
					}
					return nil, context.DeadlineExceeded, true
				}
				if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/keys/11" {
					cleanupCalls++
					if cleanup == "ambiguous" {
						return c5SafeResponse(r, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{}}), nil, true
					}
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "model_sync" || failure.Reason != upstream.ErrorNetworkTimeout || failure.UpstreamKeyID != "11" {
				t.Fatal("import timeout lost its model stage, safe cause or known Key ID")
			}
			if remote.keyCreates != 1 || previewCalls != 1 || cleanupCalls != 1 || remote.accountCreates != 0 || repo.saveCalls != 0 || remote.reads != 0 || remote.protectedDeletes != 0 {
				t.Fatal("model timeout created/persisted an account or repeated resource operations")
			}
			if cleanup == "confirmed" {
				if failure.Cleanup != "confirmed" || !failure.RetryAllowed || failure.StatusCode != http.StatusBadGateway {
					t.Fatal("trusted Key cleanup did not produce a retryable model timeout")
				}
			} else if failure.Cleanup != "pending" || failure.RetryAllowed || failure.StatusCode != http.StatusConflict {
				t.Fatal("ambiguous Key cleanup authorized a new operation after timeout")
			}
		})
	}
}

func TestC5ImportKeyAndModelFailuresReportActualCleanup(t *testing.T) {
	for _, mode := range []string{"known-id-no-key", "untrusted-id", "unknown-id", "model-cleanup-confirmed", "model-cleanup-ambiguous", "model-cleanup-failed", "canceled-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", previewFailure: strings.HasPrefix(mode, "model-") || mode == "canceled-cleanup"}
			s, repo := c5RedService(t, remote)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleanupCalls := 0
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method == "POST" && r.URL.Path == "/api/v1/keys" {
					switch mode {
					case "known-id-no-key":
						return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"id": 11}}), nil, true
					case "untrusted-id":
						return c5SafeResponse(r, 200, map[string]any{"code": 1, "data": map[string]any{"id": 11, "key": "synthetic-key"}}), nil, true
					case "unknown-id":
						return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"key": "synthetic-key"}}), nil, true
					}
				}
				if mode == "canceled-cleanup" && strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					cancel()
					return nil, context.Canceled, true
				}
				if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/11" {
					cleanupCalls++
					if r.Context().Err() != nil {
						t.Fatal("cleanup inherited browser cancellation")
					}
					if mode == "model-cleanup-ambiguous" {
						return c5SafeResponse(r, 200, map[string]any{}), nil, true
					}
					if mode == "model-cleanup-failed" {
						return c5SafeResponse(r, 403, map[string]any{"code": "FORBIDDEN"}), nil, true
					}
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || remote.accountCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("failed prerequisite created or persisted account")
			}
			if mode == "untrusted-id" || mode == "unknown-id" {
				if cleanupCalls != 0 || failure.RetryAllowed || failure.StatusCode != 409 {
					t.Fatal("unproved credential was deleted or retryable")
				}
				if mode == "untrusted-id" && failure.UpstreamKeyID != "11" {
					t.Fatal("observed ID lost")
				}
				return
			}
			want := "confirmed"
			if mode == "model-cleanup-ambiguous" {
				want = "pending"
			}
			if mode == "model-cleanup-failed" {
				want = "retained"
			}
			if cleanupCalls != 1 || failure.Cleanup != want || failure.RetryAllowed != (want == "confirmed") || failure.UpstreamKeyID != "11" {
				t.Fatalf("inaccurate cleanup %s retry=%t calls=%d", failure.Cleanup, failure.RetryAllowed, cleanupCalls)
			}
		})
	}
}

type c5OutcomeRepository struct {
	*testConnRepo
	saveError    error
	storeOnError bool
	mutate       func(*RealConnection)
	queryError   error
	queries      int
	replay       *RealConnection
	beforeSave   func()
	checkContext func(context.Context)
}

func (r *c5OutcomeRepository) SaveRealConnectionWithPricingMapping(ctx context.Context, conn RealConnection) error {
	r.saveCalls++
	if r.beforeSave != nil {
		r.beforeSave()
	}
	if r.saveError == nil || r.storeOnError {
		stored := conn
		if r.mutate != nil {
			r.mutate(&stored)
		}
		r.connection = &stored
	}
	return r.saveError
}
func (r *c5OutcomeRepository) GetRealConnection(ctx context.Context, id, user, workspace string) (*RealConnection, error) {
	r.queries++
	if r.checkContext != nil {
		r.checkContext(ctx)
	}
	if r.queryError != nil {
		return nil, r.queryError
	}
	return r.testConnRepo.GetRealConnection(ctx, id, user, workspace)
}
func (r *c5OutcomeRepository) GetRealConnectionByOperationID(context.Context, string, string, string) (*RealConnection, error) {
	return r.replay, nil
}

func TestC5ImportCommitUncertaintyRequiresCompleteBinding(t *testing.T) {
	for _, mode := range []string{"committed", "confirmed-not-committed", "commit-error-after-save", "uncertain-no-record", "uncertain-query-error", "wrong-operation", "wrong-groups", "wrong-account", "wrong-key", "wrong-site", "wrong-group", "wrong-pricing", "wrong-user", "wrong-workspace", "wrong-id", "not-active"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, base := c5RedService(t, remote)
			repo := &c5OutcomeRepository{testConnRepo: base}
			s.connRepository = repo
			if mode != "committed" {
				repo.saveError = errors.New("synthetic commit disconnect")
			}
			if mode == "confirmed-not-committed" {
				repo.saveError = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: repo.saveError}
			} else if mode != "uncertain-no-record" && mode != "committed" {
				repo.storeOnError = true
			}
			if mode == "uncertain-query-error" {
				repo.queryError = errors.New("synthetic query error")
			}
			repo.mutate = func(conn *RealConnection) {
				switch mode {
				case "wrong-operation":
					conn.OperationID = "other"
				case "wrong-groups":
					conn.OwnGroupIDs = []string{"7"}
				case "wrong-account":
					conn.AdminAccountID = "999"
				case "wrong-key":
					conn.UpstreamKeyID = "999"
				case "wrong-site":
					conn.UpstreamSiteID = "other"
				case "wrong-group":
					conn.UpstreamGroupID = "other"
				case "wrong-pricing":
					conn.PricingMappingEnabled = true
				case "wrong-user":
					conn.UserID = "another-user"
				case "wrong-workspace":
					conn.WorkspaceAdminAccountID = "another-workspace"
				case "wrong-id":
					conn.ID = "another-id"
				case "not-active":
					conn.Status = ConnectionStatusMissing
				}
			}
			response, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			if mode == "committed" || mode == "commit-error-after-save" {
				if err != nil || response.ConfigurationStatus != "confirmed" || remote.protectedDeletes != 0 || remote.keyDeletes != 0 {
					t.Fatal("verified commit was compensated or not successful")
				}
				return
			}
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "persistence" {
				t.Fatal("persistence outcome was not returned safely")
			}
			if mode == "confirmed-not-committed" {
				if failure.Cleanup != "confirmed" || !failure.RetryAllowed || remote.protectedDeletes != 1 || remote.keyDeletes != 1 || repo.connection != nil || repo.queries != 0 {
					t.Fatal("confirmed uncommitted transaction did not compensate exactly once")
				}
			} else if failure.Cleanup != "retained" || failure.RetryAllowed || failure.StatusCode != 409 || remote.protectedDeletes != 0 || remote.keyDeletes != 0 || repo.queries != 1 {
				t.Fatal("uncertain or mismatched transaction blindly deleted or reported success")
			}
		})
	}
}

func TestC5ImportReplayUsesCurrentConfigurationBeforeMutableValidation(t *testing.T) {
	for _, mode := range []string{"current-whitelist", "current-unrestricted", "current-passthrough", "current-outside-new-ranges", "unavailable", "different-target"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, base := c5RedService(t, remote)
			original, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			if err != nil {
				t.Fatal(err)
			}
			replay := *base.connection
			repo := &c5OutcomeRepository{testConnRepo: base, replay: &replay}
			s.connRepository = repo
			keyCreates, accountCreates, previews := remote.keyCreates, remote.accountCreates, remote.previews
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path == "/api/v1/admin/groups" {
					t.Fatal("replay revalidated mutable groups")
				}
				if r.Method == "GET" && r.URL.Path == "/api/v1/admin/accounts/22" {
					if mode == "unavailable" {
						return nil, io.EOF, true
					}
					data := map[string]any{"id": 22, "name": "current-renamed", "platform": "openai", "type": "apikey", "priority": 6, "concurrency": 19, "credentials": map[string]any{"pool_mode": false, "model_mapping": map[string]any{"alias-*": "other-model"}}, "extra": map[string]any{"upstream_billing_probe_enabled": false}, "group_ids": []int{9}, "groups": []map[string]any{{"id": 9, "name": "current-group"}}}
					if mode == "current-unrestricted" {
						delete(data["credentials"].(map[string]any), "model_mapping")
					}
					if mode == "current-passthrough" {
						data["extra"].(map[string]any)["openai_passthrough"] = true
					}
					if mode == "current-outside-new-ranges" {
						data["priority"] = 0
						data["concurrency"] = 2000
					}
					return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": data}), nil, true
				}
				return nil, nil, false
			})
			req := c5RedRequest(t, `{"priority":0,"concurrency":0}`)
			req.OwnGroupIDs = nil
			req.GroupType = "unknown"
			if mode == "different-target" {
				req.UpstreamSiteID = "different-site"
			}
			response, err := s.RealConnect(t.Context(), "user-1", req)
			if remote.keyCreates != keyCreates || remote.accountCreates != accountCreates || remote.previews != previews || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("replay created/synced/overwrote/deleted resources")
			}
			if mode == "different-target" {
				var failure *ImportFailure
				if !errors.As(err, &failure) || failure.RetryAllowed || failure.StatusCode != 409 {
					t.Fatal("different replay target reused resource")
				}
				return
			}
			if err != nil || response.Connection.ID != original.Connection.ID {
				t.Fatal("successful replay lost completed fact")
			}
			if mode == "unavailable" {
				if response.ConfigurationStatus != "unavailable" || response.Configuration != nil {
					t.Fatal("unavailable current config masqueraded as synced")
				}
				return
			}
			want := map[string]string{"current-whitelist": "current_whitelist", "current-unrestricted": "current_unrestricted", "current-passthrough": "not_required", "current-outside-new-ranges": "current_whitelist"}[mode]
			wantPriority := 6
			if mode == "current-outside-new-ranges" {
				wantPriority = 0
				if response.Configuration.Concurrency != 2000 {
					t.Fatal("replay imposed new account concurrency range")
				}
			}
			if response.Configuration.Observation != "current" || response.Configuration.ModelState != want || response.Configuration.Priority != wantPriority || response.Configuration.Name != "current-renamed" || !reflect.DeepEqual(response.Configuration.OwnGroups, []ImportOwnGroup{{ID: "9", Name: "current-group"}}) {
				t.Fatal("replay used creation/form snapshot instead of current config")
			}
		})
	}
}

type c5SwitchingResolver struct {
	currentID string
	calls     int
}

func (r *c5SwitchingResolver) RequireCurrentID(context.Context, string) (string, error) {
	r.calls++
	return r.currentID, nil
}

func TestC5ImportRetainsStartWorkspaceAndDetachedCommitVerification(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, base := c5RedService(t, remote)
	resolver := &c5SwitchingResolver{currentID: "admin-1"}
	s.SetAdminAccountResolver(resolver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &c5OutcomeRepository{testConnRepo: base, saveError: errors.New("synthetic uncertain commit"), storeOnError: true, beforeSave: cancel, checkContext: func(check context.Context) {
		if check.Err() != nil {
			t.Fatal("commit reconciliation inherited request cancellation")
		}
		deadline, ok := check.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("commit reconciliation was not bounded to ten seconds")
		}
	}}
	s.connRepository = repo
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
			resolver.currentID = "other-workspace"
		}
		return nil, nil, false
	})
	req := c5RedRequest(t, "")
	enabled := true
	req.AddToPricingMapping = &enabled
	response, err := s.RealConnect(ctx, "user-1", req)
	if err != nil || resolver.calls != 1 || response.WorkspaceAdminAccountID != "admin-1" || base.connection == nil || base.connection.WorkspaceAdminAccountID != "admin-1" || !base.connection.PricingMappingEnabled || repo.queries != 1 || remote.protectedDeletes != 0 || remote.keyDeletes != 0 {
		t.Fatal("request workspace changed or canceled ambiguous commit deleted resources")
	}
}

func TestC5ImportHandlerPropagatesSafeContractAndRejectsNull(t *testing.T) {
	for _, mode := range []string{"model-failure", "persistence-pending", "null-priority", "null-concurrency", "null-switch", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", previewFailure: mode == "model-failure"}
			s, base := c5RedService(t, remote)
			if mode == "persistence-pending" {
				s.connRepository = &c5OutcomeRepository{testConnRepo: base, saveError: errors.New("synthetic-private-credential-value")}
			}
			settings := `{"priorityMode":"automatic","priority":100,"concurrency":50,"passthrough":false,"poolMode":true,"upstreamBillingProbeEnabled":true}`
			switch mode {
			case "null-priority":
				settings = `{"priority":null}`
			case "null-concurrency":
				settings = `{"concurrency":null}`
			case "null-switch":
				settings = `{"passthrough":null}`
			case "unknown-field":
				settings = `{"model_mapping":{"injected":"injected"}}`
			}
			body := `{"upstreamSiteId":"site-1","upstreamGroupId":"70","upstreamGroupName":"upstream-only","ownGroupIds":["7","8"],"accountSettings":` + settings + `}`
			r := httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connect", strings.NewReader(body))
			r = r.WithContext(authctx.WithUserID(r.Context(), "user-1"))
			w := httptest.NewRecorder()
			(&Handler{service: s}).realConnect(w, r)
			var payload map[string]any
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil {
				t.Fatal("handler returned invalid JSON")
			}
			if strings.Contains(w.Body.String(), "synthetic-private-credential-value") || payload["Cause"] != nil || payload["StatusCode"] != nil {
				t.Fatal("handler leaked private cause or internal fields")
			}
			if mode == "unknown-field" {
				if w.Code != 400 || remote.keyCreates != 0 {
					t.Fatal("nested arbitrary model mapping was accepted")
				}
				return
			}
			if strings.HasPrefix(mode, "null-") {
				if w.Code != 400 || payload["stage"] != "validation" || payload["cleanup"] != "not_needed" || payload["retryAllowed"] != true || remote.keyCreates != 0 {
					t.Fatal("explicit null silently defaulted or created resources")
				}
				return
			}
			wantStatus, wantStage, wantCleanup, wantRetry := 502, "model_sync", "confirmed", true
			if mode == "persistence-pending" {
				wantStatus, wantStage, wantCleanup, wantRetry = 409, "persistence", "retained", false
			}
			if w.Code != wantStatus || payload["stage"] != wantStage || payload["cleanup"] != wantCleanup || payload["retryAllowed"] != wantRetry || payload["upstreamKeyId"] != "11" {
				t.Fatal("actual handler lost ImportFailure state or IDs")
			}
		})
	}
}

func TestC5ImportContradictoryCreateEnvelopePreservesIDsWithoutDeletion(t *testing.T) {
	for _, target := range []string{"key", "account"} {
		t.Run(target, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				path, id := "/api/v1/keys", 11
				if target == "account" {
					path, id = "/api/v1/admin/accounts", 22
				}
				if r.Method == http.MethodPost && r.URL.Path == path {
					return c5SafeResponse(r, 200, map[string]any{"code": 0, "success": false, "data": map[string]any{"id": id, "key": "synthetic-secret-must-not-return"}}), nil, true
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.StatusCode != 409 || failure.RetryAllowed || remote.keyDeletes != 0 || remote.protectedDeletes != 0 || repo.saveCalls != 0 {
				t.Fatal("contradictory receipt was retried/deleted/persisted")
			}
			if target == "key" && failure.UpstreamKeyID != "11" || target == "account" && failure.AdminResourceID != "22" {
				t.Fatal("safe observed ID lost")
			}
			encoded, _ := json.Marshal(failure)
			if strings.Contains(string(encoded), "synthetic-secret-must-not-return") {
				t.Fatal("evidence projection leaked secret")
			}
		})
	}
}

func TestC5ImportNewAPIMainCompatibilityUsesOneIdentityAndOriginalPayload(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "settings-rejected"}[unsupported], func(t *testing.T) {
			authCalls, keyCreates, channelCreates := 0, 0, 0
			channelName := ""
			var channelPayload map[string]any
			transport := stageAResourceTransport(func(r *http.Request) (*http.Response, error) {
				var payload any
				switch r.URL.Path {
				case "/api/user/self":
					authCalls++
					payload = map[string]any{"data": map[string]any{"role": 10}}
				case "/api/group/":
					payload = map[string]any{"data": []string{"vip"}}
				case "/api/user/self/groups":
					payload = map[string]any{"data": map[string]any{"vip": 1}}
				case "/api/pricing":
					payload = map[string]any{}
				case "/api/v1/keys":
					keyCreates++
					payload = map[string]any{"data": map[string]any{"id": 11, "key": "legacy-synthetic-key"}}
				case "/api/channel/":
					if r.Method == http.MethodPost {
						channelCreates++
						_ = json.NewDecoder(r.Body).Decode(&channelPayload)
						channel, _ := channelPayload["channel"].(map[string]any)
						channelName, _ = channel["name"].(string)
						payload = map[string]any{"success": true}
					} else {
						payload = map[string]any{"data": []map[string]any{{"id": 44, "name": channelName}}, "total": 1}
					}
				default:
					t.Fatalf("unexpected legacy request %s %s", r.Method, r.URL.Path)
				}
				return c5SafeResponse(r, 200, payload), nil
			})
			admin := platformTestSession(upstream.PlatformNewAPI, "https://legacy-main.invalid")
			source := platformTestSession(upstream.PlatformSub2API, "https://legacy-source.invalid")
			states := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: admin}}
			repo := &testConnRepo{stateRepo: states}
			lookup := testUpstreamLookup{sites: map[string]*upstream.Site{"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "legacy-source", BaseURL: source.BaseURL, Session: &source, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "70", Name: "upstream", Platform: stringPointer("openai")}}}}}}
			s := NewService(states, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
			resolver := &c5SwitchingResolver{currentID: "admin-1"}
			s.SetAdminAccountResolver(resolver)
			s.connRepository = repo
			req := RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "70", UpstreamGroupName: "upstream", OwnGroupIDs: []string{"vip"}}
			if unsupported {
				req.AccountSettings = &ImportAccountSettingsRequest{}
			}
			response, err := s.RealConnect(t.Context(), "user-1", req)
			if authCalls != 1 || resolver.calls != 1 {
				t.Fatal("legacy main added authentication or workspace resolution")
			}
			if unsupported {
				if err == nil || keyCreates != 0 || channelCreates != 0 || repo.connection != nil {
					t.Fatal("NewAPI main accepted account settings or created resources")
				}
				return
			}
			channel, _ := channelPayload["channel"].(map[string]any)
			if err != nil || response.ConfigurationStatus != "not_applicable" || response.Configuration != nil || keyCreates != 1 || channelCreates != 1 || channel["type"] != float64(1) || channel["group"] != "vip" || channel["priority"] != float64(0) || channel["model_mapping"] != "" || channel["base_url"] != source.BaseURL || channel["key"] != "legacy-synthetic-key" || repo.connection == nil {
				t.Fatal("legacy NewAPI main creation payload/behavior changed")
			}
		})
	}
}

// Existing safety/name regression fixtures now expose the exact C5 readback
// contract. They still verify their original deletion and naming outcomes.
func c5SingleAccountFixture(name string) string {
	data := map[string]any{"id": 22, "name": name, "platform": "openai", "type": "apikey", "priority": 100, "concurrency": 50, "credentials": map[string]any{"pool_mode": true, "model_mapping": map[string]string{"live-a": "live-a"}}, "extra": map[string]any{"openai_passthrough": false, "upstream_billing_probe_enabled": true}, "group_ids": []int{7}, "groups": []map[string]any{{"id": 7, "name": "vip"}}}
	encoded, _ := json.Marshal(map[string]any{"code": 0, "data": data})
	return string(encoded)
}
