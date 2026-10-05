package connection_health

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestAdminGroupsTimingLogsOnlySlowRequests(t *testing.T) {
	for _, elapsed := range []time.Duration{time.Millisecond, 3 * time.Second, 4 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			repo := newFakeRepository()
			service := newAdminGroupsService(fakePlatformGroupReader{}, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			_, err := service.adminGroupsForWorkspaceWithConnectionsProgress(context.Background(), "user1", "ws1", true, false, nil, true, nil, nil, nil, time.Now().Add(-elapsed), 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(output.String(), "admin groups timing"); got != (elapsed >= 3*time.Second) {
				t.Fatalf("total timing log=%v elapsed=%s output=%q", got, elapsed, output.String())
			}
			if strings.Contains(output.String(), "fresh multiplier refresh completed") {
				t.Fatalf("fast fresh multiplier refresh must be quiet: %q", output.String())
			}
		})
	}
}

func TestAdminGroupsFreshMultiplierLoggingThreshold(t *testing.T) {
	for _, duration := range []time.Duration{3*time.Second - time.Nanosecond, 3 * time.Second, 3*time.Second + time.Nanosecond} {
		t.Run(duration.String(), func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			logSlowAdminGroupsOperation(duration, "[connection-health] fresh multiplier refresh completed workspace=%s duration=%s", "ws1", duration)
			if got := strings.Contains(output.String(), "fresh multiplier refresh completed"); got != (duration >= 3*time.Second) {
				t.Fatalf("multiplier timing log=%v duration=%s output=%q", got, duration, output.String())
			}
		})
	}
}
