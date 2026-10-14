package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func notificationProducerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for isolated notification producer tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("cannot parse notification test database configuration")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open notification test database")
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("notification_producer_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("cannot create isolated notification test schema")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("cannot clean isolated notification test schema")
		} else {
			t.Logf("isolated notification test schema removed: %s", schema)
		}
	})
	if _, err := admin.Exec(ctx, "COMMENT ON SCHEMA "+schema+" IS 'FAKE TEST DATA ONLY: notification producer regression fixtures'"); err != nil {
		t.Fatal("cannot label isolated notification test schema")
	}
	config = config.Copy()
	config.ConnConfig.RuntimeParams["search_path"] = strings.Trim(schema, `"`)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated notification schema")
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `
CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL);
CREATE TABLE upstream_sites (id text PRIMARY KEY);
INSERT INTO admin_accounts(id,user_id) VALUES ('workspace','user'),('foreign-workspace','another-user');
`); err != nil {
		t.Fatal("cannot create notification parent fixtures")
	}
	if err := NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal("cannot create notification schema")
	}
	return pool
}

func notificationProducerInput(event string, level Level) NoticeInput {
	return NoticeInput{
		UserID: "user", AdminAccountID: "workspace", Kind: "fixture.local_fact", EventKey: event,
		SubjectType: "fixture", SubjectID: "object-1", Level: level,
		DiscoveredAt: time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC),
		Content:      Content{Title: "本地事实已记录", Summary: "这是一条隔离测试消息。", Details: []Detail{{Label: "对象", Value: "对象甲"}}},
	}
}

func notificationProducerCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications`).Scan(&got); err != nil {
		t.Fatal("cannot read notification fact count")
	}
	if got != want {
		t.Fatalf("notification facts=%d, want %d", got, want)
	}
}

func TestNotificationProducerFourLevelsPublishAndAppendTxPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	for _, level := range []Level{LevelNormal, LevelAttention, LevelImportant, LevelUrgent} {
		input := notificationProducerInput(fmt.Sprintf("published-%d", level), level)
		id, inserted, err := service.Publish(t.Context(), input)
		if err != nil || id == "" || !inserted {
			t.Fatalf("publish level %d: id=%q err=%v", level, id, err)
		}
		var actual Level
		var readAt, completedAt *time.Time
		if err := pool.QueryRow(t.Context(), `SELECT level,read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, id).Scan(&actual, &readAt, &completedAt); err != nil || actual != level || readAt != nil || completedAt != nil {
			t.Fatalf("level %d fact was not stored unread and uncompleted", level)
		}
	}
	notificationProducerCount(t, pool, 4)
	appendTx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal("cannot begin four-level caller transaction")
	}
	defer appendTx.Rollback(context.Background())
	for _, level := range []Level{LevelNormal, LevelAttention, LevelImportant, LevelUrgent} {
		if id, inserted, err := NewRepository(pool).AppendTx(t.Context(), appendTx, notificationProducerInput(fmt.Sprintf("appended-%d", level), level)); err != nil || id == "" || !inserted {
			t.Fatalf("repository append level %d: inserted=%t err=%v", level, inserted, err)
		}
	}
	if err := appendTx.Commit(t.Context()); err != nil {
		t.Fatal("cannot commit four-level caller transaction")
	}
	notificationProducerCount(t, pool, 8)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal("cannot begin caller transaction")
	}
	defer tx.Rollback(context.Background())
	if id, inserted, err := service.AppendTx(t.Context(), tx, notificationProducerInput("caller-rollback", LevelUrgent)); err != nil || id == "" || !inserted {
		t.Fatalf("append caller transaction: id=%q err=%v", id, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal("cannot roll back caller transaction")
	}
	notificationProducerCount(t, pool, 8)
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal("cannot begin caller commit transaction")
	}
	defer tx.Rollback(context.Background())
	if _, inserted, err := service.AppendTx(t.Context(), tx, notificationProducerInput("caller-commit", LevelImportant)); err != nil || !inserted {
		t.Fatalf("append caller commit: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal("cannot commit caller transaction")
	}
	notificationProducerCount(t, pool, 9)
}

func TestNotificationProducerConcurrentStableEventRetryPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	input := notificationProducerInput("concurrent-event", LevelUrgent)
	type outcome struct {
		id       string
		inserted bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, 4)
	for range 4 {
		go func() {
			<-start
			id, inserted, err := service.Publish(t.Context(), input)
			results <- outcome{id, inserted, err}
		}()
	}
	close(start)
	outcomes := make([]outcome, 0, 4)
	for range 4 {
		outcomes = append(outcomes, <-results)
	}
	id, inserts := "", 0
	for _, result := range outcomes {
		if result.err != nil || result.id == "" {
			t.Fatalf("concurrent retry failed: %v", result.err)
		}
		if id != "" && id != result.id {
			t.Fatal("concurrent same-event retry returned different notification identities")
		}
		id = result.id
		if result.inserted {
			inserts++
		}
	}
	if inserts != 1 {
		t.Fatalf("same event inserted %d times, want 1", inserts)
	}
	notificationProducerCount(t, pool, 1)
}

func TestNotificationProducerStableEventKeyAndImmutableConflictPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	input := notificationProducerInput("stable-event", LevelUrgent)
	id, inserted, err := service.Publish(t.Context(), input)
	if err != nil || !inserted {
		t.Fatal("first event publish did not insert")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE site_notifications SET read_at=now(),reminder_completed_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal("cannot set saved progress fixture")
	}
	if repeatedID, repeatedInserted, err := service.Publish(t.Context(), input); err != nil || repeatedID != id || repeatedInserted {
		t.Fatalf("same event did not return original fact: %q %v", repeatedID, err)
	}
	mutations := []struct {
		name   string
		mutate func(*NoticeInput)
	}{
		{"level", func(i *NoticeInput) { i.Level = LevelNormal }},
		{"subject", func(i *NoticeInput) { i.SubjectID = "object-2" }},
		{"discovery", func(i *NoticeInput) { i.DiscoveredAt = i.DiscoveredAt.Add(time.Second) }},
		{"title", func(i *NoticeInput) { i.Content.Title = "不同事实" }},
		{"action", func(i *NoticeInput) { i.Content.Action = &Action{Kind: "connection_health"} }},
	}
	for _, sample := range mutations {
		t.Run(sample.name, func(t *testing.T) {
			changed := input
			sample.mutate(&changed)
			if _, _, err := service.Publish(t.Context(), changed); !errors.Is(err, ErrEventConflict) {
				t.Fatalf("different immutable fact must conflict: %v", err)
			}
		})
	}
	var title string
	var completed bool
	if err := pool.QueryRow(t.Context(), `SELECT content->>'title',read_at IS NOT NULL AND reminder_completed_at IS NOT NULL FROM site_notifications WHERE id=$1`, id).Scan(&title, &completed); err != nil || title != input.Content.Title || !completed {
		t.Fatal("event retry changed original content or cleared saved progress")
	}
	notificationProducerCount(t, pool, 1)
	input.EventKey = "independent-event"
	if nextID, nextInserted, err := service.Publish(t.Context(), input); err != nil || nextID == id || !nextInserted {
		t.Fatal("same content with a new business event key must produce another fact")
	}
}

func TestNotificationProducerRejectsForeignWorkspacePostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	for _, workspace := range []string{"foreign-workspace", "deleted-workspace"} {
		input := notificationProducerInput(workspace, LevelNormal)
		input.AdminAccountID = workspace
		if _, _, err := service.Publish(t.Context(), input); !errors.Is(err, ErrWorkspace) {
			t.Fatalf("wrong workspace %q accepted or misleading error: %v", workspace, err)
		}
	}
	notificationProducerCount(t, pool, 0)
}

func TestNotificationProducerPreservesSafeUnknownActionAndIdentifiersPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	input := notificationProducerInput(strings.Repeat("事", 512), LevelNormal)
	input.SubjectID = strings.Repeat("物", 512)
	input.Content.Action = &Action{Kind: "future_business_check"}
	id, inserted, err := service.Publish(t.Context(), input)
	if err != nil || !inserted {
		t.Fatalf("a safe unknown action must retain the message body: %v", err)
	}
	var event, subject string
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT event_key,subject_id,content FROM site_notifications WHERE id=$1`, id).Scan(&event, &subject, &raw); err != nil {
		t.Fatal("cannot read unknown-action fact")
	}
	var content Content
	if err := json.Unmarshal(raw, &content); err != nil || content.Title != input.Content.Title || content.Action == nil || content.Action.Kind != "future_business_check" || event != input.EventKey || subject != input.SubjectID {
		t.Fatal("safe unknown-action content or complete business identifiers were lost")
	}
}

func TestNotificationProducerRejectsUnsafeOrOversizedContentPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	service := NewService(NewRepository(pool))
	cases := []struct {
		name   string
		mutate func(*NoticeInput)
	}{
		{"empty-user", func(i *NoticeInput) { i.UserID = "" }},
		{"wrong-level-low", func(i *NoticeInput) { i.Level = 0 }},
		{"wrong-level-high", func(i *NoticeInput) { i.Level = 5 }},
		{"zero-discovery", func(i *NoticeInput) { i.DiscoveredAt = time.Time{} }},
		{"title-limit", func(i *NoticeInput) { i.Content.Title = strings.Repeat("字", 161) }},
		{"summary-limit", func(i *NoticeInput) { i.Content.Summary = strings.Repeat("字", 1001) }},
		{"detail-value-limit", func(i *NoticeInput) {
			i.Content.Details = []Detail{{Label: "对象", Value: strings.Repeat("字", 1001)}}
		}},
		{"detail-count-limit", func(i *NoticeInput) { i.Content.Details = make([]Detail, 21) }},
		{"total-byte-limit", func(i *NoticeInput) {
			for n := 0; n < 20; n++ {
				i.Content.Details = append(i.Content.Details, Detail{Label: "对象", Value: strings.Repeat("字", 990)})
			}
			i.Content.Details = i.Content.Details[1:]
		}},
		{"identifier-limit", func(i *NoticeInput) { i.SubjectID = strings.Repeat("字", 513) }},
		{"identifier-control", func(i *NoticeInput) { i.EventKey = "one\ntwo" }},
		{"html-title", func(i *NoticeInput) { i.Content.Title = "<script>alert(1)</script>" }},
		{"html-detail", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "对象", Value: "<img src=x onerror=x>"}} }},
		{"bearer", func(i *NoticeInput) { i.Content.Summary = "Authorization: Bearer fixture-only" }},
		{"token", func(i *NoticeInput) { i.Content.Summary = "access_token=fixture-only" }},
		{"password-pair", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "密码", Value: "fixture-only"}} }},
		{"token-pair", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "AccessToken", Value: "fixture-only"}} }},
		{"token-json-object", func(i *NoticeInput) {
			i.Content.Details = []Detail{{Label: "信息", Value: `{"accessToken":"fixture-only"}`}}
		}},
		{"cookie-json-object", func(i *NoticeInput) { i.Content.Summary = `{"Cookie":"fixture-only"}` }},
		{"session-pair", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "Session", Value: "fixture-only"}} }},
		{"real-connection-pair", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "RealConnection", Value: "fixture-only"}} }},
		{"session-whole-object", func(i *NoticeInput) { i.Content.Summary = `{"platform":"sub2api","accessToken":"fixture-only"}` }},
		{"connection-whole-object", func(i *NoticeInput) {
			i.Content.Details = []Detail{{Label: "信息", Value: `{"adminAccountId":"fixture-account","upstreamKeyId":"fixture-key"}`}}
		}},
		{"http-whole-object", func(i *NoticeInput) { i.Content.Summary = `{"success":false,"data":{},"message":"fixture-error"}` }},
		{"ordinary-whole-object", func(i *NoticeInput) { i.Content.Details = []Detail{{Label: "信息", Value: `{"name":"fixture"}`}} }},
		{"ordinary-whole-array", func(i *NoticeInput) { i.Content.Summary = `["fixture"]` }},
		{"postgres-address", func(i *NoticeInput) { i.Content.Summary = "postgresql://fixture:fixture@fixture.invalid/example" }},
		{"redis-address", func(i *NoticeInput) { i.Content.Summary = "redis://fixture:fixture@fixture.invalid/0" }},
		{"action-arbitrary-url", func(i *NoticeInput) { i.Content.Action = &Action{Kind: "https://fixture.invalid"} }},
		{"unsafe-action-id", func(i *NoticeInput) { i.Content.Action = &Action{Kind: "group_rates", SiteID: "fixture\nsite"} }},
	}
	for _, sample := range cases {
		t.Run(sample.name, func(t *testing.T) {
			input := notificationProducerInput("invalid-"+sample.name, LevelNormal)
			sample.mutate(&input)
			if _, _, err := service.Publish(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("unsafe producer input reached storage or was accepted: %v", err)
			}
		})
	}
	notificationProducerCount(t, pool, 0)
}

func TestNotificationProducerInvalidInputExplainsFieldAndRecoveryWithoutOriginalValue(t *testing.T) {
	service := NewService(NewRepository(nil))
	for _, sample := range []struct {
		name     string
		mutate   func(*NoticeInput)
		expected []string
	}{
		{"user-ownership", func(i *NoticeInput) { i.UserID = "" }, []string{"用户", "缺少"}},
		{"notice-kind", func(i *NoticeInput) { i.Kind = "" }, []string{"消息类型", "缺少"}},
		{"event-identifier", func(i *NoticeInput) { i.EventKey = strings.Repeat("e", 513) }, []string{"事件编号", "超过", "512", "核查"}},
		{"level", func(i *NoticeInput) { i.Level = 0 }, []string{"消息等级", "1", "4"}},
		{"discovered-time", func(i *NoticeInput) { i.DiscoveredAt = time.Time{} }, []string{"发现时间", "缺少"}},
		{"title-length", func(i *NoticeInput) { i.Content.Title = strings.Repeat("题", 161) }, []string{"标题", "超过", "160"}},
		{"summary-length", func(i *NoticeInput) { i.Content.Summary = strings.Repeat("摘", 1001) }, []string{"摘要", "超过", "1000"}},
		{"details-count", func(i *NoticeInput) { i.Content.Details = make([]Detail, 21) }, []string{"详情", "超过", "20"}},
		{"detail-value-length", func(i *NoticeInput) {
			i.Content.Details = []Detail{{Label: "对象", Value: strings.Repeat("值", 1001)}}
		}, []string{"详情", "1", "超过", "1000"}},
		{"detail-credential-label", func(i *NoticeInput) {
			i.Content.Details = []Detail{{Label: "AccessToken", Value: "fixture-secret-value"}}
		}, []string{"详情", "1", "凭据"}},
		{"summary-credential-value", func(i *NoticeInput) { i.Content.Summary = "access_token=fixture-secret-value" }, []string{"摘要", "凭据"}},
		{"title-html", func(i *NoticeInput) { i.Content.Title = "<script>fixture-secret-value</script>" }, []string{"标题", "网页标签"}},
		{"summary-object", func(i *NoticeInput) { i.Content.Summary = `{"name":"fixture-secret-value"}` }, []string{"摘要", "整段资料", "纯文字"}},
		{"action-group-identifier", func(i *NoticeInput) {
			i.Content.Action = &Action{Kind: "group_rates", SiteID: "site", FocusGroupID: strings.Repeat("组", 513)}
		}, []string{"检查入口", "分组编号", "超过", "512", "核查"}},
		{"content-byte-limit", func(i *NoticeInput) {
			i.Content.Details = make([]Detail, 20)
			for n := range i.Content.Details {
				i.Content.Details[n] = Detail{Label: "对象", Value: strings.Repeat("值", 990)}
			}
		}, []string{"全部内容", "超过", "32"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			input := notificationProducerInput("invalid-detail-"+sample.name, LevelNormal)
			sample.mutate(&input)
			_, inserted, err := service.Publish(t.Context(), input)
			if !errors.Is(err, ErrInvalidInput) || inserted {
				t.Fatal("detailed rejection must retain the invalid-input category and no saved result")
			}
			got := err.Error()
			for _, expected := range append(sample.expected, "请", "重试") {
				if !strings.Contains(got, expected) {
					t.Errorf("invalid producer input lacks its concrete field, limit or recovery instruction %q", expected)
				}
			}
			for _, forbidden := range []string{"fixture-secret-value", "<script>", "access_token=", `{"name":`} {
				if strings.Contains(got, forbidden) {
					t.Error("invalid-input explanation exposed original sensitive content")
				}
			}
		})
	}
}

func TestNotificationStorageErrorsExplainKnownCauseWithoutRawCredentials(t *testing.T) {
	for _, sample := range []struct {
		state    string
		expected []string
	}{
		{"42P01", []string{"缺少必要结构", "核查启动迁移"}},
		{"42703", []string{"缺少必要结构", "核查启动迁移"}},
		{"42501", []string{"未允许本次存储操作", "数据库权限"}},
		{"23502", []string{"存储约束", "核查这条事件"}},
		{"23503", []string{"存储约束", "核查这条事件"}},
		{"23505", []string{"存储约束", "核查这条事件"}},
		{"23514", []string{"存储约束", "核查这条事件"}},
		{"55P03", []string{"冲突", "原事件编号重试"}},
		{"40P01", []string{"冲突", "原事件编号重试"}},
		{"XX000", []string{"原因尚未确认", "核查", "通知保存步骤"}},
	} {
		t.Run(sample.state, func(t *testing.T) {
			raw := &pgconn.PgError{Code: sample.state, Message: "fixture-secret-message", Detail: "AccessToken=fixture-secret-detail", Hint: "postgresql://fixture-secret-user:fixture-secret-password@fixture.invalid/db", InternalQuery: "SELECT 'fixture-secret-sql'", Where: "fixture-secret-location"}
			got := safeStorageCause(fmt.Errorf("database=postgresql://fixture-secret-outer@fixture.invalid/db: %w", raw))
			for _, expected := range sample.expected {
				if !strings.Contains(got, expected) {
					t.Errorf("safe database explanation lacks required cause or handling hint %q", expected)
				}
			}
			if !strings.Contains(got, "SQLSTATE "+sample.state) {
				t.Error("database state should supplement the plain explanation")
			}
			for _, forbidden := range []string{"fixture-secret", "postgresql://", "AccessToken=", "SELECT "} {
				if strings.Contains(got, forbidden) {
					t.Error("raw database diagnostic exposed credential or query material")
				}
			}
		})
	}
	unknown := safeStorageCause(errors.New("postgresql://fixture-secret-user:fixture-secret-password@fixture.invalid/db"))
	if !strings.Contains(unknown, "原因尚未确认") || !strings.Contains(unknown, "数据库连接和通知保存步骤") || strings.Contains(unknown, "fixture-secret") {
		t.Error("unknown storage failure must retain the uncertainty and a safe concrete check location")
	}
	for _, sample := range []struct {
		err      error
		expected string
	}{{context.DeadlineExceeded, "等待超时"}, {context.Canceled, "已取消"}} {
		if got := safeStorageCause(fmt.Errorf("fixture-secret-context: %w", sample.err)); !strings.Contains(got, sample.expected) || strings.Contains(got, "fixture-secret") {
			t.Error("storage cancellation or timeout reason was lost or leaked its original diagnostic")
		}
	}
}

func TestNotificationProducerAcceptsExactThirtyTwoKiBContentPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	input := notificationProducerInput("exact-content-boundary", LevelNormal)
	input.Content.Title, input.Content.Summary = strings.Repeat("题", 160), strings.Repeat("s", 1000)
	input.Content.Details = make([]Detail, 20)
	for i := range input.Content.Details {
		input.Content.Details[i] = Detail{Label: strings.Repeat("l", 160), Value: strings.Repeat("文", 450)}
	}
	raw, err := json.Marshal(input.Content)
	if err != nil {
		t.Fatal("cannot construct safe exact-size content fixture")
	}
	remaining := 32*1024 - len(raw)
	if remaining < 0 || 450+remaining/3+remaining%3 > 1000 {
		t.Fatal("exact-size fixture cannot fit the confirmed per-field limits")
	}
	input.Content.Details[0].Value += strings.Repeat("文", remaining/3) + strings.Repeat("a", remaining%3)
	raw, err = json.Marshal(input.Content)
	if err != nil || len(raw) != 32*1024 {
		t.Fatal("safe exact-size fixture must use precisely the confirmed 32 KiB content limit")
	}
	id, inserted, err := NewService(NewRepository(pool)).Publish(t.Context(), input)
	if err != nil || !inserted || id == "" {
		t.Fatalf("safe content at the confirmed limit must be saved: %v", err)
	}
	var storedSize int
	if err := pool.QueryRow(t.Context(), `SELECT octet_length(content::text) FROM site_notifications WHERE id=$1`, id).Scan(&storedSize); err != nil || storedSize <= 32*1024 {
		t.Fatal("fixture must exercise PostgreSQL jsonb formatting expansion without introducing a second content limit")
	}
	notificationProducerCount(t, pool, 1)
}

func TestNotificationStorageErrorMappingRetainsRollbackAndSafeRecoveryPostgres(t *testing.T) {
	pool := notificationProducerPool(t)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION notification_error_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE=current_setting('notification_fixture.error_state'),MESSAGE='fixture-secret-message',DETAIL='AccessToken=fixture-secret-detail',HINT='postgresql://fixture-secret-user:fixture-secret-password@fixture.invalid/db'; END $$; CREATE TRIGGER notification_error_fixture BEFORE INSERT ON site_notifications FOR EACH ROW EXECUTE FUNCTION notification_error_fixture()`); err != nil {
		t.Fatal("cannot create isolated notification storage-error fixture")
	}
	for _, sample := range []struct{ state, expected string }{{"42P01", "缺少必要结构"}, {"42501", "数据库权限"}, {"23514", "存储约束"}, {"55P03", "冲突"}, {"XX000", "原因尚未确认"}} {
		t.Run(sample.state, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal("cannot begin isolated error-mapping transaction")
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(t.Context(), `SELECT set_config('notification_fixture.error_state',$1,true)`, sample.state); err != nil {
				t.Fatal("cannot configure isolated storage failure")
			}
			_, inserted, err := NewRepository(pool).AppendTx(t.Context(), tx, notificationProducerInput("error-"+sample.state, LevelImportant))
			if err == nil || inserted {
				t.Fatal("storage rejection must not report a saved notification")
			}
			got := err.Error()
			for _, expected := range []string{"写入这条通知失败", sample.expected, "本次数据库保存已中止", "原事件编号重试", "SQLSTATE " + sample.state} {
				if !strings.Contains(got, expected) {
					t.Errorf("storage rejection lost failure step, cause, save result or recovery hint %q", expected)
				}
			}
			if strings.Contains(got, "fixture-secret") || strings.Contains(got, "postgresql://") || strings.Contains(got, "AccessToken=") {
				t.Error("storage rejection exposed raw database credentials or diagnostics")
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal("cannot roll back isolated storage-error transaction")
			}
		})
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER notification_error_fixture ON site_notifications; DROP FUNCTION notification_error_fixture()`); err != nil {
		t.Fatal("cannot restore isolated notification storage behavior")
	}
	notificationProducerCount(t, pool, 0)
	if _, inserted, err := NewService(NewRepository(pool)).Publish(t.Context(), notificationProducerInput("after-error-restoration", LevelImportant)); err != nil || !inserted {
		t.Fatal("storage fixture restoration did not permit a fresh notification save")
	}
	notificationProducerCount(t, pool, 1)
	t.Log("isolated PostgreSQL error fixtures: rejected writes left 0 messages; trigger removal restored successful save with 1 message")
}
