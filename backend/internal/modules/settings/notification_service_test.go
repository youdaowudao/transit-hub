package settings

import (
	"context"
	"net/http"
	"testing"
)

type fakeNotificationRepository struct {
	channels NotificationChannelSettings
}

func (r *fakeNotificationRepository) GetNotificationChannels(context.Context, string, string) (NotificationChannelSettings, error) {
	return r.channels, nil
}

func (r *fakeNotificationRepository) SaveNotificationChannels(context.Context, string, string, NotificationChannelSettings) error {
	return nil
}

type fixedSettingsAccountResolver struct {
	id string
}

func (r fixedSettingsAccountResolver) RequireCurrentID(context.Context, string) (string, error) {
	return r.id, nil
}

func TestSendToBotsSkipsDisabledTelegram(t *testing.T) {
	calls := 0
	service := NewService(&http.Client{Transport: globalizationTransport(func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("disabled Telegram must not make a request")
		return nil, nil
	})}, nil)
	service.notificationRepo = &fakeNotificationRepository{channels: NotificationChannelSettings{
		Telegram: []TelegramChannelSettings{{ID: "disabled-bot", Enabled: false, BotToken: "fixture", ChatID: "fixture"}},
	}}
	service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "workspace-1"})
	service.SendToBots(context.Background(), "user-1", []string{"disabled-bot"}, "alert")
	if calls != 0 {
		t.Fatalf("disabled channel received %d requests", calls)
	}
}
