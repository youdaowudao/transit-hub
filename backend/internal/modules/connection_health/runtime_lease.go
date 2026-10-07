package connection_health

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const runtimeLeaseTTL = 2 * time.Minute
const runtimeLeaseQueryTimeout = 5 * time.Second

// RuntimeLeaseHandle never regains authority after a failed renewal. Lost and
// Context notify the worker promptly; the database sending gate remains the
// authority when a paused process has not yet observed that notification.
type RuntimeLeaseHandle struct {
	Key     string
	OwnerID string
	Context context.Context
	Lost    <-chan struct{}
	release func()
	lose    func()
}

func (h *RuntimeLeaseHandle) Release() {
	if h != nil && h.release != nil {
		h.release()
	}
}

type actionLeaseContextKey struct{}
type mutationLeaseContextKey struct{}
type workspacePriorityLeaseContextKey struct{}

func workspacePriorityLeaseFromContext(ctx context.Context) *RuntimeLeaseHandle {
	handle, _ := ctx.Value(workspacePriorityLeaseContextKey{}).(*RuntimeLeaseHandle)
	return handle
}

func mutationLeaseFromContext(ctx context.Context) *RuntimeLeaseHandle {
	handle, _ := ctx.Value(mutationLeaseContextKey{}).(*RuntimeLeaseHandle)
	return handle
}
func mutationRuntimeLeaseKey(userID, workspace string) string {
	return "connection-health:sub2api-mutation:" + strconv.Itoa(len(userID)) + ":" + userID + workspace
}
func (s *Service) acquireActionMutationLease(ctx context.Context, userID, workspace string) (context.Context, func(), error) {
	target := actionLeaseFromContext(ctx)
	next, release, acquired, err := s.acquireActionLease(ctx, mutationRuntimeLeaseKey(userID, workspace), true)
	if err != nil || !acquired {
		if err == nil {
			err = ErrRemoteActionLeaseLost
		}
		return ctx, nil, err
	}
	mutation := actionLeaseFromContext(next)
	next = context.WithValue(next, mutationLeaseContextKey{}, mutation)
	return actionLeaseContext(next, target), release, nil
}

func actionLeaseFromContext(ctx context.Context) *RuntimeLeaseHandle {
	handle, _ := ctx.Value(actionLeaseContextKey{}).(*RuntimeLeaseHandle)
	return handle
}

func actionLeaseContext(ctx context.Context, handle *RuntimeLeaseHandle) context.Context {
	return context.WithValue(ctx, actionLeaseContextKey{}, handle)
}

type actionLeaseRepository interface {
	AcquireActionLease(context.Context, string, bool) (*RuntimeLeaseHandle, bool, error)
}

func (s *Service) acquireActionTargetLease(ctx context.Context, targetID string, wait bool) (context.Context, func(), bool, error) {
	return s.acquireActionLease(ctx, "connection-health:target:"+targetID, wait)
}

func priorityRuntimeLeaseKey(userID, adminAccountID string) string {
	return "connection-health:priority-sync:" + strconv.Itoa(len(userID)) + ":" + userID + adminAccountID
}

func (s *Service) acquireActionLease(ctx context.Context, key string, wait bool) (context.Context, func(), bool, error) {
	repository, ok := s.repo.(actionLeaseRepository)
	if !ok {
		return ctx, nil, false, errors.New("action lease storage unavailable")
	}
	handle, acquired, err := repository.AcquireActionLease(ctx, key, wait)
	if err != nil || !acquired {
		return ctx, nil, acquired, err
	}
	return actionLeaseContext(handle.Context, handle), handle.Release, true, nil
}

func (r *Repository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	ownerID, err := newID()
	if err != nil {
		return nil, false, err
	}
	for {
		var returnedOwner string
		err = r.db.QueryRow(ctx, `
			INSERT INTO connection_health_runtime_leases (lease_key, owner_id, expires_at, updated_at)
			VALUES ($1,$2,clock_timestamp()+make_interval(secs=>$3),clock_timestamp())
			ON CONFLICT (lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id,
				expires_at=EXCLUDED.expires_at, updated_at=clock_timestamp()
			WHERE connection_health_runtime_leases.expires_at <= clock_timestamp()
			RETURNING owner_id`, key, ownerID, int(runtimeLeaseTTL/time.Second)).Scan(&returnedOwner)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
		if !wait {
			return nil, false, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
	}
	leaseParent := ctx
	if detached, _ := ctx.Value(detachedActionLeaseKey{}).(bool); detached {
		leaseParent = context.WithoutCancel(ctx)
	}
	leaseCtx, cancel := context.WithCancel(leaseParent)
	lost := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	var loseOnce, releaseOnce sync.Once
	lose := func() { loseOnce.Do(func() { close(lost); cancel() }) }
	handle := &RuntimeLeaseHandle{Key: key, OwnerID: ownerID, Context: leaseCtx, Lost: lost, lose: lose}
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-leaseCtx.Done():
				lose()
				return
			case <-ticker.C:
				renewCtx, cancelRenew := context.WithTimeout(context.Background(), runtimeLeaseQueryTimeout)
				renewErr := renewActionLease(renewCtx, r.db, handle)
				cancelRenew()
				if renewErr != nil {
					return
				}
			}
		}
	}()
	handle.release = func() {
		releaseOnce.Do(func() {
			lose()
			close(stop)
			<-done
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), runtimeLeaseQueryTimeout)
			defer cancelRelease()
			_, _ = r.db.Exec(releaseCtx, `DELETE FROM connection_health_runtime_leases WHERE lease_key=$1 AND owner_id=$2`, key, ownerID)
		})
	}
	return handle, true, nil
}

type runtimeLeaseExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func renewActionLease(ctx context.Context, db runtimeLeaseExecer, handle *RuntimeLeaseHandle) error {
	if handle == nil || handle.Context.Err() != nil {
		return ErrRemoteActionLeaseLost
	}
	select {
	case <-handle.Lost:
		return ErrRemoteActionLeaseLost
	default:
	}
	tag, err := db.Exec(ctx, `
		UPDATE connection_health_runtime_leases
		SET expires_at=clock_timestamp()+make_interval(secs=>$3),updated_at=clock_timestamp()
		WHERE lease_key=$1 AND owner_id=$2 AND expires_at>clock_timestamp()`, handle.Key, handle.OwnerID, int(runtimeLeaseTTL/time.Second))
	if err != nil || tag.RowsAffected() != 1 {
		handle.lose()
		if err != nil {
			return err
		}
		return ErrRemoteActionLeaseLost
	}
	return nil
}
