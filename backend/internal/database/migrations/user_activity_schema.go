package migrations

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsureUserActivitySchema shares the migration source with normal module initialization.
func EnsureUserActivitySchema(ctx context.Context, db *pgxpool.Pool) error {
	sql, err := migrationFiles.ReadFile("000046_user_activity.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, string(sql))
	return err
}
