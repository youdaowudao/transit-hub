package group_rate_campaigns

import (
	"testing"
	"time"
)

func TestCampaignNotificationTimeUsesSingaporeWithoutChangingInstant(t *testing.T) {
	instant := time.Date(2026, 10, 2, 16, 30, 0, 0, time.UTC)
	original := instant
	if got := formatTime(&instant); got != "2026-10-03 00:30:00" {
		t.Fatalf("notification time = %q, want Singapore business time", got)
	}
	if !instant.Equal(original) || instant.Location() != time.UTC {
		t.Fatal("notification rendering must not rewrite the underlying timestamp")
	}
}
