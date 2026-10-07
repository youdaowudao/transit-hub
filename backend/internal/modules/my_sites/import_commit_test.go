package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type c5Transaction struct {
	t                                                          *testing.T
	beginErr, commitErr, rollbackErr, rowErr, errorOnStatement error
	failStatement                                              string
	commits, rollbacks, queries                                int
	statements                                                 []string
	insertArgs                                                 []any
	mappingJSON                                                []byte
}

func (tx *c5Transaction) Begin(context.Context) (pgx.Tx, error) { return tx, tx.beginErr }
func (tx *c5Transaction) Commit(context.Context) error          { tx.commits++; return tx.commitErr }
func (tx *c5Transaction) Rollback(context.Context) error        { tx.rollbacks++; return tx.rollbackErr }
func (tx *c5Transaction) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, query)
	if strings.Contains(query, tx.failStatement) && tx.failStatement != "" {
		return pgconn.CommandTag{}, tx.errorOnStatement
	}
	if strings.Contains(query, "INSERT INTO real_connections") {
		tx.insertArgs = args
	}
	if strings.Contains(query, "UPDATE my_site_states") {
		tx.mappingJSON = []byte(args[5].(string))
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (tx *c5Transaction) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.queries++
	return c5TransactionRow{tx}
}
func (tx *c5Transaction) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	tx.t.Fatal("unexpected CopyFrom")
	return 0, nil
}
func (tx *c5Transaction) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	tx.t.Fatal("unexpected SendBatch")
	return nil
}
func (tx *c5Transaction) LargeObjects() pgx.LargeObjects {
	tx.t.Fatal("unexpected LargeObjects")
	return pgx.LargeObjects{}
}
func (tx *c5Transaction) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	tx.t.Fatal("unexpected Prepare")
	return nil, nil
}
func (tx *c5Transaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	tx.t.Fatal("unexpected Query")
	return nil, nil
}
func (tx *c5Transaction) Conn() *pgx.Conn { tx.t.Fatal("unexpected Conn"); return nil }

type c5TransactionRow struct{ tx *c5Transaction }

func (row c5TransactionRow) Scan(dest ...any) error {
	if row.tx.rowErr != nil {
		return row.tx.rowErr
	}
	for i, value := range []string{"user-1", "admin-1", "https://main.invalid", ""} {
		*dest[i].(*string) = value
	}
	*dest[4].(*[]byte) = []byte(`{}`)
	*dest[5].(*[]byte) = []byte(`[]`)
	*dest[6].(*[]byte) = []byte(`[]`)
	return nil
}

func TestC5ImportActualTransactionClassifiesCommitAndAtomicMapping(t *testing.T) {
	for _, mode := range []string{"committed", "begin-failure", "state-read-failure", "mapping-failure", "insert-failure", "commit-rollback", "commit-disconnect", "rollback-success-does-not-prove-commit"} {
		t.Run(mode, func(t *testing.T) {
			tx := &c5Transaction{t: t}
			synthetic := errors.New("synthetic transaction failure")
			switch mode {
			case "begin-failure":
				tx.beginErr = synthetic
			case "state-read-failure":
				tx.rowErr = synthetic
			case "mapping-failure":
				tx.failStatement = "UPDATE my_site_states"
				tx.errorOnStatement = synthetic
			case "insert-failure":
				tx.failStatement = "INSERT INTO real_connections"
				tx.errorOnStatement = synthetic
			case "commit-rollback":
				tx.commitErr = pgx.ErrTxCommitRollback
			case "commit-disconnect", "rollback-success-does-not-prove-commit":
				tx.commitErr = synthetic
			}
			conn := RealConnection{ID: "c5-connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", UpstreamGroupID: "70", UpstreamGroupName: "upstream", UpstreamKeyID: "11", AdminAccountID: "22", OwnGroupIDs: []string{"7", "8", "9"}, OwnGroupNames: []string{"group-a", "group-b", "group-c"}, PricingMappingEnabled: true, OperationID: "c5-operation", Status: ConnectionStatusActive}
			err := saveRealConnectionWithPricingMapping(t.Context(), conn, tx.Begin)
			want := CommitConfirmedNotCommitted
			if mode == "committed" {
				want = CommitCommitted
			}
			if mode == "commit-disconnect" || mode == "rollback-success-does-not-prove-commit" {
				want = CommitUncertain
			}
			if connectionCommitOutcome(err) != want {
				t.Fatalf("commit outcome=%s wanted %s", connectionCommitOutcome(err), want)
			}
			if mode == "begin-failure" {
				if tx.commits != 0 || tx.rollbacks != 0 {
					t.Fatal("failed Begin reached transaction")
				}
				return
			}
			if mode == "committed" {
				if tx.commits != 1 || tx.rollbacks != 0 || tx.queries != 1 || len(tx.statements) != 2 {
					t.Fatal("atomic save was not a single complete transaction")
				}
				var mappings []GroupMapping
				if json.Unmarshal(tx.mappingJSON, &mappings) != nil || len(mappings) != 3 {
					t.Fatal("all groups were not added to same transaction pricing mapping")
				}
				seen := map[string]bool{}
				for _, mapping := range mappings {
					seen[mapping.OwnGroup] = true
					if len(mapping.UpstreamTargets) != 1 || mapping.UpstreamTargets[0].SiteID != "site-1" || mapping.UpstreamTargets[0].GroupName != "upstream" {
						t.Fatal("atomic mapping lost upstream identity")
					}
				}
				if !seen["group-a"] || !seen["group-b"] || !seen["group-c"] {
					t.Fatal("atomic mapping lost selected groups")
				}
				if len(tx.insertArgs) != 20 || tx.insertArgs[10] != `["7","8","9"]` || tx.insertArgs[17] != true {
					t.Fatal("full binding/pricing values were not inserted atomically")
				}
			} else if tx.rollbacks != 1 {
				t.Fatal("failed transaction was not rolled back")
			}
			if mode == "state-read-failure" || mode == "mapping-failure" || mode == "insert-failure" {
				if tx.commits != 0 {
					t.Fatal("failed transaction statement attempted Commit")
				}
			}
		})
	}
}
