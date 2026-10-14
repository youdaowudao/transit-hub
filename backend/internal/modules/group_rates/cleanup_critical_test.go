package group_rates

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These business expectations are owned by the primary agent. Implementers may
// adapt cleanupApply's call signature, but may not relax any assertions.
func cleanupFixture(t *testing.T) (*pgxpool.Pool, *Service) {
	t.Helper()
	pool := openPostgresTestPool(t)
	repo := prepareGroupRatesRepository(t, pool)
	_, err := pool.Exec(context.Background(), `
	 CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);
	 INSERT INTO admin_accounts VALUES('workspace-a','user-a'),('workspace-b','user-a');
	 ALTER TABLE upstream_sites ADD COLUMN name text NOT NULL DEFAULT 'Site A',
	 ADD COLUMN platform text NOT NULL DEFAULT 'sub2api',
	 ADD COLUMN metrics jsonb NOT NULL DEFAULT '{"groups":[]}',
	 ADD COLUMN last_synced_at bigint,
	 ADD COLUMN status text NOT NULL DEFAULT 'connected';
	 INSERT INTO upstream_sites(id,user_id,admin_account_id,base_url) VALUES
	 ('site-a','user-a','workspace-a','https://same.invalid'),
	 ('site-b','user-a','workspace-a','https://same.invalid'),
	 ('site-c','user-a','workspace-b','https://same.invalid');
	 INSERT INTO my_site_states(user_id,admin_account_id,base_url,email,session,mappings,own_groups)
	 VALUES('user-a','workspace-a','','','{}','[{"ownGroup":"own","upstreamTargets":[{"siteId":"site-a","groupName":"gone"}],"enableAutoPricing":true}]','[]');
	 INSERT INTO real_connections(id,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,upstream_key_id,upstream_key,admin_account_id,admin_account_name,own_group_ids,own_group_names,group_type,status,pricing_mapping_enabled)
	 VALUES('kept-connection','user-a','workspace-a','site-a','gone','gone','kept-key','','kept-account','','["10"]','["own"]','openai','active',true);
	 CREATE TABLE retained_module_evidence(module text PRIMARY KEY, value jsonb);
	 INSERT INTO retained_module_evidence VALUES
	 ('cost','{"today":7,"history":11,"unattributed":2}'),
	 ('health','{"priority":3,"enabled":true,"pending":false}'),
	 ('campaign','{"targets":["gone"],"originalMultiplier":2,"status":"running"}');
	`)
	if err != nil {
		t.Fatal(err)
	}
	return pool, NewService(repo)
}

func cleanupSeed(t *testing.T, pool *pgxpool.Pool, id, site, group, name, category string, rate float64, age int, deleted bool) {
	t.Helper()
	workspace := "workspace-a"
	if site == "site-c" {
		workspace = "workspace-b"
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO group_rate_snapshots(id,user_id,admin_account_id,site_id,site_name,group_id,group_name,platform,type,multiplier,created_at,last_seen_at,deleted) VALUES($1,'user-a',$2,$3,'Site A',$4,$5,'sub2api',$6,$7,$8,$8,$9)`, id, workspace, site, group, name, category, rate, time.Now().Add(time.Duration(age)*time.Hour), deleted)
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupGroup(id, name string, rate float64) SnapshotGroup {
	return SnapshotGroup{ID: id, Name: name, Multiplier: &rate}
}

func cleanupSource(t *testing.T, pool *pgxpool.Pool, groups []SnapshotGroup) (int64, string) {
	t.Helper()
	items := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		items = append(items, map[string]any{"id": g.ID, "name": g.Name, "multiplier": g.Multiplier, "platform": g.Platform, "multiplierMode": g.MultiplierMode})
	}
	raw, err := json.Marshal(map[string]any{"groups": items})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixMilli()
	var version string
	err = pool.QueryRow(context.Background(), `UPDATE upstream_sites SET metrics=$1::jsonb,last_synced_at=$2 WHERE id='site-a' RETURNING xmin::text`, string(raw), stamp).Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	return stamp, version
}

func cleanupApply(ctx context.Context, s *Service, groups []SnapshotGroup, stamp int64, version string, cleanup bool) error {
	// D1 uses the old writer to obtain an actual business RED. After D1 only
	// the approved signature adaptation adds stamp/version/cleanup arguments.
	return s.SaveSiteSnapshot(ctx, "user-a", "workspace-a", "site-a", "Site A", "sub2api", groups, stamp, version, cleanup)
}

func cleanupIDs(t *testing.T, pool *pgxpool.Pool, site string) string {
	t.Helper()
	var ids string
	err := pool.QueryRow(context.Background(), `SELECT COALESCE(string_agg(id,',' ORDER BY id),'') FROM group_rate_snapshots WHERE site_id=$1`, site).Scan(&ids)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func cleanupRetained(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var value string
	// Real persisted connection/mapping values are compared, never credentials.
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object('connections',(SELECT jsonb_agg(jsonb_build_object('id',id,'site',upstream_site_id,'group',upstream_group_id,'keyId',upstream_key_id,'account',admin_account_id,'status',status,'groups',own_group_ids,'pricing',pricing_mapping_enabled)) FROM real_connections),'mappings',(SELECT jsonb_agg(mappings) FROM my_site_states),'independent',(SELECT jsonb_object_agg(module,value) FROM retained_module_evidence))::text`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCleanupCriticalRemovesAllHistoryAndPreservesOtherObjectsPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "gone-old", "site-a", "gone", "gone", "openai", 1, -3, false)
	cleanupSeed(t, pool, "gone-latest", "site-a", "gone", "gone", "openai", 2, -2, true)
	cleanupSeed(t, pool, "valid-old", "site-a", "live", "live", "custom", 1, -3, false)
	cleanupSeed(t, pool, "valid-latest", "site-a", "live", "live", "custom", 2, -1, false)
	cleanupSeed(t, pool, "other-site", "site-b", "gone", "gone", "openai", 1, -1, false)
	cleanupSeed(t, pool, "other-workspace", "site-c", "gone", "gone", "openai", 1, -1, false)
	before := cleanupRetained(t, pool)
	groups := []SnapshotGroup{cleanupGroup("live", "live", 2)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err != nil {
		t.Fatal(err)
	}
	if got := cleanupIDs(t, pool, "site-a"); got != "valid-latest,valid-old" {
		t.Fatalf("all disappeared history must be physically removed, got %s", got)
	}
	if cleanupIDs(t, pool, "site-b") != "other-site" || cleanupIDs(t, pool, "site-c") != "other-workspace" {
		t.Fatal("cleanup escaped actual site/workspace")
	}
	if got := cleanupRetained(t, pool); got != before {
		t.Fatal("cleanup changed connection/mapping/independent records")
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "gone", "")
	if err != nil || len(history) != 0 {
		t.Fatalf("cleared history reappeared: rows=%d err=%v", len(history), err)
	}
	valid, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "live", "")
	if err != nil || len(valid) != 2 || valid[0].Delta == nil || *valid[0].Delta != 1 {
		t.Fatalf("valid history/change lost: %#v %v", valid, err)
	}
}

func TestCleanupCriticalBadInputPreservesEntireSitePostgres(t *testing.T) {
	for _, bad := range []SnapshotGroup{{ID: "bad", Name: "bad"}, cleanupGroup("bad", "", 1), cleanupGroup("bad", "bad", -1)} {
		t.Run(fmt.Sprintf("%s-%v", bad.Name, bad.Multiplier == nil), func(t *testing.T) {
			pool, s := cleanupFixture(t)
			cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 2, -1, false)
			groups := []SnapshotGroup{cleanupGroup("live", "live", 1), bad}
			stamp, version := cleanupSource(t, pool, groups)
			if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err == nil {
				t.Fatal("damaged group must reject entire writer input")
			}
			if cleanupIDs(t, pool, "site-a") != "old" {
				t.Fatal("damaged input wrote or deleted history")
			}
		})
	}
}

func TestCleanupCriticalEmptyAndPartialSiteBoundariesPostgres(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			pool, s := cleanupFixture(t)
			cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 2, -1, false)
			cleanupSeed(t, pool, "other", "site-b", "old", "old", "", 2, -1, false)
			groups := []SnapshotGroup{}
			stamp, version := cleanupSource(t, pool, groups)
			if err := cleanupApply(context.Background(), s, groups, stamp, version, allow); err != nil {
				t.Fatal(err)
			}
			want := "old"
			if allow {
				want = ""
			}
			if got := cleanupIDs(t, pool, "site-a"); got != want {
				t.Fatalf("partial/complete empty history=%q want=%q", got, want)
			}
			if cleanupIDs(t, pool, "site-b") != "other" {
				t.Fatal("empty result deleted same-URL site")
			}
			if !allow {
				var deleted bool
				if err := pool.QueryRow(context.Background(), `SELECT deleted FROM group_rate_snapshots WHERE id='old'`).Scan(&deleted); err != nil || deleted {
					t.Fatal("partial empty result hid an unreturned group")
				}
			}
		})
	}
}

func TestCleanupCriticalSameIDRenameAndClearedCategoryPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "original", "site-a", "live", "old-name", "openai", 1, -3, false)
	cleanupSeed(t, pool, "latest", "site-a", "live", "new-name", "", 2, -1, false)
	groups := []SnapshotGroup{cleanupGroup("live", "new-name", 3)}
	category := "grok"
	groups[0].Platform = &category
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "live", "")
	if err != nil || len(history) != 3 {
		t.Fatalf("renamed ID lost history: %v %d", err, len(history))
	}
	if history[0].Type != "" {
		t.Fatalf("cleared user category resurrected as %q", history[0].Type)
	}
}

func TestCleanupCriticalSavedVersionRejectsSettingsOnlyChangePostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 2, -1, false)
	groups := []SnapshotGroup{cleanupGroup("live", "live", 1)}
	stamp, version := cleanupSource(t, pool, groups)
	if _, err := pool.Exec(context.Background(), `UPDATE upstream_sites SET recharge_rate=7 WHERE id='site-a'`); err != nil {
		t.Fatal(err)
	}
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err == nil {
		t.Fatal("old saved row version must reject settings-only change")
	}
	if cleanupIDs(t, pool, "site-a") != "old" {
		t.Fatal("obsolete save source changed history")
	}
}

func TestCleanupCriticalMidTransactionFailureRollsBackAllChangesPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "gone", "site-a", "gone", "gone", "", 2, -1, false)
	cleanupSeed(t, pool, "live", "site-a", "live", "live", "", 2, -1, false)
	_, err := pool.Exec(context.Background(), `CREATE FUNCTION reject_cleanup_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected insert failure'; END $$; CREATE TRIGGER reject_cleanup_insert BEFORE INSERT ON group_rate_snapshots FOR EACH ROW EXECUTE FUNCTION reject_cleanup_insert();`)
	if err != nil {
		t.Fatal(err)
	}
	groups := []SnapshotGroup{cleanupGroup("live", "live", 3)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err == nil {
		t.Fatal("injected insert failure not reported")
	}
	var deleted bool
	if err := pool.QueryRow(context.Background(), `SELECT deleted FROM group_rate_snapshots WHERE id='gone'`).Scan(&deleted); err != nil || deleted {
		t.Fatalf("failed transaction changed removed group's row: deleted=%v err=%v", deleted, err)
	}
	if cleanupIDs(t, pool, "site-a") != "gone,live" {
		t.Fatal("partial snapshot transaction escaped rollback")
	}
}

func TestCleanupCriticalDeletedClientsAndHistoryAreEmptyPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "deleted-old", "site-a", "gone", "gone", "legacy-only", 1, -2, false)
	cleanupSeed(t, pool, "deleted-new", "site-a", "gone", "gone", "legacy-only", 2, -1, true)
	for _, status := range []string{"", "nonsense", "deleted", "all"} {
		result, err := s.List(context.Background(), "user-a", "workspace-a", ListQuery{Page: 1, PageSize: 30, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 0 || len(result.Items) != 0 || result.StatusCounts.Deleted != 0 || len(result.Types) != 0 {
			t.Fatalf("legacy deleted data leaked for status %q: %#v", status, result)
		}
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "gone", "")
	if err != nil || len(history) != 0 {
		t.Fatal("latest deleted identity still exposes earlier history")
	}
	if err := s.UpdateType(context.Background(), "user-a", "workspace-a", GroupRef{SiteID: "site-a", GroupName: "absent"}, "x"); err == nil {
		t.Fatal("editing absent record reported success")
	}
}

func TestCleanupCriticalAutoOnlyClearsConfirmedReturnedIdentityPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "auto-old", "site-a", "auto", "auto", "grok", 2, -2, false)
	cleanupSeed(t, pool, "auto-new", "site-a", "auto", "auto", "grok", 3, -1, false)
	cleanupSeed(t, pool, "not-returned", "site-a", "other", "other", "", 4, -1, false)
	groups := []SnapshotGroup{{ID: "auto", Name: "auto", MultiplierMode: "auto"}}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, false); err != nil {
		t.Fatal(err)
	}
	if got := cleanupIDs(t, pool, "site-a"); got != "not-returned" {
		t.Fatalf("confirmed auto should only clear its fixed history: %s", got)
	}
	groups = []SnapshotGroup{cleanupGroup("auto", "auto", 1)}
	stamp, version = cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, false); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "auto", "")
	if err != nil || len(history) != 1 || history[0].Delta != nil {
		t.Fatalf("fixed after auto must start fresh without fabricated decline: rows=%d err=%v", len(history), err)
	}
}

func TestCleanupCriticalSameMillisecondSavedVersionsKeepNewestPricePostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	groups := []SnapshotGroup{cleanupGroup("live", "live", 3)}
	stamp, version := cleanupSource(t, pool, groups)
	cleanupSeed(t, pool, "z-previous", "site-a", "live", "live", "", 2, -1, false)
	if _, err := pool.Exec(context.Background(), `UPDATE group_rate_snapshots SET created_at=$1,last_seen_at=$1 WHERE id='z-previous'`, time.UnixMilli(stamp)); err != nil {
		t.Fatal(err)
	}
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "live", "")
	if err != nil || len(history) != 2 || history[0].Multiplier != 3 || history[0].Delta == nil || *history[0].Delta != 1 {
		t.Fatalf("same-ms source time reordered newest saved price: %#v %v", history, err)
	}
}

func TestCleanupCriticalClearedCategorySurvivesSchemaStartupPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "original-name", "site-a", "live", "original-name", "openai", 1, -3, false)
	cleanupSeed(t, pool, "renamed-and-cleared", "site-a", "live", "renamed", "", 2, -1, false)
	groups := []SnapshotGroup{cleanupGroup("live", "original-name", 3)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), s, groups, stamp, version, true); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(context.Background(), "user-a", "workspace-a", "site-a", "live", "")
	if err != nil || len(history) != 3 || history[0].Type != "" {
		t.Fatalf("startup inferred old nonempty history over an explicit cleared category: %#v %v", history, err)
	}
	if history[2].Type != "openai" {
		t.Fatal("startup erased historical nonempty category")
	}
}

func TestCleanupCriticalResolvedLegacyIdentityCollisionRollsBackPostgres(t *testing.T) {
	pool, service := cleanupFixture(t)
	cleanupSeed(t, pool, "original-history", "site-a", "1", "old-name", "user-category", 3, -1, false)
	before := cleanupRetained(t, pool)
	groups := []SnapshotGroup{cleanupGroup("1", "renamed-group", 2), cleanupGroup("", "old-name", 9)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(context.Background(), service, groups, stamp, version, true); err == nil {
		t.Fatal("resolved duplicate identity must reject the whole site instead of borrowing old identity")
	}
	if ids := cleanupIDs(t, pool, "site-a"); ids != "original-history" {
		t.Fatalf("collision changed original history or created extra identity snapshots: %s", ids)
	}
	var category string
	var rate float64
	if err := pool.QueryRow(context.Background(), `SELECT type,multiplier FROM group_rate_snapshots WHERE id='original-history'`).Scan(&category, &rate); err != nil || category != "user-category" || rate != 3 {
		t.Fatalf("collision changed original category or price: %s %v %v", category, rate, err)
	}
	if cleanupRetained(t, pool) != before {
		t.Fatal("collision changed unrelated retained objects")
	}
}
