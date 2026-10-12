package connection_health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

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
			if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET request_protocol='messages'`); err != nil {
				t.Errorf("question answer protocol rejected Messages: %v", err)
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
