package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

type noticePathsRealPublisher struct {
	service *notifications.Service
	mu      sync.Mutex
	errors  []error
}

func (p *noticePathsRealPublisher) Publish(ctx context.Context, input notifications.NoticeInput) (string, bool, error) {
	id, inserted, err := p.service.Publish(ctx, input)
	p.mu.Lock()
	p.errors = append(p.errors, err)
	p.mu.Unlock()
	return id, inserted, err
}

func (p *noticePathsRealPublisher) results() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]error(nil), p.errors...)
}

func TestLongFailureNoticePostgresPublisherValidatesProducer(t *testing.T) {
	for _, kind := range []string{"normal", "sensitive-format-error"} {
		t.Run(kind, func(t *testing.T) {
			// Explicit TEST_DATABASE_URL only; this is the established isolated
			// schema helper, and never creates a cluster, database or listener.
			pool := openQuestionAnswerPostgresPool(t)
			if _, err := pool.Exec(t.Context(), `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts(id,user_id) VALUES('ws1','user1')`); err != nil {
				t.Fatal(err)
			}
			noticeRepository := notifications.NewRepository(pool)
			if err := noticeRepository.EnsureSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			f := newNoticePathsFixture(t)
			publisher := &noticePathsRealPublisher{service: notifications.NewService(noticeRepository)}
			noticePathsInject(f.s, publisher)
			if kind == "sensitive-format-error" {
				state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
				state.LastErrorDetail = `{"error":{"message":"套餐未开通；api_key=fixture-sensitive；Authorization: Bearer fixture-secret；<b>请联系上游开通套餐</b>；https://fixture-user:fixture-password@fixture.invalid/path；` + strings.Repeat("详细原因", 100) + `"}}`
				f.repo.states[state.ConnectionID][state.ModelName] = state
			}
			f.tick(t)
			attempts := publisher.results()
			if len(attempts) != 1 || attempts[0] != nil {
				t.Fatalf("actual notifications.Publish did not accept %s producer payload: %v", kind, attempts)
			}
			var raw []byte
			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("actual publish did not store exactly one fact: %d %v", count, err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT content FROM site_notifications WHERE user_id='user1' AND admin_account_id='ws1'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var content notifications.Content
			if err := json.Unmarshal(raw, &content); err != nil || !strings.Contains(content.Summary, "查看这条提醒不会修改主站") {
				t.Fatal("stored producer content lost its guidance")
			}
			for _, detail := range content.Details {
				if detail.Label == "最近错误" && len([]rune(detail.Value)) > 300 {
					t.Fatal("stored concrete cause exceeded 300 characters")
				}
			}
			for _, forbidden := range []string{"fixture-sensitive", "fixture-secret", "fixture-password", "<b>"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatal("actual stored notification exposed an unsafe upstream field")
				}
			}
			if kind == "sensitive-format-error" && !strings.Contains(string(raw), "套餐未开通") {
				t.Fatal("actual publish lost the known upstream cause while sanitizing")
			}
			f.tick(t)
			if len(publisher.results()) != 1 {
				t.Fatal("process memory failed to suppress an already published fact")
			}
			// A restarted producer has no memory. A changed error cannot replace
			// the old immutable fact; the real module returns ErrEventConflict.
			state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			state.LastErrorDetail = "同一失败周期的另一个已脱敏原因"
			f.repo.states[state.ConnectionID][state.ModelName] = state
			next := newAdminGroupsService(f.reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, f.repo)
			next.schedulableActions = forbiddenAutomaticSchedulingWriter{t}
			noticePathsInject(next, publisher)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = next.Shutdown(ctx)
				schedulerStatsByService.Delete(next)
			})
			next.runSchedulerTick(context.Background())
			next.runSchedulerTick(context.Background())
			attempts = publisher.results()
			if len(attempts) != 2 || !errors.Is(attempts[1], notifications.ErrEventConflict) {
				t.Fatalf("real restart conflict was not remembered as sent: %v", attempts)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications`).Scan(&count); err != nil || count != 1 {
				t.Fatal("restart replaced or duplicated notification history")
			}
		})
	}
}
