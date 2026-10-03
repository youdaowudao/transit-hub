package group_rate_campaigns

import "context"

type notificationRecipientProjector interface {
	ProjectNotificationRecipients(context.Context, string, string, []string) ([]string, bool, error)
}

type workspaceBotNotifier interface {
	SendToWorkspaceBots(context.Context, string, string, []string, string)
}

func (s *Service) projectNotify(ctx context.Context, userID, adminAccountID string, notify Notify) (Notify, error) {
	// A temporary response warning is never part of a validated saved configuration.
	notify.RecipientsUnavailable = false
	projector, ok := s.notifier.(notificationRecipientProjector)
	if !ok || len(notify.BotIDs) == 0 {
		return notify, nil
	}
	ids, invalid, err := projector.ProjectNotificationRecipients(ctx, userID, adminAccountID, notify.BotIDs)
	if err != nil {
		return Notify{}, err
	}
	notify.BotIDs = ids
	notify.RecipientsInvalid = invalid
	if invalid && len(ids) == 0 {
		notify.Enabled = false
	}
	return notify, nil
}

// Notification lookup failure must not hide a campaign or a completed business operation.
// Keep its saved configuration intact and report the temporary failure in the response only.
func (s *Service) notifyForResponse(ctx context.Context, userID, adminAccountID string, notify Notify) Notify {
	projected, err := s.projectNotify(ctx, userID, adminAccountID, notify)
	if err != nil {
		notify.RecipientsUnavailable = true
		return notify
	}
	return projected
}

func (s *Service) sendWorkspaceNotification(ctx context.Context, campaign *Campaign, message string) {
	if notifier, ok := s.notifier.(workspaceBotNotifier); ok {
		notifier.SendToWorkspaceBots(ctx, campaign.UserID, campaign.AdminAccountID, campaign.Notify.BotIDs, message)
		return
	}
	s.notifier.SendToBots(ctx, campaign.UserID, campaign.Notify.BotIDs, message)
}
