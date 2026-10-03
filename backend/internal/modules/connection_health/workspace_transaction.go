package connection_health

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Every local participant uses this one workspace lock, including insertion of
// the first configuration/checkpoint row. No caller may hold it across HTTP or
// acquisition of a runtime lease.
func lockHealthWorkspaceTx(ctx context.Context, tx pgx.Tx, userID, adminAccountID string) error {
	for _, statement := range []string{
		`SET LOCAL lock_timeout = '5s'`,
		`SET LOCAL statement_timeout = '10s'`,
		`SET LOCAL idle_in_transaction_session_timeout = '10s'`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	key := "connection-health:workspace:" + strconv.Itoa(len(userID)) + ":" + userID + adminAccountID
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}

func (r *Repository) beginWorkspaceTransaction(ctx context.Context, userID, adminAccountID string) (pgx.Tx, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := lockHealthWorkspaceTx(ctx, tx, userID, adminAccountID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
