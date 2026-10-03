package upstream

import (
	"net/http"
	"testing"
	"time"
)

func TestStageADeadlineStrictTwoSecondMargin(t *testing.T) {
	deadline := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		name        string
		offset      time.Duration
		known       bool
		wantExpired bool
	}{
		{"future", -time.Second, true, false},
		{"one-second-after", time.Second, true, false},
		{"exactly-two-seconds-after", 2 * time.Second, true, false},
		{"three-seconds-after", 3 * time.Second, true, true},
		{"untrusted-old-deadline", time.Hour, false, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if got := Sub2APIDeadlineExpired(&deadline, sample.known, deadline.Add(sample.offset)); got != sample.wantExpired {
				t.Fatalf("expired=%v want=%v", got, sample.wantExpired)
			}
		})
	}
	if !Sub2APIDeadlineExpired(nil, true, deadline) {
		t.Fatal("known null is unlimited")
	}
	if Sub2APIDeadlineExpired(nil, false, deadline) {
		t.Fatal("missing deadline is not unlimited")
	}
}

func TestStageAClockGuardInspectsEveryPageAndStrictBoundary(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		name, base string
		skew       time.Duration
		missing    bool
		want       bool
	}{
		{"exactly-two-seconds", "http://127.0.0.1:8080", 2 * time.Second, false, true},
		{"two-seconds-plus-nanosecond", "http://127.0.0.1:8080", 2*time.Second + time.Nanosecond, false, false},
		{"future-three-seconds", "http://127.0.0.1:8080", -3 * time.Second, false, false},
		{"last-page-without-date", "http://127.0.0.1:8080", 0, true, false},
		{"ipv6-loopback", "http://[::1]:8080", time.Second, false, true},
		{"localhost", "http://localhost:8080", time.Second, false, true},
		{"loopback-looking-suffix", "http://localhost.example.test:8080", 0, false, false},
		{"non-loopback", "http://100.107.57.101:8080", 0, false, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			responses := []InventoryResponseTime{{HTTPDate: now.Format(http.TimeFormat), ReceivedAt: now.Add(time.Second)}, {HTTPDate: now.Format(http.TimeFormat), ReceivedAt: now.Add(sample.skew)}}
			if sample.missing {
				responses[1].HTTPDate = ""
			}
			if got := Sub2APIInventoryTimeTrusted(sample.base, responses); got != sample.want {
				t.Fatalf("whole inventory trusted=%v want=%v", got, sample.want)
			}
		})
	}
	if Sub2APIInventoryTimeTrusted("http://127.0.0.1:8080", nil) {
		t.Fatal("missing HTTP evidence became trusted")
	}
}
