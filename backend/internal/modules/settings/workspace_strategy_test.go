package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type fakeStrategyRepository struct {
	strategies map[string]StrategySettings
	savedUser  string
	savedAdmin string
	saved      StrategySettings
}

func strategyTestKey(userID, adminAccountID string) string {
	return userID + "\x00" + adminAccountID
}

func (r *fakeStrategyRepository) GetStrategy(_ context.Context, userID, adminAccountID string) (StrategySettings, error) {
	return r.strategies[strategyTestKey(userID, adminAccountID)], nil
}

func (r *fakeStrategyRepository) ListStrategies(context.Context) ([]WorkspaceStrategy, error) {
	result := make([]WorkspaceStrategy, 0, len(r.strategies))
	for key, strategy := range r.strategies {
		_ = key
		result = append(result, WorkspaceStrategy{Settings: strategy})
	}
	return result, nil
}

func (r *fakeStrategyRepository) SaveStrategy(_ context.Context, userID, adminAccountID string, strategy StrategySettings) error {
	r.savedUser = userID
	r.savedAdmin = adminAccountID
	r.saved = strategy
	return nil
}

func TestSaveStrategyCallbackIncludesWorkspace(t *testing.T) {
	repository := &fakeStrategyRepository{}
	service := NewService(nil, nil)
	service.strategyRepo = repository
	service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "workspace-b"})

	var callbackUser, callbackAdmin string
	var callbackSettings StrategySettings
	service.OnStrategyChanged = func(userID, adminAccountID string, strategy StrategySettings) {
		callbackUser = userID
		callbackAdmin = adminAccountID
		callbackSettings = strategy
	}

	result, err := service.SaveStrategy(context.Background(), "user-1", StrategySettings{EnableRefreshInterval: true, RefreshInterval: 120})
	if err != nil {
		t.Fatalf("SaveStrategy() error = %v", err)
	}
	if repository.savedUser != "user-1" || repository.savedAdmin != "workspace-b" {
		t.Fatalf("saved workspace = (%q, %q)", repository.savedUser, repository.savedAdmin)
	}
	if callbackUser != "user-1" || callbackAdmin != "workspace-b" || !reflect.DeepEqual(callbackSettings, result) {
		t.Fatalf("callback = (%q, %q, %#v), want saved workspace and settings", callbackUser, callbackAdmin, callbackSettings)
	}
}

func TestGetStrategyForWorkspaceDoesNotUseCurrentWorkspace(t *testing.T) {
	want := StrategySettings{EnableBalanceWarning: true, DefaultBalanceThreshold: 42}
	repository := &fakeStrategyRepository{strategies: map[string]StrategySettings{
		strategyTestKey("user-1", "workspace-b"): want,
	}}
	service := NewService(nil, nil)
	service.strategyRepo = repository

	got, err := service.GetStrategyForWorkspace(context.Background(), "user-1", "workspace-b")
	if err != nil {
		t.Fatalf("GetStrategyForWorkspace() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetStrategyForWorkspace() = %#v, want %#v", got, want)
	}
}

func TestSendFormattedToWorkspaceBotsUsesExplicitWorkspace(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	repository := &capturingNotificationRepository{channels: NotificationChannelSettings{
		Wecom: []WebhookChannelSettings{{ID: "workspace-b-bot", Enabled: true, Webhook: server.URL}},
	}}
	service := NewService(server.Client(), nil)
	service.notificationRepo = repository
	service.SendFormattedToWorkspaceBots(context.Background(), "user-1", "workspace-b", []string{"workspace-b-bot"}, "alert", NotificationTemplateFormatText)

	if repository.adminAccountID != "workspace-b" {
		t.Fatalf("notification repository workspace = %q, want workspace-b", repository.adminAccountID)
	}
	if requests != 1 {
		t.Fatalf("workspace-b webhook requests = %d, want 1", requests)
	}
}

type capturingNotificationRepository struct {
	channels       NotificationChannelSettings
	adminAccountID string
}

func (r *capturingNotificationRepository) GetNotificationChannels(_ context.Context, _, adminAccountID string) (NotificationChannelSettings, error) {
	r.adminAccountID = adminAccountID
	return r.channels, nil
}

func (r *capturingNotificationRepository) SaveNotificationChannels(context.Context, string, string, NotificationChannelSettings) error {
	return nil
}
