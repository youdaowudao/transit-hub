package chanshui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

type cleanupBody struct {
	io.ReadCloser
	calls *int
	err   error
}

func (b cleanupBody) Close() error {
	*b.calls++
	_ = b.ReadCloser.Close()
	return b.err
}

type cleanupExecutor struct {
	*fakeExecutor
	closeCalls int
	queryCalls int
	writes     int
	partial    bool
	closeErr   error
	payload    []byte
}

func (f *cleanupExecutor) Query(_ context.Context, e Envelope, _ *Credential, _ bool) (*QueryResponse, error) {
	f.queryCalls++
	if e.Operation == "save-settings" || e.Operation == "save-question" || e.Operation == "delete-question" {
		f.writes++ // The operation commits before the helper writes its result.
	}
	data := f.payload
	if f.partial {
		data = data[:len(data)-1]
	}
	contentType := "application/json"
	if e.Operation == "read-image" {
		contentType = "image/png"
	}
	return &QueryResponse{Status: 200, ContentType: contentType, Length: int64(len(f.payload)), Body: cleanupBody{io.NopCloser(bytes.NewReader(data)), &f.closeCalls, f.closeErr}}, nil
}

func TestGenericHTTPCompleteBodyPreservesResultAndReportsCleanupFailure(t *testing.T) {
	cases := []struct {
		method, path, body string
		write              bool
	}{
		{"GET", "/settings", "", false},
		{"PUT", "/settings", `{"revision":1,"memoryLimitMiB":128}`, true},
		{"GET", "/questions", "", false},
		{"GET", "/questions/Q1", "", false},
		{"POST", "/questions", `{"definition":{"id":"custom:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, true},
		{"PUT", "/questions/Q1", `{"definition":{"id":"Q1"},"revision":1}`, true},
		{"DELETE", "/questions/Q1", "", true},
		{"POST", "/questions/preview", `{"definition":{"id":"Q1"}}`, false},
		{"GET", "/reports?scope=sidebar", "", false},
		{"GET", "/reports/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/items", "", false},
		{"GET", "/reports/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/items/vision/image", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			s, f, _ := fixture(t)
			payload := []byte(`{"fixture":"saved-or-read-result","revision":2}`)
			if strings.HasSuffix(tc.path, "/image") {
				payload = []byte{0x89, 'P', 'N', 'G'}
			}
			executor := &cleanupExecutor{fakeExecutor: f, closeErr: errors.New("isolated fixture: StopUnit permission denied"), payload: payload}
			s.runner = executor
			req := httptest.NewRequest(tc.method, "/api/chanshui"+tc.path, strings.NewReader(tc.body))
			req.Header.Set(WorkspaceHeader, "workspace-a")
			req.Header.Set("If-Match", "1")
			req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			var envelope struct {
				V        int      `json:"v"`
				Length   int64    `json:"length"`
				Complete bool     `json:"complete"`
				Payload  string   `json:"payloadBase64"`
				Error    *Problem `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/vnd.transithub.chanshui-result+json") || envelope.V != 1 || !envelope.Complete || envelope.Error == nil {
				t.Fatalf("complete result silently hid cleanup failure: status=%d contentType=%s body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			got, err := base64.StdEncoding.DecodeString(envelope.Payload)
			if err != nil || !bytes.Equal(got, payload) || envelope.Length != int64(len(payload)) {
				t.Fatal("committed/read result was lost", err)
			}
			if !strings.Contains(envelope.Error.Message, "StopUnit permission denied") || !strings.Contains(envelope.Error.Message, "重新读取") {
				t.Fatal("specific cause or recovery instruction lost", envelope.Error)
			}
			if tc.write && !strings.Contains(envelope.Error.Message, "已保存") && !strings.Contains(envelope.Error.Message, "已删除") {
				t.Fatal("successful mutation was described as unsaved", envelope.Error)
			}
			if !tc.write && !strings.Contains(envelope.Error.Message, "已读取") {
				t.Fatal("read result not distinguished", envelope.Error)
			}
			wantWrites := 0
			if tc.write {
				wantWrites = 1
			}
			if executor.closeCalls != 1 || executor.queryCalls != 1 || executor.writes != wantWrites {
				t.Fatalf("extra query/write or missed close: %+v", executor)
			}
		})
	}
}

func TestGenericHTTPPartialPayloadNeverClaimsComplete(t *testing.T) {
	s, f, _ := fixture(t)
	executor := &cleanupExecutor{fakeExecutor: f, partial: true, closeErr: errors.New("isolated fixture: exit could not be confirmed"), payload: []byte(`{"fixture":"unfinished"}`)}
	s.runner = executor
	req := httptest.NewRequest("GET", "/api/chanshui/settings", nil)
	req.Header.Set(WorkspaceHeader, "workspace-a")
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var envelope struct {
		V        int      `json:"v"`
		Complete bool     `json:"complete"`
		Error    *Problem `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("partial transport must still return readable completion metadata: %v", err)
	}
	if envelope.V != 1 || envelope.Complete || envelope.Error == nil || !strings.Contains(envelope.Error.Message, "exit could not be confirmed") || executor.closeCalls != 1 {
		t.Fatalf("partial result was misreported: %+v closes=%d", envelope, executor.closeCalls)
	}
}

func TestGenericHTTPPartialWriteStillReportsCommittedMutation(t *testing.T) {
	s, f, _ := fixture(t)
	executor := &cleanupExecutor{fakeExecutor: f, partial: true, closeErr: errors.New("isolated fixture: cleanup denied"), payload: []byte(`{"revision":2}`)}
	s.runner = executor
	req := httptest.NewRequest("PUT", "/api/chanshui/settings", strings.NewReader(`{"revision":1,"memoryLimitMiB":128}`))
	req.Header.Set(WorkspaceHeader, "workspace-a")
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-a"))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var envelope struct {
		Complete bool     `json:"complete"`
		Error    *Problem `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Complete || envelope.Error == nil || !strings.Contains(envelope.Error.Message, "已保存") || strings.Contains(envelope.Error.Message, "保存范围尚无法确认") || executor.writes != 1 {
		t.Fatalf("CLI OK header's committed mutation was misreported: %+v writes=%d", envelope, executor.writes)
	}
}
