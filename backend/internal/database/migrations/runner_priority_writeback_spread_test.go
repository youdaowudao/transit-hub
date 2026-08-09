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
