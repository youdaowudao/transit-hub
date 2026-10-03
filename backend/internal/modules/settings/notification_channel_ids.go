package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
)

type notificationIDRepository interface {
	PersistTelegramChannelIDs(context.Context, string, string, NotificationChannelSettings, NotificationChannelSettings) error
}

func (s *Service) notificationChannelsForWorkspace(ctx context.Context, userID, adminAccountID string) (NotificationChannelSettings, error) {
	original, err := s.notificationRepo.GetNotificationChannels(ctx, userID, adminAccountID)
	if err != nil {
		return NotificationChannelSettings{}, err
	}
	normalized := normalizeNotificationChannelSettings(original)
	needsPersist := false
	for i, bot := range original.Telegram {
		if bot.ID != normalized.Telegram[i].ID {
			needsPersist = true
			break
		}
	}
	if needsPersist {
		repository, ok := s.notificationRepo.(notificationIDRepository)
		if !ok {
			return NotificationChannelSettings{}, errors.New("notification recipient IDs could not be persisted")
		}
		if err := repository.PersistTelegramChannelIDs(ctx, userID, adminAccountID, original, normalized); err != nil {
			return NotificationChannelSettings{}, err
		}
	}
	return normalized, nil
}

// PersistTelegramChannelIDs retains the historical ID normalization behavior while
// changing only Telegram IDs. Reads never delete retired platform JSON or credentials.
func (r *Repository) PersistTelegramChannelIDs(ctx context.Context, userID, adminAccountID string, original, normalized NotificationChannelSettings) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current []byte
	if err := tx.QueryRow(ctx, `SELECT settings FROM notification_channel_settings WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, userID, adminAccountID).Scan(&current); err != nil {
		return err
	}
	telegram, err := telegramJSONWithStableIDs(current, original, normalized)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notification_channel_settings SET settings=jsonb_set(settings,'{telegram}',$3::jsonb,true),updated_at=now() WHERE user_id=$1 AND admin_account_id=$2`, userID, adminAccountID, string(telegram)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func telegramJSONWithStableIDs(current []byte, original, normalized NotificationChannelSettings) ([]byte, error) {
	var parsed NotificationChannelSettings
	if err := unmarshalNotificationChannelSettings(current, &parsed); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(parsed.Telegram, original.Telegram) || len(original.Telegram) != len(normalized.Telegram) {
		return nil, errors.New("notification settings changed; retry")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(current, &root) != nil {
		return nil, errors.New("invalid notification settings")
	}
	var bots []map[string]json.RawMessage
	if json.Unmarshal(root["telegram"], &bots) != nil {
		var bot map[string]json.RawMessage
		if json.Unmarshal(root["telegram"], &bot) != nil || bot == nil {
			return nil, errors.New("invalid Telegram settings")
		}
		bots = []map[string]json.RawMessage{bot}
	}
	if len(bots) != len(normalized.Telegram) {
		return nil, errors.New("notification settings changed; retry")
	}
	for i := range bots {
		if bots[i] == nil {
			return nil, errors.New("invalid Telegram bot")
		}
		bots[i]["id"], _ = json.Marshal(normalized.Telegram[i].ID)
	}
	return json.Marshal(bots)
}
