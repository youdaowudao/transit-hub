package settings

import "context"

// ProjectNotificationRecipients only keeps original recipient IDs present in the
// requested workspace. Disabled Telegram bots remain valid references.
func (s *Service) ProjectNotificationRecipients(ctx context.Context, userID, adminAccountID string, ids []string) ([]string, bool, error) {
	if len(ids) == 0 {
		return ids, false, nil
	}
	channels, err := s.notificationChannelsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return nil, false, err
	}
	return projectRecipientIDs(ids, channels), hasInvalidRecipientIDs(ids, channels), nil
}

func projectRecipientIDs(ids []string, channels NotificationChannelSettings) []string {
	valid := make(map[string]bool, len(channels.Telegram))
	for _, bot := range channels.Telegram {
		if bot.ID != "" {
			valid[bot.ID] = true
		}
	}
	result := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if valid[id] && !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result
}

func hasInvalidRecipientIDs(ids []string, channels NotificationChannelSettings) bool {
	valid := make(map[string]bool, len(channels.Telegram))
	for _, bot := range channels.Telegram {
		if bot.ID != "" {
			valid[bot.ID] = true
		}
	}
	for _, id := range ids {
		if !valid[id] {
			return true
		}
	}
	return false
}

func (s *Service) projectStrategy(ctx context.Context, userID, adminAccountID string, strategy StrategySettings) (StrategySettings, error) {
	strategy.BalanceTemplate = projectDefaultBalanceTemplate(strategy.BalanceTemplate)
	if len(strategy.BalanceNotifyBotIDs) > 0 {
		ids, invalid, err := s.ProjectNotificationRecipients(ctx, userID, adminAccountID, strategy.BalanceNotifyBotIDs)
		if err != nil {
			return StrategySettings{}, err
		}
		strategy.BalanceNotifyBotIDs = ids
		strategy.BalanceNotifyRecipientsInvalid = invalid
		if invalid && len(ids) == 0 {
			strategy.EnableBalanceWarning = false
		}
	}
	if len(strategy.MultiplierNotifyBotIDs) > 0 {
		ids, invalid, err := s.ProjectNotificationRecipients(ctx, userID, adminAccountID, strategy.MultiplierNotifyBotIDs)
		if err != nil {
			return StrategySettings{}, err
		}
		strategy.MultiplierNotifyBotIDs = ids
		strategy.MultiplierNotifyRecipientsInvalid = invalid
		if invalid && len(ids) == 0 {
			strategy.EnableMultiplierAlert = false
		}
	}
	return strategy, nil
}
