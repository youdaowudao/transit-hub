package connection_health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"transithub/backend/internal/modules/upstream"
)

func TestMessagesC41K6ManualQuestionAnswerPersistsChatAndSendsChat(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	question, err := repo.CreateTestQuestion(ctx, "u", "C4.1 FAKE question", "fixture", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "g", &GroupTestConfiguration{c41Messages, 20}); err != nil {
		t.Fatalf("Messages configuration cannot be saved: %v", err)
	}
	snapshot := QuestionAnswerConfigurationSnapshot{AdminAccountID: "w", InventoryComplete: true, Memberships: []TestConfigurationSource{{AdminGroupID: "g"}}}
	records, err := repo.CreateQuestionAnswerBatch(ctx, "u", "sub2api:w:1", "c41-manual", []string{"m"}, []string{question.ID}, QuestionAnswerReasoningEffortHigh, 1, snapshot)
	if err != nil || len(records) != 1 {
		t.Fatalf("manual batch=%+v err=%v", records, err)
	}
	if records[0].RequestProtocol == nil || *records[0].RequestProtocol != TestProtocolChatCompletions {
		t.Fatalf("manual record should use Chat: %+v", records[0])
	}
	calls := 0
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/v1/chat/completions" {
			t.Errorf("question answer path=%s", req.URL.Path)
		}
		return protocolServiceResponse(req, 200, `{"choices":[{"message":{"content":"answer"}}]}`), nil
	})
	answer, reason := runner.Ask(ctx, upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture"}, records[0].ModelName, records[0].QuestionBody, *records[0].ReasoningEffort, *records[0].RequestProtocol)
	if answer != "answer" || reason != "" || calls != 1 {
		t.Fatalf("manual answer=%q reason=%q calls=%d", answer, reason, calls)
	}
}

func TestMessagesC41K6ScheduledPreparationPersistsChatAndPassesExistingCheck(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := time.Now().UTC()
	question, err := repo.CreateTestQuestion(ctx, "u", "C4.1 FAKE scheduled", "fixture", []string{"answer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveGroupTestConfiguration(ctx, "u", "w", "selected", &GroupTestConfiguration{c41Messages, 30}); err != nil {
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
	accepted, err := repo.CreateRunNowQuestionAnswerScheduleExecution(ctx, "u", "w", plan.ID, "c41-scheduled", func() time.Time { return now })
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
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected", Platform: "anthropic"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "1"}}}, credByAccount: map[string]upstream.ProbeCredential{"1": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}}
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
	if target.Status != "pending" || target.TestConfigurationSnapshot.Protocol != TestProtocolChatCompletions {
		t.Fatalf("scheduled protocol should freeze Chat: %+v", target)
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
	if records[0].RequestProtocol == nil || *records[0].RequestProtocol != TestProtocolChatCompletions {
		t.Fatalf("scheduled record protocol=%+v", records[0])
	}
	calls := 0
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/v1/chat/completions" {
			t.Errorf("scheduled path=%s", req.URL.Path)
		}
		return protocolServiceResponse(req, 200, `{"choices":[{"message":{"content":"answer"}}]}`), nil
	})
	answer, reason := runner.Ask(ctx, upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture"}, "available", "fixture", QuestionAnswerReasoningEffortHigh, *records[0].RequestProtocol)
	if answer != "answer" || reason != "" || calls != 1 {
		t.Fatalf("scheduled answer=%q reason=%q calls=%d", answer, reason, calls)
	}
}

func TestMessagesC41K7SevenColumnsAndUpgradeOrders(t *testing.T) {
	columns := [][2]string{{"connection_health_group_test_configs", "protocol"}, {"connection_health_states", "last_probe_protocol"}, {"connection_health_states", "last_applied_probe_protocol"}, {"connection_health_states", "counter_protocol"}, {"connection_health_states", "health_evidence_protocol"}, {"connection_health_states", "last_success_protocol"}, {"connection_health_events", "request_protocol"}}
	for _, order := range []string{"old-tables-then-migration", "schema-only", "migration-then-schema"} {
		t.Run(order, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			repo := NewRepository(pool)
			ctx := context.Background()
			migration, err := os.ReadFile("../../database/migrations/000038_connection_health_test_protocol_messages.sql")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			// On the baseline there is no upgrade; execute a no-op so RED is an
			// actual rejected Messages value rather than a missing-file failure.
			if len(migration) == 0 {
				migration = []byte("SELECT 1;")
			}
			upgrade := func() {
				t.Helper()
				if _, err := pool.Exec(ctx, string(migration)); err != nil {
					t.Fatal(err)
				}
			}
			if order == "migration-then-schema" {
				_, err := pool.Exec(ctx, `CREATE TABLE connection_health_group_test_configs(user_id text NOT NULL,admin_account_id text NOT NULL,admin_group_id text NOT NULL,protocol text NOT NULL CHECK(protocol IN ('chat_completions','responses')),probe_timeout_seconds integer NOT NULL CHECK(probe_timeout_seconds BETWEEN 5 AND 120),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,admin_account_id,admin_group_id))`)
				if err != nil {
					t.Fatal(err)
				}
				upgrade()
			}
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if order == "old-tables-then-migration" {
				for i, column := range columns {
					table, field := column[0], column[1]
					rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attname=$2 WHERE c.conrelid=$1::regclass AND c.contype='c' AND c.conkey=ARRAY[a.attnum] AND pg_get_constraintdef(c.oid) LIKE '%chat_completions%'`, table, field)
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
					for _, name := range names {
						if _, err := pool.Exec(ctx, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" DROP CONSTRAINT "+pgx.Identifier{name}.Sanitize()); err != nil {
							t.Fatal(err)
						}
					}
					query := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s IS NULL OR %s IN ('chat_completions','responses'))", pgx.Identifier{table}.Sanitize(), pgx.Identifier{fmt.Sprintf("c41_old_%d", i)}.Sanitize(), pgx.Identifier{field}.Sanitize(), pgx.Identifier{field}.Sanitize())
					if _, err := pool.Exec(ctx, query); err != nil {
						t.Fatal(err)
					}
				}
				upgrade()
				upgrade()
			}
			// Exercise actual writes to each of the seven original table columns.
			for _, query := range []string{
				`INSERT INTO connection_health_group_test_configs(user_id,admin_account_id,admin_group_id,protocol,probe_timeout_seconds) VALUES('u','w','g','chat_completions',20)`,
				`INSERT INTO connection_health_states(connection_id,model_name,user_id,upstream_site_id,upstream_group_name,state,current_weight) VALUES('target','m','u','site','group','healthy',100)`,
				`INSERT INTO connection_health_events(id,connection_id,user_id,result) VALUES('event','target','u','ok')`,
				`INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body) VALUES('qa','u','target','batch','m','q','Q','body')`,
			} {
				if _, err := pool.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			for _, column := range columns {
				query := "UPDATE " + pgx.Identifier{column[0]}.Sanitize() + " SET " + pgx.Identifier{column[1]}.Sanitize() + "=$1"
				if column[1] == "health_evidence_protocol" {
					query += ",health_evidence_status='valid'"
				}
				if _, err := pool.Exec(ctx, query, "messages"); err != nil {
					t.Errorf("%s.%s rejected messages: %v", column[0], column[1], err)
				}
				if _, err := pool.Exec(ctx, query, "bogus"); err == nil {
					t.Errorf("%s.%s accepted bogus", column[0], column[1])
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET request_protocol='messages'`); err == nil {
				t.Error("question answer protocol constraint was widened")
			}
			upgrade()
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatalf("repeat schema: %v", err)
			}
			upgrade()
			for _, column := range columns {
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attname=$2 WHERE c.conrelid=$1::regclass AND c.contype='c' AND c.conkey=ARRAY[a.attnum] AND pg_get_constraintdef(c.oid) LIKE '%chat_completions%'`, column[0], column[1]).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("duplicate/missing single-field value check %s.%s: %d", column[0], column[1], count)
				}
			}
		})
	}
}
