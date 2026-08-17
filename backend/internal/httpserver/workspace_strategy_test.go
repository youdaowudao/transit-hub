package httpserver

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
)

type staticWorkspaceStrategies struct {
	items []settings.WorkspaceStrategy
}

func (s staticWorkspaceStrategies) ListStrategies(context.Context) ([]settings.WorkspaceStrategy, error) {
	return s.items, nil
}

type capturedWorkspaceRefresh struct {
	userID         string
	adminAccountID string
	config         upstream.RefreshConfig
}

type capturingWorkspaceRefresher struct {
	items []capturedWorkspaceRefresh
}

func (r *capturingWorkspaceRefresher) SetWorkspaceRefreshConfig(userID, adminAccountID string, config upstream.RefreshConfig) {
	r.items = append(r.items, capturedWorkspaceRefresh{userID: userID, adminAccountID: adminAccountID, config: config})
}

func TestRestoreWorkspaceRefreshConfigsAppliesEveryStrategy(t *testing.T) {
	provider := staticWorkspaceStrategies{items: []settings.WorkspaceStrategy{
		{UserID: "user-1", AdminAccountID: "workspace-a", Settings: settings.StrategySettings{EnableRefreshInterval: true, RefreshInterval: 120}},
		{UserID: "user-1", AdminAccountID: "workspace-b", Settings: settings.StrategySettings{EnableRefreshInterval: false, RefreshInterval: 300}},
	}}
	refresher := &capturingWorkspaceRefresher{}

	if err := restoreWorkspaceRefreshConfigs(context.Background(), provider, refresher); err != nil {
		t.Fatalf("restoreWorkspaceRefreshConfigs() error = %v", err)
	}
	if len(refresher.items) != 2 {
		t.Fatalf("applied refresh configs = %d, want 2", len(refresher.items))
	}
	if refresher.items[0].userID != "user-1" || refresher.items[0].adminAccountID != "workspace-a" || !refresher.items[0].config.Enabled || refresher.items[0].config.Interval != 120*time.Second {
		t.Fatalf("first refresh config = %#v", refresher.items[0])
	}
	if refresher.items[1].adminAccountID != "workspace-b" || refresher.items[1].config.Enabled || refresher.items[1].config.Interval != 300*time.Second {
		t.Fatalf("second refresh config = %#v", refresher.items[1])
	}
}
