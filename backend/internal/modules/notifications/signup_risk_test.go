package notifications

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func signupRiskPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := notificationProducerPool(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE users(id text PRIMARY KEY,current_admin_account_id text);
INSERT INTO users VALUES('user','workspace'),('another-user','foreign-workspace');
ALTER TABLE admin_accounts ADD COLUMN platform text NOT NULL DEFAULT 'sub2api',ADD COLUMN base_url text NOT NULL DEFAULT 'https://fixture.invalid',ADD COLUMN display_name text NOT NULL DEFAULT '隔离站点';`); err != nil {
		t.Fatal("cannot prepare risk workspace fixtures")
	}
	return pool
}
func riskTestUser(id string) SignupRiskUser {
	return SignupRiskUser{ID: id, Email: "token=abc+" + id + "@example.com", CreatedAt: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func signupRiskTarget(t *testing.T, r *Repository) SignupRiskTarget {
	t.Helper()
	targets, err := r.ListSignupRiskTargets(t.Context())
	if err != nil || len(targets) != 1 {
		t.Fatal("cannot read enabled risk target")
	}
	return targets[0]
}
func riskCounts(t *testing.T, pool *pgxpool.Pool, processed, notices int) {
	t.Helper()
	var p, n int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM signup_risk_processed_users),(SELECT count(*) FROM site_notifications WHERE kind='sub2api.signup_risk')`).Scan(&p, &n); err != nil || p != processed || n != notices {
		t.Fatalf("risk counts=%d/%d want=%d/%d", p, n, processed, notices)
	}
}

func TestSignupRiskSpecialEmailSafetyIsReversibleAndNeverBlocksAnotherUser(t *testing.T) {
	target := SignupRiskTarget{UserID: "user", AdminAccountID: "workspace", BaseURL: "https://fixture.invalid", SiteName: "token=untrusted"}
	for _, email := range []string{"risk+token=a@example.com", "token=abc+1@example.com", strings.Repeat("=", 240) + "@example.com", "name%3D+1@example.com", "bearer +1@example.com", "<b>+1@example.com"} {
		user := riskTestUser("42")
		user.Email = email
		input, err := signupRiskNotice(target, user, time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("special email must retain a safe complete notification: %v", err)
		}
		shown := input.Content.Details[2].Value
		decoded, err := url.PathUnescape(shown)
		if err != nil || decoded != email {
			t.Fatal("safe email display cannot recover the complete original")
		}
		if !strings.Contains(input.Content.Summary, shown) || input.Level != LevelImportant || input.Content.Details[0].Value != "当前Sub2API站点" {
			t.Fatal("risk popup omitted identity or unsafe site name was not replaced")
		}
	}
}

func TestSignupRiskNoticeExplainsTheInvalidIdentityField(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*SignupRiskUser)
	}{
		{"id", "用户编号不是完整的正整数", func(u *SignupRiskUser) { u.ID = "invalid" }},
		{"email", "注册邮箱为空", func(u *SignupRiskUser) { u.Email = " " }},
		{"email-length", "注册邮箱超过255字节", func(u *SignupRiskUser) { u.Email = strings.Repeat("a", 256) }},
		{"time", "注册时间缺失", func(u *SignupRiskUser) { u.CreatedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := riskTestUser("42")
			tc.change(&user)
			_, err := signupRiskNotice(SignupRiskTarget{}, user, time.Now())
			if err == nil || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), "没有登记或生成消息") {
				t.Fatalf("notice rejection must identify the broken identity field and actual result: %v", err)
			}
		})
	}
}

func TestSignupRiskPersistenceBootstrapDuplicateConcurrentRestartAndCleanupPostgres(t *testing.T) {
	pool := signupRiskPool(t)
	repo := NewRepository(pool)
	service := NewService(repo)
	ctx := t.Context()
	if p, err := service.GetSignupRiskPreferences(ctx, "user", "workspace"); err != nil || p.Enabled || p.Initialized || p.UpdatedAt != nil {
		t.Fatal("missing config must be a read-only disabled default")
	}
	if _, err := service.SaveSignupRiskPreferences(ctx, "user", "workspace", true); err != nil {
		t.Fatal(err)
	}
	target := signupRiskTarget(t, repo)
	users := []SignupRiskUser{riskTestUser("1"), riskTestUser("2")}
	if err := repo.CommitSignupRiskRound(ctx, target, users, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 2, 0)
	if empty, err := repo.ProcessedSignupRiskIDs(ctx, target, nil); err != nil || len(empty) != 0 {
		t.Fatal("empty end-page identity lookup must remain a successful empty read")
	}
	if known, err := repo.ProcessedSignupRiskIDs(ctx, target, []string{"1", "2", "99"}); err != nil || !known["1"] || !known["2"] || known["99"] {
		t.Fatal("processed identity lookup lost or invented persisted users")
	}
	target = signupRiskTarget(t, repo)
	if !target.Initialized {
		t.Fatal("first complete scan did not initialize")
	}
	oldTime := target.UpdatedAt
	if same, err := service.SaveSignupRiskPreferences(ctx, "user", "workspace", true); err != nil || !same.Initialized || !same.UpdatedAt.Equal(oldTime) {
		t.Fatal("same-value setting reset initialization or invalidated the poll")
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- NewRepository(pool).CommitSignupRiskRound(ctx, target, []SignupRiskUser{riskTestUser("3")}, time.Now())
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	riskCounts(t, pool, 3, 1)
	if err := NewRepository(pool).CommitSignupRiskRound(ctx, target, []SignupRiskUser{riskTestUser("3")}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal("restart changed an existing event or generated duplicate")
	}
	riskCounts(t, pool, 3, 1)
	var content string
	if err := pool.QueryRow(ctx, `SELECT content::text FROM site_notifications WHERE kind=$1`, SignupRiskKind).Scan(&content); err != nil || !strings.Contains(content, "token%3Dabc+3@example.com") || strings.Contains(content, SignupRiskNote) {
		t.Fatal("notice lost safe email or stored raw notes")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM site_notifications WHERE kind=$1`, SignupRiskKind); err != nil {
		t.Fatal("cannot model notification history cleanup")
	}
	if err := NewRepository(pool).CommitSignupRiskRound(ctx, target, []SignupRiskUser{riskTestUser("3")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 3, 0) // processed IDs survive notification retention cleanup
	if _, err := pool.Exec(ctx, `DELETE FROM admin_accounts WHERE id='workspace'`); err != nil {
		t.Fatal("cannot remove risk fixture workspace")
	}
	riskCounts(t, pool, 0, 0)
}

func TestSignupRiskSameRemoteUserAcrossSitesAndWorkspaceIsolationPostgres(t *testing.T) {
	pool := signupRiskPool(t)
	repo := NewRepository(pool)
	service := NewService(repo)
	ctx := t.Context()
	if _, err := service.SaveSignupRiskPreferences(ctx, "user", "workspace", true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSignupRiskPreferences(ctx, "another-user", "foreign-workspace", true); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_accounts SET base_url='https://second.invalid' WHERE id='foreign-workspace'`); err != nil {
		t.Fatal("cannot prepare second test site")
	}
	targets, err := repo.ListSignupRiskTargets(ctx)
	if err != nil || len(targets) != 2 {
		t.Fatal("cannot enumerate independent enabled sites")
	}
	for _, target := range targets {
		if err := repo.CommitSignupRiskRound(ctx, target, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	targets, _ = repo.ListSignupRiskTargets(ctx)
	for _, target := range targets {
		if err := repo.CommitSignupRiskRound(ctx, target, []SignupRiskUser{riskTestUser("42")}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	riskCounts(t, pool, 2, 2)
	var owners, events int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT user_id),count(DISTINCT event_key) FROM site_notifications WHERE kind=$1`, SignupRiskKind).Scan(&owners, &events); err != nil || owners != 2 || events != 2 {
		t.Fatal("different sites shared a remote user or notification scope")
	}
	if _, err := service.GetSignupRiskPreferences(ctx, "user", "foreign-workspace"); err == nil {
		t.Fatal("another user's risk preferences became readable")
	}
}

func TestSignupRiskDisabledAndReopenedPollSnapshotCannotWritePostgres(t *testing.T) {
	pool := signupRiskPool(t)
	repo := NewRepository(pool)
	service := NewService(repo)
	ctx := t.Context()
	_, _ = service.SaveSignupRiskPreferences(ctx, "user", "workspace", true)
	old := signupRiskTarget(t, repo)
	_, _ = service.SaveSignupRiskPreferences(ctx, "user", "workspace", false)
	if targets, err := repo.ListSignupRiskTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatal("disabled workspace still starts new remote polls")
	}
	if err := repo.CommitSignupRiskRound(ctx, old, []SignupRiskUser{riskTestUser("1")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 0, 0)
	_, _ = service.SaveSignupRiskPreferences(ctx, "user", "workspace", true)
	if err := repo.CommitSignupRiskRound(ctx, old, []SignupRiskUser{riskTestUser("1")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 0, 0)
	fresh := signupRiskTarget(t, repo)
	if fresh.UpdatedAt.Equal(old.UpdatedAt) || fresh.Initialized {
		t.Fatal("reopen retained a stale polling snapshot")
	}
	if err := repo.CommitSignupRiskRound(ctx, fresh, []SignupRiskUser{riskTestUser("1")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 1, 0)
	if err := repo.CommitSignupRiskRound(ctx, signupRiskTarget(t, repo), []SignupRiskUser{riskTestUser("2")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 2, 1)
}

func TestSignupRiskRoundTransactionRollsBackAllIDsAndNoticesPostgres(t *testing.T) {
	pool := signupRiskPool(t)
	repo := NewRepository(pool)
	service := NewService(repo)
	ctx := t.Context()
	_, _ = service.SaveSignupRiskPreferences(ctx, "user", "workspace", true)
	if err := repo.CommitSignupRiskRound(ctx, signupRiskTarget(t, repo), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION refuse_second_risk() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_key LIKE '%:4' THEN RAISE EXCEPTION 'isolated failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_second_risk BEFORE INSERT ON site_notifications FOR EACH ROW EXECUTE FUNCTION refuse_second_risk()`); err != nil {
		t.Fatal("cannot inject isolated persistence failure")
	}
	users := []SignupRiskUser{riskTestUser("3"), riskTestUser("4")}
	if err := repo.CommitSignupRiskRound(ctx, signupRiskTarget(t, repo), users, time.Now()); err == nil {
		t.Fatal("notification failure was hidden")
	}
	riskCounts(t, pool, 0, 0)
	if _, err := pool.Exec(ctx, `DROP TRIGGER refuse_second_risk ON site_notifications`); err != nil {
		t.Fatal("cannot clear isolated failure")
	}
	if err := repo.CommitSignupRiskRound(ctx, signupRiskTarget(t, repo), users, time.Now()); err != nil {
		t.Fatal(err)
	}
	riskCounts(t, pool, 2, 2)
}
