package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/credentialstore"
	"transithub/backend/internal/shared/secretbox"
)

// These fixtures opt in through TEST_* only. Every PG connection uses a fresh
// search_path; every Redis command, including Lua KEYS, is namespaced before IO.
// Fixture errors deliberately report the step, never connection strings/values.
func c3Require(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("C3 fixture %s failed (%T)", step, err)
	}
}

func c3Box(t *testing.T, value byte) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{value}, 32))
	c3Require(t, "key", err)
	return box
}

func c3Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required for isolated C3 PostgreSQL acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, raw)
	c3Require(t, "connect PostgreSQL", err)
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("c3_credentials_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
	c3Require(t, "create isolated schema", err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error("C3 isolated schema cleanup failed")
			return
		}
		var remaining int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname=$1`, schema).Scan(&remaining); err != nil || remaining != 0 {
			t.Error("C3 isolated schema remains after cleanup")
		} else {
			t.Log("C3 temporary PostgreSQL schemas: 0 -> 1 -> 0; business schemas untouched")
		}
	})
	cfg, err := pgxpool.ParseConfig(raw)
	c3Require(t, "parse PostgreSQL config", err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	c3Require(t, "open isolated schema", err)
	t.Cleanup(pool.Close)
	for _, statement := range []string{
		`CREATE TABLE users (id text PRIMARY KEY)`,
		`CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`,
		`INSERT INTO users VALUES ('c3-user')`,
		`INSERT INTO admin_accounts VALUES ('c3-workspace','c3-user')`,
	} {
		_, err := pool.Exec(ctx, statement)
		c3Require(t, "prepare isolated ownership", err)
	}
	c3Require(t, "my sites schema", my_sites.NewRepository(pool).EnsureSchema(ctx))
	c3Require(t, "upstream schema", upstream.NewRepository(pool).EnsureSchema(ctx))
	c3Require(t, "settings schema", settings.NewRepository(pool).EnsureSchema(ctx))
	return pool
}

type c3RedisNamespace struct {
	prefix    string
	mu        sync.Mutex
	keys      map[string]struct{}
	beforeCAS func()
}

func (h *c3RedisNamespace) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return next(ctx, network, address)
	}
}

func (h *c3RedisNamespace) rewrite(cmd redis.Cmder) {
	args := cmd.Args()
	var indexes []int
	switch cmd.Name() {
	case "get", "set", "pttl", "ttl", "pexpire", "expire", "sadd", "srem", "smembers", "sscan", "scard":
		indexes = []int{1}
	case "del", "exists":
		for i := 1; i < len(args); i++ {
			indexes = append(indexes, i)
		}
	case "eval", "evalsha":
		// The C3 migration script has exactly one KEYS argument.
		indexes = []int{3}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, index := range indexes {
		key := fmt.Sprint(args[index])
		if !strings.HasPrefix(key, h.prefix) {
			key = h.prefix + key
		}
		args[index] = key
		h.keys[key] = struct{}{}
	}
}

func (h *c3RedisNamespace) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.rewrite(cmd)
		if cmd.Name() == "eval" || cmd.Name() == "evalsha" {
			h.mu.Lock()
			before := h.beforeCAS
			h.beforeCAS = nil
			h.mu.Unlock()
			if before != nil {
				before()
			}
		}
		return next(ctx, cmd)
	}
}

func (h *c3RedisNamespace) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			h.rewrite(cmd)
		}
		return next(ctx, cmds)
	}
}

func c3Redis(t *testing.T) (*redis.Client, *c3RedisNamespace) {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL is required for isolated C3 Redis acceptance")
	}
	opts, err := redis.ParseURL(raw)
	c3Require(t, "parse Redis config", err)
	opts.PoolSize = 3
	client := redis.NewClient(opts)
	hook := &c3RedisNamespace{prefix: fmt.Sprintf("c3-credentials-%d:", time.Now().UnixNano()), keys: map[string]struct{}{}}
	client.AddHook(hook)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hook.mu.Lock()
		keys := make([]string, 0, len(hook.keys))
		for key := range hook.keys {
			keys = append(keys, key)
		}
		hook.mu.Unlock()
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Error("C3 exact-key Redis cleanup failed")
			}
			if remaining, err := client.Exists(ctx, keys...).Result(); err != nil || remaining != 0 {
				t.Error("C3 temporary Redis keys remain")
			}
		}
		t.Logf("C3 temporary Redis keys: 0 -> %d tracked keys -> 0; no global scan/flush", len(keys))
		if err := client.Close(); err != nil {
			t.Error("C3 Redis client cleanup failed")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c3Require(t, "Redis ping", client.Ping(ctx).Err())
	return client, hook
}

func c3Session() upstream.Session {
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://c3-fixture.invalid", AccessToken: "c3-fake-access-token", AdminAPIKey: "c3-fake-admin-key", RefreshToken: "c3-fake-refresh-token"}
}

func c3Site() upstream.Site {
	session := c3Session()
	return upstream.Site{ID: "c3-site", UserID: "c3-user", AdminAccountID: "c3-workspace", Name: "C3 fixture", BaseURL: session.BaseURL, Platform: upstream.PlatformSub2API, RequestedPlatform: upstream.PlatformSub2API, Status: upstream.StatusConnected, RechargeRate: 1, Session: &session}
}

func c3Connection(id string) my_sites.RealConnection {
	return my_sites.RealConnection{ID: id, UserID: "c3-user", WorkspaceAdminAccountID: "c3-workspace", UpstreamSiteID: "c3-site", UpstreamGroupID: id, UpstreamGroupName: id, UpstreamKeyID: id, UpstreamKey: "c3-fake-connection-key", AdminAccountID: id, AdminAccountName: "C3 fixture", OwnGroupIDs: []string{"own-id"}, OwnGroupNames: []string{"own-group"}, GroupType: "openai", ProvisioningMode: my_sites.ProvisioningModeManaged, Status: my_sites.ConnectionStatusActive, UpstreamPlatform: "sub2api", AdminPlatform: "sub2api", PricingMappingEnabled: true, OperationID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

type c3PGCredential struct {
	table, column, predicate, aad string
	json                          bool
	write                         func(*secretbox.Box) error
	read                          func(*secretbox.Box) error
}

func c3PGCredentials(t *testing.T, pool *pgxpool.Pool) []c3PGCredential {
	t.Helper()
	ctx := t.Context()
	state := my_sites.State{UserID: "c3-user", AdminAccountID: "c3-workspace", BaseURL: c3Session().BaseURL, Email: "c3-fixture@example.invalid", Session: c3Session(), Mappings: []my_sites.GroupMapping{{OwnGroup: "own-group"}}, OwnGroups: []my_sites.GroupOption{}}
	return []c3PGCredential{
		{"my_site_states", "session", `user_id='c3-user' AND admin_account_id='c3-workspace'`, secretbox.AAD("my_site_states.session", "c3-user", "c3-workspace"), true,
			func(box *secretbox.Box) error { return my_sites.NewRepository(pool, box).Save(ctx, state) },
			func(box *secretbox.Box) error {
				got, err := my_sites.NewRepository(pool, box).Get(ctx, "c3-user", "c3-workspace")
				if err == nil && (got == nil || !reflect.DeepEqual(got.Session, c3Session())) {
					return errors.New("my-site fixture session mismatch")
				}
				return err
			}},
		{"upstream_sites", "session", `id='c3-site'`, secretbox.AAD("upstream_sites.session", "c3-user", "c3-workspace", "c3-site"), true,
			func(box *secretbox.Box) error { return upstream.NewRepository(pool, box).SaveSite(ctx, c3Site()) },
			func(box *secretbox.Box) error {
				got, err := upstream.NewRepository(pool, box).ListSitesForUser(ctx, "c3-user")
				if err == nil && (len(got) != 1 || got[0].Session == nil || !reflect.DeepEqual(*got[0].Session, c3Session())) {
					return errors.New("upstream fixture session mismatch")
				}
				return err
			}},
		{"real_connections", "upstream_key", `id='c3-connection'`, secretbox.AAD("real_connections.upstream_key", "c3-user", "c3-workspace", "c3-connection"), false,
			func(box *secretbox.Box) error {
				_, err := pool.Exec(ctx, `DELETE FROM real_connections WHERE id='c3-connection'`)
				if err != nil {
					return err
				}
				return my_sites.NewRepository(pool, box).SaveRealConnection(ctx, c3Connection("c3-connection"))
			},
			func(box *secretbox.Box) error {
				got, err := my_sites.NewRepository(pool, box).GetRealConnection(ctx, "c3-connection", "c3-user", "c3-workspace")
				if err == nil && (got == nil || got.UpstreamKey != c3Connection("c3-connection").UpstreamKey) {
					return errors.New("connection fixture key mismatch")
				}
				return err
			}},
		{"notification_channel_settings", "settings", `user_id='c3-user' AND admin_account_id='c3-workspace'`, secretbox.AAD("notification_channel_settings.settings", "c3-user", "c3-workspace"), true,
			func(box *secretbox.Box) error {
				return settings.NewRepository(pool, box).SaveNotificationChannels(ctx, "c3-user", "c3-workspace", settings.NotificationChannelSettings{Telegram: []settings.TelegramChannelSettings{{ID: "c3-bot", Enabled: true, BotToken: "c3-fake-bot-token", ChatID: "c3-chat"}}})
			},
			func(box *secretbox.Box) error {
				got, err := settings.NewRepository(pool, box).GetNotificationChannels(ctx, "c3-user", "c3-workspace")
				if err == nil && (len(got.Telegram) != 1 || got.Telegram[0].BotToken != "c3-fake-bot-token") {
					return errors.New("notification fixture token mismatch")
				}
				return err
			}},
	}
}

func c3Raw(t *testing.T, pool *pgxpool.Pool, item c3PGCredential) string {
	t.Helper()
	var raw string
	c3Require(t, "read stored representation", pool.QueryRow(t.Context(), `SELECT `+item.column+`::text FROM `+item.table+` WHERE `+item.predicate).Scan(&raw))
	return raw
}

func c3SetRaw(t *testing.T, pool *pgxpool.Pool, item c3PGCredential, raw string) {
	t.Helper()
	cast := ""
	if item.json {
		cast = "::jsonb"
	}
	_, err := pool.Exec(t.Context(), `UPDATE `+item.table+` SET `+item.column+`=$1`+cast+` WHERE `+item.predicate, raw)
	c3Require(t, "replace isolated representation", err)
}

func c3StoredIsSealed(item c3PGCredential, raw string) bool {
	if item.json {
		return secretbox.IsSealedJSON([]byte(raw))
	}
	return secretbox.IsSealed(raw)
}

func c3AssertPGSealed(t *testing.T, pool *pgxpool.Pool, item c3PGCredential) {
	t.Helper()
	if !c3StoredIsSealed(item, c3Raw(t, pool, item)) {
		t.Fatalf("%s original write path restored plaintext", item.table)
	}
}

func c3BusinessRows(t *testing.T, pool *pgxpool.Pool, item c3PGCredential) string {
	t.Helper()
	var rows string
	c3Require(t, "snapshot noncredential fields", pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(stored_row)-$1 ORDER BY (to_jsonb(stored_row)-$1)::text),'[]'::jsonb)::text FROM `+item.table+` stored_row`, item.column).Scan(&rows))
	return rows
}

func TestAbsorbC3PostgresOriginalCredentialPaths(t *testing.T) {
	pool := c3Postgres(t)
	box, wrong := c3Box(t, 1), c3Box(t, 2)
	for _, item := range c3PGCredentials(t, pool) {
		t.Run(item.table, func(t *testing.T) {
			c3Require(t, "legacy save", item.write(nil))
			legacy := c3Raw(t, pool, item)
			if c3StoredIsSealed(item, legacy) {
				t.Fatal("legacy mode unexpectedly changed storage format")
			}
			c3Require(t, "legacy read without key", item.read(nil))
			c3Require(t, "legacy read with key", item.read(box))
			c3Require(t, "encrypted save", item.write(box))
			stored := c3Raw(t, pool, item)
			if !c3StoredIsSealed(item, stored) || strings.Contains(stored, "c3-fake-") {
				t.Fatal("credential was not encrypted in PostgreSQL")
			}
			c3Require(t, "encrypted read", item.read(box))
			for _, candidate := range []struct {
				name string
				box  *secretbox.Box
				want error
			}{{"missing key", nil, secretbox.ErrNoKey}, {"wrong key", wrong, secretbox.ErrUndecryptable}} {
				if err := item.read(candidate.box); !errors.Is(err, candidate.want) {
					t.Fatalf("%s did not reject unreadable fixture", candidate.name)
				}
				if c3Raw(t, pool, item) != stored {
					t.Fatal("failed read modified credential")
				}
			}
			var plain []byte
			var err error
			if item.json {
				plain, err = box.OpenJSON([]byte(stored), item.aad)
			} else {
				plain, err = box.Open(stored, item.aad)
			}
			c3Require(t, "open correct fixture", err)
			var moved string
			if item.json {
				raw, err := box.SealJSON(plain, secretbox.AAD("different-location", "c3-other-user", "c3-other-workspace", "c3-other-object"))
				c3Require(t, "seal wrong AAD fixture", err)
				moved = string(raw)
			} else {
				moved, err = box.Seal(plain, secretbox.AAD("different-location", "c3-other-user", "c3-other-workspace", "c3-other-object"))
				c3Require(t, "seal wrong AAD fixture", err)
			}
			c3SetRaw(t, pool, item, moved)
			before := c3Raw(t, pool, item)
			if err := item.read(box); !errors.Is(err, secretbox.ErrUndecryptable) {
				t.Fatal("different storage identity did not reject fixture")
			}
			if c3Raw(t, pool, item) != before {
				t.Fatal("AAD failure modified credential")
			}
			c3SetRaw(t, pool, item, stored)
		})
	}

	repo := my_sites.NewRepository(pool, box)
	updated, err := repo.MutateState(t.Context(), "c3-user", "c3-workspace", func(state *my_sites.State) error { state.Email = "changed@example.invalid"; return nil })
	c3Require(t, "locked state mutation", err)
	if updated == nil || updated.Email != "changed@example.invalid" || !reflect.DeepEqual(updated.Session, c3Session()) {
		t.Fatal("state mutation lost encrypted session")
	}
	c3AssertPGSealed(t, pool, c3PGCredentials(t, pool)[0])
	c3Require(t, "mapping transaction", repo.SaveRealConnectionWithPricingMapping(t.Context(), c3Connection("c3-mapping")))
	var mappedKey string
	c3Require(t, "read mapping transaction ciphertext", pool.QueryRow(t.Context(), `SELECT upstream_key FROM real_connections WHERE id='c3-mapping'`).Scan(&mappedKey))
	if !secretbox.IsSealed(mappedKey) {
		t.Fatal("mapping transaction saved plaintext connection key")
	}
	state, err := repo.Get(t.Context(), "c3-user", "c3-workspace")
	c3Require(t, "mapping state read", err)
	if state == nil || !reflect.DeepEqual(state.Session, c3Session()) || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 {
		t.Fatal("mapping transaction lost session or mapping")
	}
	for _, item := range c3PGCredentials(t, pool) {
		if !c3StoredIsSealed(item, c3Raw(t, pool, item)) {
			t.Fatal("transaction restored plaintext")
		}
	}
	listed, err := repo.ListRealConnections(t.Context(), "c3-user", "c3-workspace")
	c3Require(t, "connection list", err)
	if len(listed) != 2 {
		t.Fatal("connection list lost fixture")
	}
	for _, conn := range listed {
		if conn.UpstreamKey != "c3-fake-connection-key" {
			t.Fatal("connection list did not decrypt")
		}
	}
	conn, err := repo.GetRealConnectionByOperationID(t.Context(), "c3-user", "c3-workspace", "c3-mapping")
	c3Require(t, "operation read", err)
	if conn == nil || conn.UpstreamKey != "c3-fake-connection-key" {
		t.Fatal("operation read did not decrypt")
	}

	up := upstream.NewRepository(pool, box)
	site := c3Site()
	version, err := up.SaveSiteWithSnapshotVersion(t.Context(), site)
	c3Require(t, "site snapshot save", err)
	if version == "" {
		t.Fatal("snapshot version missing")
	}
	c3AssertPGSealed(t, pool, c3PGCredentials(t, pool)[1])
	candidate := site
	candidate.Remark = "conditional update"
	saved, err := up.SaveExistingSiteIfUnchanged(t.Context(), site, candidate)
	c3Require(t, "conditional encrypted site update", err)
	if !saved {
		t.Fatal("conditional site save rejected unchanged fixture")
	}
	c3AssertPGSealed(t, pool, c3PGCredentials(t, pool)[1])
	guarded := candidate
	guarded.Remark = "guarded update"
	actual, _, saved, accepted, err := up.SaveExistingSiteWithGroupGuard(t.Context(), candidate, guarded, candidate)
	c3Require(t, "group guarded encrypted site update", err)
	if !saved || !accepted || actual.Session == nil || !reflect.DeepEqual(*actual.Session, c3Session()) {
		t.Fatal("group guard lost encrypted session")
	}
	c3AssertPGSealed(t, pool, c3PGCredentials(t, pool)[1])
	rotated := actual
	rotated.Session = &upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "c3-rotated-fake-token"}
	c3Require(t, "rotate site session", up.SaveSite(t.Context(), rotated))
	before := c3Raw(t, pool, c3PGCredentials(t, pool)[1])
	if saved, err := up.SaveExistingSiteIfUnchanged(t.Context(), actual, guarded); err != nil || saved {
		t.Fatal("stale configuration overwrote rotated session")
	}
	if _, _, saved, _, err := up.SaveExistingSiteWithGroupGuard(t.Context(), actual, guarded, actual); err != nil || saved {
		t.Fatal("stale group guard overwrote rotated session")
	}
	if c3Raw(t, pool, c3PGCredentials(t, pool)[1]) != before {
		t.Fatal("stale save changed encrypted row")
	}
}

type c3ConcurrentPostgres struct {
	credentialstore.Postgres
	before func(context.Context, string) error
}

func (db *c3ConcurrentPostgres) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if db.before != nil && strings.HasPrefix(sql, "UPDATE ") {
		before := db.before
		db.before = nil
		if err := before(ctx, sql); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	return db.Postgres.Exec(ctx, sql, args...)
}

func TestAbsorbC3PostgresMigrationPreservesConcurrentCredentials(t *testing.T) {
	for _, table := range []string{"my_site_states", "upstream_sites", "real_connections", "notification_channel_settings"} {
		t.Run(table, func(t *testing.T) {
			pool := c3Postgres(t)
			var target c3PGCredential
			for _, item := range c3PGCredentials(t, pool) {
				if item.table == table {
					target = item
				}
			}
			c3Require(t, "legacy migration fixture", target.write(nil))
			newer := "c3-concurrent-key"
			if target.json {
				newer = `{"concurrent":"c3-newer"}`
			}
			db := &c3ConcurrentPostgres{Postgres: pool, before: func(ctx context.Context, _ string) error { c3SetRaw(t, pool, target, newer); return nil }}
			if err := credentialstore.EncryptPostgres(t.Context(), db, c3Box(t, 1)); err == nil {
				t.Fatal("migration did not report concurrent modification")
			}
			if c3Raw(t, pool, target) != newer && !target.json {
				t.Fatal("migration overwrote newer credential")
			}
			if target.json {
				var got map[string]string
				c3Require(t, "read newer JSON", json.Unmarshal([]byte(c3Raw(t, pool, target)), &got))
				if got["concurrent"] != "c3-newer" {
					t.Fatal("migration overwrote newer JSON")
				}
			}
		})
	}
}

func TestAbsorbC3PostgresMigrationBatchesAndRetry(t *testing.T) {
	pool := c3Postgres(t)
	box := c3Box(t, 1)
	for _, item := range c3PGCredentials(t, pool) {
		c3Require(t, "legacy migration fixture", item.write(nil))
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO real_connections SELECT 'c3-batch-'||g,user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,upstream_key_id,upstream_key,admin_account_id,admin_account_name,own_group_ids,group_type,created_at,own_group_names,provisioning_mode,status,upstream_platform,admin_platform,pricing_mapping_enabled,'' FROM real_connections CROSS JOIN generate_series(1,101) g WHERE id='c3-connection'`)
	c3Require(t, "prepare second migration batch", err)
	businessBefore := make(map[string]string)
	for _, item := range c3PGCredentials(t, pool) {
		businessBefore[item.table] = c3BusinessRows(t, pool, item)
	}
	c3Require(t, "verify legacy fixture", credentialstore.VerifyPostgres(t.Context(), pool, nil))
	c3Require(t, "encrypt real PG", credentialstore.EncryptPostgres(t.Context(), pool, box))
	for _, item := range c3PGCredentials(t, pool) {
		c3Require(t, "read migrated credential", item.read(box))
		if !c3StoredIsSealed(item, c3Raw(t, pool, item)) {
			t.Fatal("migration did not encrypt target")
		}
		if c3BusinessRows(t, pool, item) != businessBefore[item.table] {
			t.Fatal("credential migration changed noncredential fields or row membership")
		}
	}
	var encrypted, all int
	c3Require(t, "count batch", pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE upstream_key LIKE 'enc:v1:%'),count(*) FROM real_connections`).Scan(&encrypted, &all))
	if encrypted != 102 || all != 102 {
		t.Fatal("migration lost rows or skipped second batch")
	}
	before := c3Raw(t, pool, c3PGCredentials(t, pool)[0])
	c3Require(t, "retry completed migration", credentialstore.EncryptPostgres(t.Context(), pool, box))
	if c3Raw(t, pool, c3PGCredentials(t, pool)[0]) != before {
		t.Fatal("retry rewrote already encrypted credential")
	}
	c3Require(t, "verify migrated fixture", credentialstore.VerifyPostgres(t.Context(), pool, box))
}

func c3Admin() dashboard.AdminSession {
	return dashboard.AdminSession{Platform: "sub2api", Identity: "C3 fixture", Session: c3Session()}
}

func c3TTL(t *testing.T, client *redis.Client, key string) time.Duration {
	t.Helper()
	ttl, err := client.PTTL(t.Context(), key).Result()
	c3Require(t, "read TTL", err)
	return ttl
}

func TestAbsorbC3RedisOriginalCredentialPathsAndTTL(t *testing.T) {
	client, _ := c3Redis(t)
	box, wrong := c3Box(t, 1), c3Box(t, 2)
	ctx := t.Context()
	adminKey, siteKey := "dashboard:admin:session:c3-user:c3-workspace", "upstream:site:c3-site"
	for _, encrypted := range []bool{false, true} {
		var writer *secretbox.Box
		if encrypted {
			writer = box
		}
		c3Require(t, "admin save", dashboard.NewRepository(client, writer).Save(ctx, "c3-user", "c3-workspace", c3Admin()))
		c3Require(t, "site cache set", upstream.NewRedisSiteCache(client, writer).Set(ctx, &[]upstream.Site{c3Site()}[0]))
		for _, key := range []string{adminKey, siteKey} {
			raw, err := client.Get(ctx, key).Result()
			c3Require(t, "read Redis representation", err)
			if secretbox.IsSealed(raw) != encrypted {
				t.Fatal("Redis stored representation disagrees with encryption mode")
			}
			if ttl := c3TTL(t, client, key); ttl != -1 {
				t.Fatalf("original persistent Redis policy changed: TTL=%s", ttl)
			}
		}
		admin, err := dashboard.NewRepository(client, box).Get(ctx, "c3-user", "c3-workspace")
		c3Require(t, "admin get", err)
		if admin == nil || !reflect.DeepEqual(admin.Session, c3Session()) {
			t.Fatal("admin session did not round trip")
		}
		site, err := upstream.NewRedisSiteCache(client, box).Get(ctx, "c3-site")
		c3Require(t, "site get", err)
		if site == nil || site.Session == nil || !reflect.DeepEqual(*site.Session, c3Session()) {
			t.Fatal("site payload did not round trip")
		}
		sites, err := upstream.NewRedisSiteCache(client, box).ListByUser(ctx, "c3-user")
		c3Require(t, "site list", err)
		if len(sites) != 1 || sites[0].Session == nil || !reflect.DeepEqual(*sites[0].Session, c3Session()) {
			t.Fatal("site list did not decode full payload")
		}
		refs, err := dashboard.NewRepository(client, box).ActiveSessions(ctx)
		c3Require(t, "active sessions", err)
		if len(refs) != 1 || refs[0].UserID != "c3-user" || refs[0].AdminAccountID != "c3-workspace" {
			t.Fatal("admin index changed")
		}
	}
	for _, candidate := range []struct {
		box  *secretbox.Box
		want error
	}{{nil, secretbox.ErrNoKey}, {wrong, secretbox.ErrUndecryptable}} {
		if _, err := dashboard.NewRepository(client, candidate.box).Get(ctx, "c3-user", "c3-workspace"); !errors.Is(err, candidate.want) {
			t.Fatal("admin unreadable fixture accepted")
		}
		if _, err := upstream.NewRedisSiteCache(client, candidate.box).Get(ctx, "c3-site"); !errors.Is(err, candidate.want) {
			t.Fatal("site unreadable fixture accepted")
		}
		if _, err := upstream.NewRedisSiteCache(client, candidate.box).ListByUser(ctx, "c3-user"); !errors.Is(err, candidate.want) {
			t.Fatal("site list unreadable fixture accepted")
		}
	}
	for _, key := range []string{adminKey, siteKey} {
		raw, err := client.Get(ctx, key).Result()
		c3Require(t, "capture original Redis fixture", err)
		var aad string
		if key == adminKey {
			aad = secretbox.AAD(key, "c3-user", "c3-workspace")
		} else {
			aad = secretbox.AAD(key, "c3-site")
		}
		plain, err := box.Open(raw, aad)
		c3Require(t, "open Redis fixture", err)
		moved, err := box.Seal(plain, secretbox.AAD("c3-other-cache-key", "c3-other-object"))
		c3Require(t, "seal moved Redis fixture", err)
		c3Require(t, "store moved Redis fixture", client.Set(ctx, key, moved, time.Minute).Err())
		beforeTTL := c3TTL(t, client, key)
		if key == adminKey {
			_, err = dashboard.NewRepository(client, box).Get(ctx, "c3-user", "c3-workspace")
		} else {
			_, err = upstream.NewRedisSiteCache(client, box).Get(ctx, "c3-site")
		}
		if !errors.Is(err, secretbox.ErrUndecryptable) {
			t.Fatal("moved Redis credential accepted")
		}
		after, err := client.Get(ctx, key).Result()
		c3Require(t, "read rejected Redis fixture", err)
		if after != moved || c3TTL(t, client, key) > beforeTTL || c3TTL(t, client, key) < beforeTTL-time.Second {
			t.Fatal("rejected Redis read changed value or TTL")
		}
		c3Require(t, "restore Redis fixture", client.Set(ctx, key, raw, 0).Err())
	}
}

func TestAbsorbC3RedisMigrationKeepsTTLAndConcurrentUpdates(t *testing.T) {
	client, hook := c3Redis(t)
	ctx, box := t.Context(), c3Box(t, 1)
	key := "dashboard:admin:session:c3-user:c3-workspace"
	for _, ttl := range []time.Duration{0, time.Minute} {
		c3Require(t, "legacy admin fixture", dashboard.NewRepository(client).Save(ctx, "c3-user", "c3-workspace", c3Admin()))
		if ttl > 0 {
			c3Require(t, "set finite TTL fixture", client.PExpire(ctx, key, ttl).Err())
		}
		before := c3TTL(t, client, key)
		c3Require(t, "verify Redis before migration", dashboard.NewRepository(client, box).VerifyCredentials(ctx))
		c3Require(t, "migrate Redis admin fixture", dashboard.NewRepository(client, box).EncryptCredentials(ctx))
		after := c3TTL(t, client, key)
		if ttl == 0 && after != -1 || ttl > 0 && (after > before || after < before-time.Second) {
			t.Fatal("migration reset Redis TTL")
		}
		got, err := dashboard.NewRepository(client, box).Get(ctx, "c3-user", "c3-workspace")
		c3Require(t, "read migrated admin fixture", err)
		if got == nil || !reflect.DeepEqual(got.Session, c3Session()) {
			t.Fatal("Redis migration lost session")
		}
	}
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			c3Require(t, "legacy concurrent fixture", dashboard.NewRepository(client).Save(ctx, "c3-user", "c3-workspace", c3Admin()))
			hook.mu.Lock()
			hook.beforeCAS = func() {
				if operation == "delete" {
					c3Require(t, "concurrent Redis deletion", client.Del(ctx, key).Err())
				} else {
					c3Require(t, "concurrent Redis update", client.Set(ctx, key, "c3-newer-value", 30*time.Second).Err())
				}
			}
			hook.mu.Unlock()
			if err := dashboard.NewRepository(client, box).EncryptCredentials(ctx); err == nil {
				t.Fatal("Redis migration did not reject concurrent change")
			}
			if operation == "delete" {
				if n, err := client.Exists(ctx, key).Result(); err != nil || n != 0 {
					t.Fatal("Redis migration recreated deleted session")
				}
			} else {
				got, err := client.Get(ctx, key).Result()
				c3Require(t, "read concurrent Redis fixture", err)
				if got != "c3-newer-value" || c3TTL(t, client, key) < 29*time.Second || c3TTL(t, client, key) > 30*time.Second {
					t.Fatal("Redis migration lost concurrent value or TTL")
				}
			}
		})
	}
	// Exercise the shared CAS helper directly for removed and changed payloads too.
	if err := credentialstore.ReplaceRedisIfUnchanged(ctx, client, "c3-cas-missing", "old", "new"); err == nil {
		t.Fatal("CAS recreated missing key")
	}
}

func c3Stores(pool *pgxpool.Pool, client *redis.Client, box *secretbox.Box) startupCredentialStores {
	return startupCredentialStores{db: pool, box: box, dashboard: dashboard.NewRepository(client, box), sites: upstream.NewRedisSiteCache(client, box)}
}

func TestAbsorbC3StartupPreflightRejectsAllUnreadableStoresBeforeMigration(t *testing.T) {
	pool := c3Postgres(t)
	client, _ := c3Redis(t)
	ctx, box := t.Context(), c3Box(t, 1)
	items := c3PGCredentials(t, pool)
	for _, location := range []string{"my_site_states", "upstream_sites", "real_connections", "notification_channel_settings", "Redis admin", "Redis site"} {
		t.Run(location, func(t *testing.T) {
			for _, item := range items {
				c3Require(t, "reset legacy fixture", item.write(nil))
			}
			c3Require(t, "reset legacy admin", dashboard.NewRepository(client).Save(ctx, "c3-user", "c3-workspace", c3Admin()))
			c3Require(t, "reset legacy cache", upstream.NewRedisSiteCache(client).Set(ctx, &[]upstream.Site{c3Site()}[0]))
			for _, item := range items {
				if item.table == location {
					c3Require(t, "target encrypted fixture", item.write(box))
				}
			}
			if location == "Redis admin" {
				c3Require(t, "target encrypted admin", dashboard.NewRepository(client, box).Save(ctx, "c3-user", "c3-workspace", c3Admin()))
			}
			if location == "Redis site" {
				c3Require(t, "target encrypted cache", upstream.NewRedisSiteCache(client, box).Set(ctx, &[]upstream.Site{c3Site()}[0]))
			}
			before := make([]string, len(items))
			for i, item := range items {
				before[i] = c3Raw(t, pool, item)
			}
			for _, candidate := range []struct {
				name string
				box  *secretbox.Box
				want error
			}{{"missing", nil, secretbox.ErrNoKey}, {"wrong", c3Box(t, 2), secretbox.ErrUndecryptable}} {
				if err := c3Stores(pool, client, candidate.box).Verify(); !errors.Is(err, candidate.want) {
					t.Fatalf("%s key startup accepted unreadable %s", candidate.name, location)
				}
			}
			if err := c3Stores(pool, client, c3Box(t, 2)).Encrypt(ctx); !errors.Is(err, secretbox.ErrUndecryptable) {
				t.Fatal("normal startup did not preflight before migration")
			}
			for i, item := range items {
				if c3Raw(t, pool, item) != before[i] {
					t.Fatal("preflight failure partially migrated PG")
				}
			}
			c3Require(t, "correct key preflight", c3Stores(pool, client, box).Verify())
		})
	}
	// Once every store has passed, the same existing startup path may migrate.
	c3Require(t, "valid startup conversion", c3Stores(pool, client, box).Encrypt(ctx))
	for _, item := range items {
		if !c3StoredIsSealed(item, c3Raw(t, pool, item)) {
			t.Fatal("valid startup did not migrate PG")
		}
		c3Require(t, "read converted PG", item.read(box))
	}
	raw, err := client.Get(ctx, "dashboard:admin:session:c3-user:c3-workspace").Result()
	c3Require(t, "read converted admin", err)
	if !secretbox.IsSealed(raw) {
		t.Fatal("valid startup did not migrate admin cache")
	}
}

func c3StartupPanic(run func()) (result any) {
	defer func() { result = recover() }()
	run()
	return nil
}

func TestAbsorbC3APIOnlyStartupVerifiesButNeverMigrates(t *testing.T) {
	pool := c3Postgres(t)
	client, _ := c3Redis(t)
	box := c3Box(t, 1)
	items := c3PGCredentials(t, pool)
	for _, item := range items {
		c3Require(t, "APIOnly legacy fixture", item.write(nil))
	}
	c3Require(t, "APIOnly encrypted fixture", items[3].write(box))
	before := make([]string, len(items))
	for i, item := range items {
		before[i] = c3Raw(t, pool, item)
	}
	attachmentDir := t.TempDir()
	for _, candidate := range []struct {
		name, key string
		want      error
	}{{"missing", "", secretbox.ErrNoKey}, {"wrong", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), secretbox.ErrUndecryptable}} {
		cfg := config.Config{APIOnly: true, TicketUploadDir: attachmentDir, CredentialEncryptionKey: candidate.key}
		recovered := c3StartupPanic(func() { New(cfg, pool, client) })
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, candidate.want) {
			t.Fatalf("APIOnly %s key did not stop on unreadable credential", candidate.name)
		}
	}
	cfg := config.Config{APIOnly: true, TicketUploadDir: attachmentDir, CredentialEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	server := New(cfg, pool, client)
	c3Require(t, "APIOnly shutdown", server.Shutdown(t.Context()))
	for i, item := range items {
		if c3Raw(t, pool, item) != before[i] {
			t.Fatal("APIOnly changed stored credentials")
		}
	}
	var tables int
	c3Require(t, "APIOnly did not initialize analysis tables", pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'user_activity_%'`).Scan(&tables))
	if tables != 0 {
		t.Fatal("APIOnly initialized new tables")
	}
	entries, err := os.ReadDir(attachmentDir)
	c3Require(t, "inspect fixture config directory", err)
	if len(entries) != 0 || server.cfg.CredentialEncryptionKey != cfg.CredentialEncryptionKey {
		t.Fatal("startup created key/config files or changed supplied config")
	}
	if recovered := c3StartupPanic(func() { New(config.Config{APIOnly: true, CredentialEncryptionKey: "invalid-fixture-key"}, nil, nil) }); !errors.Is(c3PanicError(recovered), secretbox.ErrInvalidKey) {
		t.Fatal("invalid configured key accepted")
	}
}

func c3PanicError(value any) error { err, _ := value.(error); return err }
