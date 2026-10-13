package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func TestImportWarningPreservesEightPlatformsAndManualAndAutomaticSettings(t *testing.T) {
	for _, platform := range []string{"openai", "anthropic", "gemini", "antigravity", "kimi", "zhipu", "deepseek", "minimax"} {
		for _, mode := range []string{"manual", "automatic"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				remote := &c5RedTransport{platform: platform, previewFailure: true}
				s, repo := c5RedService(t, remote)
				priority := 7
				if mode == "automatic" {
					priority = 456
				}
				req := c5RedRequest(t, fmt.Sprintf(`{"priorityMode":%q,"priority":%d,"concurrency":93,"passthrough":false,"poolMode":false,"upstreamBillingProbeEnabled":false}`, mode, priority))
				req.GroupType = platform
				response, err := s.RealConnect(t.Context(), "user-1", req)
				if err != nil || response.Configuration == nil || response.Configuration.Platform != platform || response.Configuration.Priority != priority || response.Configuration.Concurrency != 93 || response.Configuration.PoolMode || response.Configuration.UpstreamBillingProbeEnabled || response.Configuration.Passthrough || response.Configuration.ModelState != "not_synced" || !equalImportIDSets(repo.connection.OwnGroupIDs, []string{"7", "8"}) || repo.connection.PricingMappingEnabled {
					t.Fatal("warning changed selected platform, explicit settings, groups or mapping choice")
				}
				if remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || repo.saveCalls != 1 || remote.keyDeletes != 0 {
					t.Fatal("warning changed import flow")
				}
			})
		}
	}
}

func TestImportDiagnosticReasonsUseStructuredCodeAndActualPeerStatus(t *testing.T) {
	for _, sample := range []struct{ code, reason string }{
		{"FORBIDDEN", upstream.ErrorForbidden}, {"INSUFFICIENT_BALANCE", upstream.ErrorUpstreamInsufficientBalance},
		{"API_KEY_QUOTA_EXHAUSTED", upstream.ErrorUpstreamKeyQuotaExhausted}, {"INSUFFICIENT_QUOTA", upstream.ErrorUpstreamKeyQuotaExhausted},
		{"API_KEY_EXPIRED", upstream.ErrorUpstreamKeyExpired}, {"ANNOUNCEMENT_ACK_REQUIRED", upstream.ErrorAnnouncementAckRequired},
	} {
		t.Run(sample.code, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, _ := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					return c5SafeResponse(r, 403, map[string]any{"code": sample.code, "message": "请查看此步骤记录"}), nil, true
				}
				return nil, nil, false
			})
			response, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			if err != nil || len(response.Diagnostics) != 1 || response.Diagnostics[0].Reason != sample.reason || response.Diagnostics[0].HTTPStatus != 403 || response.Diagnostics[0].Target != "main" {
				t.Fatal("reason guessed from status or actual main status was lost")
			}
		})
	}
}

func TestImportInvalidModelWarningsNameExactKnownCategoryAndDiscardWholeList(t *testing.T) {
	for _, sample := range []struct {
		name, label string
		payload     any
	}{
		{"envelope", "code、success 或 data", map[string]any{"code": 0, "data": []any{}}},
		{"models", "models", map[string]any{"code": 0, "data": map[string]any{"models": "invalid"}}},
		{"non-string", "非字符串", map[string]any{"code": 0, "data": map[string]any{"models": []any{"valid-first", 7}}}},
		{"blank", "空模型", map[string]any{"code": 0, "data": map[string]any{"models": []any{"valid-first", " "}}}},
		{"wildcard", "通配符", map[string]any{"code": 0, "data": map[string]any{"models": []any{"valid-first", "invalid-*"}}}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					return c5SafeResponse(r, 200, sample.payload), nil, true
				}
				return nil, nil, false
			})
			response, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
			if err != nil || response.Configuration == nil || response.Configuration.ModelState != "not_synced" || len(response.Configuration.Models) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].HTTPStatus != 200 || !strings.Contains(response.Diagnostics[0].Detail, sample.label) || repo.saveCalls != 1 || remote.keyDeletes != 0 {
				t.Fatal("known invalid model category lost exact explanation or leaked partial models into import")
			}
			credentials := remote.created["credentials"].(map[string]any)
			if len(credentials["model_mapping"].(map[string]any)) != 0 {
				t.Fatal("invalid list preserved its valid prefix")
			}
		})
	}
}

type importObservedRepo struct {
	*testStateRepo
	trace     *[]string
	loadError error
	ctx       context.Context
}

func (r *importObservedRepo) Get(ctx context.Context, user, workspace string) (*State, error) {
	*r.trace = append(*r.trace, "get")
	if ctx != r.ctx {
		return nil, errors.New("repository context changed")
	}
	if r.loadError != nil {
		return nil, r.loadError
	}
	return r.testStateRepo.Get(ctx, user, workspace)
}
func (r *importObservedRepo) Save(ctx context.Context, state State) error {
	*r.trace = append(*r.trace, "save")
	if ctx != r.ctx {
		return errors.New("repository context changed")
	}
	return r.testStateRepo.Save(ctx, state)
}

func TestImportSessionFailureObserverRetainsCauseWithoutChangingLegacySequence(t *testing.T) {
	cases := []struct {
		mode, message, target string
		status                int
		trace                 []string
	}{
		{"load", ErrorUnknown, "local", 0, []string{"get"}},
		{"refresh-network", ErrorImportSessionRefreshFailed, "main", 0, []string{"get", "refresh"}},
		{"refresh-500", ErrorImportSessionRefreshFailed, "main", 500, []string{"get", "refresh"}},
		{"refresh-401", ErrorImportSessionRefreshFailed, "main", 401, []string{"get", "refresh"}},
		{"refresh-sql", ErrorImportSessionRefreshFailed, "main", 500, []string{"get", "refresh"}},
		{"verify-network", ErrorImportAdminVerificationFailed, "main", 0, []string{"get", "verify"}},
		{"verify-403", ErrorImportAdminVerificationFailed, "main", 403, []string{"get", "verify"}},
		{"save", ErrorImportSessionSaveFailed, "local", 0, []string{"get", "refresh", "save"}},
	}
	for _, sample := range cases {
		t.Run(sample.mode, func(t *testing.T) {
			var oldTrace, importTrace []string
			var oldError string
			for _, observed := range []bool{false, true} {
				sqlFixtureInjected := false
				trace := &oldTrace
				if observed {
					trace = &importTrace
				}
				remote := &c5RedTransport{platform: "openai"}
				s, connRepo := c5RedService(t, remote)
				ctx := context.WithValue(context.Background(), struct{}{}, "preserve-repository-context")
				repo := &importObservedRepo{testStateRepo: s.repository.(*testStateRepo), trace: trace, ctx: ctx}
				if sample.mode == "load" {
					repo.loadError = errors.New("SQLSTATE 23505 private database secret")
				}
				if strings.HasPrefix(sample.mode, "refresh-") || sample.mode == "save" {
					repo.state.Session.RefreshToken = "synthetic-refresh-secret"
					repo.state.Session.ExpiresAt = nil
				}
				if sample.mode == "save" {
					repo.saveErr = errors.New("private SQL password connection detail")
				}
				s.repository = repo
				c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
					if r.URL.Path == "/api/v1/auth/refresh" {
						*trace = append(*trace, "refresh")
						// Original RefreshSession/VerifyAdmin methods use Background.
						if r.Context().Value(struct{}{}) != nil {
							t.Fatal("authentication method context changed")
						}
						if sample.mode == "refresh-network" {
							return nil, io.EOF, true
						}
						if sample.mode == "save" {
							return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"access_token": "synthetic-new-token"}}), nil, true
						}
						if sample.mode == "refresh-sql" {
							sqlFixtureInjected = true
							return c5SafeResponse(r, 500, map[string]any{"message": "SQLSTATE 23505 private database secret"}), nil, true
						}
						return c5SafeResponse(r, sample.status, map[string]any{"message": "主站刷新请求被拒绝"}), nil, true
					}
					if r.URL.Path == "/api/v1/auth/me" {
						*trace = append(*trace, "verify")
						if r.Context().Value(struct{}{}) != nil {
							t.Fatal("authentication method context changed")
						}
						if sample.mode == "verify-network" {
							return nil, io.EOF, true
						}
						return c5SafeResponse(r, 403, map[string]any{"code": "FORBIDDEN", "message": "主站不允许当前身份"}), nil, true
					}
					return nil, nil, false
				})
				if !observed {
					_, err := s.authenticatedState(ctx, "user-1", "admin-1")
					if err == nil {
						t.Fatal("legacy failure unexpectedly accepted")
					}
					oldError = err.Error()
					if sample.mode == "refresh-sql" && !sqlFixtureInjected {
						t.Fatal("R1 RED refresh SQL fixture was not actually injected into refresh response")
					}
					continue
				}
				_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
				var failure *ImportFailure
				if !errors.As(err, &failure) || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].Message != sample.message || failure.Diagnostics[0].Target != sample.target || failure.Diagnostics[0].HTTPStatus != sample.status || failure.Stage != "validation" || failure.Cleanup != "not_needed" || remote.keyCreates != 0 || connRepo.saveCalls != 0 {
					t.Fatal("observer lost failed step or falsely started resource creation")
				}
				if sample.mode != "load" && sample.mode != "save" && failure.Message != oldError {
					t.Fatal("observer changed original error key")
				}
				wire, _ := json.Marshal(failure)
				if sample.mode == "refresh-sql" && !sqlFixtureInjected {
					t.Fatal("R1 RED observed SQL fixture was not actually injected into refresh response")
				}
				for _, secret := range []string{"SQLSTATE", "private SQL", "synthetic-refresh-secret", "synthetic-new-token"} {
					if strings.Contains(string(wire), secret) {
						t.Fatal("session observer exposed raw error or session")
					}
				}
			}
			if !reflect.DeepEqual(oldTrace, sample.trace) || !reflect.DeepEqual(importTrace, oldTrace) {
				t.Fatal("observer changed request order or count")
			}
		})
	}
}

func TestImportMissingSub2APISessionProvesNoCreation(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, repo := c5RedService(t, remote)
	s.repository.(*testStateRepo).state = nil
	_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.StatusCode != 401 || failure.Message != ErrorAuthRequired || failure.Stage != "validation" || failure.Cleanup != "not_needed" || !failure.RetryAllowed || remote.keyCreates != 0 || repo.saveCalls != 0 || len(failure.Diagnostics) != 1 || !strings.Contains(failure.Diagnostics[0].Detail, "未创建账号或 Key") {
		t.Fatal("missing Sub2API session became uncertain creation or lost login guidance")
	}
}

func TestImportNewAPIAuthenticationKeepsLegacyResponseAndIgnoresObserver(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, _ := c5RedService(t, remote)
	s.repository.(*testStateRepo).state.Session = platformTestSession(upstream.PlatformNewAPI, "https://legacy.invalid")
	requests, observed := 0, 0
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/api/user/self" {
			t.Fatal("legacy NewAPI authentication request changed")
		}
		return c5SafeResponse(r, 403, map[string]any{"success": false, "message": "forbidden"}), nil, true
	})
	_, err := s.authenticatedStateObserved(t.Context(), "user-1", "admin-1", func(string, string, error) { observed++ })
	if err == nil || err.Error() != ErrorAdminOnly || requests != 1 || observed != 0 {
		t.Fatal("observer changed NewAPI authentication decision or request count")
	}
	requests = 0
	_, err = s.validatedStateObserved(t.Context(), s.repository.(*testStateRepo).state, func(string, string, error) { observed++ })
	if err == nil || err.Error() != ErrorAdminOnly || requests != 1 || observed != 0 {
		t.Fatal("direct NewAPI validation invoked an import observer or changed original decision")
	}
	requests = 0
	r := httptest.NewRequest("POST", "/api/my-sites/real-connect", strings.NewReader(`{"upstreamSiteId":"site-1","upstreamGroupId":"70","ownGroupIds":["7"]}`))
	r = r.WithContext(authctx.WithUserID(r.Context(), "user-1"))
	w := httptest.NewRecorder()
	(&Handler{service: s}).realConnect(w, r)
	var wire map[string]any
	if json.Unmarshal(w.Body.Bytes(), &wire) != nil || w.Code != 403 || wire["message"] != ErrorAdminOnly || len(wire) != 1 || requests != 1 {
		t.Fatal("legacy NewAPI HTTP status, message or response fields changed")
	}
}

func TestImportInvalid200ConfigurationNamesKnownFieldAndPreservesResources(t *testing.T) {
	for _, field := range []string{"id", "name", "platform", "type", "priority", "concurrency", "credentials", "extra", "pool_mode", "upstream_billing_probe_enabled", "upstream_billing_rate_sync_enabled", "openai_passthrough", "model_mapping", "group_ids"} {
		t.Run(field, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/admin/accounts/22" {
					return nil, nil, false
				}
				response, err := remote.RoundTrip(r)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if json.NewDecoder(response.Body).Decode(&payload) != nil {
					t.Fatal("invalid fixture")
				}
				response.Body.Close()
				data := payload["data"].(map[string]any)
				location := data
				switch field {
				case "pool_mode", "model_mapping":
					location = data["credentials"].(map[string]any)
				case "upstream_billing_probe_enabled", "upstream_billing_rate_sync_enabled", "openai_passthrough":
					location = data["extra"].(map[string]any)
				}
				location[field] = []string{"synthetic-private-read-value"}
				return c5SafeResponse(r, 200, payload), nil, true
			})
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Cleanup != "retained" || failure.RetryAllowed || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].HTTPStatus != 200 || repo.saveCalls != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("strict 200 structure failure lost received status or original resource protection")
			}
			label := field
			if field == "group_ids" {
				label = "group_ids"
			}
			if !strings.Contains(failure.Diagnostics[0].Detail, label) {
				t.Fatal("known invalid response field was discarded")
			}
			wire, _ := json.Marshal(failure)
			if strings.Contains(string(wire), "synthetic-private-read-value") {
				t.Fatal("invalid response material leaked")
			}
		})
	}
}

func TestImportReadbackReportsEverySafeSettingDifference(t *testing.T) {
	observed := upstream.Sub2APIImportConfiguration{Platform: "anthropic", Type: "oauth", Priority: 12, Concurrency: 60, Passthrough: true, PoolMode: false, UpstreamBillingProbeEnabled: false, UpstreamBillingRateSyncEnabled: true, Groups: []upstream.Sub2APIImportGroup{{ID: "9"}}, ModelMapping: map[string]string{"wrong": "other"}}
	settings := ImportAccountSettings{Priority: 100, Concurrency: 50, PoolMode: true, UpstreamBillingProbeEnabled: true}
	_, err := verifyImportedAccountConfiguration(observed, "openai", settings, []string{"7"}, importModelResult{Models: []string{"valid"}})
	d := newImportDiagnostic("configuration_check", "main", ErrorImportConfigurationMismatch, err, "")
	for _, field := range []string{"platform", "type", "Priority", "concurrency", "passthrough", "poolMode", "upstreamBillingProbeEnabled", "upstreamBillingRateSyncEnabled", "目标分组", "模型映射"} {
		if !strings.Contains(d.Detail, field) {
			t.Fatalf("missing mismatch %s", field)
		}
	}
}

func TestImportSettingsExplainEveryKnownInvalidField(t *testing.T) {
	priority, concurrency, passthrough := 10, 1001, true
	_, err := normalizeImportAccountSettings(&ImportAccountSettingsRequest{PriorityMode: stringPointer("manual"), Priority: &priority, Concurrency: &concurrency, Passthrough: &passthrough}, "gemini")
	d := newImportDiagnostic("validation", "local", ErrorImportSettingsInvalid, err, "")
	for _, text := range []string{"人工 Priority", "1–9", "concurrency", "1–1000", "passthrough", "OpenAI"} {
		if !strings.Contains(d.Detail, text) {
			t.Fatal("known invalid field or allowed range missing")
		}
	}
}

func TestImportWarningNeverConvertsLaterBlockingStepsIntoSuccess(t *testing.T) {
	for _, mode := range []string{"account-rejected", "account-uncertain", "read-unavailable", "source-changed", "commit-not-committed", "commit-uncertain", "commit-confirmed"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", previewFailure: true}
			s, base := c5RedService(t, remote)
			if strings.HasPrefix(mode, "commit-") {
				cause := error(errors.New("synthetic private storage failure"))
				if mode == "commit-not-committed" {
					cause = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: cause}
				}
				s.connRepository = &c5OutcomeRepository{testConnRepo: base, saveError: cause, storeOnError: mode == "commit-confirmed"}
			}
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method == "POST" && r.URL.Path == "/api/v1/admin/accounts" && strings.HasPrefix(mode, "account-") {
					remote.accountCreates++
					status := 403
					if mode == "account-uncertain" {
						status = 500
					}
					return c5SafeResponse(r, status, map[string]any{"code": "FORBIDDEN", "message": "主站拒绝本次操作"}), nil, true
				}
				if r.Method == "GET" && r.URL.Path == "/api/v1/admin/accounts/22" {
					if mode == "read-unavailable" {
						remote.reads++
						return c5SafeResponse(r, 403, map[string]any{"code": "FORBIDDEN", "message": "无法读取账号配置"}), nil, true
					}
					if mode == "source-changed" {
						s.upstreamLookup.(testUpstreamLookup).sites["site-1"].Metrics.Groups = nil
					}
				}
				return nil, nil, false
			})
			response, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
			if mode == "commit-confirmed" {
				if err != nil || response.Configuration == nil || response.Configuration.ModelState != "not_synced" || len(response.Diagnostics) != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
					t.Fatal("complete commit confirmation lost warning success or deleted resources")
				}
				return
			}
			var failure *ImportFailure
			if !errors.As(err, &failure) || len(failure.Diagnostics) != 2 || failure.Diagnostics[1].Message != ErrorImportModelSyncWarning || response.Connection.ID != "" {
				t.Fatal("later failure was masked by warning or lost both diagnostics")
			}
			if mode == "account-rejected" || mode == "commit-not-committed" {
				if failure.Cleanup != "confirmed" || !failure.RetryAllowed || remote.keyDeletes != 1 {
					t.Fatal("known rejection lost original safe cleanup and retry")
				}
			} else if failure.RetryAllowed || failure.StatusCode != 409 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("uncertain or retained result enabled retry or unsafe deletion")
			}
		})
	}
}

func TestImportWarningReplayReadsCurrentEmptyMappingWithoutHistoricalWarning(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", previewFailure: true}
	s, repo := c5RedService(t, remote)
	replayRepo := &c5OutcomeRepository{testConnRepo: repo}
	s.connRepository = replayRepo
	req := c5RedRequest(t, `{}`)
	if _, err := s.RealConnect(t.Context(), "user-1", req); err != nil {
		t.Fatal(err)
	}
	replayRepo.replay = repo.connection
	result, err := s.RealConnect(t.Context(), "user-1", req)
	if err != nil || result.Configuration == nil || result.Configuration.Observation != "current" || result.Configuration.ModelState != "current_unrestricted" || len(result.Diagnostics) != 0 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 2 || repo.saveCalls != 1 {
		t.Fatal("replay resynchronized, recreated or invented a historical model warning")
	}
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.URL.Path == "/api/v1/admin/accounts/22" {
			return c5SafeResponse(r, 403, map[string]any{"message": "无法读取当前配置"}), nil, true
		}
		return nil, nil, false
	})
	result, err = s.RealConnect(t.Context(), "user-1", req)
	if err != nil || result.ConfigurationStatus != "unavailable" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Stage != "configuration_check" || remote.previews != 1 || remote.accountCreates != 1 || repo.saveCalls != 1 {
		t.Fatal("unreadable replay lost safe cause or recreated resources")
	}
}

type importFailingBodyReader struct{}

func (importFailingBodyReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestImportAccountInvalidJSONAndReadFailurePreserveKnownKeyAndCannotRetry(t *testing.T) {
	for _, mode := range []string{"invalid-json", "read-failed"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/admin/accounts" {
					return nil, nil, false
				}
				remote.accountCreates++
				body := `{"code":0,"data":{"id":22,"key":"synthetic-private-response"}}`
				if mode == "invalid-json" {
					body += `{`
				}
				reader := io.Reader(strings.NewReader(body))
				if mode == "read-failed" {
					reader = io.MultiReader(reader, importFailingBodyReader{})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader), Request: r}, nil, true
			})
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.StatusCode != 409 || failure.Cleanup != "pending" || failure.RetryAllowed || failure.AdminResourceID != "" || failure.UpstreamKeyID != "11" || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].HTTPStatus != 200 || repo.saveCalls != 0 || remote.reads != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("malformed received account result lost known Key, invented account proof or allowed deletion/retry")
			}
			wire, _ := json.Marshal(failure)
			if strings.Contains(string(wire), "synthetic-private-response") {
				t.Fatal("untrusted parse/read body reached diagnostics")
			}
		})
	}
}

func TestImportPreflightKnownSourceProblemsExplainExactCauseWithoutCreating(t *testing.T) {
	for _, sample := range []struct{ mode, label string }{
		{"missing-site-id", "尚未选择上游站点"}, {"missing-group-id", "缺少编号"}, {"long-operation", "128 字节"},
		{"site-gone", "站点不存在"}, {"site-session", "缺少登录资料"}, {"site-workspace", "不属于当前用户或工作区"},
		{"group-gone", "编号已不存在"}, {"group-duplicate", "对应多条记录"}, {"group-empty-name", "名称为空"},
	} {
		t.Run(sample.mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			req := c5RedRequest(t, `{}`)
			lookup := s.upstreamLookup.(testUpstreamLookup)
			site := lookup.sites["site-1"]
			switch sample.mode {
			case "missing-site-id":
				req.UpstreamSiteID = ""
			case "missing-group-id":
				req.UpstreamGroupID = ""
			case "long-operation":
				req.OperationID = strings.Repeat("x", 129)
			case "site-gone":
				delete(lookup.sites, "site-1")
			case "site-session":
				site.Session = nil
			case "site-workspace":
				site.AdminAccountID = "other-workspace"
			case "group-gone":
				site.Metrics.Groups = nil
			case "group-duplicate":
				site.Metrics.Groups = append(site.Metrics.Groups, site.Metrics.Groups[0])
			case "group-empty-name":
				site.Metrics.Groups[0].Name = " "
			}
			_, err := s.RealConnect(t.Context(), "user-1", req)
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "validation" || failure.Cleanup != "not_needed" || !failure.RetryAllowed || len(failure.Diagnostics) != 1 || !strings.Contains(failure.Diagnostics[0].Detail, sample.label) || remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("known source problem lost specific cause or created resources")
			}
		})
	}
}

func TestImportDatabaseTechnicalHintsCannotReachHTTPToDTOOrChangeResourceResults(t *testing.T) {
	for _, message := range []string{
		`FATAL: database "private_database" does not exist`,
		`constraint "private_constraint" of relation "private_table" does not exist`,
		`trigger "private_trigger" for table "private_table" does not exist`,
		`policy "private_policy" for table "private_table" does not exist`,
		`function private_schema.private_function(integer, text) does not exist`,
	} {
		for _, stage := range []string{"model", "account"} {
			for _, status := range []int{200, 500} {
				remote := &c5RedTransport{platform: "openai"}
				s, repo := c5RedService(t, remote)
				c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
					if stage == "model" && strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
						return c5SafeResponse(r, status, map[string]any{"success": false, "message": message}), nil, true
					}
					if stage == "account" && r.Method == "POST" && r.URL.Path == "/api/v1/admin/accounts" {
						remote.accountCreates++
						return c5SafeResponse(r, status, map[string]any{"success": false, "message": message, "data": map[string]any{"id": 22}}), nil, true
					}
					return nil, nil, false
				})
				response, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, `{}`))
				var wire []byte
				if stage == "model" {
					if err != nil || response.Configuration == nil || response.Configuration.ModelState != "not_synced" || len(response.Diagnostics) != 1 || response.Diagnostics[0].HTTPStatus != status || response.Diagnostics[0].Detail != "" || repo.saveCalls != 1 || repo.connection == nil || repo.connection.AdminAccountID != "22" || repo.connection.UpstreamKeyID != "11" || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
						t.Fatal("filtered model hint reached DTO or changed complete warning import")
					}
					wire, _ = json.Marshal(response)
				} else {
					var failure *ImportFailure
					if !errors.As(err, &failure) || failure.Cleanup != "pending" || failure.RetryAllowed || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].HTTPStatus != status || failure.Diagnostics[0].Detail != "" || repo.saveCalls != 0 || remote.reads != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
						t.Fatal("filtered account hint changed ID evidence or original uncertain resource protection")
					}
					wire, _ = json.Marshal(failure)
				}
				if strings.Contains(string(wire), "private_") {
					t.Fatal("database identifiers from actual HTTP response reached import DTO")
				}
			}
		}
	}
}
