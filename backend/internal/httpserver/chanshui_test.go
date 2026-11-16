package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/chanshui"
)

func TestChanshuiCrossOriginPreflightPreservesExistingHeaders(t *testing.T) {
	s := &Server{allowed: makeAllowedOrigins([]string{"https://console.example.test"})}
	handler := s.cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("preflight reached a business operation") }))
	for _, origin := range []string{"https://console.example.test", "https://unlisted.example.test"} {
		req := httptest.NewRequest(http.MethodOptions, "/api/chanshui/questions/Q1", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
		req.Header.Set("Access-Control-Request-Headers", "authorization,x-chanshui-workspace,if-match")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatal(w.Code)
		}
		if origin == "https://unlisted.example.test" {
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("unlisted origin was allowed")
			}
			continue
		}
		allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
		for _, name := range []string{"content-type", "authorization", "x-transithub-question-answer-contract", "x-chanshui-workspace", "if-match"} {
			if !strings.Contains(allowed, name) {
				t.Errorf("model detection or old header blocked: %s", name)
			}
		}
		if w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatal("existing allowed origin behavior changed")
		}
	}
}

func TestChanshuiAPIProtectionAndIndependentStaticFallback(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/chanshui", "/api/chanshui/reports", "/api/chanshui/reports/id/items/item/image", "/api/chanshui/questions/Q1"} {
		if !s.protectedPath(path) {
			t.Fatalf("unprotected %s", path)
		}
	}
	if s.protectedPath("/api/public-mass-email") {
		t.Fatal("unrelated public route changed")
	}
	root := t.TempDir()
	vue := filepath.Join(root, "vue")
	detector := filepath.Join(root, "detector")
	for _, dir := range []string{vue, detector} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(vue, "index.html"), []byte("original-vue"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(detector, "index.html"), []byte("detector-ui"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg = config.Config{PublicDir: vue, ChanshuiWebDir: detector}
	s.chanshuiService = chanshui.NewService(s.cfg, nil, nil)
	for _, item := range []struct {
		path   string
		status int
		text   string
	}{{"/chanshui/", 200, "detector-ui"}, {"/chanshui/_next/missing.js", 404, "404"}, {"/admin/connection-health", 200, "original-vue"}} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", item.path, nil))
		if w.Code != item.status || !strings.Contains(w.Body.String(), item.text) {
			t.Fatalf("%s: %d %s", item.path, w.Code, w.Body)
		}
	}
}
