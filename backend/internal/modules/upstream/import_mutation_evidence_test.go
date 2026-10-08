package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestC5ImportHTTPFailureIDProjectionIsOptInAndSafe(t *testing.T) {
	const privateMarker = "c5-private-marker"
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusInternalServerError} {
		for _, preserve := range []bool{false, true} {
			t.Run(fmt.Sprintf("http-%d/opt-in-%t", status, preserve), func(t *testing.T) {
				var captured bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&captured)
				defer log.SetOutput(previous)
				calls := 0
				client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					return c5ImportResponse(r, status, map[string]any{
						"code": "FORBIDDEN", "success": false, "message": privateMarker,
						"data": map[string]any{"id": 11, "key": privateMarker, "credentials": map[string]any{"api_key": privateMarker}},
					})
				})})
				response, err := client.requestJSONWithContext(context.Background(), "https://c5.invalid/create", requestOptions{Method: http.MethodPost, PreserveImportMutationID: preserve})
				var failure *RequestError
				if calls != 1 || !errors.As(err, &failure) || failure.StatusCode != status {
					t.Fatal("failed receipt lost status, was accepted, or retried")
				}
				if preserve {
					want := map[string]any{"data": map[string]any{"id": "11"}}
					if !reflect.DeepEqual(response.Payload, want) || response.StatusCode != status || failure.UpstreamMessage != "" || RemoteMutationOutcome(err) != MutationUncertain {
						t.Fatal("opt-in failed receipt did not keep only safe ID evidence and uncertainty")
					}
					encoded, _ := json.Marshal(struct {
						Payload any
						Failure *RequestError
					}{response.Payload, failure})
					if strings.Contains(string(encoded), privateMarker) || strings.Contains(string(encoded), "api_key") {
						t.Fatal("opt-in failure propagated private response material")
					}
				} else {
					if response.Payload != nil || failure.UpstreamMessage != privateMarker {
						t.Fatal("non-opt-in shared caller changed its original payload/message contract")
					}
					if status == http.StatusOK {
						if !reflect.DeepEqual(response, jsonResponse{}) {
							t.Fatal("old 2xx business-rejection response shape changed")
						}
					} else if response.StatusCode != status || response.ReceivedAt.IsZero() {
						t.Fatal("old non-2xx receipt metadata changed")
					}
				}
				if strings.Contains(captured.String(), privateMarker) {
					t.Fatal("failure log exposed response or credential material")
				}
			})
		}
	}
}

func TestC5ImportHTTPNon2xxIDProjectionRequiresCompleteBoundedLegalJSON(t *testing.T) {
	prefix, suffix := `{"code":"FORBIDDEN","data":{"id":11},"padding":"`, `"}`
	boundary := prefix + strings.Repeat("x", 4096-len(prefix)-len(suffix)) + suffix
	for _, sample := range []struct {
		name, body, wantID string
		readFailure        bool
	}{
		{"numeric-positive", `{"data":{"id":11,"key":"private"}}`, "11", false},
		{"canonical-string", `{"data":{"id":"11","key":"private"}}`, "11", false},
		{"maximum-exact-number", `{"data":{"id":9007199254740991}}`, "9007199254740991", false},
		{"complete-at-bound", boundary, "11", false},
		{"over-bound", boundary + " ", "", false},
		{"read-error-after-id", `{"data":{"id":11}}`, "", true},
		{"malformed-after-id", `{"data":{"id":11},`, "", false},
		{"trailing-json", `{"data":{"id":11}}{}`, "", false},
		{"root-array", `[{"data":{"id":11}}]`, "", false},
		{"missing-id", `{"data":{"key":"private"}}`, "", false},
		{"root-id-not-evidence", `{"id":11}`, "", false},
		{"wrong-data-type", `{"data":[{"id":11}]}`, "", false},
		{"zero-id", `{"data":{"id":0}}`, "", false},
		{"negative-id", `{"data":{"id":-11}}`, "", false},
		{"fractional-id", `{"data":{"id":11.5}}`, "", false},
		{"unsafe-number", `{"data":{"id":9007199254740992}}`, "", false},
		{"leading-zero-string", `{"data":{"id":"011"}}`, "", false},
		{"non-integer-string", `{"data":{"id":"11.0"}}`, "", false},
		{"id-null", `{"data":{"id":null}}`, "", false},
		{"id-bool", `{"data":{"id":true}}`, "", false},
		{"id-object", `{"data":{"id":{"value":11}}}`, "", false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			calls := 0
			client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				response, err := c5ImportResponse(r, http.StatusInternalServerError, sample.body)
				if sample.readFailure {
					response.Body = io.NopCloser(io.MultiReader(strings.NewReader(sample.body), stageAHTTPFailingReader{err: io.ErrUnexpectedEOF}))
				}
				return response, err
			})})
			response, err := client.requestJSONWithContext(context.Background(), "https://c5.invalid/create", requestOptions{Method: http.MethodPost, PreserveImportMutationID: true})
			if calls != 1 || err == nil || RemoteMutationOutcome(err) != MutationUncertain {
				t.Fatal("incomplete or failed receipt was accepted, classified as no mutation, or retried")
			}
			if sample.wantID == "" {
				if response.Payload != nil {
					t.Fatal("invalid or incomplete ID evidence was invented")
				}
			} else if !reflect.DeepEqual(response.Payload, map[string]any{"data": map[string]any{"id": sample.wantID}}) {
				t.Fatal("complete bounded legal ID evidence was lost or extra response fields leaked")
			}
		})
	}
}

func TestC5ImportCreationObservedIDOverridesRejectionOnlyLocally(t *testing.T) {
	for _, platform := range []Platform{PlatformSub2API, PlatformNewAPI} {
		for _, endpoint := range []string{"create", "create-key"} {
			for _, originalOutcome := range []string{MutationNotSent, MutationConfirmedRejected, MutationUncertain} {
				t.Run(string(platform)+"/"+endpoint+"/"+originalOutcome, func(t *testing.T) {
					original := &RequestError{MessageKey: ErrorRequest, StatusCode: http.StatusForbidden, RemoteReason: "FORBIDDEN", MutationOutcome: originalOutcome}
					classified := classifyImportCreationError(original, platform, endpoint, "11")
					if classified == original || RemoteMutationOutcome(classified) != MutationUncertain || original.MutationOutcome != originalOutcome {
						t.Fatal("observed ID did not override contradictory rejection locally or mutated original evidence")
					}
					if importCredentialFailureOutcome(classified) != ImportCredentialUncertain {
						t.Fatal("credential outcome converter overrode observed-ID uncertainty")
					}
				})
			}
		}
	}
	legacy := &RequestError{MessageKey: ErrorRequest, StatusCode: http.StatusForbidden, RemoteReason: "FORBIDDEN", MutationOutcome: MutationUncertain}
	if RemoteMutationOutcome(classifySub2APIMutationError(legacy, "create")) != MutationConfirmedRejected {
		t.Fatal("original non-C5 rejection classifier changed")
	}
	noID := &RequestError{MessageKey: ErrorRequest, StatusCode: http.StatusForbidden, RemoteReason: "FORBIDDEN", MutationOutcome: MutationUncertain}
	if RemoteMutationOutcome(classifyImportCreationError(noID, PlatformSub2API, "create", "")) != MutationConfirmedRejected {
		t.Fatal("C5 no-ID trusted rejection contract changed")
	}
}

func TestC5ImportCreationFailureWithoutObservedIDRetainsOriginalOutcome(t *testing.T) {
	for _, target := range []string{"sub2api-key", "newapi-key", "main-account"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			service := c5ImportService(func(r *http.Request) (*http.Response, error) {
				calls++
				return c5ImportResponse(r, http.StatusForbidden, `{"code":"FORBIDDEN","success":false}`)
			})
			if target == "main-account" {
				id, err := service.CreateSub2APIImportAccountContext(context.Background(), c5ImportSession(PlatformSub2API), map[string]any{})
				if calls != 1 || id != "" || err == nil || RemoteMutationOutcome(err) != MutationConfirmedRejected {
					t.Fatal("main-site no-ID middleware rejection changed")
				}
				return
			}
			platform, want := PlatformSub2API, ImportCredentialRejected
			if target == "newapi-key" {
				platform, want = PlatformNewAPI, ImportCredentialUncertain
			}
			credential, err := service.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(platform), "safe-name", "7")
			if calls != 1 || credential.ID != "" || credential.Key != "" || err == nil || credential.Outcome != want {
				t.Fatal("no-ID upstream creation rejection changed or caused a follow-up request")
			}
		})
	}
}
