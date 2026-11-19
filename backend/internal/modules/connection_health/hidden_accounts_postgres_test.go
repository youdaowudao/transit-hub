package connection_health

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestHiddenAccountsRepositoryWritesOnlyDisplayColumnsAndRechecksWorkspace(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS admin_accounts (id text PRIMARY KEY, user_id text NOT NULL);
		INSERT INTO admin_accounts (id, user_id) VALUES ('ws1', 'user1');
		INSERT INTO connection_health_account_configs (user_id, admin_account_id, target_id, account_tier, intelligence_weight)
		VALUES ('user1', 'ws1', 'sub2api:ws1:img', 1, 42)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema upgrade must be idempotent: %v", err)
	}

	var updatedBefore time.Time
	if err := pool.QueryRow(ctx, `UPDATE connection_health_account_configs SET updated_at = '2026-01-01T00:00:00Z' WHERE target_id = 'sub2api:ws1:img' RETURNING updated_at`).Scan(&updatedBefore); err != nil {
		t.Fatal(err)
	}
	first, err := repo.SaveAccountHidden(ctx, "user1", "ws1", "sub2api:ws1:img", true, "画图账号")
	if err != nil || first == nil {
		t.Fatalf("hide: %v %v", first, err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := repo.SaveAccountHidden(ctx, "user1", "ws1", "sub2api:ws1:img", true, "改名后")
	if err != nil || second == nil || !second.Equal(*first) {
		t.Fatalf("repeat hide must keep the original time: %v %v %v", first, second, err)
	}
	var tier, weight int
	if err := pool.QueryRow(ctx, `SELECT account_tier, intelligence_weight FROM connection_health_account_configs WHERE target_id = 'sub2api:ws1:img'`).Scan(&tier, &weight); err != nil || tier != 1 || weight != 42 {
		t.Fatalf("hiding changed other columns: tier=%d weight=%d err=%v", tier, weight, err)
	}
	tiers, err := repo.ListAccountTiers(ctx, "user1", "ws1")
	if err != nil || tiers["sub2api:ws1:img"] != 1 {
		t.Fatalf("tier read after hide: %v %v", tiers, err)
	}
	items, err := repo.ListHiddenAccounts(ctx, "user1", "ws1")
	if err != nil || len(items) != 1 || items[0].AccountName != "改名后" {
		t.Fatalf("list: %+v %v", items, err)
	}

	if _, err := repo.SaveAccountHidden(ctx, "user1", "ws1", "sub2api:ws1:img", false, ""); err != nil {
		t.Fatal(err)
	}
	if items, err := repo.ListHiddenAccounts(ctx, "user1", "ws1"); err != nil || len(items) != 0 {
		t.Fatalf("unhide: %+v %v", items, err)
	}
	var updatedAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT account_tier, updated_at FROM connection_health_account_configs WHERE target_id = 'sub2api:ws1:img'`).Scan(&tier, &updatedAfter); err != nil || tier != 1 || !updatedAfter.Equal(updatedBefore) {
		t.Fatalf("hide/unhide changed other columns: tier=%d updated_at %v -> %v err=%v", tier, updatedBefore, updatedAfter, err)
	}

	// 工作区已删除（admin_accounts 行不存在）时不写入，也不会留下属于它的行。
	if _, err := repo.SaveAccountHidden(ctx, "user1", "gone", "sub2api:gone:x", true, "x"); !errors.Is(err, requestError(ErrorNoCurrentAccount)) {
		t.Fatalf("deleted workspace err=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs WHERE admin_account_id = 'gone'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan rows=%d err=%v", count, err)
	}
}

func TestHiddenAccountsModelControlListExcludeAllAndWorkspace(t *testing.T) {
	stageAPostgresPool(t)
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	f.manage("2", "A")

	plain := f.expect("GET", "model-control/items?view=all", nil, 200)
	if counts := plain["counts"].(map[string]any); counts["total"] != float64(2) {
		t.Fatalf("plain counts=%v", counts)
	}
	filtered := f.expect("GET", "model-control/items?view=all&all=1&workspace="+c3REDWorkspace+"&excludeTargetIds="+c3REDTarget("2"), nil, 200)
	items := filtered["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["targetId"] != c3REDTarget("1") || filtered["totalPages"] != float64(1) {
		t.Fatalf("filtered items=%v", filtered)
	}
	if counts := filtered["counts"].(map[string]any); counts["total"] != float64(1) {
		t.Fatalf("excluded accounts must not be counted: %v", counts)
	}
	f.expect("GET", "model-control/items?view=all&all=1&workspace=other-workspace", nil, 409)
	f.expect("GET", "model-control/verify-targets?workspace=other-workspace", nil, 409)

	// 另一页面切到非 Sub2API 工作区：旧抽屉带着原工作区读取，应得到 409（工作区已切换），而不是平台不支持。
	f.service.accounts = fakeAdminAccountResolver{id: "ws-newapi"}
	f.service.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	f.expect("GET", "model-control/items?view=all&all=1&workspace="+c3REDWorkspace, nil, 409)
	f.expect("GET", "model-control/verify-targets?workspace="+c3REDWorkspace, nil, 409)
	// 不带 workspace 的旧调用保持原行为：当前工作区不是 Sub2API 时仍按平台不支持拒绝。
	f.expect("GET", "model-control/items?view=all", nil, 400)

	// 第一次比对之后、平台校验之前才切到 NewAPI 工作区：两个入口仍应返回 409。
	for _, path := range []string{"model-control/items?view=all&all=1&workspace=" + c3REDWorkspace, "model-control/verify-targets?workspace=" + c3REDWorkspace} {
		f.service.accounts = &switchingAdminAccountResolver{ids: []string{c3REDWorkspace, "ws-newapi"}}
		f.expect("GET", path, nil, 409)
	}
}

// switchingAdminAccountResolver 依次返回给定的当前工作区，最后一个之后保持不变，用于模拟读取之间被另一页面切换。
type switchingAdminAccountResolver struct {
	ids   []string
	calls int
}

func (r *switchingAdminAccountResolver) RequireCurrentID(context.Context, string) (string, error) {
	id := r.ids[min(r.calls, len(r.ids)-1)]
	r.calls++
	return id, nil
}

func TestHiddenAccountsBlockAutomaticModelControl(t *testing.T) {
	stageAPostgresPool(t)
	hide := func(t *testing.T, f *c32REDFixture, targetID string) {
		t.Helper()
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_account_configs (user_id, admin_account_id, target_id, display_hidden_at, display_hidden_name)
			VALUES ($1, $2, $3, now(), '画图账号')
			ON CONFLICT (user_id, admin_account_id, target_id) DO UPDATE SET display_hidden_at = now()`, c32REDUser, c32REDWorkspace, targetID); err != nil {
			t.Fatal(err)
		}
	}
	for _, restore := range []bool{false, true} {
		for _, when := range []string{"before first check", "between checks"} {
			t.Run(fmt.Sprintf("restore=%v/%s", restore, when), func(t *testing.T) {
				f := c32AutoFixture(t, restore)
				op := "close"
				if restore {
					op = "restore"
				}
				candidate := c32Candidate(t, f, "1", "A", op)
				writes := f.writeCount()
				if when == "before first check" {
					hide(t, f, candidate.TargetID)
				} else {
					f.beforeDetail = func() { f.beforeDetail = nil; hide(t, f, candidate.TargetID) }
				}
				reason := c32Run(t, f, candidate)
				if f.writeCount() != writes {
					t.Fatalf("hidden account was written to the main site: %s", reason)
				}
				if o := c32AutoObject(t, f, "1", "A"); o.Pending != nil {
					t.Fatal("hidden account kept a pending model operation")
				}
			})
		}
	}
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%v/hidden after second check still completes", restore), func(t *testing.T) {
			f := c32AutoFixture(t, restore)
			op := "close"
			if restore {
				op = "restore"
			}
			candidate := c32Candidate(t, f, "1", "A", op)
			writes := f.writeCount()
			hook := &c32AutoHookRepository{Repository: f.service.modelControls.(*Repository)}
			f.service.modelControls = hook
			hook.beforeMutate = func(stage string) {
				if stage == "prepared" {
					hook.beforeMutate = nil
					hide(t, f, candidate.TargetID)
				}
			}
			if reason := c32Run(t, f, candidate); reason != "" || f.writeCount() == writes {
				t.Fatalf("account hidden after the second check did not complete this write: reason=%q", reason)
			}
		})
	}
	t.Run("hidden list read failure skips automatic writes but keeps due verification", func(t *testing.T) {
		// 用自动恢复场景：模型已关闭，账号同时是恢复候选和到期核对对象（到期核对只看有已关闭模型的账号）。
		f := c32AutoFixture(t, true)
		c32Candidate(t, f, "1", "A", "restore")
		if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET auto_verify_at=clock_timestamp()-interval '1 day'`); err != nil {
			t.Fatal(err)
		}
		f.service.repo = hiddenReadFailingRepository{f.service.repo.(*Repository)}
		writes, reads := f.writeCount(), f.detailReads
		attemptBefore := c32AutoObject(t, f, "1", "A").AutoState
		f.service.runModelControlAutoScan(t.Context(), c32REDUser, c32REDWorkspace)
		if f.writeCount() != writes {
			t.Fatal("automatic model write ran although the hidden list could not be read")
		}
		// 扫描这一层就要跳过候选：不应留下新的自动尝试记录（执行前的第二次检查只是第二道保护）。
		if attemptAfter := c32AutoObject(t, f, "1", "A").AutoState; !reflect.DeepEqual(attemptBefore, attemptAfter) {
			t.Fatal("automatic attempt was recorded although the hidden list could not be read", attemptAfter)
		}
		if f.detailReads == reads {
			t.Fatal("due verification was skipped after the hidden list read failed")
		}
	})
	t.Run("control: scan acts on a visible candidate", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		c32Candidate(t, f, "1", "A", "close")
		writes := f.writeCount()
		f.service.runModelControlAutoScan(t.Context(), c32REDUser, c32REDWorkspace)
		if f.writeCount() == writes {
			t.Fatal("fixture is not eligible; the hidden-account assertions would prove nothing")
		}
	})
	t.Run("scan skips hidden candidates", func(t *testing.T) {
		f := c32AutoFixture(t, false)
		candidate := c32Candidate(t, f, "1", "A", "close")
		hide(t, f, candidate.TargetID)
		writes := f.writeCount()
		f.service.runModelControlAutoScan(t.Context(), c32REDUser, c32REDWorkspace)
		if f.writeCount() != writes {
			t.Fatal("automatic scan acted on a hidden account")
		}
	})
}

// 嵌入真实仓库，只让屏蔽清单读取失败；其余可选接口照常可用。
type hiddenReadFailingRepository struct{ *Repository }

func (hiddenReadFailingRepository) ListHiddenAccounts(context.Context, string, string) ([]HiddenAccount, error) {
	return nil, errors.New("hidden list unavailable")
}

func (hiddenReadFailingRepository) SaveAccountHidden(context.Context, string, string, string, bool, string) (*time.Time, error) {
	return nil, errors.New("hidden list unavailable")
}

func TestHiddenHealthSaveGenerationPostgres(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS admin_accounts (id text PRIMARY KEY, user_id text NOT NULL);
		INSERT INTO admin_accounts (id, user_id) VALUES ('ws1', 'user1')`); err != nil {
		t.Fatal(err)
	}
	save := func(hidden bool) {
		t.Helper()
		if _, err := repo.SaveAccountHidden(ctx, "user1", "ws1", "sub2api:ws1:img", hidden, "image"); err != nil {
			t.Fatal(err)
		}
	}
	assertRows := func(wantGeneration int, wantPresets int) {
		t.Helper()
		var generation, presets int
		err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT config_generation FROM connection_health_workspace_settings), -1), (SELECT count(*) FROM connection_health_rule_presets)`).Scan(&generation, &presets)
		if err != nil || generation != wantGeneration || presets != wantPresets {
			t.Fatalf("after screen change, config generation=%d, presets=%d, want %d and %d: %v", generation, presets, wantGeneration, wantPresets, err)
		}
	}
	save(true)
	assertRows(-1, 0)
	save(false)
	assertRows(-1, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_workspace_settings (user_id, admin_account_id, config_generation) VALUES ('user1','ws1',4)`); err != nil {
		t.Fatal(err)
	}
	save(true)
	assertRows(5, 0)
	save(true)
	assertRows(5, 0)
	save(false)
	assertRows(6, 0)
	save(false)
	assertRows(6, 0)

	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_workspace_settings ADD CONSTRAINT test_reject_generation CHECK (config_generation <= 6)`); err != nil {
		t.Fatal(err)
	}
	_, err := repo.SaveAccountHidden(ctx, "user1", "ws1", "sub2api:ws1:img", true, "image")
	if err == nil {
		t.Fatal("hiding must report a failed generation write")
	}
	items, err := repo.ListHiddenAccounts(ctx, "user1", "ws1")
	if err != nil || len(items) != 0 {
		t.Fatalf("failed transaction must leave the target visible: %v, %v", items, err)
	}
	assertRows(6, 0)
}

func TestHiddenHealthSaveAndWorkspaceDeletionSerializePostgres(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL);
		INSERT INTO admin_accounts (id, user_id) VALUES ('delete-first', 'u'), ('hide-first', 'u');
		INSERT INTO connection_health_workspace_settings (user_id, admin_account_id, config_generation) VALUES ('u', 'hide-first', 4)`); err != nil {
		t.Fatal(err)
	}

	for _, workspace := range []string{"delete-first", "hide-first"} {
		target := "sub2api:" + workspace + ":fixture"
		wantGeneration := -1
		if workspace == "hide-first" {
			if hiddenAt, err := repo.SaveAccountHidden(ctx, "u", workspace, target, true, "fixture"); err != nil || hiddenAt == nil {
				t.Fatalf("hide before deletion: timestamp=%v err=%v", hiddenAt, err)
			}
			wantGeneration = 5
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var deletingPID int
		// Reproduce the workspace row lock and the relevant deletes in
		// admin_accounts.Repository.DeleteWorkspace for this isolated schema.
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM admin_accounts WHERE user_id = $1 AND id = $2 FOR UPDATE`, "u", workspace).Scan(&deletingPID); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		if workspace == "delete-first" {
			go func() {
				_, err := repo.SaveAccountHidden(ctx, "u", workspace, target, true, "fixture")
				result <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)))`, deletingPID).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("screening did not wait for the workspace deletion row lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM connection_health_account_configs WHERE user_id = $1 AND admin_account_id = $2`, "u", workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admin_accounts WHERE user_id = $1 AND id = $2`, "u", workspace); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if workspace == "delete-first" {
			if err := <-result; !errors.Is(err, requestError(ErrorNoCurrentAccount)) {
				t.Fatalf("save after deletion must be rejected: %v", err)
			}
		}
		var generation, presets, configs int
		err = pool.QueryRow(ctx, `SELECT
			COALESCE((SELECT config_generation FROM connection_health_workspace_settings WHERE admin_account_id=$1), -1),
			(SELECT count(*) FROM connection_health_rule_presets WHERE admin_account_id=$1),
			(SELECT count(*) FROM connection_health_account_configs WHERE admin_account_id=$1)`, workspace).Scan(&generation, &presets, &configs)
		if err != nil || generation != wantGeneration || presets != 0 || configs != 0 {
			t.Fatalf("%s after deletion: generation=%d (want %d) presets=%d configs=%d err=%v", workspace, generation, wantGeneration, presets, configs, err)
		}
	}
}
