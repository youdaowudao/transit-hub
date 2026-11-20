package workspacescope

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrChanged = errors.New("工作区已变化，本次没有保存或发送；请重新读取当前工作区后再操作。")

type Scope struct {
	UserID         string `json:"-"`
	AdminAccountID string `json:"workspaceAdminAccountId"`
	CreatedAt      string `json:"workspaceCreatedAt"`
}
type CommitError struct{ Cause error }

func (e *CommitError) Error() string {
	return "保存结果尚未确认；请先重新读取当前设置后再操作，避免重复修改。"
}
func (e *CommitError) Unwrap() error { return e.Cause }

type Reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func Resolve(ctx context.Context, db Reader, expected Scope) error {
	var id string
	var created time.Time
	err := db.QueryRow(ctx, `SELECT a.id,
        a.created_at
        FROM users u JOIN admin_accounts a ON a.user_id=u.id
        AND a.id=u.current_admin_account_id
        WHERE u.id=$1`, expected.UserID).Scan(&id, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	if id != expected.AdminAccountID || created.Format(time.RFC3339Nano) != expected.CreatedAt {
		return ErrChanged
	}
	return nil
}

// Transaction locks the current user before its parent workspace for HTTP writes.
// Background publications lock only the captured parent and never follow a new selection.
func Transaction(ctx context.Context, db Beginner, scope Scope, current bool, fn func(pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Lock(ctx, tx, scope, current); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return Commit(ctx, tx)
}

// Lock allows a caller-owned Repeatable Read transaction to use the same lock order.
func Lock(ctx context.Context, tx pgx.Tx, scope Scope, current bool) error {
	var err error
	if current {
		var id string
		err = tx.QueryRow(ctx, `SELECT current_admin_account_id
        FROM users
        WHERE id=$1 FOR SHARE`, scope.UserID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChanged
		}
		if err != nil {
			return err
		}
		if id != scope.AdminAccountID {
			return ErrChanged
		}
	}
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT created_at
        FROM admin_accounts
        WHERE user_id=$1
        AND id=$2 FOR KEY SHARE`, scope.UserID, scope.AdminAccountID).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	if created.Format(time.RFC3339Nano) != scope.CreatedAt {
		return ErrChanged
	}
	return nil
}

func Commit(ctx context.Context, tx pgx.Tx) error {
	var err error
	if err = tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return err
		}
		return &CommitError{err}
	}
	return nil
}
