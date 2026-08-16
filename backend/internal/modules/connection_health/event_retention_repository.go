package connection_health

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	eventRetentionIndexName = "idx_connection_health_events_created_id"
	maxEventDeleteBatchSize = 5000
)

func (r *Repository) TryAcquireEventRetentionLease(ctx context.Context) (func(), bool, error) {
	return r.acquireRuntimeLease(ctx, "connection-health:event-retention", false)
}

// EnsureEventRetentionIndex uses PostgreSQL's concurrent index operations so upgrading a
// large existing event table does not take the write-blocking lock of CREATE INDEX.
func (r *Repository) EnsureEventRetentionIndex(ctx context.Context) error {
	var valid bool
	err := r.db.QueryRow(ctx, `
		SELECT index.indisvalid
		FROM pg_class relation
		JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		JOIN pg_index index ON index.indexrelid = relation.oid
		WHERE namespace.nspname = current_schema() AND relation.relname = $1
	`, eventRetentionIndexName).Scan(&valid)
	if err == nil && valid {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect event retention index: %w", err)
	}
	if err == nil && !valid {
		if _, dropErr := r.db.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS idx_connection_health_events_created_id`); dropErr != nil {
			return fmt.Errorf("drop invalid event retention index: %w", dropErr)
		}
	}
	if _, err := r.db.Exec(ctx, `
		CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_connection_health_events_created_id
		ON connection_health_events (created_at, id)
	`); err != nil {
		return fmt.Errorf("create event retention index: %w", err)
	}
	return nil
}

// DeleteEventsBefore removes at most limit rows in one statement. SKIP LOCKED avoids
// waiting on an event row held by another transaction; each call commits independently.
func (r *Repository) DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > maxEventDeleteBatchSize {
		return 0, fmt.Errorf("event retention batch size must be between 1 and %d", maxEventDeleteBatchSize)
	}
	commandTag, err := r.db.Exec(ctx, `
		WITH expired AS (
			SELECT id
			FROM connection_health_events
			WHERE created_at < $1
			ORDER BY created_at ASC, id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM connection_health_events event
		USING expired
		WHERE event.id = expired.id
	`, cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return commandTag.RowsAffected(), nil
}
