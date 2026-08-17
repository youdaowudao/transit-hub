package users

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

const findUserByIDSQL = `
	SELECT id, email, NULL::text AS name, ''::text AS "passwordHash", "createdAt", "updatedAt"
	FROM users
	WHERE id = $1
`

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) FindByID(ctx context.Context, userID string) ([]User, error) {
	rows, err := r.db.Query(ctx, findUserByIDSQL, userID)
	if err != nil {
		return nil, err
	}
	return scanUsers(rows)
}

func scanUsers(rows pgxRows) ([]User, error) {
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

type pgxRows interface {
	Close()
	Next() bool
	Scan(dest ...any) error
	Err() error
}
