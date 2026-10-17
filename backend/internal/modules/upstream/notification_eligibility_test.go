package upstream

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type notificationEligibilityRowFunc func(...any) error

func (f notificationEligibilityRowFunc) Scan(targets ...any) error { return f(targets...) }

type notificationEligibilityDBFunc func(context.Context, string, ...any) pgx.Row

func (f notificationEligibilityDBFunc) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f(ctx, sql, args...)
}

func TestSiteNotificationEligibilityUsesFiveSecondBoundAndReleasesChildContext(t *testing.T) {
	var queryCtx context.Context
	reader := notificationEligibilityDBFunc(func(ctx context.Context, _ string, args ...any) pgx.Row {
		queryCtx = ctx
		if !reflect.DeepEqual(args, []any{"user", "workspace", "site"}) {
			t.Fatal("qualification must retain all ownership identifiers")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) < 4900*time.Millisecond {
			t.Fatal("independent PostgreSQL qualification must have the existing five second timeout")
		}
		return notificationEligibilityRowFunc(func(targets ...any) error { *targets[0].(*bool) = true; return nil })
	})
	enabled, err := readSiteNotificationEnabled(t.Context(), reader, "user", "workspace", "site")
	if !enabled || err != nil {
		t.Fatal("a successful narrow enabled read must return its result")
	}
	if queryCtx.Err() != context.Canceled || t.Context().Err() != nil {
		t.Fatal("read child context must be released without cancelling its caller")
	}
}

func TestSiteNotificationEligibilityHonorsParentDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"short-parent-deadline", "explicit-parent-cancel"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "short-parent-deadline" {
				parent, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
				defer cancel()
			}
			reader := notificationEligibilityDBFunc(func(ctx context.Context, _ string, _ ...any) pgx.Row {
				if mode == "short-parent-deadline" {
					want, _ := parent.Deadline()
					got, ok := ctx.Deadline()
					if !ok || !got.Equal(want) {
						t.Fatal("shorter parent deadline must be retained")
					}
				}
				if mode == "explicit-parent-cancel" {
					cancel()
				}
				return notificationEligibilityRowFunc(func(...any) error { <-ctx.Done(); return ctx.Err() })
			})
			started := time.Now()
			enabled, err := readSiteNotificationEnabled(parent, reader, "user", "workspace", "site")
			if enabled || err == nil || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)) || time.Since(started) > time.Second {
				t.Fatal("blocked qualification must promptly return the actual parent cancellation instead of a false successful result")
			}
		})
	}
}
