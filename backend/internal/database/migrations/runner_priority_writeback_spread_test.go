package migrations

import (
	"strings"
	"testing"
)

func TestPriorityWritebackSpreadMigrationIsSafeWhenBusinessTablesDoNotExist(t *testing.T) {
	sqlBytes, err := migrationFiles.ReadFile("000023_connection_health_priority_writeback_spread_e.sql")
	if err != nil {
		t.Fatalf("read E migration: %v", err)
	}
	sql := strings.ToUpper(string(sqlBytes))
	if strings.Count(sql, "ALTER TABLE IF EXISTS") != 3 {
		t.Fatalf("every E schema change must tolerate an empty database: %s", sql)
	}
	if strings.Contains(sql, "UPDATE ") || strings.Contains(sql, "INSERT ") || strings.Contains(sql, "DELETE ") {
		t.Fatalf("E migration must not mutate rows or reference an absent table unconditionally: %s", sql)
	}
}

func TestPriorityRoundCountMigrationIsSafeAndAddsOnlyTheFField(t *testing.T) {
	sqlBytes, err := migrationFiles.ReadFile("000024_connection_health_priority_round_count_f.sql")
	if err != nil {
		t.Fatalf("read F migration: %v", err)
	}
	sql := strings.ToUpper(string(sqlBytes))
	if strings.Count(sql, "ALTER TABLE IF EXISTS") != 1 || !strings.Contains(sql, "LAST_WRITE_ROUND_TARGET_COUNT INTEGER NOT NULL DEFAULT 0") {
		t.Fatalf("F migration must add exactly the round-count column safely: %s", sql)
	}
	if strings.Contains(sql, "UPDATE ") || strings.Contains(sql, "INSERT ") || strings.Contains(sql, "DELETE ") || strings.Contains(sql, "SELECT ") {
		t.Fatalf("F migration must not scan or mutate history rows: %s", sql)
	}
}
