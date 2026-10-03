package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type leaseRenewalFake struct {
	rows  int
	err   error
	calls int
}

func (f *leaseRenewalFake) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	f.calls++
	if f.rows == 1 {
		return pgconn.NewCommandTag("UPDATE 1"), f.err
	}
	return pgconn.NewCommandTag("UPDATE 0"), f.err
}

func TestRuntimeLeaseFailedRenewalPermanentlyLosesAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"zero rows", nil}, {"database error", errors.New("unavailable")}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lost := make(chan struct{})
			var once sync.Once
			handle := &RuntimeLeaseHandle{Key: "fixture", OwnerID: "owner-a", Context: ctx, Lost: lost, lose: func() { once.Do(func() { close(lost); cancel() }) }}
			fake := &leaseRenewalFake{err: tc.err}
			if err := renewActionLease(context.Background(), fake, handle); err == nil {
				t.Fatal("failed renewal retained authority")
			}
			select {
			case <-lost:
			default:
				t.Fatal("Lost was not signaled")
			}
			if ctx.Err() == nil {
				t.Fatal("lease dispatch context was not cancelled")
			}
			fake.rows = 1
			fake.err = nil
			if err := renewActionLease(context.Background(), fake, handle); err == nil {
				t.Fatal("later successful renewal revived authorization")
			}
			if fake.calls != 1 {
				t.Fatalf("lost lease retried storage: calls=%d", fake.calls)
			}
		})
	}
}

func TestRuntimeLeaseSuccessfulRenewalKeepsAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := make(chan struct{})
	handle := &RuntimeLeaseHandle{Key: "fixture", OwnerID: "owner-a", Context: ctx, Lost: lost, lose: func() { close(lost); cancel() }}
	if err := renewActionLease(ctx, &leaseRenewalFake{rows: 1}, handle); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
		t.Fatal("valid renewal lost authorization")
	default:
	}
}
