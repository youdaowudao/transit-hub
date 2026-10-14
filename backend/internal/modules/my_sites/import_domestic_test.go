package my_sites

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestC51AllOpenAIFamilyImports(t *testing.T) {
	family := []string{"openai", "kimi", "zhipu", "deepseek", "minimax"}
	for _, source := range family {
		for _, target := range family {
			t.Run(source+"/"+target, func(t *testing.T) {
				prefix := "X"
				if target == "openai" {
					prefix = "A"
				}
				c51AssertImportedPlatform(t, source, " "+strings.ToUpper(target)+" ", target, prefix)
			})
		}
	}
}

func TestImportSelectionAllSourceAndTargetPlatforms(t *testing.T) {
	platforms := []string{"openai", "kimi", "zhipu", "deepseek", "minimax", "anthropic", "gemini", "antigravity", "grok"}
	prefixes := map[string]string{"openai": "A", "anthropic": "B", "gemini": "C", "antigravity": "D", "grok": "X", "kimi": "X", "zhipu": "X", "deepseek": "X", "minimax": "X"}
	for _, source := range platforms {
		for _, target := range platforms {
			t.Run(source+"/"+target, func(t *testing.T) {
				c51AssertImportedPlatform(t, source, target, target, prefixes[target])
			})
		}
	}
	for _, target := range []string{"", "unknown", "opencode-go"} {
		t.Run("untyped/"+target, func(t *testing.T) {
			c51AssertRejectedBeforeResources(t, &c5RedTransport{platform: "", mainPlatform: target}, target)
		})
	}
}

func TestC51DomesticPassthroughRejectedBeforeResources(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax", "grok"} {
		t.Run(platform, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai", mainPlatform: platform}
			service, repo := c5RedService(t, remote)
			req := c5RedRequest(t, `{"passthrough":true}`)
			req.GroupType = platform
			_, err := service.RealConnect(t.Context(), "user-1", req)
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Message != ErrorImportSettingsInvalid || failure.Stage != "validation" || failure.Cleanup != "not_needed" || !failure.RetryAllowed {
				t.Fatal("domestic passthrough must retain existing unsupported-passthrough validation result")
			}
			if remote.keyCreates != 0 || remote.accountCreates != 0 || remote.previews != 0 || repo.saveCalls != 0 {
				t.Fatal("domestic passthrough reached resource creation")
			}
		})
	}
}

func TestC51DomesticRequestPlatformFallback(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax", "grok"} {
		t.Run(platform, func(t *testing.T) { c51AssertImportedPlatform(t, platform, "   ", platform, "X") })
	}
}

func TestC51DomesticFailuresPreserveCleanupAndPendingResults(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax", "grok"} {
		for _, mode := range []string{"preview", "account-rejected", "account-uncertain", "read-unavailable", "read-mismatch", "commit-uncertain", "commit-not-committed"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				remote := &c5RedTransport{platform: "openai", mainPlatform: platform, previewFailure: mode == "preview", configurationMismatch: mode == "read-mismatch"}
				service, base := c5RedService(t, remote)
				if strings.HasPrefix(mode, "commit-") {
					saveError := error(errors.New("synthetic local commit failure"))
					if mode == "commit-not-committed" {
						saveError = &ConnectionCommitError{Outcome: CommitConfirmedNotCommitted, Cause: saveError}
					}
					service.connRepository = &c5OutcomeRepository{testConnRepo: base, saveError: saveError}
				}
				c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
					if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts" && strings.HasPrefix(mode, "account-") {
						remote.accountCreates++
						if mode == "account-uncertain" {
							return c5SafeResponse(r, 500, map[string]any{"code": "ERROR", "data": map[string]any{"id": 22}}), nil, true
						}
						return c5SafeResponse(r, 403, map[string]any{"code": "FORBIDDEN"}), nil, true
					}
					if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/22" && mode == "read-unavailable" {
						remote.reads++
						return nil, io.EOF, true
					}
					return nil, nil, false
				})
				req := c5RedRequest(t, "")
				req.GroupType = platform
				response, err := service.RealConnect(t.Context(), "user-1", req)
				if mode == "preview" {
					if err != nil || response.Configuration == nil || response.Configuration.ModelState != "not_synced" || len(response.Diagnostics) != 1 || response.Configuration.Platform != platform || base.connection == nil || base.saveCalls != 1 || remote.accountCreates != 1 || remote.reads != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
						t.Fatal("domestic model warning lost full import or original platform")
					}
					return
				}
				var failure *ImportFailure
				if !errors.As(err, &failure) || response.Configuration != nil || response.Connection.ID != "" {
					t.Fatal("failed domestic import claimed success")
				}
				stage, cleanup, message := "model_sync", "confirmed", ErrorImportModelSyncFailed
				accounts, reads, saves, deletes, protected := 0, 0, 0, 1, 0
				switch mode {
				case "account-rejected":
					stage, message, accounts = "account_create", ErrorImportAccountCreateFailed, 1
				case "account-uncertain":
					stage, cleanup, message, accounts, deletes = "account_create", "pending", "admin.mySites.errors.accountCreationPendingVerification", 1, 0
				case "read-unavailable":
					stage, cleanup, message, accounts, reads, deletes = "configuration_check", "retained", ErrorImportConfigurationUnavailable, 1, 1, 0
				case "read-mismatch":
					stage, message, accounts, reads, protected = "configuration_check", ErrorImportConfigurationMismatch, 1, 1, 1
				case "commit-uncertain":
					stage, cleanup, message, accounts, reads, saves, deletes = "persistence", "retained", ErrorImportPersistencePending, 1, 1, 1, 0
				case "commit-not-committed":
					stage, message, accounts, reads, saves, protected = "persistence", ErrorImportPersistenceFailed, 1, 1, 1, 1
				}
				if failure.Stage != stage || failure.Cleanup != cleanup || failure.Message != message || failure.RetryAllowed != (cleanup == "confirmed") || failure.UpstreamKeyID != "11" {
					t.Fatalf("wrong existing failure contract: stage=%s cleanup=%s retry=%t message=%s", failure.Stage, failure.Cleanup, failure.RetryAllowed, failure.Message)
				}
				if cleanup != "confirmed" && (failure.StatusCode != 409 || failure.AdminResourceID != "22") {
					t.Fatal("uncertain domestic result lost resource identity or conflict status")
				}
				if remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != accounts || remote.reads != reads || base.saveCalls != saves || remote.keyDeletes != deletes || remote.protectedDeletes != protected || base.connection != nil {
					t.Fatalf("cleanup pipeline changed: keys=%d preview=%d accounts=%d reads=%d saves=%d deleted-key=%d protected=%d", remote.keyCreates, remote.previews, remote.accountCreates, remote.reads, base.saveCalls, remote.keyDeletes, remote.protectedDeletes)
				}
			})
		}
	}
}

func TestC51DomesticReplayReadsCurrentConfigurationWithoutCreating(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek", "minimax", "grok"} {
		for _, unavailable := range []bool{false, true} {
			t.Run(platform+map[bool]string{false: "/current", true: "/unavailable"}[unavailable], func(t *testing.T) {
				remote := &c5RedTransport{platform: "openai", mainPlatform: platform}
				service, base := c5RedService(t, remote)
				req := c5RedRequest(t, "")
				req.GroupType = platform
				created, err := service.RealConnect(t.Context(), "user-1", req)
				if err != nil {
					t.Fatal(err)
				}
				service.connRepository = &c5OutcomeRepository{testConnRepo: base, replay: base.connection}
				c5UseOverride(service, remote, func(r *http.Request) (*http.Response, error, bool) {
					if r.URL.Path == "/api/v1/admin/groups" {
						t.Fatal("replay validated mutable groups")
					}
					if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/22" {
						remote.reads++
						if unavailable {
							return nil, io.EOF, true
						}
						data := map[string]any{"id": 22, "name": "current-domestic", "platform": platform, "type": "apikey", "priority": 7, "concurrency": 19, "credentials": map[string]any{"pool_mode": false, "model_mapping": map[string]string{"current-alias": "changed-model"}}, "extra": map[string]any{"upstream_billing_probe_enabled": false}, "group_ids": []int{9}, "groups": []map[string]any{{"id": 9, "name": "current-group"}}}
						return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": data}), nil, true
					}
					return nil, nil, false
				})
				req = c5RedRequest(t, `{"passthrough":true,"priority":0}`)
				req.GroupType, req.OwnGroupIDs = "unknown", nil
				result, err := service.RealConnect(t.Context(), "user-1", req)
				if err != nil || result.Connection.ID != created.Connection.ID || result.Connection.GroupType != platform || remote.keyCreates != 1 || remote.accountCreates != 1 || remote.previews != 1 || remote.reads != 2 || base.saveCalls != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
					t.Fatal("domestic replay lost completed binding or mutated resources")
				}
				if unavailable {
					if result.ConfigurationStatus != "unavailable" || result.Configuration != nil {
						t.Fatal("unavailable current config claimed confirmation")
					}
					return
				}
				if result.ConfigurationStatus != "confirmed" || result.Configuration == nil || result.Configuration.Platform != platform || result.Configuration.Observation != "current" || result.Configuration.ModelState != "current_whitelist" || result.Configuration.Priority != 7 || result.Configuration.Concurrency != 19 || result.Configuration.PoolMode || result.Configuration.Passthrough || result.Configuration.UpstreamBillingProbeEnabled || !reflect.DeepEqual(result.Configuration.Models, []string{"current-alias"}) || !reflect.DeepEqual(result.Configuration.OwnGroups, []ImportOwnGroup{{ID: "9", Name: "current-group"}}) {
					t.Fatal("domestic replay did not return current configuration")
				}
			})
		}
	}
}
