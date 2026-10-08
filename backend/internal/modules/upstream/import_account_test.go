package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"testing"
	"time"
)

type c5ImportTransport func(*http.Request) (*http.Response, error)

func (f c5ImportTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func c5ImportResponse(r *http.Request, status int, body any) (*http.Response, error) {
	var encoded []byte
	if raw, ok := body.(string); ok {
		encoded = []byte(raw)
	} else {
		encoded, _ = json.Marshal(body)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
}
func c5ImportSession(platform Platform) Session {
	return Session{Platform: platform, BaseURL: "https://c5.invalid", AccessToken: "synthetic-session", UserID: "1"}
}
func c5ImportService(run c5ImportTransport) *PlatformService {
	return NewPlatformService(NewHTTPClient(&http.Client{Transport: run}))
}

func TestC5ImportModelPreviewTimeoutHasBoundedNativeRequest(t *testing.T) {
	calls := 0
	s := c5ImportService(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/models/sync-upstream-preview" {
			t.Fatal("timeout did not use the native preview endpoint")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			t.Fatal("native preview timeout was not bounded to thirty seconds")
		}
		return nil, context.DeadlineExceeded
	})
	models, err := s.PreviewSub2APIImportModelsContext(context.Background(), c5ImportSession(PlatformSub2API), "openai", "https://source.invalid", "synthetic-key")
	var failure *RequestError
	if calls != 1 || len(models) != 0 || !errors.As(err, &failure) || !failure.Timeout || failure.Reason != ErrorNetworkTimeout {
		t.Fatal("preview timeout retried, returned models, or lost its safe timeout classification")
	}
}

func TestC5ImportModelPreviewStrictLiveSet(t *testing.T) {
	for _, sample := range []struct {
		name   string
		body   any
		status int
		want   []string
	}{
		{"live-with-warning", map[string]any{"code": 0, "data": map[string]any{"models": []string{" z ", "a", "z"}, "warnings": []string{"synthetic warning"}}}, 200, []string{"a", "z"}},
		{"empty", `{"code":0,"data":{"models":[]}}`, 200, nil},
		{"wildcard", `{"code":0,"data":{"models":["a","*"]}}`, 200, nil},
		{"blank", `{"code":0,"data":{"models":["a"," "]}}`, 200, nil},
		{"wrong-type", `{"code":0,"data":{"models":["a",3]}}`, 200, nil},
		{"missing-code", `{"data":{"models":["a"]}}`, 200, nil},
		{"invalid-envelope", `{"code":1,"data":{"models":["a"]}}`, 200, nil},
		{"bad-json", "{broken", 200, nil},
		{"unauthorized", `{"code":401}`, 401, nil},
		{"forbidden", `{"code":403}`, 403, nil},
		{"unsupported", `{"code":404}`, 404, nil},
		{"method-unsupported", `{"code":405}`, 405, nil},
	} {
		t.Run(sample.name, func(t *testing.T) {
			calls := 0
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/admin/accounts/models/sync-upstream-preview" {
					t.Fatal("preview used a wrong endpoint")
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["type"] != "apikey" || body["platform"] != "openai" || body["base_url"] != "https://source.invalid" || body["api_key"] != "synthetic-key" || len(body) != 4 {
					t.Fatal("preview changed live request contract")
				}
				return c5ImportResponse(r, sample.status, sample.body)
			})
			models, err := s.PreviewSub2APIImportModelsContext(context.Background(), c5ImportSession(PlatformSub2API), "openai", "https://source.invalid", "synthetic-key")
			if calls != 1 || (err == nil) != (sample.want != nil) || !reflect.DeepEqual(models, sample.want) {
				t.Fatalf("models=%v error=%v calls=%d", models, err, calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := c5ImportService(func(r *http.Request) (*http.Response, error) {
		t.Fatal("canceled preview sent HTTP request")
		return nil, nil
	})
	if _, err := s.PreviewSub2APIImportModelsContext(ctx, c5ImportSession(PlatformSub2API), "openai", "x", "x"); !errors.Is(err, context.Canceled) {
		t.Fatal("preview lost request cancellation")
	}
}

func TestC5ImportSub2APIKeyPreservesStageEvidence(t *testing.T) {
	for _, sample := range []struct {
		name, body, wantID, wantOutcome string
		wantOK                          bool
	}{
		{"success", `{"code":0,"data":{"id":11,"key":"synthetic-key"}}`, "11", ImportCredentialKnownID, true},
		{"known-id-no-key", `{"code":0,"data":{"id":11}}`, "11", ImportCredentialKnownID, false},
		{"empty-key", `{"code":0,"data":{"id":11,"key":" "}}`, "11", ImportCredentialKnownID, false},
		{"missing-id", `{"code":0,"data":{"key":"synthetic-key"}}`, "", ImportCredentialUnknownID, false},
		{"fractional-id", `{"code":0,"data":{"id":11.4,"key":"synthetic-key"}}`, "", ImportCredentialUnknownID, false},
		{"missing-code", `{"data":{"id":11,"key":"synthetic-key"}}`, "11", ImportCredentialUncertain, false},
		{"contradictory-code", `{"code":1,"data":{"id":11,"key":"synthetic-key"}}`, "11", ImportCredentialUncertain, false},
		{"contradictory-success", `{"code":0,"success":false,"data":{"id":11,"key":"must-never-project"}}`, "11", ImportCredentialUncertain, false},
		{"bad-json", "{broken", "", ImportCredentialUncertain, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			s := c5ImportService(func(r *http.Request) (*http.Response, error) { return c5ImportResponse(r, 200, sample.body) })
			result, err := s.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(PlatformSub2API), "safe-name", "7")
			if result.ID != sample.wantID || result.Outcome != sample.wantOutcome || (err == nil) != sample.wantOK {
				t.Fatalf("stage evidence lost: id=%s outcome=%s err=%v", result.ID, result.Outcome, err)
			}
		})
	}
	s := c5ImportService(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, io.EOF
	})
	result, err := s.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(PlatformSub2API), "safe-name", "7")
	if err == nil || result.Outcome != ImportCredentialUncertain {
		t.Fatal("lost-response create was not uncertain")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = s.CreateImportUpstreamCredentialContext(ctx, c5ImportSession(PlatformSub2API), "safe-name", "7")
	if err == nil || result.Outcome != ImportCredentialNotSent {
		t.Fatal("canceled before send did not prove not-sent")
	}
}

func TestC5ImportNewAPIReadsCompleteUniquePagination(t *testing.T) {
	for _, mode := range []string{"unique-second-page", "duplicate-second-page", "missing-total", "changed-total", "short-page", "wrong-page", "wrong-page-size", "invalid-id", "duplicate-id", "invalid-name", "over-limit", "no-match", "key-fails", "key-missing", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			pages, keyReads := 0, 0
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" && r.URL.Path == "/api/token/" {
					return c5ImportResponse(r, 200, map[string]any{"success": true})
				}
				if r.Method == "POST" && r.URL.Path == "/api/token/101/key" {
					keyReads++
					if mode == "key-fails" {
						return nil, io.EOF
					}
					if mode == "key-missing" {
						return c5ImportResponse(r, 200, `{"success":true,"data":{}}`)
					}
					return c5ImportResponse(r, 200, `{"success":true,"data":{"key":"synthetic-key"}}`)
				}
				if r.Method != "GET" || r.URL.Path != "/api/token/" {
					t.Fatalf("unexpected import request %s %s", r.Method, r.URL.Path)
				}
				pages++
				if mode == "timeout" {
					return nil, context.DeadlineExceeded
				}
				page := pages
				count := 100
				if page == 2 {
					count = 1
				}
				items := make([]any, 0, count)
				for i := 0; i < count; i++ {
					id := (page-1)*100 + i + 1
					name := fmt.Sprintf("unrelated-%d", id)
					if id == 101 && mode != "no-match" || mode == "duplicate-second-page" && id == 1 {
						name = "safe-name"
					}
					items = append(items, map[string]any{"id": id, "name": name})
				}
				data := map[string]any{"page": page, "page_size": 100, "total": 101, "items": items}
				switch mode {
				case "missing-total":
					delete(data, "total")
				case "changed-total":
					if page == 2 {
						data["total"] = 102
					}
				case "short-page":
					if page == 1 {
						data["items"] = items[:99]
					}
				case "wrong-page":
					data["page"] = 0
				case "wrong-page-size":
					data["page_size"] = 50
				case "invalid-id":
					items[0].(map[string]any)["id"] = 0
				case "duplicate-id":
					if page == 2 {
						items[0].(map[string]any)["id"] = 1
					}
				case "invalid-name":
					items[0].(map[string]any)["name"] = 2
				case "over-limit":
					data["total"] = 1001
				}
				return c5ImportResponse(r, 200, map[string]any{"success": true, "data": data})
			})
			result, err := s.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(PlatformNewAPI), "safe-name", "vip")
			if mode == "unique-second-page" {
				if err != nil || result.ID != "101" || result.Key == "" || pages != 2 || keyReads != 1 {
					t.Fatal("complete unique lookup failed")
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe list/key lookup returned success")
			}
			if mode == "key-fails" || mode == "key-missing" {
				if result.ID != "101" || result.Outcome != ImportCredentialKnownID || keyReads != 1 {
					t.Fatal("known token ID lost after Key failure")
				}
			} else if result.ID != "" || result.Outcome != ImportCredentialUnknownID || keyReads != 0 {
				t.Fatal("unproved candidate was selected or secret fetched")
			}
		})
	}
}

func TestC5ImportNewAPITenPagesMustAllProveUniqueness(t *testing.T) {
	for _, mode := range []string{"unique-first", "unique-last", "first-and-last-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			pages, keyReads := 0, 0
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/token/" {
					return c5ImportResponse(r, 200, `{"success":true}`)
				}
				if strings.HasSuffix(r.URL.Path, "/key") {
					keyReads++
					if pages != 10 {
						t.Fatal("Key fetched before completing all ten pages")
					}
					return c5ImportResponse(r, 200, `{"success":true,"data":{"key":"synthetic-key"}}`)
				}
				pages++
				items := make([]any, 100)
				for i := range items {
					id := (pages-1)*100 + i + 1
					name := fmt.Sprintf("other-%d", id)
					if id == 1 && mode != "unique-last" || id == 1000 && mode != "unique-first" {
						name = "safe-name"
					}
					items[i] = map[string]any{"id": id, "name": name}
				}
				return c5ImportResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"page": pages, "page_size": 100, "total": 1000, "items": items}})
			})
			result, err := s.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(PlatformNewAPI), "safe-name", "vip")
			if pages != 10 {
				t.Fatal("bounded maximum pagination did not finish")
			}
			if mode == "first-and-last-duplicate" {
				if err == nil || result.ID != "" || keyReads != 0 || result.Outcome != ImportCredentialUnknownID {
					t.Fatal("first matching name was trusted before a duplicate on last page")
				}
			} else if err != nil || result.ID == "" || keyReads != 1 {
				t.Fatal("complete ten-page unique match failed")
			}
		})
	}
}

func TestC5ImportKeyCleanupRequiresBusinessReceipt(t *testing.T) {
	for _, sample := range []struct {
		name     string
		platform Platform
		status   int
		body     string
		want     string
	}{
		{"sub-confirmed", PlatformSub2API, 200, `{"code":0,"data":{"message":"deleted"}}`, ImportCleanupConfirmed},
		{"sub-missing-code", PlatformSub2API, 200, `{"data":{"message":"deleted"}}`, ImportCleanupPending},
		{"sub-missing-message", PlatformSub2API, 200, `{"code":0,"data":{}}`, ImportCleanupPending},
		{"sub-conflicting-success", PlatformSub2API, 200, `{"code":0,"success":false,"data":{"message":"deleted"}}`, ImportCleanupPending},
		{"new-confirmed", PlatformNewAPI, 200, `{"success":true}`, ImportCleanupConfirmed},
		{"new-code-conflict", PlatformNewAPI, 200, `{"success":true,"code":2}`, ImportCleanupPending},
		{"new-missing-success", PlatformNewAPI, 200, `{}`, ImportCleanupPending},
		{"empty", PlatformSub2API, 204, ``, ImportCleanupPending},
		{"malformed", PlatformNewAPI, 200, `{broken`, ImportCleanupPending},
		{"not-found", PlatformSub2API, 404, `{"code":404}`, ImportCleanupPending},
		{"confirmed-rejected", PlatformSub2API, 403, `{"code":"FORBIDDEN"}`, ImportCleanupRetained},
	} {
		t.Run(sample.name, func(t *testing.T) {
			calls := 0
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				calls++
				return c5ImportResponse(r, sample.status, sample.body)
			})
			outcome, err := s.DeleteImportUpstreamCredentialContext(context.Background(), c5ImportSession(sample.platform), "11")
			if calls != 1 || outcome != sample.want || (err == nil) != (sample.want == ImportCleanupConfirmed) {
				t.Fatalf("cleanup=%s err=%v calls=%d", outcome, err, calls)
			}
		})
	}
}

func TestC5ImportConfigurationIsSafeAndStrict(t *testing.T) {
	for _, mode := range []string{"valid", "missing-closed", "enabled-missing", "invalid-switch", "invalid-mapping", "fractional-priority", "group-set-contradiction", "unreadable-name", "missing-both-groups", "explicit-empty-groups"} {
		t.Run(mode, func(t *testing.T) {
			data := map[string]any{"id": 22, "name": "safe-account", "platform": "openai", "type": "apikey", "priority": 100, "concurrency": 50, "credentials": map[string]any{"api_key": "must-never-project", "base_url": "must-never-project", "pool_mode": true, "model_mapping": map[string]any{"model-a": "model-a"}}, "extra": map[string]any{"upstream_billing_probe_enabled": true, "openai_passthrough": false}, "group_ids": []int{7, 8}, "groups": []map[string]any{{"id": 7, "name": "readback-a"}, {"id": 8, "name": "readback-b"}}}
			switch mode {
			case "missing-closed":
				delete(data["extra"].(map[string]any), "openai_passthrough")
			case "enabled-missing":
				delete(data["extra"].(map[string]any), "upstream_billing_probe_enabled")
			case "invalid-switch":
				data["credentials"].(map[string]any)["pool_mode"] = "true"
			case "invalid-mapping":
				data["credentials"].(map[string]any)["model_mapping"] = []string{"a"}
			case "fractional-priority":
				data["priority"] = 100.5
			case "group-set-contradiction":
				data["group_ids"] = []int{7}
			case "unreadable-name":
				data["groups"].([]map[string]any)[0]["name"] = nil
			case "missing-both-groups":
				delete(data, "group_ids")
				delete(data, "groups")
			case "explicit-empty-groups":
				data["group_ids"] = []int{}
				data["groups"] = []map[string]any{}
			}
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				return c5ImportResponse(r, 200, map[string]any{"code": 0, "data": data})
			})
			result, err := s.ReadSub2APIImportConfigurationContext(context.Background(), c5ImportSession(PlatformSub2API), "22")
			wantOK := mode == "valid" || mode == "missing-closed" || mode == "enabled-missing" || mode == "unreadable-name" || mode == "explicit-empty-groups"
			if (err == nil) != wantOK {
				t.Fatalf("unexpected safe projection outcome %v", err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "must-never-project") {
				t.Fatal("safe projection leaked credentials")
			}
			if mode == "unreadable-name" && result.Groups[0].Name != "名称不可读" {
				t.Fatal("unreadable group name replaced by form values")
			}
		})
	}
}
