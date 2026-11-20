package migrations

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func EnsureSalesSchema(ctx context.Context, db *pgxpool.Pool) error {
	statement, err := migrationFiles.ReadFile("000048_sales.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, string(statement))
	return err
}
