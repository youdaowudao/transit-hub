package store

import (
	"context"
	"strings"
	"testing"
)

func TestRuntimeDefaultsAndHighConfiguredLimits(t *testing.T) {
	s := DefaultSettings()
	if s.Concurrency != 32 || s.TotalTimeoutSeconds != 3600 { t.Errorf("runtime defaults: concurrency=%d total=%d", s.Concurrency, s.TotalTimeoutSeconds) }
	s.Concurrency, s.TotalTimeoutSeconds = 64, 7200
	if err := s.Validate(); err != nil { t.Fatalf("64 concurrent requests and 7200 seconds must be configurable: %v", err) }
	for _, n := range []int{0, 65} { s.Concurrency = n; if s.Validate() == nil { t.Errorf("invalid concurrency accepted: %d", n) } }
}

func TestMultipleQueuedReportsButOnlyOneRunningReport(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, id := range []string{first, second} {
		if _, err := s.CreateReport(ctx, testOwner, createRequest(id)); err != nil { t.Fatalf("waiting reports must coexist: %v", err) }
	}
	if err := s.BeginRun(ctx, testOwner, first); err != nil { t.Fatal(err) }
	if err := s.BeginRun(ctx, testOwner, second); err == nil { t.Fatal("two reports were allowed to run at once") }
	if err := s.Finish(ctx, testOwner, first, "done", map[string]any{}, map[string]any{}); err != nil { t.Fatal(err) }
	if err := s.BeginRun(ctx, testOwner, second); err != nil { t.Fatalf("next waiting report was blocked after completion: %v", err) }
}
