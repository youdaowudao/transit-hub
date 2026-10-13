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
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// These key business expectations are owned by the primary agent. Implementers
// may not weaken them. Every remote request uses an in-memory transport.
func warningWire(t *testing.T, value any) map[string]any {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func warningDiagnostics(t *testing.T, value any, count int) []any {
	t.Helper()
	wire := warningWire(t, value)
	diagnostics, ok := wire["diagnostics"].([]any)
	if !ok || len(diagnostics) != count {
		t.Fatalf("expected %d safe diagnostics, got %v", count, wire["diagnostics"])
	}
	return diagnostics
}

func TestImportWarningREDModelFailureStillPersistsCompleteAssociation(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload any
		cause   error
	}{
		{"main-403", 403, map[string]any{"code": "FORBIDDEN", "message": "接口拒绝，请检查权限"}, nil},
		{"main-502-upstream-403", 502, map[string]any{"code": "UPSTREAM_ERROR", "message": "上游返回 HTTP 403，请查看上游记录"}, nil},
		{"child-timeout-parent-valid", 0, nil, context.DeadlineExceeded},
		{"empty", 200, map[string]any{"code": 0, "data": map[string]any{"models": []any{}}}, nil},
		{"invalid-structure", 200, map[string]any{"code": 0, "data": map[string]any{"models": "invalid"}}, nil},
		{"partial-invalid", 200, map[string]any{"code": 0, "data": map[string]any{"models": []any{"valid-first", 1}}}, nil},
		{"blank-model", 200, map[string]any{"code": 0, "data": map[string]any{"models": []string{"valid-first", " "}}}, nil},
		{"wildcard-model", 200, map[string]any{"code": 0, "data": map[string]any{"models": []string{"valid-first", "model-*"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			previews := 0
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					previews++
					if tc.cause != nil {
						return nil, tc.cause, true
					}
					return c5SafeResponse(r, tc.status, tc.payload), nil, true
				}
				return nil, nil, false
			})
			req := c5RedRequest(t, `{"priorityMode":"manual","priority":4,"concurrency":73,"passthrough":false,"poolMode":false,"upstreamBillingProbeEnabled":false}`)
			mapping := true
			req.AddToPricingMapping = &mapping
			response, err := s.RealConnect(context.Background(), "user-1", req)
			if err != nil {
				t.Fatalf("model failure must be a warning after full import: %v", err)
			}
			if remote.keyCreates != 1 || previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || repo.saveCalls != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("warning did not perform exactly one complete import without cleanup")
			}
			if repo.connection == nil || repo.connection.AdminAccountID != "22" || repo.connection.UpstreamKeyID != "11" || repo.connection.OperationID != req.OperationID || !repo.connection.PricingMappingEnabled || !equalImportIDSets(repo.connection.OwnGroupIDs, []string{"7", "8"}) {
				t.Fatal("warning success omitted full association or selected pricing mapping")
			}
			credentials := remote.created["credentials"].(map[string]any)
			modelMapping, exists := credentials["model_mapping"]
			if !exists || len(modelMapping.(map[string]any)) != 0 {
				t.Fatal("failed or partially valid preview must explicitly write an empty mapping")
			}
			if remote.created["priority"] != float64(4) || remote.created["concurrency"] != float64(73) || credentials["pool_mode"] != false || remote.created["upstream_billing_probe_enabled"] != false {
				t.Fatal("warning changed manual settings or explicit false")
			}
			if response.ConfigurationStatus != "confirmed" || response.Configuration == nil || response.Configuration.ModelState != "not_synced" || response.Configuration.Observation != "creation" || len(response.Configuration.Models) != 0 || response.Configuration.Passthrough {
				t.Fatal("warning was falsely reported as model sync success")
			}
			d := warningDiagnostics(t, response, 1)[0].(map[string]any)
			if d["stage"] != "model_sync" || d["target"] != "main" || d["message"] != "admin.mySites.errors.importModelSyncWarning" {
				t.Fatal("warning lost actual step or request target")
			}
			if tc.status != 0 && d["httpStatus"] != float64(tc.status) {
				t.Fatal("diagnostic invented or lost peer status")
			}
			if tc.status == 0 && d["httpStatus"] != nil {
				t.Fatal("timeout without response invented an HTTP status")
			}
			if tc.status == 403 && strings.Contains(stringValueForWarning(d["reason"]), "Balance") {
				t.Fatal("bare 403 must not imply insufficient balance")
			}
		})
	}
}

func stringValueForWarning(value any) string { result, _ := value.(string); return result }

func TestImportWarningREDParentCancellationNeverBecomesWarning(t *testing.T) {
	for _, mode := range []string{"canceled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "expired" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(20*time.Millisecond))
				defer cancel()
			}
			cleanups := 0
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					if mode == "expired" {
						<-ctx.Done()
					} else {
						cancel()
					}
					return nil, ctx.Err(), true
				}
				if r.Method == http.MethodDelete {
					cleanups++
					if r.Context().Err() != nil {
						t.Fatal("known Key cleanup inherited canceled parent")
					}
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
			if err == nil || remote.accountCreates != 0 || repo.saveCalls != 0 || cleanups != 1 || remote.keyDeletes != 1 {
				t.Fatal("canceled parent continued import or lost original detached cleanup")
			}
		})
	}
}

func TestImportWarningREDInitialWarningAndCleanupFailureAllSurvive(t *testing.T) {
	for _, cleanup := range []string{"account", "key"} {
		t.Run(cleanup, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", previewFailure: true, configurationMismatch: true}
			s, repo := c5RedService(t, remote)
			if cleanup == "account" {
				s.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error {
					remote.protectedDeletes++
					return &upstream.RequestError{MessageKey: upstream.ErrorForbidden, Reason: upstream.ErrorForbidden, StatusCode: 403, MutationOutcome: upstream.MutationConfirmedRejected}
				}})
			} else {
				c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
					if r.Method == http.MethodDelete {
						remote.keyDeletes++
						return c5SafeResponse(r, 403, map[string]any{"code": "FORBIDDEN", "message": "不允许删除此 Key"}), nil, true
					}
					return nil, nil, false
				})
			}
			_, err := s.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "configuration_check" || failure.RetryAllowed || failure.StatusCode != 409 || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || repo.saveCalls != 0 || remote.accountCreates != 1 || remote.protectedDeletes != 1 {
				t.Fatal("warning obscured blocking configuration mismatch or resource protection")
			}
			d := warningDiagnostics(t, failure, 3)
			first := d[0].(map[string]any)
			second := d[1].(map[string]any)
			last := d[2].(map[string]any)
			if first["message"] != ErrorImportConfigurationMismatch || first["stage"] != "configuration_check" || !strings.Contains(stringValueForWarning(first["detail"]), "Priority") || second["stage"] != "model_sync" || last["stage"] != cleanup+"_cleanup" {
				t.Fatal("cleanup overwrote the initial failure, model warning, or known mismatch")
			}
			if cleanup == "account" && remote.keyDeletes != 0 {
				t.Fatal("failed account protection must stop later Key cleanup")
			}
		})
	}
}

func TestImportWarningREDSafeReasonAndDiagnosticBudgetPreserveMutationEvidence(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		t.Run(string(rune('a'+size-4096)), func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts" {
					prefix := `{"code":"FORBIDDEN","message":"禁止操作 synthetic-c5-key access-token tiny https://user:pass@example.invalid/path?q=secret#fragment","data":{"id":22,"key":"tiny"},"padding":"`
					body := prefix + strings.Repeat("x", size-len(prefix)-2) + `"}`
					if len(body) != size {
						t.Fatal("fixture budget is not exact")
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil, true
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.AdminResourceID != "22" || failure.Cleanup != "pending" || failure.RetryAllowed || repo.saveCalls != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("diagnostic budget changed known-ID evidence or unsafe mutation handling")
			}
			d := warningDiagnostics(t, failure, 1)[0].(map[string]any)
			if d["target"] != "main" || d["httpStatus"] != float64(200) {
				t.Fatal("business rejection lost actual received status")
			}
			detail := stringValueForWarning(d["detail"])
			if size == 4096 && !strings.Contains(detail, "禁止操作") {
				t.Fatal("safe response message below budget was discarded")
			}
			if size == 4097 && !strings.Contains(detail, "响应过大") {
				t.Fatal("over-budget diagnostic did not use fixed explanation")
			}
			encoded, _ := json.Marshal(failure)
			for _, secret := range []string{"synthetic-c5-key", "access-token", "tiny", "user:pass", "q=secret", "#fragment"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("diagnostic exposed request or response credentials")
				}
			}
		})
	}
}

func TestImportWarningREDPreflight409IsTrustedNoCreation(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, repo := c5RedService(t, remote)
	s.SetAdminAccountResolver(testAdminResolver{})
	r := httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connect", strings.NewReader(`{"upstreamSiteId":"site-1","upstreamGroupId":"70","ownGroupIds":["7"]}`))
	r = r.WithContext(authctx.WithUserID(r.Context(), "user-1"))
	w := httptest.NewRecorder()
	(&Handler{service: s}).realConnect(w, r)
	var wire map[string]any
	if json.Unmarshal(w.Body.Bytes(), &wire) != nil {
		t.Fatal("invalid response")
	}
	if w.Code != 409 || wire["stage"] != "validation" || wire["cleanup"] != "not_needed" || wire["retryAllowed"] != false || remote.keyCreates != 0 || repo.saveCalls != 0 {
		t.Fatal("known preflight 409 did not prove no resources created while forbidding retry")
	}
	warningDiagnostics(t, wire, 1)
}

func TestImportWarningREDParentCanceledAfterReadbackCannotSave(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, repo := c5RedService(t, remote)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/22" {
			response, err := remote.RoundTrip(r)
			cancel()
			return response, err, true
		}
		return nil, nil, false
	})
	_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.RetryAllowed || failure.StatusCode != 409 || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || repo.saveCalls != 0 || remote.accountCreates != 1 || remote.reads != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
		t.Fatal("parent canceled after confirmed creation/readback must retain resources without saving or deleting")
	}
}

func TestImportWarningREDCommonSQLDetailsCannotReachDiagnostics(t *testing.T) {
	for _, message := range []string{
		`ERROR: duplicate key value violates unique constraint "private_table_key"`,
		`syntax error at or near "private_table"`,
		`password authentication failed for user "private_database_user"`,
		`relation "private_table" does not exist`,
		`column "private_column" does not exist`,
		`schema "private_schema" does not exist`,
		`sequence "private_sequence" does not exist`,
		`permission denied for table private_table`,
		`permission denied for schema private_schema`,
		`permission denied for sequence private_sequence`,
		`FATAL: database "private_database" does not exist`,
		`ERROR: table "private_table" does not exist`,
		`ERROR: index "private_index" does not exist`,
		`ERROR: view "private_view" does not exist`,
		`ERROR: type "private_type" does not exist`,
		`ERROR: function "private_function" does not exist`,
		`ERROR: procedure "private_procedure" does not exist`,
		`ERROR: extension "private_extension" does not exist`,
		`ERROR: role "private_role" does not exist`,
		`ERROR: tablespace "private_tablespace" does not exist`,
		`ERROR: constraint "private_constraint" does not exist`,
		`ERROR: trigger "private_trigger" does not exist`,
	} {
		cause := &upstream.RequestError{MessageKey: upstream.ErrorUpstreamServer, StatusCode: 500, UpstreamMessage: message}
		diagnostic := newImportDiagnostic("validation", "main", ErrorImportAdminVerificationFailed, cause, "")
		if diagnostic.Detail != "" || diagnostic.HTTPStatus != 500 {
			t.Fatal("recognizable raw database details must be withheld while preserving actual peer status")
		}
	}
}
