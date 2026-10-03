package settings

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGlobalizationNotificationFailureDoesNotBlockWorkspaceRefreshRestoration(t *testing.T) {
	svc := NewService(nil, nil)
	svc.notificationRepo = unavailableNotificationRepo{}
	svc.strategyRepo = &fakeStrategyRepository{strategies: map[string]StrategySettings{
		strategyTestKey("user", "one"): {EnableRefreshInterval: true, RefreshInterval: 90, EnableBalanceWarning: true, BalanceNotifyBotIDs: []string{"original"}},
		strategyTestKey("user", "two"): {EnableRefreshInterval: true, RefreshInterval: 120, EnableMultiplierAlert: true, MultiplierNotifyBotIDs: []string{"original"}},
	}}
	rows, err := svc.ListStrategies(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("notification failure must not block restoring either workspace: rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if !row.Settings.EnableRefreshInterval || (row.Settings.RefreshInterval != 90 && row.Settings.RefreshInterval != 120) {
			t.Fatal("refresh business configuration changed")
		}
	}
}

func TestGlobalizationNotificationFailureDoesNotBlockAfterSyncStrategyRead(t *testing.T) {
	svc := NewService(nil, nil)
	svc.notificationRepo = unavailableNotificationRepo{}
	repo := &fakeStrategyRepository{strategies: map[string]StrategySettings{
		strategyTestKey("user", "workspace"): {EnableRefreshInterval: true, RefreshInterval: 90, EnableAutoChangeMultiplier: true, DefaultBalanceThreshold: 700.125, EnableBalanceWarning: true, BalanceNotifyBotIDs: []string{"original"}},
	}}
	svc.strategyRepo = repo
	strategy, err := svc.GetStrategyForWorkspace(context.Background(), "user", "workspace")
	if err != nil || !strategy.EnableRefreshInterval || !strategy.EnableAutoChangeMultiplier || strategy.DefaultBalanceThreshold != 700.125 {
		t.Fatalf("notification failure must not abort AfterSync business path: err=%v", err)
	}
	if repo.savedUser != "" {
		t.Fatal("runtime read must not persist fallback notification settings")
	}
}

func TestGlobalizationCleanupCountsAmountUnitCustomTemplatesWithoutRewriting(t *testing.T) {
	for _, template := range []string{"余额 {balance} 元", "阈值 {threshold}元", "奖励 {amount} 元", "售价 700.25元", "<strong>{balance}</strong> 元", "{threshold}&nbsp;元", "**{balance}** 元", "`{amount}` 元", "[{price}](https://fixture.invalid) 元"} {
		t.Run(template, func(t *testing.T) {
			before, _ := json.Marshal(map[string]any{"balanceTemplate": template, "defaultBalanceThreshold": 700.25})
			plan, err := PlanNotificationCleanup("user", "workspace", []CleanupRow{{Table: "strategy_settings", Before: before}})
			if err != nil || plan.Summary.CustomLegacyTemplates != 1 || len(plan.Rows) != 0 {
				t.Fatalf("custom amount-unit template must be reported once and kept: count=%d changes=%d err=%v", plan.Summary.CustomLegacyTemplates, len(plan.Rows), err)
			}
		})
	}
}
