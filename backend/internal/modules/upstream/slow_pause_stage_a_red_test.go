package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These assertions are fixed by A01. Reflection allows the old parser to run
// and demonstrate that it discards restrictions rather than failing compilation.
func stageATimeField(t *testing.T, account AdminGroupAccountInfo, timeField, knownField string) (*time.Time, bool) {
	t.Helper()
	v := reflect.ValueOf(account)
	known, deadline := v.FieldByName(knownField), v.FieldByName(timeField)
	if !known.IsValid() || !deadline.IsValid() {
		t.Fatalf("account parser discarded required three-state field %s/%s", timeField, knownField)
	}
	if known.Kind() != reflect.Bool || deadline.Type() != reflect.TypeOf((*time.Time)(nil)) {
		t.Fatalf("invalid three-state representation for %s", timeField)
	}
	return deadline.Interface().(*time.Time), known.Bool()
}

func TestStageAAccountRestrictionThreeStates(t *testing.T) {
	for _, field := range []struct{ wire, deadline, known string }{
		{"temp_unschedulable_until", "TempUnschedulableUntil", "TempUnschedulableKnown"},
		{"rate_limit_reset_at", "RateLimitResetAt", "RateLimitKnown"},
		{"overload_until", "OverloadUntil", "OverloadKnown"},
	} {
		for _, sample := range []struct {
			name                     string
			value                    any
			present, known, deadline bool
		}{
			{"null", nil, true, true, false},
			{"valid", "2026-10-03T10:00:00Z", true, true, true},
			{"missing", nil, false, false, false},
			{"invalid", "not-a-time", true, false, false},
			{"wrong-type", float64(1), true, false, false},
		} {
			t.Run(field.wire+"/"+sample.name, func(t *testing.T) {
				record := map[string]any{"id": float64(1), "status": "active", "schedulable": true}
				if sample.present {
					record[field.wire] = sample.value
				}
				deadline, known := stageATimeField(t, parseSub2APIAccount(record), field.deadline, field.known)
				if known != sample.known || (deadline != nil) != sample.deadline {
					t.Fatalf("restriction state differs: known=%v deadline=%v", known, deadline)
				}
			})
		}
	}
}

func TestStageAInventoryHTTPDateGuard(t *testing.T) {
	for _, sample := range []struct {
		name, base string
		skew       time.Duration
		date       bool
		wantKnown  bool
	}{
		{"loopback-one-second", "http://127.0.0.1:8080", -time.Second, true, true},
		{"loopback-three-seconds", "http://127.0.0.1:8080", -3 * time.Second, true, false},
		{"missing-date", "http://127.0.0.1:8080", 0, false, false},
		{"non-loopback", "http://example.test", 0, true, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			transport := protocolInventoryTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.Path != "/api/v1/admin/accounts" {
					t.Fatalf("unexpected inventory query")
				}
				payload, _ := json.Marshal(map[string]any{"data": []any{
					map[string]any{"id": 1, "temp_unschedulable_until": "2026-10-03T10:00:00Z", "rate_limit_reset_at": nil},
				}, "total": 1})
				header := http.Header{}
				if sample.date {
					header.Set("Date", time.Now().Add(sample.skew).UTC().Format(http.TimeFormat))
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(string(payload))), Request: req}, nil
			})
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
			accounts, err := service.ListSub2APIAdminAccountsContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: sample.base, AccessToken: "synthetic-test-token"})
			if err != nil || len(accounts) != 1 {
				t.Fatalf("inventory failed: %v", err)
			}
			_, known := stageATimeField(t, accounts[0], "TempUnschedulableUntil", "TempUnschedulableKnown")
			if known != sample.wantKnown {
				t.Fatalf("HTTP Date guard: known=%v want=%v", known, sample.wantKnown)
			}
			deadline, unlimited := stageATimeField(t, accounts[0], "RateLimitResetAt", "RateLimitKnown")
			if !unlimited || deadline != nil {
				t.Fatal("null must remain unlimited even when guard fails")
			}
		})
	}
}
