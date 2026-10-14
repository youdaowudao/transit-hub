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
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

// All external responses are in-memory; these tests start no listeners.
func grokRequest(t *testing.T) RealConnectRequest {
	t.Helper()
	req := c5RedRequest(t, "")
	req.GroupType = "grok"
	return req
}

func TestGrokModelWarningKeepsCompleteImport(t *testing.T) {
	cases := []struct {
		name   string
		status int
		models any
		cause  error
	}{
		{name: "main-403", status: 403}, {name: "main-502", status: 502},
		{name: "child-timeout", cause: context.DeadlineExceeded},
		{name: "empty", status: 200, models: []string{}},
		{name: "not-array", status: 200, models: "invalid"},
		{name: "not-string", status: 200, models: []any{"valid-first", 1}},
		{name: "blank", status: 200, models: []string{"valid-first", " "}},
		{name: "wildcard", status: 200, models: []string{"valid-first", "model-*"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				if !strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					return nil, nil, false
				}
				remote.previews++
				var request map[string]any
				if json.NewDecoder(r.Body).Decode(&request) != nil || request["platform"] != "grok" || request["type"] != "apikey" {
					t.Fatal("warning preview lost selected Grok platform")
				}
				if tc.cause != nil {
					return nil, tc.cause, true
				}
				payload := map[string]any{"code": 0, "data": map[string]any{"models": tc.models}}
				if tc.status != 200 {
					payload = map[string]any{"code": "FORBIDDEN", "message": "主站未能读取模型，请查看主站请求记录"}
				}
				return c5SafeResponse(r, tc.status, payload), nil, true
			})
			req := c5RedRequest(t, `{"priorityMode":"manual","priority":4,"concurrency":73,"poolMode":false,"upstreamBillingProbeEnabled":false}`)
			req.GroupType = "grok"
			enabled := true
			req.AddToPricingMapping = &enabled
			result, err := service.RealConnect(t.Context(), "user-1", req)
			if err != nil {
				t.Fatal(err)
			}
			c := result.Configuration
			if c == nil || result.ConfigurationStatus != "confirmed" || c.Platform != "grok" || c.ModelState != "not_synced" || len(c.Models) != 0 || c.Passthrough || c.PoolMode || c.UpstreamBillingProbeEnabled || c.Priority != 4 || c.Concurrency != 73 {
				t.Fatal("Grok warning lost settings or falsely claimed synced models")
			}
			if len(remote.created["credentials"].(map[string]any)["model_mapping"].(map[string]any)) != 0 {
				t.Fatal("partial failed list leaked into whitelist")
			}
			if repo.connection == nil || repo.connection.GroupType != "grok" || !repo.connection.PricingMappingEnabled || !equalImportIDSets(repo.connection.OwnGroupIDs, []string{"7", "8"}) {
				t.Fatal("warning omitted complete Grok association")
			}
			if remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || repo.saveCalls != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("warning changed original pipeline")
			}
			diagnostics := warningDiagnostics(t, result, 1)
			d := diagnostics[0].(map[string]any)
			if d["stage"] != "model_sync" || d["target"] != "main" || d["message"] != "admin.mySites.errors.importModelSyncWarning" {
				t.Fatal("warning lost concrete cause and original stage")
			}
			if tc.cause != nil && d["reason"] != upstream.ErrorNetworkTimeout {
				t.Fatal("child timeout lost its exact safe cause")
			}
			if tc.status != 0 && strings.TrimSpace(stringValueForWarning(d["detail"])) == "" {
				t.Fatal("peer response warning lost its detailed cause")
			}
			if tc.status != 0 && d["httpStatus"] != float64(tc.status) || tc.status == 0 && d["httpStatus"] != nil {
				t.Fatal("warning changed peer status evidence")
			}
		})
	}
}

func TestGrokParentCancellationStopsCreationAndSave(t *testing.T) {
	for _, phase := range []string{"model_sync", "after_readback"} {
		t.Run(phase, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				if phase == "model_sync" && strings.HasSuffix(r.URL.Path, "sync-upstream-preview") {
					remote.previews++
					cancel()
					return nil, ctx.Err(), true
				}
				if phase == "after_readback" && r.URL.Path == "/api/v1/admin/accounts/22" {
					cancel()
				}
				if r.Method == http.MethodDelete && r.Context().Err() != nil {
					t.Fatal("cleanup inherited canceled parent")
				}
				return nil, nil, false
			})
			_, err := service.RealConnect(ctx, "user-1", grokRequest(t))
			if err == nil || repo.saveCalls != 0 || repo.connection != nil {
				t.Fatal("cancellation became saved warning success")
			}
			if phase == "model_sync" && (remote.accountCreates != 0 || remote.keyDeletes != 1 || remote.protectedDeletes != 0) {
				t.Fatal("canceled model step created account or lost detached Key cleanup")
			}
			if phase == "after_readback" && (remote.accountCreates != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0) {
				t.Fatal("canceled persistence changed retained remote resources")
			}
		})
	}
}

func TestGrokReadbackDifferencesCannotConfirmImport(t *testing.T) {
	for _, field := range []string{"platform", "type", "group_ids", "priority", "concurrency", "pool_mode", "model_mapping", "probe", "rate_sync"} {
		t.Run(field, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path != "/api/v1/admin/accounts/22" {
					return nil, nil, false
				}
				remote.reads++
				data := map[string]any{}
				for k, v := range remote.created {
					data[k] = v
				}
				data["id"] = 22
				data["extra"] = map[string]any{"upstream_billing_probe_enabled": true, "upstream_billing_rate_sync_enabled": false}
				switch field {
				case "platform":
					data[field] = "openai"
				case "type":
					data[field] = "oauth"
				case "group_ids":
					data[field] = []int{7}
				case "priority":
					data[field] = 1
				case "concurrency":
					data[field] = 1
				case "pool_mode":
					data["credentials"].(map[string]any)[field] = false
				case "model_mapping":
					data["credentials"].(map[string]any)[field] = map[string]any{"live-a": "changed", "live-z": "live-z"}
				case "probe":
					data["extra"].(map[string]any)["upstream_billing_probe_enabled"] = false
				case "rate_sync":
					data["extra"].(map[string]any)["upstream_billing_rate_sync_enabled"] = true
				}
				return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": data}), nil, true
			})
			_, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "configuration_check" || failure.Cleanup != "confirmed" || !failure.RetryAllowed || failure.Message != ErrorImportConfigurationMismatch || repo.saveCalls != 0 || repo.connection != nil || remote.protectedDeletes != 1 || remote.keyDeletes != 1 {
				t.Fatalf("mismatched %s bypassed configuration verification: %v", field, err)
			}
		})
	}
}

func TestGrokProtectedCleanupNeverDeletesKeyBeforeAccountConfirmation(t *testing.T) {
	for _, outcome := range []string{upstream.MutationConfirmedRejected, upstream.MutationUncertain} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok", configurationMismatch: true}
			service, repo := c5RedService(t, remote)
			service.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error {
				remote.protectedDeletes++
				return &upstream.RequestError{MessageKey: upstream.ErrorForbidden, Reason: upstream.ErrorForbidden, StatusCode: 403, MutationOutcome: outcome}
			}})
			_, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.RetryAllowed || failure.Cleanup == "confirmed" || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || remote.protectedDeletes != 1 || remote.keyDeletes != 0 || repo.saveCalls != 0 {
				t.Fatal("unsafe cleanup crossed unconfirmed protected deletion")
			}
		})
	}
}

func TestGrokInvalidTargetsRejectBeforeResources(t *testing.T) {
	for _, mode := range []string{"mixed", "no-target", "invalid-id", "other-workspace"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			req := grokRequest(t)
			switch mode {
			case "mixed":
				remote.secondGroupPlatform = "openai"
			case "no-target":
				req.OwnGroupIDs = nil
			case "invalid-id":
				req.OwnGroupIDs = []string{"07"}
			case "other-workspace":
				service.upstreamLookup.(testUpstreamLookup).sites["site-1"].AdminAccountID = "other"
			}
			_, err := service.RealConnect(t.Context(), "user-1", req)
			if err == nil || remote.keyCreates != 0 || remote.accountCreates != 0 || remote.previews != 0 || repo.saveCalls != 0 {
				t.Fatal("invalid Grok target created remote resources")
			}
		})
	}
}

func TestGrokSourceChangesRetainKnownResources(t *testing.T) {
	for _, change := range []string{"missing", "owner", "url", "platform"} {
		t.Run(change, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			site := service.upstreamLookup.(testUpstreamLookup).sites["site-1"]
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path == "/api/v1/admin/accounts/22" {
					switch change {
					case "missing":
						site.Metrics.Groups = nil
					case "owner":
						site.UserID = "other"
					case "url":
						site.BaseURL = "https://changed.invalid"
					case "platform":
						site.Platform = upstream.PlatformNewAPI
					}
				}
				return nil, nil, false
			})
			_, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Stage != "persistence" || failure.Cleanup != "retained" || failure.RetryAllowed || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || repo.saveCalls != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("source change saved success or deleted uncertain resources")
			}
		})
	}
}

func TestGrokConcurrentImportAndBindingKeepOneCreator(t *testing.T) {
	for _, mode := range []string{"same-target", "same-operation", "same-operation-other-target", "bind-during-import", "import-during-bind"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "grok", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			service.upstreamLookup.(testUpstreamLookup).sites["site-1"].Metrics.Groups = append(service.upstreamLookup.(testUpstreamLookup).sites["site-1"].Metrics.Groups, upstream.GroupInfo{ID: "71", Name: "second", Platform: stringPointer("grok")})
			entered, release := make(chan struct{}), make(chan struct{})
			blocked := false
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				path := "/api/v1/admin/groups"
				if mode == "import-during-bind" {
					path = "/api/v1/keys"
				}
				if r.Method == http.MethodGet && r.URL.Path == path && !blocked {
					blocked = true
					close(entered)
					<-release
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/keys" {
					return c5SafeResponse(r, 200, map[string]any{"data": []any{}}), nil, true
				}
				return nil, nil, false
			})
			first := grokRequest(t)
			first.OperationID = "grok-first"
			binding := RealBindRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "70", UpstreamGroupName: "upstream-only", UpstreamKeyID: "11", UpstreamKey: "synthetic", OwnGroupIDs: []string{"7"}, OperationID: "binding-first"}
			done := make(chan error, 1)
			go func() {
				var err error
				if mode == "import-during-bind" {
					_, err = service.RealBind(t.Context(), "user-1", binding)
				} else {
					_, err = service.RealConnect(t.Context(), "user-1", first)
				}
				done <- err
			}()
			<-entered
			second := first
			second.OperationID = "grok-second"
			if strings.HasPrefix(mode, "same-operation") {
				second.OperationID = first.OperationID
			}
			if mode == "same-operation-other-target" {
				second.UpstreamGroupID = "71"
			}
			var err error
			if mode == "bind-during-import" {
				_, err = service.RealBind(t.Context(), "user-1", binding)
			} else {
				_, err = service.RealConnect(t.Context(), "user-1", second)
			}
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Message != ErrorImportInProgress || failure.RetryAllowed {
				t.Error("concurrent Grok operation was allowed to mutate")
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "import-during-bind" {
				expected = 0
			}
			if repo.connection == nil || remote.keyCreates != expected || remote.accountCreates != expected || repo.saveCalls != 1 {
				t.Fatal("concurrent Grok operations duplicated or lost creator")
			}
		})
	}
}

func TestGrokExistingBindingAndDisconnectPreserveOwnership(t *testing.T) {
	for _, source := range []string{"openai", "grok"} {
		t.Run(source, func(t *testing.T) {
			remote := &c5RedTransport{platform: source, mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.Method != http.MethodGet {
					return nil, nil, false
				}
				switch r.URL.Path {
				case "/api/v1/keys":
					return c5SafeResponse(r, 200, map[string]any{"data": []map[string]any{{"id": 11, "key": "synthetic-existing", "group_id": 70, "name": "existing-key", "status": "active"}}}), nil, true
				case "/api/v1/admin/accounts":
					return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"items": []map[string]any{{"id": 22, "name": "existing-grok", "platform": "grok", "type": "apikey", "status": "active", "group_ids": []int{7, 8}}}, "total": 1, "page": 1, "page_size": 100}}), nil, true
				}
				return nil, nil, false
			})
			disabled := false
			result, err := service.RealBind(t.Context(), "user-1", RealBindRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "70", UpstreamGroupName: "upstream-only", UpstreamKeyID: "11", AdminGroupID: "7", AdminResourceID: "22", OperationID: "existing-grok", AddToPricingMapping: &disabled})
			if err != nil {
				t.Fatal(err)
			}
			if repo.connection == nil || repo.connection.GroupType != source || repo.connection.ProvisioningMode != ProvisioningModeExisting || result.Connection.CanDeleteRemote || !reflect.DeepEqual(repo.connection.OwnGroupIDs, []string{"7", "8"}) || repo.connection.AdminAccountName != "existing-grok" {
				t.Fatal("binding changed original source semantics or resource ownership")
			}
			if err := service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: result.Connection.ID, Mode: "full"}); !errors.Is(err, requestError(ErrorManagedDeleteOnly)) {
				t.Fatal("bound Grok remote resource became deletable")
			}
			if repo.connection == nil {
				t.Fatal("rejected full delete dropped local binding")
			}
			if err := service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: result.Connection.ID, Mode: "unlink"}); err != nil {
				t.Fatal(err)
			}
			if repo.connection != nil || remote.keyCreates != 0 || remote.previews != 0 || remote.accountCreates != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 || repo.deleteCalls != 1 {
				t.Fatal("binding or unlink mutated remotely owned resources")
			}
		})
	}
}

func TestGrokManagedDisconnectRespectsProtectedDeletion(t *testing.T) {
	for _, mode := range []string{"unlink", "full", "rejected", "uncertain", "missing"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "grok", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			result, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			service.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(ctx context.Context, user, workspace string, session upstream.Session, id, source string) error {
				if user != "user-1" || workspace != "admin-1" || id != "22" || source != "manual_delete" {
					t.Fatal("Grok delete bypassed captured ownership")
				}
				remote.protectedDeletes++
				if mode == "rejected" {
					return &upstream.RequestError{MessageKey: upstream.ErrorForbidden, MutationOutcome: upstream.MutationConfirmedRejected}
				}
				if mode == "uncertain" {
					return io.EOF
				}
				return nil
			}})
			action := "full"
			if mode == "unlink" {
				action = "unlink"
			}
			if mode == "missing" {
				repo.connection.Status = ConnectionStatusMissing
			}
			err = service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: result.Connection.ID, Mode: action})
			if mode == "rejected" || mode == "uncertain" || mode == "missing" {
				if err == nil || repo.connection == nil || repo.deleteCalls != 0 || remote.keyDeletes != 0 {
					t.Fatal("unconfirmed deletion discarded local binding or Key")
				}
				return
			}
			if err != nil || repo.connection != nil || repo.deleteCalls != 1 {
				t.Fatal("confirmed Grok disconnect left association")
			}
			expected := 0
			if mode == "full" {
				expected = 1
			}
			if remote.protectedDeletes != expected || remote.keyDeletes != expected {
				t.Fatal("Grok disconnect changed deletion sequence")
			}
		})
	}
}

func TestGrokNewAPIUpstreamRetainsTokenIdentity(t *testing.T) {
	for _, name := range []string{"short", strings.Repeat("站点", 30), strings.Repeat("long-ascii", 20)} {
		t.Run(name, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: "grok"}
			service, repo := c5RedService(t, remote)
			site := service.upstreamLookup.(testUpstreamLookup).sites["site-1"]
			session := platformTestSession(upstream.PlatformNewAPI, site.BaseURL)
			site.Platform, site.Session, site.Name = upstream.PlatformNewAPI, &session, name
			tokenName := ""
			c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
				switch r.URL.Path {
				case "/api/token/":
					if r.Method == http.MethodPost {
						var body map[string]any
						if json.NewDecoder(r.Body).Decode(&body) != nil {
							t.Fatal("invalid token request")
						}
						tokenName, _ = body["name"].(string)
						remote.keyCreates++
						return c5SafeResponse(r, 200, map[string]any{"success": true}), nil, true
					}
					return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"items": []map[string]any{{"id": 11, "name": tokenName, "group": "70"}}, "total": 1, "page": 1, "page_size": 100}}), nil, true
				case "/api/token/11/key":
					return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"key": "synthetic-c5-key"}}), nil, true
				}
				return nil, nil, false
			})
			result, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			if repo.connection == nil || repo.connection.GroupType != "grok" || repo.connection.UpstreamPlatform != "newapi" || result.Configuration == nil || result.Configuration.Platform != "grok" || result.Configuration.ModelState != "synced" || remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || repo.saveCalls != 1 {
				t.Fatal("NewAPI token did not complete Grok pipeline")
			}
			if len(tokenName) > 50 || !utf8.ValidString(tokenName) || !strings.HasSuffix(tokenName, "-"+repo.connection.ID) || len(repo.connection.ID) != 32 {
				t.Fatal("NewAPI name lost full unique suffix or original byte bound")
			}
		})
	}
}

func TestGrokPostgresPersistsPlatformTargetsAndMappingAtomically(t *testing.T) {
	for _, mapping := range []bool{false, true} {
		t.Run(fmt.Sprintf("mapping-%t", mapping), func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			prepareCleanupState(t, repo, nil)
			if _, err := pool.Exec(t.Context(), `CREATE TABLE upstream_sites (id text, user_id text, admin_account_id text, name text)`); err != nil {
				t.Fatal(err)
			}
			connection := RealConnection{ID: "grok-persisted", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "grok-source", UpstreamGroupID: "70", UpstreamGroupName: "grok-source-group", UpstreamKeyID: "11", UpstreamKey: "synthetic-grok", AdminAccountID: "22", AdminAccountName: "X-grok", OwnGroupIDs: []string{"7", "8"}, OwnGroupNames: []string{"own-a", "own-b"}, GroupType: "grok", ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusActive, UpstreamPlatform: "sub2api", AdminPlatform: "sub2api", PricingMappingEnabled: mapping, OperationID: "grok-operation", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := repo.SaveRealConnectionWithPricingMapping(t.Context(), connection); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRealConnection(t.Context(), connection.ID, "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			if saved == nil || saved.GroupType != "grok" || saved.PricingMappingEnabled != mapping || !reflect.DeepEqual(saved.OwnGroupIDs, connection.OwnGroupIDs) || !reflect.DeepEqual(saved.OwnGroupNames, connection.OwnGroupNames) || saved.UpstreamKey != connection.UpstreamKey || saved.OperationID != connection.OperationID {
				t.Fatal("PostgreSQL lost Grok settings, groups or connection identity")
			}
			state, err := repo.Get(t.Context(), "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]bool{"own-a": false, "own-b": false}
			for _, m := range state.Mappings {
				for _, target := range m.UpstreamTargets {
					if !mapping || target.SiteID != connection.UpstreamSiteID || target.GroupName != connection.UpstreamGroupName {
						t.Fatal("disabled or wrong source pricing mapping persisted")
					}
					expected[m.OwnGroup] = true
				}
			}
			if mapping && (!expected["own-a"] || !expected["own-b"]) {
				t.Fatal("Grok targets did not persist all pricing mappings")
			}
			if !mapping && len(state.Mappings) != 1 {
				t.Fatal("mapping-off modified existing mapping rows")
			}
		})
	}
}

func TestGrokReadbackDiagnosticNamesActualSupportedPlatform(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	service, repo := c5RedService(t, remote)
	c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.URL.Path != "/api/v1/admin/accounts/22" {
			return nil, nil, false
		}
		response, err := remote.RoundTrip(r)
		if err != nil {
			return response, err, true
		}
		var payload map[string]any
		if json.NewDecoder(response.Body).Decode(&payload) != nil {
			t.Fatal("invalid readback fixture")
		}
		response.Body.Close()
		payload["data"].(map[string]any)["platform"] = "grok"
		return c5SafeResponse(r, 200, payload), nil, true
	})
	_, err := service.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.Message != ErrorImportConfigurationMismatch || repo.saveCalls != 0 || remote.protectedDeletes != 1 || remote.keyDeletes != 1 {
		t.Fatal("wrong-platform Grok readback bypassed safe compensation")
	}
	diagnostic := warningDiagnostics(t, failure, 1)[0].(map[string]any)
	detail := stringValueForWarning(diagnostic["detail"])
	if !strings.Contains(detail, "platform 平台要求 openai，实际 grok") || strings.Contains(detail, "不支持的平台") {
		t.Fatal("supported Grok was lost in the actual-platform diagnostic")
	}
}

func TestGrokMissingConnectionCleanupIsLocalAndRetainsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime-failure-%t", fail), func(t *testing.T) {
			remote := &c5RedTransport{platform: "grok"}
			service, repo := c5RedService(t, remote)
			result, err := service.RealConnect(t.Context(), "user-1", grokRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			repo.connection.Status = ConnectionStatusMissing
			runtime := &cleanupRuntimeRecorder{}
			if fail {
				runtime.err = errors.New("synthetic runtime cleanup unavailable")
			}
			service.SetConnectionRuntimeCleaner(runtime)
			err = service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: result.Connection.ID, Mode: "unlink"})
			if runtime.calls != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
				t.Fatal("missing Grok cleanup skipped runtime or mutated remote resources")
			}
			if fail {
				if err == nil || repo.connection == nil || repo.deleteCalls != 0 {
					t.Fatal("failed runtime cleanup lost local association")
				}
				return
			}
			if err != nil || repo.connection != nil || repo.deleteCalls != 1 {
				t.Fatal("confirmed local cleanup did not remove missing Grok association")
			}
		})
	}
}
