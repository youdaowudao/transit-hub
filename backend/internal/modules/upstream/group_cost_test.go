package upstream

import (
	"math"
	"testing"
	"time"
)

func TestCalculateGroupCostSnapshotUsesApproximateHourlyBaseline(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	samples := []GroupCostSample{
		{Date: "2026-08-16", RawAmount: 2.0, ObservedAt: now.Add(-75 * time.Minute)},
		{Date: "2026-08-16", RawAmount: 2.4, ObservedAt: now.Add(-65 * time.Minute)},
		{Date: "2026-08-16", RawAmount: 3.0, ObservedAt: now.Add(-5 * time.Minute)},
	}

	today, recentHour, observedAt, sample := calculateGroupCostSnapshot(samples, "2026-08-16", now, 7)
	if today == nil || *today != 21 {
		t.Fatalf("today cost = %v, want 21", today)
	}
	if recentHour == nil || math.Abs(*recentHour-4.2) > 1e-9 {
		t.Fatalf("recent hour cost = %v, want 4.2", recentHour)
	}
	if observedAt == nil || !observedAt.Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("observedAt = %v, want latest sample", observedAt)
	}
	if sample == nil || sample.Date != "2026-08-16" {
		t.Fatalf("sample = %+v, want same business date", sample)
	}
}

func TestCalculateGroupCostSnapshotReturnsUnknownForStaleOrMissingBaseline(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		samples []GroupCostSample
	}{
		{
			name: "retained latest sample",
			samples: []GroupCostSample{
				{Date: "2026-08-16", RawAmount: 3, ObservedAt: now.Add(-16 * time.Minute)},
			},
		},
		{
			name: "no hourly baseline",
			samples: []GroupCostSample{
				{Date: "2026-08-16", RawAmount: 2, ObservedAt: now.Add(-10 * time.Minute)},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			today, recentHour, observedAt, sample := calculateGroupCostSnapshot(tc.samples, "2026-08-16", now, 7)
			if tc.name == "retained latest sample" {
				if today == nil || *today != 21 || recentHour != nil || observedAt == nil || sample == nil {
					t.Fatalf("retained sample should keep today cost only, got today=%v recentHour=%v observedAt=%v sample=%v", today, recentHour, observedAt, sample)
				}
			}
			if tc.name == "no hourly baseline" {
				if today == nil || *today != 14 {
					t.Fatalf("today cost = %v, want 14", today)
				}
				if recentHour != nil || observedAt == nil {
					t.Fatalf("hourly comparison should be unknown, got recentHour=%v observedAt=%v", recentHour, observedAt)
				}
			}
		})
	}
}

func TestCalculateGroupCostSnapshotIgnoresPreviousBusinessDate(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	samples := []GroupCostSample{
		{Date: "2026-08-15", RawAmount: 100, ObservedAt: now.Add(-70 * time.Minute)},
		{Date: "2026-08-16", RawAmount: 3, ObservedAt: now.Add(-5 * time.Minute)},
	}

	today, recentHour, _, _ := calculateGroupCostSnapshot(samples, "2026-08-16", now, 7)
	if today == nil || *today != 21 {
		t.Fatalf("today cost = %v, want 21", today)
	}
	if recentHour != nil {
		t.Fatalf("previous business date must not be used as baseline: %v", *recentHour)
	}
}

func TestCalculateGroupCostSnapshotRejectsNegativeAndCounterRollback(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cases := map[string][]GroupCostSample{
		"negative cumulative": {{Date: "2026-08-16", RawAmount: -1, ObservedAt: now.Add(-5 * time.Minute)}},
		"negative hourly baseline": {
			{Date: "2026-08-16", RawAmount: -1, ObservedAt: now.Add(-65 * time.Minute)},
			{Date: "2026-08-16", RawAmount: 3, ObservedAt: now.Add(-5 * time.Minute)},
		},
		"counter rollback": {
			{Date: "2026-08-16", RawAmount: 5, ObservedAt: now.Add(-65 * time.Minute)},
			{Date: "2026-08-16", RawAmount: 3, ObservedAt: now.Add(-5 * time.Minute)},
		},
	}
	for name, samples := range cases {
		t.Run(name, func(t *testing.T) {
			today, recentHour, observedAt, sample := calculateGroupCostSnapshot(samples, "2026-08-16", now, 7)
			if today != nil || recentHour != nil || observedAt != nil || sample != nil {
				t.Fatalf("abnormal cumulative value must be unknown, got today=%v recentHour=%v observedAt=%v sample=%v", today, recentHour, observedAt, sample)
			}
		})
	}
}
