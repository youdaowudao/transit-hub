package businesstime

import (
	"testing"
	"time"
)

func TestGlobalizationSingaporeDayPreservesCurrentLedgerBoundary(t *testing.T) {
	if Timezone != "Asia/Singapore" || Location().String() != "Asia/Singapore" {
		t.Errorf("business timezone must be Asia/Singapore, got %s / %s", Timezone, Location())
	}
	before := time.Date(2026, 10, 2, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if DateAt(before) != "2026-10-02" || DateAt(after) != "2026-10-03" {
		t.Fatal("the existing midnight ledger boundary changed")
	}
	start, end, err := Bounds("2026-10-03")
	if err != nil {
		t.Fatal(err)
	}
	if !start.Equal(after) || !end.Equal(after.Add(24*time.Hour-time.Nanosecond)) {
		t.Fatal("daily ledger range must preserve both boundary instants")
	}
}
