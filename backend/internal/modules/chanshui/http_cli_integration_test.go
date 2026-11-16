package chanshui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/config"
	"transithub/backend/internal/shared/authctx"
)

// The real helper must finish cleanup before HTTP completion metadata is emitted.
// No main database, external model, listener or production credentials are used.
func TestChanshuiActualHTTPSettingsQuestionsAndImagePreview(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit actual HTTP/CLI resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := actualSlice(t)
	runner := NewRunner(RunnerConfig{BinaryPath: filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui"), DataDir: t.TempDir(), ProjectDir: root, Manager: "user"})
	s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, runner: runner, initialized: true}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := runner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		assertActualEmpty(t, conn)
	})
	call := func(method, path string, value any) map[string]any {
		t.Helper()
		var body []byte
		if value != nil {
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "/api/chanshui"+path, bytes.NewReader(body))
		req.Header.Set(WorkspaceHeader, "workspace-a")
		req = req.WithContext(authctx.WithUserID(req.Context(), "actual-http-fixture"))
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
		if w.Code != 200 || envelope.V != 1 || !envelope.Complete || envelope.Error != nil {
			t.Fatalf("%s %s did not confirm result and cleanup: %d %s", method, path, w.Code, w.Body.String())
		}
		data, err := base64.StdEncoding.DecodeString(envelope.Payload)
		if err != nil || int64(len(data)) != envelope.Length {
			t.Fatal("invalid transported result", err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		assertActualEmpty(t, conn)
		return result
	}
	settings := call("GET", "/settings", nil)
	settings["memoryLimitMiB"] = 128
	saved := call("PUT", "/settings", settings)
	if saved["revision"] != float64(2) || saved["memoryLimitMiB"] != float64(128) {
		t.Fatal("HTTP did not retain successful settings mutation", saved)
	}
	if read := call("GET", "/settings", nil); read["revision"] != float64(2) || read["memoryLimitMiB"] != float64(128) {
		t.Fatal("settings not persisted across HTTP helper processes", read)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page := call("GET", "/questions?limit=20&cursor="+url.QueryEscape(cursor), nil)
		for _, item := range page["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate HTTP question", id)
			}
			seen[id] = true
		}
		cursor, _ = page["nextCursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) != 35 {
		t.Fatalf("HTTP omitted preset definitions: %d", len(seen))
	}
	question := call("GET", "/questions/Q1", nil)
	question["title"] = "HTTP隔离保存验收"
	updated := call("PUT", "/questions/Q1", map[string]any{"definition": question, "revision": question["revision"]})
	if updated["title"] != "HTTP隔离保存验收" || updated["revision"] != float64(2) {
		t.Fatal("HTTP question mutation lost", updated)
	}
	if read := call("GET", "/questions/Q1", nil); read["title"] != "HTTP隔离保存验收" {
		t.Fatal("saved HTTP question missing", read)
	}
	vision := call("GET", "/questions/vision", nil)
	preview := call("POST", "/questions/preview", map[string]any{"definition": vision, "seed": 42})
	image, _ := preview["image"].(string)
	if !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatal("image preview did not traverse HTTP transport")
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(image, "data:image/png;base64,"))
	if err != nil || len(png) < 10000 || !bytes.Equal(png[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatal("image preview was truncated or changed", err)
	}
	t.Logf("actual HTTP/CLI: 35 presets paginated, settings and question persisted, PNG preview %d bytes, cleanup confirmed before every completed response", len(png))
}
