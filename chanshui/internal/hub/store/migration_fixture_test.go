package store

import "testing"

// Older migration fixtures are seeded through Open (now v8), then deliberately
// reverted to the historical version under test. Remove the actual v8 schema
// as well as lowering user_version so they exercise a real v7-to-v8 upgrade.
// Never change the old migration's business assertions.
func stripV8ForMigrationFixture(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`DROP INDEX hub_reports_owner_batch;
		ALTER TABLE hub_reports DROP COLUMN batch_id;
		DROP TABLE hub_batches`); err != nil {
		t.Fatal(err)
	}
}
