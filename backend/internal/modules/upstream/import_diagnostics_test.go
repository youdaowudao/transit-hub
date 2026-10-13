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

func TestImportDiagnosticsBudgetPreservesBusinessAndIDEvidence(t *testing.T) {
	for _, status := range []int{200, 403} {
		for _, size := range []int{4096, 4097} {
			for _, rejected := range []bool{false, true} {
				t.Run(fmt.Sprintf("status%d/bytes%d/rejected%t", status, size, rejected), func(t *testing.T) {
					prefix := fmt.Sprintf(`{"code":0,"success":%t,"message":"请检查接口限制","data":{"id":11,"key":"hidden"},"padding":"`, !rejected)
					body := prefix + strings.Repeat("x", size-len(prefix)-2) + `"}`
					call := func(diagnostics bool) (jsonResponse, error) {
						client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
							return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
						})})
						return client.requestJSONWithContext(context.Background(), "https://import.invalid/create", requestOptions{Method: "POST", PreserveImportMutationID: true, ImportDiagnostics: diagnostics})
					}
					before, oldErr := call(false)
					after, newErr := call(true)
					if !reflect.DeepEqual(before.Payload, after.Payload) || RemoteMutationOutcome(oldErr) != RemoteMutationOutcome(newErr) || (oldErr == nil) != (newErr == nil) {
						t.Fatal("diagnostic budget changed full business payload, ID projection or mutation outcome")
					}
					if size == 4097 && after.SafeImportMessage != importDiagnosticOverBudget {
						t.Fatal("over-budget hint not suppressed")
					}
					if size == 4096 && after.SafeImportMessage != "请检查接口限制" {
						t.Fatal("complete under-budget hint was lost")
					}
				})
			}
		}
	}
}

func TestImportDiagnosticsMasksFixedRequestAndResponseCredentialsBeforeTruncation(t *testing.T) {
	markers := []string{"short", "request-key", "request-password", "access-secret", "refresh-secret", "cookie-secret", "admin-secret", "response-key", "nested-secret", "error-secret"}
	message := "拒绝操作 " + strings.Join(markers, " ") + " https://user:pass@example.invalid/path?q=hidden#fragment\n\t"
	record := map[string]any{"success": false, "error": map[string]any{"message": message, "authorization": "error-secret"}, "key": "short", "data": map[string]any{"key": "response-key", "credentials": map[string]any{"api_key": "nested-secret"}}}
	options := requestOptions{Method: "POST", PreserveImportMutationID: true, ImportDiagnostics: true, AccessToken: "access-secret", Cookie: "session=cookie-secret", AdminAPIKey: "admin-secret", Body: map[string]any{"api_key": "request-key", "password": "request-password", "credentials": map[string]any{"refresh_token": "refresh-secret"}}}
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(original)
	client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) { return c5ImportResponse(r, 200, record) })})
	response, err := client.requestJSONWithContext(context.Background(), "https://import.invalid/create", options)
	var failure *RequestError
	if !errors.As(err, &failure) || !strings.Contains(failure.UpstreamMessage, "拒绝操作") || failure.UpstreamMessage != response.SafeImportMessage {
		t.Fatal("safe nested error hint missing")
	}
	wire, _ := json.Marshal(struct {
		Response jsonResponse
		Failure  *RequestError
	}{response, failure})
	for _, marker := range append(markers, "user:pass", "q=hidden", "#fragment") {
		if strings.Contains(string(wire), marker) || strings.Contains(logs.String(), marker) {
			t.Fatal("credential reached safe diagnostic or log")
		}
	}
	if strings.Contains(failure.UpstreamMessage, "\n") || strings.Contains(failure.UpstreamMessage, "\t") || len([]rune(failure.UpstreamMessage)) > 120 {
		t.Fatal("remote hint breached display limit")
	}
}

func TestImportDiagnosticsRejectsUnstructuredAndRawDatabaseHints(t *testing.T) {
	for _, body := range []string{`{"message":"SQLSTATE 23505: secret private table"}`, `{"message":"pq: connection failure"}`, `<html>private secret</html>`, `{"message":"private"}{}`, `{"message":17}`} {
		client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
		})})
		response, err := client.requestJSONWithContext(context.Background(), "https://import.invalid/preview", requestOptions{ImportDiagnostics: true})
		var failure *RequestError
		if !errors.As(err, &failure) || failure.UpstreamMessage != "" || response.SafeImportMessage != "" {
			t.Fatal("unsafe or incomplete body became a diagnostic hint")
		}
	}
}

func TestImportDiagnosticsWithholdCommonDatabaseDetailsWithoutChangingDefaultOrOutcome(t *testing.T) {
	for _, message := range []string{
		`ERROR: duplicate key value violates unique constraint "private_table_key"`,
		`syntax error at or near "private_table"`,
		`password authentication failed for user "private_database_user"`,
		`insert or update on table "private_child" violates foreign key constraint "private_parent_id"`,
		`null value in column "private_column" violates not-null constraint`,
		`new row for relation "private_table" violates check constraint "private_check"`,
		`relation "private_table" does not exist`,
		`column "private_column" does not exist`,
		`column "private_column" of relation "private_table" does not exist`,
		`schema "private_schema" does not exist`,
		`sequence "private_sequence" does not exist`,
		`permission denied for table private_table`,
		`permission denied for schema private_schema`,
		`permission denied for sequence private_sequence`,
		`permission denied for database private_database`,
		`permission denied for function private_function`,
		`permission denied for view private_view`,
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
		`constraint "private_constraint" of relation "private_table" does not exist`,
		`trigger "private_trigger" for table "private_table" does not exist`,
		`policy "private_policy" for table "private_table" does not exist`,
		`function private_schema.private_function(integer, text) does not exist`,
		`procedure private_schema.private_procedure(integer, text) does not exist`,
		`permission denied for relation private_relation`,
		`permission denied for column private_column`,
		`permission denied for index private_index`,
		`permission denied for type private_type`,
		`permission denied for procedure private_procedure`,
		`permission denied for extension private_extension`,
		`permission denied for role private_role`,
		`permission denied for tablespace private_tablespace`,
		`permission denied for constraint private_constraint`,
		`permission denied for trigger private_trigger`,
		`permission denied for policy private_policy`,
	} {
		for _, status := range []int{200, 500} {
			for _, preserveID := range []bool{false, true} {
				client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
					return c5ImportResponse(r, status, map[string]any{"success": false, "message": message, "data": map[string]any{"id": 22, "key": "synthetic-private-response-key"}})
				})})
				before, oldErr := client.requestJSONWithContext(context.Background(), "https://import.invalid/preview", requestOptions{Method: "POST", PreserveImportMutationID: preserveID})
				after, newErr := client.requestJSONWithContext(context.Background(), "https://import.invalid/preview", requestOptions{Method: "POST", PreserveImportMutationID: preserveID, ImportDiagnostics: true})
				var old, current *RequestError
				if !errors.As(oldErr, &old) || !errors.As(newErr, &current) || (!preserveID && old.UpstreamMessage == "") || (preserveID && old.UpstreamMessage != "") || current.UpstreamMessage != "" || after.SafeImportMessage != "" {
					t.Fatal("import diagnostic leaked recognizable database details or changed default message behavior")
				}
				if current.StatusCode != status || old.StatusCode != current.StatusCode || old.MessageKey != current.MessageKey || old.Reason != current.Reason || RemoteMutationOutcome(oldErr) != RemoteMutationOutcome(newErr) || !reflect.DeepEqual(before.Payload, after.Payload) || before.StatusCode != after.StatusCode || !reflect.DeepEqual(before.Header, after.Header) || before.ReceivedAt.IsZero() != after.ReceivedAt.IsZero() {
					t.Fatal("withholding raw database details changed status, classification, response shape or mutation outcome")
				}
				if preserveID && !reflect.DeepEqual(after.Payload, map[string]any{"data": map[string]any{"id": "22"}}) {
					t.Fatal("database hint filtering lost known mutation ID or exposed credential fields")
				}
			}
		}
	}
	for _, message := range []string{"服务暂时不可用，请检查请求记录", "permission denied for requested action", `model "safe-model" does not exist`, `resource "safe-resource" does not exist`, `request field "priority" does not exist`, "schema validation failed for this API request"} {
		client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
			return c5ImportResponse(r, 500, map[string]any{"message": message})
		})})
		_, err := client.requestJSONWithContext(context.Background(), "https://import.invalid/preview", requestOptions{ImportDiagnostics: true})
		var failure *RequestError
		if !errors.As(err, &failure) || failure.UpstreamMessage != message {
			t.Fatal("narrow database filter discarded an ordinary safe upstream hint")
		}
	}

}

func TestImportNewAPIKeyStrictReasonsPreserveStageEvidence(t *testing.T) {
	for _, mode := range []string{"envelope", "data", "page", "page_size", "total", "items", "items-count", "item-object", "items.id", "items.name", "duplicate-id", "no-match", "multiple-match", "read-envelope", "read-data", "read-key", "changed-total"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			s := c5ImportService(func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" && r.URL.Path == "/api/token/" {
					return c5ImportResponse(r, 200, map[string]any{"success": true})
				}
				if strings.HasSuffix(r.URL.Path, "/key") {
					payload := map[string]any{"success": true, "data": map[string]any{"key": "synthetic-safe-key"}}
					if mode == "read-envelope" {
						delete(payload, "success")
					}
					if mode == "read-data" {
						payload["data"] = []any{}
					}
					if mode == "read-key" {
						payload["data"] = map[string]any{"key": 7}
					}
					return c5ImportResponse(r, 200, payload)
				}
				reads++
				data := map[string]any{"page": 1, "page_size": 100, "total": 1, "items": []any{map[string]any{"id": 12, "name": "wanted"}}}
				payload := map[string]any{"success": true, "data": data}
				switch mode {
				case "envelope":
					delete(payload, "success")
				case "data":
					payload["data"] = []any{}
				case "page":
					data["page"] = 2
				case "page_size":
					data["page_size"] = 20
				case "total":
					data["total"] = 1001
				case "items":
					data["items"] = "invalid"
				case "items-count":
					data["items"] = []any{}
				case "item-object":
					data["items"] = []any{7}
				case "items.id":
					data["items"] = []any{map[string]any{"id": 0, "name": "wanted"}}
				case "items.name":
					data["items"] = []any{map[string]any{"id": 12, "name": 7}}
				case "duplicate-id":
					data["total"] = 2
					data["items"] = []any{map[string]any{"id": 12, "name": "wanted"}, map[string]any{"id": 12, "name": "other"}}
				case "no-match":
					data["items"] = []any{map[string]any{"id": 12, "name": "other"}}
				case "multiple-match":
					data["total"] = 2
					data["items"] = []any{map[string]any{"id": 12, "name": "wanted"}, map[string]any{"id": 13, "name": "wanted"}}
				case "changed-total":
					data["total"] = 101
					items := []any{}
					for id := 1; id <= 100; id++ {
						items = append(items, map[string]any{"id": id, "name": "other"})
					}
					data["items"] = items
					if reads == 2 {
						data["page"] = 2
						data["total"] = 102
						data["items"] = []any{map[string]any{"id": 101, "name": "wanted"}, map[string]any{"id": 102, "name": "other"}}
					}
				}
				return c5ImportResponse(r, 200, payload)
			})
			credential, err := s.CreateImportUpstreamCredentialContext(context.Background(), c5ImportSession(PlatformNewAPI), "wanted", "upstream")
			var failure *RequestError
			if !errors.As(err, &failure) || failure.StatusCode != 200 || credential.Key != "" {
				t.Fatal("strict Key failure lost actual status or accepted malformed Key")
			}
			label := mode
			switch mode {
			case "envelope", "read-envelope":
				label = "success 或 code"
			case "read-data":
				label = "data"
			case "read-key":
				label = "data.key"
			case "items-count", "item-object":
				label = "items"
			case "duplicate-id":
				label = "items.id"
			case "no-match":
				label = "没有找到本次"
			case "multiple-match":
				label = "匹配多条"
			case "changed-total":
				label = "total"
			}
			if !strings.Contains(failure.UpstreamMessage, label) {
				t.Fatal("known Key lookup/read failure lost concrete field")
			}
			if strings.HasPrefix(mode, "read-") {
				if credential.ID != "12" || credential.Outcome != ImportCredentialKnownID {
					t.Fatal("strict Key read lost created ID or known stage")
				}
			} else if credential.ID != "" || credential.Outcome != ImportCredentialUnknownID {
				t.Fatal("strict Key lookup fabricated ID or changed original unknown stage")
			}
		})
	}
}

func TestImportDeleteStrictReasonsPreserveUncertaintyAndLegacyGroupParser(t *testing.T) {
	for _, sample := range []struct {
		platform Platform
		payload  any
		label    string
	}{
		{PlatformSub2API, map[string]any{"code": 0, "data": []any{}}, "code、success 或 data"},
		{PlatformSub2API, map[string]any{"code": 0, "data": map[string]any{}}, "data.message"},
		{PlatformNewAPI, map[string]any{"code": 0}, "success 或 code"},
	} {
		s := c5ImportService(func(r *http.Request) (*http.Response, error) { return c5ImportResponse(r, 200, sample.payload) })
		cleanup, err := s.DeleteImportUpstreamCredentialContext(context.Background(), c5ImportSession(sample.platform), "12")
		var failure *RequestError
		if cleanup != ImportCleanupPending || !errors.As(err, &failure) || failure.StatusCode != 200 || RemoteMutationOutcome(err) != MutationUncertain || !strings.Contains(failure.UpstreamMessage, sample.label) {
			t.Fatal("delete strict detail changed original pending evidence")
		}
	}
	_, oldErr := readImportGroups(map[string]any{"group_ids": "invalid"})
	_, importErr := readImportGroups(map[string]any{"group_ids": "invalid"}, jsonResponse{StatusCode: 200})
	var old, current *RequestError
	if !errors.As(oldErr, &old) || !errors.As(importErr, &current) || old.UpstreamMessage != "" || old.StatusCode != 0 || old.MessageKey != current.MessageKey || !strings.Contains(current.UpstreamMessage, "group_ids") || current.StatusCode != 200 {
		t.Fatal("shared model-control group parser changed original error shape or lost import-only field detail")
	}
}

func TestImportInvalidJSONAndBodyReadFailureRetainReceivedStatusAndOriginalUncertainty(t *testing.T) {
	for _, mode := range []string{"invalid-json", "read-failed"} {
		t.Run(mode, func(t *testing.T) {
			var prior jsonResponse
			var priorOutcome string
			for _, diagnostics := range []bool{false, true} {
				client := NewHTTPClient(&http.Client{Transport: c5ImportTransport(func(r *http.Request) (*http.Response, error) {
					body := `{"code":0,"data":{"id":22,"key":"synthetic-private-response"}}`
					if mode == "invalid-json" {
						body += `{`
					}
					reader := io.Reader(strings.NewReader(body))
					if mode == "read-failed" {
						reader = io.MultiReader(reader, stageAHTTPFailingReader{err: io.ErrUnexpectedEOF})
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader), Request: r}, nil
				})})
				response, err := client.requestJSONWithContext(context.Background(), "https://import.invalid/create", requestOptions{Method: "POST", PreserveImportMutationID: true, ImportDiagnostics: diagnostics})
				var failure *RequestError
				if !errors.As(err, &failure) || failure.StatusCode != 200 || response.Payload != nil || failure.UpstreamMessage != "" || RemoteMutationOutcome(err) != MutationUncertain {
					t.Fatal("known 200 parse/read failure lost received status, guessed ID or became safely rejected")
				}
				if !diagnostics {
					prior, priorOutcome = response, RemoteMutationOutcome(err)
				} else if !reflect.DeepEqual(prior, response) || priorOutcome != RemoteMutationOutcome(err) {
					t.Fatal("diagnostics changed original incomplete response shape or outcome")
				}
			}
		})
	}
}
