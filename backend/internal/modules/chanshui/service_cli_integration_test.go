package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/connection_health"
	"transithub/backend/internal/modules/upstream"
)

type actualServiceTargets struct {
	audit connection_health.AuditContext
}

func (f actualServiceTargets) ListAuditTargets(context.Context, string) ([]connection_health.AuditTarget, error) {
	return []connection_health.AuditTarget{{TargetID: f.audit.TargetID, AccountName: f.audit.AccountName}}, nil
}
func (f actualServiceTargets) ResolveAuditContext(context.Context, string, string, string, bool) (connection_health.AuditContext, error) {
	return f.audit, nil
}

// This boundary is intentionally exercised through Service, not directly Runner:
// its generated recovery payload and production credential enums reach the CLI.
func TestChanshuiActualServiceSequentialRunsAndCredentialEnums(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit actual Service/CLI resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := actualSlice(t)
	for _, scheme := range []string{"chat", "authorization_bearer", "x_api_key"} {
		t.Run(scheme, func(t *testing.T) {
			var paid, modelReads, wrongHeaders atomic.Int32
			key := "isolated-service-cli-fixture-key"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer := scheme != "x_api_key"
				valid := r.Header.Get("Authorization") == "Bearer "+key && r.Header.Get("x-api-key") == ""
				if scheme == "chat" && r.URL.Path == "/v1/models" {
					// Existing Chat model discovery sends both compatibility headers.
					valid = r.Header.Get("Authorization") == "Bearer "+key && r.Header.Get("x-api-key") == key
				}
				if !bearer {
					valid = r.Header.Get("x-api-key") == key && r.Header.Get("Authorization") == ""
				}
				if !valid {
					wrongHeaders.Add(1)
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":{"message":"isolated fixture: wrong authentication header"}}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/models" {
					modelReads.Add(1)
					fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
					return
				}
				var input struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					w.WriteHeader(400)
					return
				}
				paid.Add(1)
				if input.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					if scheme == "chat" {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"最终答案: 开车去"},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}

data: [DONE]

`)
					} else {
						fmt.Fprint(w, `data: {"type":"message_start","message":{"content":[],"usage":{"input_tokens":10}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"最终答案: 开车去"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

data: {"type":"message_stop"}

`)
					}
				} else if scheme == "chat" {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"最终答案: 开车去"},"finish_reason":"stop"}]}`)
				} else {
					fmt.Fprint(w, `{"content":[{"type":"text","text":"最终答案: 开车去"}],"stop_reason":"end_turn"}`)
				}
			}))
			defer up.Close()
			protocol := "anthropic"
			if scheme == "chat" {
				protocol = "chat"
			}
			authScheme := scheme
			if scheme == "chat" {
				authScheme = "x_api_key"
			}
			audit := connection_health.AuditContext{WorkspaceID: "workspace-a", TargetID: "fixture-account", AccountID: "fixture-account", AccountName: "跨模块隔离验收", GroupID: "fixture-group", GroupName: "GPT隔离组", BaseURL: up.URL, Protocol: protocol, CanRun: true, Credential: upstream.ProbeCredential{BaseURL: up.URL, Key: key, AnthropicAuth: authScheme}}
			runner := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui"), DataDir: t.TempDir(), ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
			s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, targets: actualServiceTargets{audit}, runner: runner, initialized: true}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				// Also wait for the Service observer before deleting its SQLite files.
				_ = s.Shutdown(ctx)
				assertActualEmpty(t, conn)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := Owner{"service-cli-fixture", "workspace-a"}
			// This fixture checks the configured protocol's exact credentials.
			// The default auto mode deliberately sends compatibility headers
			// for model discovery, so it is not the explicit Messages case.
			raw, err := s.queryJSON(ctx, "read-settings", owner, map[string]any{}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err = json.Unmarshal(raw, &settings); err != nil {
				t.Fatal(err)
			}
			settings["protocol"] = protocol
			if _, err = s.queryJSON(ctx, "save-settings", owner, settings, nil, false); err != nil {
				t.Fatal(err)
			}
			cred := credential(audit)
			models, err := s.queryJSON(ctx, "models", owner, map[string]any{}, &cred, false)
			if err != nil || !strings.Contains(string(models), "fixture-model") || wrongHeaders.Load() != 0 || modelReads.Load() != 1 || paid.Load() != 0 {
				t.Fatalf("production credential enum failed models: scheme=%s err=%v wrongHeaders=%d paid=%d", scheme, err, wrongHeaders.Load(), paid.Load())
			}
			const taskCount = 20
			for n := 1; n <= taskCount; n++ {
				id := fmt.Sprintf("%032x", n)
				input := StartInput{ReportID: id, TargetID: audit.TargetID, GroupID: audit.GroupID, Model: "fixture-model", SelectedQuestionIDs: []string{"Q2"}, ExpectedContext: expected(audit)}
				if _, err := s.Start(ctx, owner, input); err != nil {
					t.Fatalf("start %d: %v", n, err)
				}
				until := time.Now().Add(5 * time.Second)
				for time.Now().Before(until) {
					s.mu.Lock()
					idle := s.active == nil
					s.mu.Unlock()
					if idle {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				s.mu.Lock()
				active := s.active
				diagnostic := ""
				if active != nil {
					diagnostic = active.Diagnostic
				}
				s.mu.Unlock()
				if active != nil {
					t.Fatalf("run %d completed but Service did not release activity: %s", n, diagnostic)
				}
				report, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, false)
				if err != nil || !strings.Contains(string(report), `"status":"done"`) {
					t.Fatal(string(report), err)
				}
				assertActualEmpty(t, conn)
			}
			if paid.Load() != taskCount || wrongHeaders.Load() != 0 {
				t.Fatalf("wrong auth/replayed model requests: paid=%d wrong=%d", paid.Load(), wrongHeaders.Load())
			}
			t.Logf("actual Service %s models read once; %d sequential tasks saved, active released, all processes reclaimed", scheme, taskCount)
		})
	}
}
