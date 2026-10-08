package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"transithub/backend/internal/config"
)

// A temporary API instance must use already-initialized shared storage. Nil
// clients make any startup schema/cache/worker access fail immediately without
// connecting to a database or starting an external process.
func TestC5REDAPIOnlyAssemblesRoutesWithoutStorageStartup(t *testing.T) {
	t.Setenv("TRANSITHUB_API_ONLY", "1")
	t.Setenv("SMTP_ENCRYPTION_KEY", "")
	t.Setenv("LOTTERY_ALLOW_PRIVATE_SUB2API_TARGETS", "false")
	cfg := config.Load()
	cfg.TicketUploadDir = t.TempDir()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("temporary API startup touched uninitialized shared storage: %T", recovered)
		}
	}()
	server := New(cfg, nil, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("temporary API did not retain health route: status=%d", response.Code)
	}
}
