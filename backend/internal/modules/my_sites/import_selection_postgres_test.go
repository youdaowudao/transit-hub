package my_sites

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Explicit TEST_DATABASE_URL only, on the existing local cluster in the helper's
// isolated schema. No HTTP server or background process starts.
func TestImportSelectionPostgresAtomicTargetCompetition(t *testing.T) {
	for _, mapping := range []bool{false, true} {
		t.Run(fmt.Sprintf("mapping-%t", mapping), func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			prepareCleanupState(t, repo, nil)
			if _, err := pool.Exec(t.Context(), `CREATE TABLE upstream_sites (id text, user_id text, admin_account_id text, name text)`); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				conn := RealConnection{ID: fmt.Sprintf("selection-%d", i), UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "selection-source", UpstreamGroupID: "70", UpstreamGroupName: fmt.Sprintf("source-%d", i), UpstreamKeyID: fmt.Sprintf("key-%d", i), AdminAccountID: fmt.Sprintf("account-%d", i), OwnGroupIDs: []string{"7"}, OwnGroupNames: []string{"own-a"}, GroupType: "openai", ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusActive, UpstreamPlatform: "sub2api", AdminPlatform: "sub2api", PricingMappingEnabled: mapping, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- repo.SaveRealConnectionWithPricingMapping(t.Context(), conn)
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			success, rejected := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if connectionCommitOutcome(err) == CommitConfirmedNotCommitted && errors.Is(err, requestError(ErrorConnectionExists)) {
					rejected++
				} else {
					t.Fatalf("unexpected transaction result: %v", err)
				}
			}
			if success != 1 || rejected != 1 {
				t.Fatal("same target was not saved exactly once")
			}
			connections, err := repo.ListRealConnections(t.Context(), "user-1", "workspace-1")
			if err != nil || len(connections) != 1 {
				t.Fatal("database contains duplicate or missing connection")
			}
			state, err := repo.Get(t.Context(), "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			if mapping && (len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 || state.Mappings[0].UpstreamTargets[0].GroupName != connections[0].UpstreamGroupName) {
				t.Fatal("losing transaction wrote its mapping")
			}
			if !mapping && len(state.Mappings[0].UpstreamTargets) != 0 {
				t.Fatal("disabled mapping changed pricing")
			}
			if _, err := pool.Exec(t.Context(), `UPDATE real_connections SET status = 'missing'`); err != nil {
				t.Fatal(err)
			}
			fresh := connections[0]
			fresh.ID = "after-missing"
			fresh.OperationID = "after-missing"
			if err := repo.SaveRealConnectionWithPricingMapping(t.Context(), fresh); err != nil {
				t.Fatal("missing historical target blocked rebuilding")
			}
			if _, err := pool.Exec(t.Context(), `UPDATE real_connections SET status = '' WHERE id = 'after-missing'`); err != nil {
				t.Fatal(err)
			}
			fresh.ID = "after-legacy-active"
			if err := repo.SaveRealConnectionWithPricingMapping(t.Context(), fresh); !errors.Is(err, requestError(ErrorConnectionExists)) {
				t.Fatal("historical empty active status allowed duplicate")
			}
			fresh.WorkspaceAdminAccountID = "workspace-2"
			if err := repo.Save(t.Context(), State{UserID: "user-1", AdminAccountID: "workspace-2"}); err != nil {
				t.Fatal(err)
			}
			if err := repo.SaveRealConnectionWithPricingMapping(t.Context(), fresh); err != nil {
				t.Fatal("other workspace was blocked by original target")
			}
		})
	}
}
