package businesstime

import (
	"testing"
	"time"
)

func TestDateAtUsesFixedSingaporeBusinessDay(t *testing.T) {
	instant := time.Date(2026, 7, 31, 15, 16, 45, 0, time.UTC)
	if got := DateAt(instant); got != "2026-07-31" {
		t.Fatalf("DateAt() = %q, want 2026-07-31", got)
	}
}

func TestBoundsUsesSingaporeMidnight(t *testing.T) {
	start, end, err := Bounds("2026-07-31")
	if err != nil {
		t.Fatalf("Bounds() error: %v", err)
	}
	if got := start.Format(time.RFC3339); got != "2026-07-31T00:00:00+08:00" {
		t.Fatalf("start = %s, want Singapore midnight", got)
	}
	if got := end.Format(time.RFC3339); got != "2026-07-31T23:59:59+08:00" {
		t.Fatalf("end = %s, want Singapore end of day", got)
	}
}
