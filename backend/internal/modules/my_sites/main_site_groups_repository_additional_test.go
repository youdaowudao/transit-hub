package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"strings"
	"testing"
	"time"
)

type groupReassignmentTx struct {
	pgx.Tx
	current                    RealConnection
	commitErr                  error
	commits, rollbacks, writes int
	noState                    bool
}

func (tx *groupReassignmentTx) Commit(context.Context) error   { tx.commits++; return tx.commitErr }
func (tx *groupReassignmentTx) Rollback(context.Context) error { tx.rollbacks++; return nil }
func (tx *groupReassignmentTx) Exec(_ context.Context, q string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(q, "UPDATE real_connections") {
		tx.writes++
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (tx *groupReassignmentTx) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	return groupReassignmentRow{tx: tx, state: strings.Contains(q, "FROM my_site_states")}
}

type groupReassignmentRow struct {
	tx    *groupReassignmentTx
	state bool
}

func (row groupReassignmentRow) Scan(dest ...any) error {
	if row.state {
		return pgx.ErrNoRows
	}
	c := row.tx.current
	values := []string{c.ID, c.UserID, c.WorkspaceAdminAccountID, c.UpstreamSiteID, c.UpstreamGroupID, c.UpstreamGroupName, c.UpstreamKeyID, "", c.AdminAccountID, c.AdminAccountName}
	for i, v := range values {
		*dest[i].(*string) = v
	}
	ids, _ := json.Marshal(c.OwnGroupIDs)
	names, _ := json.Marshal(c.OwnGroupNames)
	*dest[10].(*[]byte) = ids
	*dest[11].(*[]byte) = names
	for i, v := range []string{c.GroupType, c.ProvisioningMode, c.Status, c.UpstreamPlatform, c.AdminPlatform} {
		*dest[12+i].(*string) = v
	}
	*dest[17].(*bool) = c.PricingMappingEnabled
	*dest[18].(*string) = ""
	*dest[19].(*time.Time) = time.Now()
	return nil
}
func TestMainSiteGroupsRepositoryCommitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commit    error
		uncertain bool
	}{{"committed", nil, false}, {"rollback", pgx.ErrTxCommitRollback, false}, {"lost commit response", errors.New("connection lost after commit"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			old := RealConnection{ID: "c", UserID: "u", WorkspaceAdminAccountID: "w", OwnGroupIDs: []string{"10"}, OwnGroupNames: []string{"old"}}
			tx := &groupReassignmentTx{current: old, commitErr: tc.commit}
			err := reassignRealConnectionGroupsChecked(t.Context(), old, []string{"11"}, []string{"new"}, true, func(context.Context) (pgx.Tx, error) { return tx, nil })
			if tc.commit == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var failure *GroupReassignmentError
				if !errors.As(err, &failure) || failure.Uncertain != tc.uncertain || failure.Step != "提交对接分组与调价数据源" {
					t.Fatalf("wrong commit evidence: %v", err)
				}
			}
			if tx.commits != 1 || tx.writes != 1 || tx.rollbacks != 1 {
				t.Fatalf("transaction lifecycle commits=%d writes=%d rollbacks=%d", tx.commits, tx.writes, tx.rollbacks)
			}
		})
	}
}
func TestMainSiteGroupsRepositoryMissingPricingState(t *testing.T) {
	old := RealConnection{ID: "c", UserID: "u", WorkspaceAdminAccountID: "w", PricingMappingEnabled: true}
	tx := &groupReassignmentTx{current: old, noState: true}
	err := reassignRealConnectionGroupsChecked(t.Context(), old, []string{"11"}, []string{"new"}, true, func(context.Context) (pgx.Tx, error) { return tx, nil })
	if err == nil || !strings.Contains(mainGroupReassignmentDetail(err), "调价数据源记录已不存在") || tx.writes != 0 || tx.commits != 0 {
		t.Fatalf("missing state was not safely reported: %v", err)
	}
}
func TestMainSiteGroupsRepositoryRetainsUnmovedSourcesAndScope(t *testing.T) {
	for _, mode := range []string{"same workspace", "other user", "other workspace"} {
		t.Run(mode, func(t *testing.T) {
			_, repo := openMySitesCleanupPostgres(t)
			ctx := t.Context()
			state := State{UserID: "u", AdminAccountID: "w", Mappings: []GroupMapping{{OwnGroup: "A", UpstreamTargets: []UpstreamGroupRef{{SiteID: "s", GroupName: "up"}}}, {OwnGroup: "B", UpstreamTargets: []UpstreamGroupRef{{SiteID: "s", GroupName: "up"}, {SiteID: "keep", GroupName: "keep"}}}}}
			if err := repo.Save(ctx, state); err != nil {
				t.Fatal(err)
			}
			old := RealConnection{ID: "c", UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "s", UpstreamGroupName: "up", AdminAccountID: "101", OwnGroupIDs: []string{"10", "11"}, OwnGroupNames: []string{"A", "B"}, PricingMappingEnabled: true, CreatedAt: time.Now().Format(time.RFC3339Nano)}
			if err := repo.SaveRealConnection(ctx, old); err != nil {
				t.Fatal(err)
			}
			expected := old
			if mode == "other user" {
				expected.UserID = "different"
			}
			if mode == "other workspace" {
				expected.WorkspaceAdminAccountID = "different"
			}
			err := repo.ReassignRealConnectionGroupsChecked(ctx, expected, []string{"11", "12"}, []string{"B", "C"}, true)
			after, readErr := repo.Get(ctx, "u", "w")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode != "same workspace" {
				if err == nil || !reflect.DeepEqual(after.Mappings, state.Mappings) {
					t.Fatal("cross-scope request changed source workspace")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mapping := map[string][]UpstreamGroupRef{}
			for _, m := range after.Mappings {
				mapping[m.OwnGroup] = m.UpstreamTargets
			}
			if len(mapping["A"]) != 0 || !reflect.DeepEqual(mapping["B"], state.Mappings[1].UpstreamTargets) || !reflect.DeepEqual(mapping["C"], state.Mappings[0].UpstreamTargets) {
				t.Fatalf("wrong migration: %+v", mapping)
			}
			saved, _ := repo.GetRealConnection(ctx, "c", "u", "w")
			if !reflect.DeepEqual(saved.OwnGroupIDs, []string{"11", "12"}) || !reflect.DeepEqual(saved.OwnGroupNames, []string{"B", "C"}) {
				t.Fatal("ID/name pairs lost")
			}
			if err := repo.ReassignRealConnectionGroupsChecked(ctx, *saved, []string{"11", "12"}, []string{"B改名", "C改名"}, false); err != nil {
				t.Fatal(err)
			}
			saved, _ = repo.GetRealConnection(ctx, "c", "u", "w")
			if !reflect.DeepEqual(saved.OwnGroupNames, []string{"B改名", "C改名"}) {
				t.Fatal("updated names not saved")
			}
		})
	}
}
