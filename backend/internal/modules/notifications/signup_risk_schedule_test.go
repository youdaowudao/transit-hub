package notifications

import (
	"testing"
	"time"
)

func TestSignupRiskScheduleWorkingEdgesAndNextDay(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
	}{{"Asia/Singapore", 8}, {"Asia/Tokyo", 9}, {"UTC", 0}} {
		t.Run(zone.name, func(t *testing.T) {
			schedule := SignupRiskSchedule{WorkStart: "09:00", WorkEnd: "18:00", TimeZone: zone.name}
			start := time.Date(2031, 1, 31, 9-zone.offset, 0, 0, 0, time.UTC)
			end := start.Add(9 * time.Hour)
			for _, tc := range []struct {
				name   string
				now    time.Time
				active bool
				next   time.Time
			}{{"before", start.Add(-time.Nanosecond), false, start}, {"start", start, true, start.AddDate(0, 0, 1)}, {"last", end.Add(-time.Nanosecond), true, start.AddDate(0, 0, 1)}, {"end", end, false, start.AddDate(0, 0, 1)}, {"next-day", start.AddDate(0, 0, 1), true, start.AddDate(0, 0, 2)}} {
				t.Run(tc.name, func(t *testing.T) {
					until, next, err := schedule.Window(tc.now)
					if err != nil || (until != nil) != tc.active || next == nil || !next.Equal(tc.next) {
						t.Fatalf("window mismatch: until=%v next=%v error=%v", until, next, err)
					}
					if until != nil && !until.Equal(tc.now.AddDate(0, 0, 0).Truncate(24*time.Hour).Add(time.Duration(18-zone.offset)*time.Hour)) {
						t.Fatal("wrong exclusive working end")
					}
				})
			}
		})
	}
}
func TestSignupRiskScheduleRejectsIncompleteInvalidAndOvernight(t *testing.T) {
	for _, s := range []SignupRiskSchedule{{}, {"9:00", "18:00", "UTC"}, {"09:00", "24:00", "UTC"}, {"09:00", "09:00", "UTC"}, {"18:00", "09:00", "UTC"}, {"09:00", "18:00", "Local"}, {"09:00", "18:00", "Asia/Shanghai"}} {
		if _, _, err := s.Window(time.Now()); err == nil {
			t.Fatalf("invalid schedule accepted: %+v", s)
		}
	}
}
