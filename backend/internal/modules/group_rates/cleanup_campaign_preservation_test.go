package group_rates

import (
	"context"
	"reflect"
	"testing"
	"time"

	campaigns "transithub/backend/internal/modules/group_rate_campaigns"
)

// Verify preservation using the production campaign repository and tables,
// including already frozen recovery targets that no longer have a live rate.
func TestCleanupCampaignRecoveryTargetsPreservedPostgres(t *testing.T) {
	pool, service := cleanupFixture(t)
	ctx := context.Background()
	repository := campaigns.NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Hour)
	original := 3.0
	if err := repository.Insert(ctx, campaigns.Campaign{
		ID: "retained-campaign", UserID: "user-a", AdminAccountID: "workspace-a",
		Name: "existing running campaign", Status: campaigns.StatusRunning,
		Selection:  campaigns.Selection{Mode: campaigns.SelectionType, Types: []string{"gone-type"}},
		Adjustment: campaigns.Adjustment{Mode: campaigns.AdjustmentMultiply, Value: 2},
		StartMode:  campaigns.StartNow, EndMode: campaigns.EndScheduled, EndAt: &end,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveItems(ctx, []campaigns.CampaignItem{{
		ID: "retained-target", CampaignID: "retained-campaign", UserID: "user-a", AdminAccountID: "workspace-a",
		GroupID: "gone", GroupName: "gone", OriginalMultiplier: &original, CampaignMultiplier: 6,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateItemApply(ctx, "retained-target", &original, campaigns.ItemApplied, "already applied", nil); err != nil {
		t.Fatal(err)
	}
	beforeCampaign, err := repository.Get(ctx, "user-a", "workspace-a", "retained-campaign")
	if err != nil {
		t.Fatal(err)
	}
	beforeItems, err := repository.ListItems(ctx, "retained-campaign")
	if err != nil {
		t.Fatal(err)
	}
	cleanupSeed(t, pool, "gone-history", "site-a", "gone", "gone", "gone-type", 3, -1, false)
	cleanupSeed(t, pool, "live-history", "site-a", "live", "live", "live-type", 2, -1, false)
	groups := []SnapshotGroup{cleanupGroup("live", "live", 2)}
	stamp, version := cleanupSource(t, pool, groups)
	if err := cleanupApply(ctx, service, groups, stamp, version, true); err != nil {
		t.Fatal(err)
	}
	afterCampaign, err := repository.Get(ctx, "user-a", "workspace-a", "retained-campaign")
	if err != nil {
		t.Fatal(err)
	}
	afterItems, err := repository.ListItems(ctx, "retained-campaign")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeCampaign, afterCampaign) || !reflect.DeepEqual(beforeItems, afterItems) {
		t.Fatal("rate cleanup changed existing campaign or frozen recovery targets")
	}
	if len(afterItems) != 1 || afterItems[0].OriginalMultiplier == nil || *afterItems[0].OriginalMultiplier != original || afterItems[0].ApplyStatus != campaigns.ItemApplied {
		t.Fatal("existing recovery target lost its original rate or execution state")
	}
	if history, err := service.History(ctx, "user-a", "workspace-a", "site-a", "gone", ""); err != nil || len(history) != 0 {
		t.Fatalf("campaign preservation must not recreate obsolete rate history: %v", err)
	}
	if names, err := service.ListGroupNames(ctx, "user-a", "workspace-a", "", "gone-type", ""); err != nil || len(names) != 0 {
		t.Fatalf("new type selection included the cleared group: %v %v", names, err)
	}
	if names, err := service.ListGroupNames(ctx, "user-a", "workspace-a", "live", "live-type", "sub2api"); err != nil || len(names) != 1 || names[0] != "live" {
		t.Fatalf("new current-filter selection lost the valid group: %v %v", names, err)
	}
}
