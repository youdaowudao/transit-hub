package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"testing"
)

func TestTaskBConcurrencyBulkFieldsAndReceipts(t *testing.T) {
	for _, load := range []*int{nil, pointerToInt(48)} {
		for _, tc := range []struct {
			name    string
			status  int
			payload any
			want    string
		}{
			{"applied", 200, map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 15, "success": true}}}}, MutationConfirmedApplied},
			{"rejected", 400, map[string]any{"code": 400, "message": "Invalid request: concurrency"}, MutationConfirmedRejected},
			{"unsupported404", 404, map[string]any{}, MutationUncertain},
			{"unsupported405", 405, map[string]any{}, MutationUncertain},
			{"unsupported501", 501, map[string]any{}, MutationUncertain},
			{"server-failed", 500, map[string]any{}, MutationUncertain},
			{"bare-success", 200, map[string]any{"success": true}, MutationUncertain},
			{"wrong-account", 200, map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 16, "success": true}}}}, MutationUncertain},
		} {
			t.Run(tc.name+map[bool]string{true: "-load", false: "-no-load"}[load != nil], func(t *testing.T) {
				calls := 0
				s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodPost || req.URL.Path != "/api/v1/admin/accounts/bulk-update" {
						t.Fatalf("unexpected route %s %s", req.Method, req.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					want := map[string]any{"account_ids": []any{float64(15)}, "concurrency": float64(48)}
					if load != nil {
						want["load_factor"] = float64(48)
					}
					if !reflect.DeepEqual(body, want) {
						t.Fatalf("field-level payload=%v want=%v", body, want)
					}
					return stageAMutationResponse(req, tc.status, tc.payload)
				})
				err := s.UpdateSub2APIAdminAccountConcurrencyContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "fixture"}, "15", 48, load)
				if RemoteMutationOutcome(err) != tc.want || calls != 1 {
					t.Fatalf("outcome=%s calls=%d want=%s/1", RemoteMutationOutcome(err), calls, tc.want)
				}
			})
		}
	}
}

func pointerToInt(n int) *int { return &n }

func TestTaskBConcurrencyInvalidAndCanceledAreNotSent(t *testing.T) {
	for _, tc := range []struct {
		account     string
		concurrency int
		load        *int
		platform    Platform
		cancel      bool
	}{
		{"15", 0, nil, PlatformSub2API, false}, {"15", 1001, nil, PlatformSub2API, false}, {"15", 1, pointerToInt(0), PlatformSub2API, false},
		{"invalid", 1, nil, PlatformSub2API, false}, {"15", 1, nil, PlatformNewAPI, false}, {"15", 1, nil, PlatformSub2API, true},
	} {
		calls := 0
		s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected transport")
		})
		ctx, cancel := context.WithCancel(context.Background())
		if tc.cancel {
			cancel()
		}
		err := s.UpdateSub2APIAdminAccountConcurrencyContext(ctx, Session{Platform: tc.platform, BaseURL: "http://synthetic.test", AccessToken: "fixture"}, tc.account, tc.concurrency, tc.load)
		cancel()
		if RemoteMutationOutcome(err) != MutationNotSent || calls != 0 {
			t.Fatalf("invalid/canceled request outcome=%s calls=%d", RemoteMutationOutcome(err), calls)
		}
	}
}

func TestTaskBConcurrencyTransportUncertainIsNeverRetried(t *testing.T) {
	calls := 0
	s := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(req.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, errors.New("fixture connection reset after send")
	})
	err := s.UpdateSub2APIAdminAccountConcurrencyContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://synthetic.test", AccessToken: "fixture"}, "15", 48, nil)
	if RemoteMutationOutcome(err) != MutationUncertain || calls != 1 {
		t.Fatalf("uncertain mutation outcome=%s calls=%d", RemoteMutationOutcome(err), calls)
	}
}
