package connection_health

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestFailureFixReview3LocalSchedulingKeepsUpstreamFailureCount(t *testing.T) {
	for _, localErr := range []error{errMultiplierQueueFull, errMultiplierQueueTimeout, context.Canceled} {
		for _, retain := range []bool{false, true} {
			t.Run(localErr.Error()+fmt.Sprint(retain), func(t *testing.T) {
				keys := map[string]upstreamKeyMetadata{}
				if retain {
					keys["fixture-key"] = upstreamKeyMetadata{id: "fixture-key", groupName: "vip"}
				}
				entry := &multiplierSnapshotEntry{workspaceKey: "fixture", keys: keys, consecutiveFailures: 2, nextRetryAt: time.Now().Add(30 * time.Minute)}
				service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{"fixture": entry}}
				for attempt := 0; attempt < 3; attempt++ {
					entry.inFlight, entry.done = true, make(chan struct{})
					waiter := entry.done
					captured := *entry
					service.finishMultiplierSnapshotLocked(entry, &captured, nil, nil, multiplierSiteMetadata{}, fmt.Errorf("fixture local scheduling: %w", localErr))
					if entry.consecutiveFailures != 2 {
						t.Errorf("local scheduling changed upstream failure count to %d", entry.consecutiveFailures)
					}
					failureFixRetryInterval(t, entry, 30*time.Second)
					if !reflect.DeepEqual(entry.keys, keys) || entry.inFlight {
						t.Error("local failure dropped stale keys or retained an in-flight task")
					}
					select {
					case <-waiter:
					default:
						t.Error("local failure did not release its waiter")
					}
				}
				captured := *entry
				service.finishMultiplierSnapshotLocked(entry, &captured, nil, nil, multiplierSiteMetadata{}, &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401})
				if entry.consecutiveFailures != 3 {
					t.Error("next upstream failure did not continue from the preserved failure count")
				}
				failureFixRetryInterval(t, entry, 30*time.Minute)
				captured = *entry
				service.finishMultiplierSnapshotLocked(entry, &captured, map[string]upstreamKeyMetadata{}, nil, multiplierSiteMetadata{}, nil)
				if entry.consecutiveFailures != 0 || !entry.nextRetryAt.IsZero() {
					t.Error("successful refresh failed to reset upstream backoff")
				}
			})
		}
	}
}

func TestFailureFixReview3ActualUpstreamFailuresStillBackOff(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		keyFailures map[string]string
	}{
		{"auth", &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}, nil},
		{"rate_limit", &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 429}, nil},
		{"server", &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 502}, nil},
		{"request_timeout", errMultiplierRequestTimeout, nil},
		{"key_failure", nil, map[string]string{"fixture-key": "auth_failed"}},
		{"key_failure_before_cancellation", context.Canceled, map[string]string{"fixture-key": "auth_failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &multiplierSnapshotEntry{workspaceKey: "fixture", keys: map[string]upstreamKeyMetadata{}}
			service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{"fixture": entry}}
			for i, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
				captured := *entry
				service.finishMultiplierSnapshotLocked(entry, &captured, nil, tc.keyFailures, multiplierSiteMetadata{}, tc.err)
				if entry.consecutiveFailures != i+1 {
					t.Error("actual upstream failure did not increase count")
				}
				failureFixRetryInterval(t, entry, want)
			}
		})
	}
}

func TestFailureFixReview3BeforeRequestTimeoutUsesLocalRetry(t *testing.T) {
	for _, cause := range []string{"queue_deadline", "parent_deadline", "parent_cancel"} {
		t.Run(cause, func(t *testing.T) {
			service, target, captured := newTimeoutMultiplierEntry()
			target.consecutiveFailures, captured.consecutiveFailures = 2, 2
			reader := &timeoutMultiplierReader{}
			parent := context.Background()
			if cause == "queue_deadline" {
				captured.enqueuedAt = time.Now().Add(-multiplierRefreshTimeout - time.Second)
			}
			if cause == "parent_deadline" {
				ctx, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
				defer cancel()
				parent = ctx
			}
			if cause == "parent_cancel" {
				ctx, cancel := context.WithCancel(parent)
				cancel()
				parent = ctx
			}
			waiter := target.done
			service.refreshMultiplierSnapshot(parent, reader, captured, target)
			if reader.calls.Load() != 0 {
				t.Error("local timeout or cancellation started an upstream request")
			}
			if target.consecutiveFailures != 2 {
				t.Error("pre-request local failure changed upstream failure count")
			}
			failureFixRetryInterval(t, target, 30*time.Second)
			if cause != "parent_cancel" && target.lastOutcome != "queue_timeout" {
				t.Errorf("pre-request timeout outcome=%s want queue_timeout", target.lastOutcome)
			}
			select {
			case <-waiter:
			default:
				t.Error("pre-request failure retained its waiter")
			}
		})
	}
}
