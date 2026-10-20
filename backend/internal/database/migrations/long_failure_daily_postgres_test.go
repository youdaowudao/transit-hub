package migrations

import (
	"strings"
	"testing"
)

// Mark the existing migrations applied, as on an upgraded site. Run discovers
// the new migration normally, so its absence is a business RED.
func TestMigrationSetsUntouchedRecommendedIntervalDaily(t *testing.T) {
	for _, withPresets := range []bool{true, false} {
		name := "without_preset_table"
		if withPresets {
			name = "existing_presets"
		}
		t.Run(name, func(t *testing.T) {
			pool := openMigrationPostgresPool(t)
			ctx := t.Context()
			if err := ensureMigrationsTable(ctx, pool); err != nil {
				t.Fatal(err)
			}
			entries, err := migrationFiles.ReadDir(".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000042" {
					continue
				}
				if _, err := pool.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", strings.TrimSuffix(entry.Name(), ".sql")); err != nil {
					t.Fatal(err)
				}
			}
			if withPresets {
				if _, err := pool.Exec(ctx, `CREATE TABLE connection_health_rule_presets(id text PRIMARY KEY,kind text NOT NULL,long_failure_interval_seconds integer NOT NULL,updated_at timestamptz NOT NULL DEFAULT '2000-01-01');
INSERT INTO connection_health_rule_presets(id,kind,long_failure_interval_seconds) VALUES
('recommended-default','recommended',3600),('recommended-other-workspace','recommended',3600),
('recommended-edited','recommended',7200),('custom','custom',3600),
('legacy-default','legacy_default',3600),('legacy-snapshot','legacy_snapshot',3600);
CREATE TABLE connection_health_workspace_settings(config_generation bigint NOT NULL);
INSERT INTO connection_health_workspace_settings VALUES(17)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := Run(ctx, pool); err != nil {
				t.Fatalf("upgrade migration failed: %v", err)
			}
			if withPresets {
				for id, want := range map[string]int{"recommended-default": 86400, "recommended-other-workspace": 86400, "recommended-edited": 7200, "custom": 3600, "legacy-default": 3600, "legacy-snapshot": 3600} {
					var got int
					if err := pool.QueryRow(ctx, "SELECT long_failure_interval_seconds FROM connection_health_rule_presets WHERE id=$1", id).Scan(&got); err != nil || got != want {
						t.Errorf("%s interval=%d want=%d err=%v", id, got, want, err)
					}
				}
				var generation int
				if err := pool.QueryRow(ctx, "SELECT config_generation FROM connection_health_workspace_settings").Scan(&generation); err != nil || generation != 17 {
					t.Fatal("cadence migration changed configuration generation")
				}
				if _, err := pool.Exec(ctx, "UPDATE connection_health_rule_presets SET long_failure_interval_seconds=3600 WHERE id='recommended-default'"); err != nil {
					t.Fatal(err)
				}
			}
			if err := Run(ctx, pool); err != nil {
				t.Fatalf("second runner call failed: %v", err)
			}
			var applied int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version='000042_connection_health_long_failure_daily'").Scan(&applied); err != nil || applied != 1 {
				t.Errorf("new migration applications=%d want=1 err=%v", applied, err)
			}
			if withPresets {
				var interval int
				if err := pool.QueryRow(ctx, "SELECT long_failure_interval_seconds FROM connection_health_rule_presets WHERE id='recommended-default'").Scan(&interval); err != nil || interval != 3600 {
					t.Fatal("second runner call overwrote the user's later interval edit")
				}
			}
		})
	}
}
