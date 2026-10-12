package connection_health

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"os"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestMessagesC42M7QuestionAnswerConstraintUpgradeOrdersAndOldValues(t *testing.T) {
	for _, order := range []string{"old-tables-then-migration", "schema-only", "migration-then-schema"} {
		t.Run(order, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			repo := NewRepository(pool)
			ctx := context.Background()
			migration, err := os.ReadFile("../../database/migrations/000039_connection_health_question_answer_protocol_messages.sql")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			// Missing upgrade on C4.1 is a no-op: RED must be a rejected business value.
			if len(migration) == 0 {
				migration = []byte("SELECT 1;")
			}
			upgrade := func() {
				t.Helper()
				if _, err := pool.Exec(ctx, string(migration)); err != nil {
					t.Fatal(err)
				}
			}
			oldTable := `CREATE TABLE connection_health_question_answer_records (
			id text PRIMARY KEY,
 request_protocol text NULL CHECK(request_protocol IS NULL OR request_protocol IN ('chat_completions','responses')),
			user_id text NOT NULL,
			target_id text NOT NULL,
			batch_id text NOT NULL,
			model_name text NOT NULL,
			question_id text NOT NULL,
			question_name text NOT NULL,
			question_body text NOT NULL,
			question_keyword_snapshot text[] NULL,
			answer_body text NOT NULL DEFAULT '',
			status text NOT NULL DEFAULT 'pending',
			error_type text NOT NULL DEFAULT '',
			manual_error boolean NOT NULL DEFAULT false,
			answer_judgment text NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			started_at timestamptz NULL,
			completed_at timestamptz NULL,
			updated_at timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT connection_health_question_answer_status CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'))
		)`
			seed := func() {
				t.Helper()
				if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,request_protocol) VALUES('old-chat','u','target','batch','m','q','Q','body','chat_completions'),('old-responses','u','target','batch','m','q','Q','body','responses'),('old-null','u','target','batch','m','q','Q','body',NULL)`); err != nil {
					t.Fatal(err)
				}
			}
			if order == "migration-then-schema" {
				if _, err := pool.Exec(ctx, oldTable); err != nil {
					t.Fatal(err)
				}
				seed()
				upgrade()
				upgrade()
			}
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if order != "migration-then-schema" {
				seed()
			}
			if order == "old-tables-then-migration" {
				rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attname='request_protocol' WHERE c.conrelid='connection_health_question_answer_records'::regclass AND c.contype='c' AND c.conkey=ARRAY[a.attnum] AND pg_get_constraintdef(c.oid) LIKE '%chat_completions%'`)
				if err != nil {
					t.Fatal(err)
				}
				names := []string{}
				for rows.Next() {
					var name string
					if err := rows.Scan(&name); err != nil {
						t.Fatal(err)
					}
					names = append(names, name)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				for _, name := range names {
					if _, err := pool.Exec(ctx, "ALTER TABLE connection_health_question_answer_records DROP CONSTRAINT "+pgx.Identifier{name}.Sanitize()); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_question_answer_records ADD CONSTRAINT c42_old_qa_protocol CHECK(request_protocol IS NULL OR request_protocol IN ('chat_completions','responses'))`); err != nil {
					t.Fatal(err)
				}
				upgrade()
				upgrade()
			}
			assertOld := func() {
				t.Helper()
				var unchanged bool
				if err := pool.QueryRow(ctx, `SELECT count(*)=3 AND bool_and(CASE id WHEN 'old-chat' THEN request_protocol='chat_completions' WHEN 'old-responses' THEN request_protocol='responses' ELSE request_protocol IS NULL END) FROM connection_health_question_answer_records WHERE id LIKE 'old-%'`).Scan(&unchanged); err != nil || !unchanged {
					t.Fatalf("old values changed unchanged=%v err=%v", unchanged, err)
				}
			}
			assertOld()
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,request_protocol) VALUES('new-messages','u','target','new','m','q','Q','body','messages')`); err != nil {
				t.Errorf("question answer field rejected messages: %v", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET request_protocol='bogus' WHERE id='old-chat'`); err == nil {
				t.Error("question answer field accepted bogus")
			}
			upgrade()
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			upgrade()
			assertOld()
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attname='request_protocol' WHERE c.conrelid='connection_health_question_answer_records'::regclass AND c.contype='c' AND c.conkey=ARRAY[a.attnum] AND pg_get_constraintdef(c.oid) LIKE '%chat_completions%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("duplicate/missing single-column protocol checks=%d", count)
			}
		})
	}
}

func TestMessagesC42M1ManualQuestionAnswerPersistsMessagesAndSendsFiveFields(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	question, err := repo.CreateTestQuestion(ctx, "u", "C4.2 FAKE question", "fixture", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "g", &GroupTestConfiguration{TestProtocolMessages, 20}); err != nil {
		t.Fatalf("Messages configuration cannot be saved: %v", err)
	}
	snapshot := QuestionAnswerConfigurationSnapshot{AdminAccountID: "w", InventoryComplete: true, Memberships: []TestConfigurationSource{{AdminGroupID: "g"}}}
	records, err := repo.CreateQuestionAnswerBatch(ctx, "u", "sub2api:w:1", "c42-manual", []string{"m"}, []string{question.ID}, QuestionAnswerReasoningEffortHigh, 1, snapshot)
	if err != nil || len(records) != 1 {
		t.Fatalf("manual batch=%+v err=%v", records, err)
	}
	if records[0].RequestProtocol == nil || *records[0].RequestProtocol != TestProtocolMessages {
		t.Fatalf("manual record should use Messages: %+v", records[0])
	}
	calls := 0
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
		calls++
		c42AssertRequest(t, req, "m", "fixture", "high")
		return protocolServiceResponse(req, 200, `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`), nil
	})
	answer, reason := runner.Ask(ctx, upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}, records[0].ModelName, records[0].QuestionBody, *records[0].ReasoningEffort, *records[0].RequestProtocol)
	if answer != "answer" || reason != "" || calls != 1 {
		t.Fatalf("manual answer=%q reason=%q calls=%d", answer, reason, calls)
	}
}

func TestMessagesC42M1ScheduledPreparationPersistsMessagesAndPassesExistingCheck(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := time.Now().UTC()
	question, err := repo.CreateTestQuestion(ctx, "u", "C4.2 FAKE scheduled", "fixture", []string{"answer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "selected", &GroupTestConfiguration{TestProtocolMessages, 30}); err != nil {
		t.Fatalf("Messages configuration cannot be saved: %v", err)
	}
	plan := c2ScheduleFixture(t, repo, ctx, "w", now, false)
	config := plan.QuestionAnswerScheduleConfig
	config.TargetMode = "groups"
	config.SelectedGroupIDs = []string{"selected"}
	config.SelectedAccountTargetIDs = []string{}
	config.QuestionIDs = []string{question.ID}
	config.Models = []string{"available"}
	plan, err = repo.UpdateQuestionAnswerSchedule(ctx, "u", "w", plan.ID, config, plan.TargetSelectionSnapshot, plan.Version, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "c42-scheduled", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	execution, err := repo.ClaimNextQuestionAnswerScheduleExecution(ctx, "u", "w", func() time.Time { return now })
	if err != nil || execution == nil || execution.ID != accepted.ID {
		t.Fatalf("claim=%+v err=%v", execution, err)
	}
	questions, configs, err := repo.ReadQuestionAnswerSchedulePreparationConfiguration(ctx, "u", "w", config.QuestionIDs)
	if err != nil {
		t.Fatal(err)
	}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected", Platform: "anthropic"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "1"}}}, credByAccount: map[string]upstream.ProbeCredential{"1": {BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}}}}
	prepRepo := &c2SchedulePreparationRepository{questions: questions, configs: configs}
	s, _ := c2SchedulePreparationFixture(t, reader, prepRepo)
	s.accounts = fakeAdminAccountResolver{id: "w"}
	prep := s.resolveQuestionAnswerScheduleExecution(ctx, *execution)
	defer func() {
		for _, reservation := range prep.reservations {
			s.releaseQuestionAnswerTargetStart(reservation)
		}
	}()
	if len(prep.outcome.Targets) != 1 || prep.outcome.Resolved == nil {
		t.Fatalf("preparation=%+v", prep.outcome)
	}
	target := prep.outcome.Targets[0]
	if target.Status != "pending" || target.TestConfigurationSnapshot.Protocol != TestProtocolMessages {
		t.Fatalf("scheduled protocol should freeze Messages: %+v", target)
	}
	if reason, err := repo.FreezeQuestionAnswerScheduleExecution(ctx, "u", "w", execution.ID, *prep.outcome.Resolved, prep.outcome.Targets, func() time.Time { return now }); err != nil || reason != "" {
		t.Fatalf("freeze=%s err=%v", reason, err)
	}
	records, created, err := repo.CreateScheduledQuestionAnswerBatch(ctx, "u", "w", execution.ID, target.TargetID, target.ID)
	if err != nil || !created || len(records) != 1 {
		t.Fatalf("scheduled batch=%+v created=%v err=%v", records, created, err)
	}
	frozen, err := repo.GetQuestionAnswerScheduleExecution(ctx, "u", "w", execution.ID)
	if err != nil || frozen == nil || !consistentScheduledQuestionAnswerRecords(*frozen, target, records) {
		t.Fatalf("existing scheduled consistency check failed: %v", err)
	}
	if records[0].RequestProtocol == nil || *records[0].RequestProtocol != TestProtocolMessages {
		t.Fatalf("scheduled record protocol=%+v", records[0])
	}
	calls := 0
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
		calls++
		c42AssertRequest(t, req, "available", "fixture", "high")
		return protocolServiceResponse(req, 200, `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`), nil
	})
	answer, reason := runner.Ask(ctx, upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}, "available", "fixture", QuestionAnswerReasoningEffortHigh, *records[0].RequestProtocol)
	if answer != "answer" || reason != "" || calls != 1 {
		t.Fatalf("scheduled answer=%q reason=%q calls=%d", answer, reason, calls)
	}
}
