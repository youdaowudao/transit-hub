package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"transithub/backend/internal/config"
)

func TestC5APIOnlyStorageStartupSkipsEveryTaskAndDefaultsPreserveOrder(t *testing.T) {
	labels := []string{"schema", "bootstrap_admin", "assign_legacy", "restore_and_flush_cache"}
	for _, apiOnly := range []bool{true, false} {
		calls := []string{}
		tasks := []func(context.Context) error{}
		for _, label := range labels {
			label := label
			tasks = append(tasks, func(ctx context.Context) error {
				if ctx.Err() != nil {
					t.Fatal("normal initialization received a canceled context")
				}
				calls = append(calls, label)
				return nil
			})
		}
		runStorageStartup(config.Config{APIOnly: apiOnly}, tasks...)
		want := labels
		if apiOnly {
			want = []string{}
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("API-only=%t startup calls=%v", apiOnly, calls)
		}
	}
}

func TestC5APIOnlyDefaultStartupStillStopsOnInitializationError(t *testing.T) {
	want := errors.New("synthetic initialization failure")
	calls := 0
	defer func() {
		if got := recover(); got != want || calls != 1 {
			t.Fatal("normal initialization swallowed failure or ran a subsequent task")
		}
	}()
	runStorageStartup(config.Config{}, func(context.Context) error { calls++; return want }, func(context.Context) error { calls++; return nil })
}

func TestC5APIOnlyKeepsRoutesWithoutCreatingAttachmentsOrWorkers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-be-created")
	server := New(config.Config{APIOnly: true, TicketUploadDir: dir}, nil, nil)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("API-only initialized the attachment directory")
	}
	if server.lotteryCancel != nil || server.lotteryWorker != nil {
		t.Fatal("API-only started lottery background tasks")
	}
	// Inspect registration without executing authenticated storage-dependent handlers.
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/api/health"}, {http.MethodPost, "/api/my-sites/real-connect"}, {http.MethodPost, "/api/upstream-sites"}, {http.MethodGet, "/api/group-rates"}} {
		_, pattern := server.mux.Handler(httptest.NewRequest(route.method, route.path, nil))
		if pattern == "" {
			t.Fatalf("API-only omitted route %s", route.path)
		}
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal("API-only shutdown accessed uninitialized storage")
	}
}
