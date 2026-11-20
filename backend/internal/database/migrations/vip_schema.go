package migrations

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

func EnsureVIPSchema(ctx context.Context, db *pgxpool.Pool) error {
	statement, err := migrationFiles.ReadFile("000047_vip.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, string(statement))
	return err
}
