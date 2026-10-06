package migrations

import (
	"context"
	"testing"
	"time"

	health "transithub/backend/internal/modules/connection_health"
)

func TestTaskARuleMigrationFreshInstallPostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := context.Background()
	start := time.Now()
	if err := Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := health.NewRepository(pool)
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil || settings.RuleVersion != health.RuleVersionV2 || settings.ConfigGeneration != 0 {
		t.Fatalf("fresh read %+v %v", settings, err)
	}
	if err := r.SavePolicyWithTargets(ctx, health.Policy{ID: "p", UserID: "u", AdminAccountID: "w", Name: "首装", Enabled: true, ProbeIntervalSeconds: 60, DailyProbeBudget: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	policy, err := r.GetPolicy(ctx, "p", "u", "w")
	if err != nil || policy == nil || policy.RulePreset == nil || policy.RulePreset.Kind != health.PresetRecommended || policy.ConfigGeneration != 1 {
		t.Fatalf("fresh create %+v %v", policy, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_rule_presets WHERE user_id='u' AND admin_account_id='w'`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	t.Logf("fresh migration→EnsureSchema→read→create elapsed=%s presets=%d", time.Since(start), n)
}

func TestTaskARuleMigrationBackfillBoundariesPostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := context.Background()
	r := health.NewRepository(pool)
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_policies(id,user_id,admin_account_id,name,failure_threshold,success_threshold,cooldown_seconds,observation_seconds,recovery_step_percent) VALUES
 ('gpt','u','w','GPT自动化策略',3,2,300,50,25),('anthropic','u','w','Anthropic 分组自动化策略',3,2,300,300,25),('luna','u','w','luna探测',3,2,300,300,25),('monitor','u','w','只监控不调度',3,2,300,300,25);
 INSERT INTO connection_health_states(connection_id,model_name,user_id,admin_account_id,upstream_site_id,upstream_group_name,state,current_weight,consecutive_failures,counter_protocol,health_evidence_status,health_evidence_protocol) VALUES
 ('sub2api:back','m','u','w','s','g','degraded',0,2,'responses','valid','responses'),('sub2api:stale','m','u','w','s','g','suspended',0,3,'responses','valid','responses'),('sub2api:edge','m','u','w','s','g','degraded',75,2,'responses','valid','responses'),('real-id','m','u','w','s','g','observing',25,2,'responses','valid','responses'),('sub2api:orphan','m','orphan','other','s','g','recovering',25,1,'chat_completions','legacy',NULL);
 INSERT INTO connection_health_events(id,connection_id,model_name,user_id,admin_account_id,result,request_protocol,probe_disposition,created_at) VALUES
 ('back-before','sub2api:back','m','u','w','server_error','responses','applied',now()-interval '28 hours'),
 ('back-switch','sub2api:back','m','u','w','server_error','chat_completions','applied',now()-interval '27 hours'),
 ('back-current','sub2api:back','m','u','w','server_error','responses','applied',now()-interval '26 hours'),
 ('stale-old','sub2api:stale','m','u','w','server_error','responses','stale',now()-interval '28 hours'),
 ('stale-current','sub2api:stale','m','u','w','server_error','responses','applied',now()-interval '1 hour'),
 ('edge-window','sub2api:edge','m','u','w','server_error','responses','applied',now()-interval '24 hours');
 INSERT INTO connection_health_events(id,connection_id,model_name,user_id,admin_account_id,result,request_protocol,probe_disposition,created_at)
 SELECT 'volume-'||i,'sub2api:volume-'||(i%100),'m','u','w','server_error','responses','applied',now()-interval '1 hour' FROM generate_series(1,36000)i;`); err != nil {
		t.Fatal(err)
	}
	sql, err := migrationFiles.ReadFile("000031_connection_health_rule_presets.sql")
	if err != nil {
		t.Fatal(err)
	}
	migrationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationTx.Rollback(ctx)
	start := time.Now()
	if _, err := migrationTx.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var seqRows, indexRows int64
	if err := migrationTx.QueryRow(ctx, `SELECT COALESCE(sum(seq_tup_read),0),COALESCE(sum(idx_tup_fetch),0) FROM pg_stat_xact_user_tables WHERE schemaname=current_schema() AND relname IN ('connection_health_events','connection_health_states','connection_health_policies')`).Scan(&seqRows, &indexRows); err != nil {
		t.Fatal(err)
	}
	var ddlLocks int
	if err := migrationTx.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND granted AND mode='AccessExclusiveLock'`).Scan(&ddlLocks); err != nil {
		t.Fatal(err)
	}
	if err := migrationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("migration actual transaction scan counters: sequential tuples=%d index tuples fetched=%d held AccessExclusive locks=%d, DDL lock upper bound=%s", seqRows, indexRows, ddlLocks, elapsed)
	presets, err := r.ListRulePresets(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 4 {
		t.Fatalf("presets=%d want recommended/default/two snapshots", len(presets))
	}
	policies, err := r.ListPolicies(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]bool{}
	for _, p := range policies {
		if p.RulePreset == nil || p.RulePreset.Kind != health.PresetRecommended || p.LegacyPresetID == "" {
			t.Fatalf("pointers %+v", p)
		}
		snapshots[p.LegacyPresetID] = true
		if p.ID == "gpt" && p.ObservationSeconds != 50 {
			t.Fatal("migration rewrote frozen column")
		}
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	for _, sample := range []struct {
		id    string
		hours int
	}{{"back", 26}, {"stale", 1}, {"edge", 24}} {
		s, err := r.GetState(ctx, "sub2api:"+sample.id, "m")
		if err != nil || s == nil || s.FailingSince == nil || s.RuleVersion != health.RuleVersionV2 || s.HealthEvidenceStatus != health.HealthEvidenceValid {
			t.Fatalf("backfill %s %+v %v", sample.id, s, err)
		}
		age := time.Since(*s.FailingSince)
		want := time.Duration(sample.hours) * time.Hour
		if age < want-time.Minute || age > want+time.Minute {
			t.Fatalf("%s age=%s want=%s", sample.id, age, want)
		}
	}
	real, err := r.GetState(ctx, "real-id", "m")
	if err != nil || real.RuleVersion != "" || real.State != health.StateObserving {
		t.Fatal("compatibility row changed", real, err)
	}
	orphan, err := r.GetState(ctx, "sub2api:orphan", "m")
	if err != nil || orphan.State != health.StateDegraded || orphan.CurrentWeight != 75 || orphan.RuleVersion != health.RuleVersionV2 {
		t.Fatal("orphan state not migrated", orphan, err)
	}
	var sourceRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events`).Scan(&sourceRows); err != nil {
		t.Fatal(err)
	}
	t.Logf("source event rows=%d migration elapsed=%s transaction/DDL lock duration upper bound=%s", sourceRows, elapsed, elapsed)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// A hostile caller search_path cannot redirect the shared conversion to another table.
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE connection_health_states (LIKE connection_health_states INCLUDING ALL);INSERT INTO pg_temp.connection_health_states(connection_id,model_name,user_id,admin_account_id,upstream_site_id,upstream_group_name,state) VALUES ('sub2api:temp','m','u','w','s','g','suspect')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT connection_health_convert_states('u','w','legacy')`); err != nil {
		t.Fatal(err)
	}
	// Function sets a fixed schema search_path; the temporary shadow row is unaffected.
	var tempState string
	if err := conn.QueryRow(ctx, `SELECT state FROM pg_temp.connection_health_states WHERE connection_id='sub2api:temp'`).Scan(&tempState); err != nil || tempState != "suspect" {
		t.Fatal("function followed caller shadow schema", tempState, err)
	}
}
