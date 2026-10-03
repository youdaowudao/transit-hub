package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"syscall"
	"testing"
)

func stageAMutationService(run func(*http.Request) (*http.Response, error)) *PlatformService {
	return NewPlatformService(NewHTTPClient(&http.Client{Transport: protocolInventoryTransport(run)}))
}

func stageAMutationResponse(req *http.Request, status int, payload any) (*http.Response, error) {
	data, _ := json.Marshal(payload)
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(data))), Request: req}, nil
}

func TestStageAMutationTransportEvidence(t *testing.T) {
	for _, sample := range []struct {
		name               string
		dial, dns, written bool
		want               string
	}{
		{"refused", true, false, false, MutationNotSent},
		{"dns", false, true, false, MutationNotSent},
		{"dial-without-trace", false, false, false, MutationUncertain},
		{"written-then-dial-error", true, false, true, MutationUncertain},
		{"reused-connection-reset", false, false, true, MutationUncertain},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				if sample.written {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				var err error = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				if sample.dial {
					trace.ConnectDone("tcp", "127.0.0.1:8080", err)
				}
				if sample.dns {
					err = &net.DNSError{Err: "no such host", Name: "synthetic.test"}
					trace.DNSDone(httptrace.DNSDoneInfo{Err: err})
				}
				if sample.written && !sample.dial {
					err = &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
				}
				return nil, err
			})
			_, err := service.SetSub2APIAdminAccountSchedulableContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}, "1", false)
			if RemoteMutationOutcome(err) != sample.want {
				t.Fatalf("outcome=%s want=%s", RemoteMutationOutcome(err), sample.want)
			}
		})
	}
}

func TestStageASchedulableResponseEvidence(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		payload any
		want    string
	}{
		{"applied", 200, map[string]any{"code": 0, "data": map[string]any{"id": 1, "schedulable": false}}, MutationConfirmedApplied},
		{"wrong-account", 200, map[string]any{"data": map[string]any{"id": 2, "schedulable": false}}, MutationUncertain},
		{"wrong-value", 200, map[string]any{"data": map[string]any{"id": 1, "schedulable": true}}, MutationUncertain},
		{"missing-value", 200, map[string]any{"data": map[string]any{"id": 1}}, MutationUncertain},
		{"string-bool", 200, map[string]any{"data": map[string]any{"id": 1, "schedulable": "false"}}, MutationUncertain},
		{"empty-2xx", 200, map[string]any{}, MutationUncertain},
		{"handler-bind-rejected", 400, map[string]any{"code": 400, "message": "Invalid request: synthetic binding failure"}, MutationConfirmedRejected},
		{"unverified-400", 400, map[string]any{"code": 400, "message": "operation failed"}, MutationUncertain},
		{"admin-permission", 403, map[string]any{"code": "FORBIDDEN", "message": "Admin access required"}, MutationConfirmedRejected},
		{"unverified-403", 403, map[string]any{}, MutationUncertain},
		{"readback-not-found", 404, map[string]any{"code": 404, "reason": "ACCOUNT_NOT_FOUND"}, MutationUncertain},
		{"server-error", 500, map[string]any{"code": 500}, MutationUncertain},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
				return stageAMutationResponse(req, sample.status, sample.payload)
			})
			_, err := service.SetSub2APIAdminAccountSchedulableContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}, "1", false)
			if RemoteMutationOutcome(err) != sample.want {
				t.Fatalf("outcome=%s want=%s", RemoteMutationOutcome(err), sample.want)
			}
		})
	}
}

func TestStageAMutationLocalFailureAndContext(t *testing.T) {
	called := false
	service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		called = true
		return stageAMutationResponse(req, 200, map[string]any{})
	})
	session := Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}
	_, err := service.SetSub2APIAdminAccountSchedulableContext(context.Background(), session, "invalid", false)
	if called || RemoteMutationOutcome(err) != MutationNotSent {
		t.Fatal("invalid id must never send")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = service.DeleteSub2APIAdminAccountContext(ctx, session, "1")
	if called || RemoteMutationOutcome(err) != MutationNotSent || !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled context must retain not_sent evidence")
	}
}

func TestStageABulkUpdateRequiresSingleItemEvidence(t *testing.T) {
	for _, sample := range []struct {
		name    string
		payload any
		want    string
	}{
		{"applied", map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 1, "success": true}}}}, MutationConfirmedApplied},
		{"bare-success", map[string]any{"success": true}, MutationUncertain},
		{"contradictory-summary", map[string]any{"data": map[string]any{"failed": 1, "results": []any{map[string]any{"account_id": 1, "success": true}}}}, MutationUncertain},
		{"wrong-account", map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 2, "success": true}}}}, MutationUncertain},
		{"failed-item", map[string]any{"data": map[string]any{"results": []any{map[string]any{"account_id": 1, "success": false, "error": "failed after write"}}}}, MutationUncertain},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
				return stageAMutationResponse(req, 200, sample.payload)
			})
			err := service.UpdateSub2APIAdminAccountStatusContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}, "1", "inactive")
			if RemoteMutationOutcome(err) != sample.want {
				t.Fatalf("outcome=%s want=%s", RemoteMutationOutcome(err), sample.want)
			}
		})
	}
}

func TestStageAMutationErrorMetadataIsRestricted(t *testing.T) {
	service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
		return stageAMutationResponse(req, 400, map[string]any{"code": 400, "message": "Invalid request: credentials synthetic-private-data", "metadata": map[string]any{"secret": "synthetic-private-data"}, "reason": "ACCOUNT_NOT_FOUND"})
	})
	_, err := service.SetSub2APIAdminAccountSchedulableContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}, "1", false)
	requestErr := err.(*RequestError)
	if requestErr.RemoteMessage != "Invalid request" || strings.Contains(requestErr.Error(), "synthetic-private-data") {
		t.Fatal("raw validation or sensitive metadata escaped")
	}
}

func TestStageADeleteResponseEvidence(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		payload any
		want    string
	}{
		{"applied", 200, map[string]any{"code": 0, "data": map[string]any{"message": "Account deleted successfully"}}, MutationConfirmedApplied},
		{"unverified-2xx", 200, map[string]any{"success": true}, MutationUncertain},
		{"partial-delete-not-found", 404, map[string]any{"code": 404, "reason": "ACCOUNT_NOT_FOUND"}, MutationUncertain},
		{"server-error", 500, map[string]any{"code": 500}, MutationUncertain},
		{"handler-invalid-id", 400, map[string]any{"code": 400, "message": "Invalid account ID"}, MutationConfirmedRejected},
		{"middleware-expired-token", 401, map[string]any{"code": "TOKEN_EXPIRED"}, MutationConfirmedRejected},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/admin/accounts/1" {
					t.Fatal("delete used a different endpoint")
				}
				return stageAMutationResponse(req, sample.status, sample.payload)
			})
			err := service.DeleteSub2APIAdminAccountContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}, "1")
			if RemoteMutationOutcome(err) != sample.want {
				t.Fatalf("outcome=%s want=%s", RemoteMutationOutcome(err), sample.want)
			}
		})
	}
}

func TestStageAMutationWrittenTimeoutAndCancellationRemainUncertain(t *testing.T) {
	for _, action := range []string{"schedulable", "delete", "bulk-update"} {
		for _, failure := range []string{"timeout", "cancel"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service := stageAMutationService(func(req *http.Request) (*http.Response, error) {
					trace := httptrace.ContextClientTrace(req.Context())
					trace.WroteRequest(httptrace.WroteRequestInfo{})
					if failure == "cancel" {
						cancel()
						return nil, context.Canceled
					}
					return nil, context.DeadlineExceeded
				})
				session := Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}
				var err error
				switch action {
				case "schedulable":
					_, err = service.SetSub2APIAdminAccountSchedulableContext(ctx, session, "1", false)
				case "delete":
					err = service.DeleteSub2APIAdminAccountContext(ctx, session, "1")
				default:
					err = service.UpdateSub2APIAdminAccountStatusContext(ctx, session, "1", "inactive")
				}
				if RemoteMutationOutcome(err) != MutationUncertain {
					t.Fatal("written request was cleared as not_sent")
				}
				if failure == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("context cancellation evidence lost")
				}
				if failure == "timeout" && !requestTimedOut(err) {
					t.Fatal("timeout evidence lost")
				}
			})
		}
	}
}
