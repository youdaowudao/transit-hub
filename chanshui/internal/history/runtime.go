package history

import "database/sql"

// RuntimeStore borrows the caller's existing runtime_state table. It does not
// open, migrate or own the database; callers must not Close this view.
func RuntimeStore(db *sql.DB) *Store { return &Store{db: db} }
