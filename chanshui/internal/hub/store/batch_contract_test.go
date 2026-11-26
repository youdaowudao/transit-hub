package store

import (
	"context"
	"strings"
	"testing"
)

func TestBatchV8PreservesOldReportAndSingleActive(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	a := strings.Repeat("a", 32)
	b := strings.Repeat("b", 32)
	for _, id := range []string{a, b} {
		if _, err := s.CreateReport(ctx, testOwner, createRequest(id)); err != nil {
			t.Fatal(err)
		}
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 8 {
		t.Fatalf("schema v8 required: version=%d err=%v", version, err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM hub_reports WHERE batch_id IS NULL").Scan(&count); err != nil || count != 2 {
		t.Fatalf("old reports changed: %d %v", count, err)
	}
	if err := s.BeginRun(ctx, testOwner, a); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRun(ctx, testOwner, b); err == nil {
		t.Fatal("second running report allowed")
	}
}
