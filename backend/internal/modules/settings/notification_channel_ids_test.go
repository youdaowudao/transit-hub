package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type stableIDNotificationRepository struct {
	raw    []byte
	fail   bool
	writes int
}

func (r *stableIDNotificationRepository) GetNotificationChannels(context.Context, string, string) (NotificationChannelSettings, error) {
	var settings NotificationChannelSettings
	err := unmarshalNotificationChannelSettings(r.raw, &settings)
	return settings, err
}
func (r *stableIDNotificationRepository) SaveNotificationChannels(context.Context, string, string, NotificationChannelSettings) error {
	return errors.New("read must not replace channel settings")
}
func (r *stableIDNotificationRepository) PersistTelegramChannelIDs(_ context.Context, _ string, _ string, original, normalized NotificationChannelSettings) error {
	if r.fail {
		return errors.New("fixture persistence unavailable")
	}
	telegram, err := telegramJSONWithStableIDs(r.raw, original, normalized)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(r.raw, &root); err != nil {
		return err
	}
	root["telegram"] = telegram
	r.raw, _ = json.Marshal(root)
	r.writes++
	return nil
}

func TestTelegramIDsStayStableAcrossRepeatedReadsAndStrategySave(t *testing.T) {
	for _, raw := range []string{
		`{"dingtalk":[{"id":"retired","secret":"unchanged"}],"telegram":{"enabled":true,"botToken":"fixture","chatId":"original-chat","futureBotFlag":true}}`,
		`{"dingtalk":[{"id":"retired","secret":"unchanged"}],"telegram":[{"id":"duplicate","enabled":true,"botToken":"fixture","chatId":"first"},{"id":"duplicate","enabled":false,"botToken":"fixture","chatId":"second","futureBotFlag":true}]}`,
	} {
		repo := &stableIDNotificationRepository{raw: []byte(raw)}
		service := NewService(nil, nil)
		service.notificationRepo = repo
		service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "workspace"})
		strategyRepo := &fakeStrategyRepository{}
		service.strategyRepo = strategyRepo
		first, err := service.GetNotificationChannels(context.Background(), "user")
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.GetNotificationChannels(context.Background(), "user")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, second) || repo.writes != 1 {
			t.Fatal("Telegram IDs must be persisted exactly once and stable")
		}
		selected := first.Telegram[len(first.Telegram)-1].ID
		if selected == "" {
			t.Fatal("existing blank ID must be repaired")
		}
		saved, err := service.SaveStrategy(context.Background(), "user", StrategySettings{EnableBalanceWarning: true, BalanceNotifyBotIDs: []string{selected}})
		if err != nil {
			t.Fatal(err)
		}
		strategyRepo.strategies = map[string]StrategySettings{strategyTestKey("user", "workspace"): saved}
		read, err := service.GetStrategy(context.Background(), "user")
		if err != nil {
			t.Fatal(err)
		}
		if !read.EnableBalanceWarning || read.BalanceNotifyRecipientsInvalid || !reflect.DeepEqual(read.BalanceNotifyBotIDs, []string{selected}) {
			t.Fatal("repeated reads must keep a saved original Telegram recipient enabled")
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(repo.raw, &root); err != nil {
			t.Fatal(err)
		}
		if string(root["dingtalk"]) != `[{"id":"retired","secret":"unchanged"}]` {
			t.Fatal("ID normalization must not delete or modify legacy platform JSON")
		}
		var bots []map[string]json.RawMessage
		if err := json.Unmarshal(root["telegram"], &bots); err != nil {
			t.Fatal(err)
		}
		if string(bots[len(bots)-1]["futureBotFlag"]) != "true" {
			t.Fatal("ID-only persistence must retain unknown Telegram fields")
		}
	}
}

func TestTelegramIDPersistenceFailureNeverExposesTemporaryRecipient(t *testing.T) {
	repo := &stableIDNotificationRepository{raw: []byte(`{"telegram":[{"enabled":true,"botToken":"fixture","chatId":"chat"}]}`), fail: true}
	service := NewService(nil, nil)
	service.notificationRepo = repo
	service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "workspace"})
	read, err := service.GetNotificationChannels(context.Background(), "user")
	if err == nil || len(read.Telegram) != 0 {
		t.Fatal("failed ID persistence must return an error without a transient recipient")
	}
}
