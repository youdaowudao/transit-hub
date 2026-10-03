package my_sites

import (
	"context"
	"log"
)

type notificationRecipientProjector interface {
	ProjectNotificationRecipients(context.Context, string, string, []string) ([]string, bool, error)
}

type workspaceBotNotifier interface {
	SendToWorkspaceBots(context.Context, string, string, []string, string)
}

func (s *Service) projectMappingNotifications(ctx context.Context, userID, adminAccountID string, mapping GroupMapping) (GroupMapping, error) {
	projector, ok := s.botNotifier.(notificationRecipientProjector)
	if !ok || len(mapping.AutoPricingNotifyBotIDs) == 0 {
		return mapping, nil
	}
	ids, invalid, err := projector.ProjectNotificationRecipients(ctx, userID, adminAccountID, mapping.AutoPricingNotifyBotIDs)
	if err != nil {
		return GroupMapping{}, err
	}
	mapping.AutoPricingNotifyBotIDs = ids
	mapping.AutoPricingNotifyRecipientsInvalid = invalid
	if invalid && len(ids) == 0 {
		mapping.EnableAutoPricingNotify = false
	}
	return mapping, nil
}

func (s *Service) projectMappingList(ctx context.Context, userID, adminAccountID string, mappings []GroupMapping) ([]GroupMapping, error) {
	projected := make([]GroupMapping, len(mappings))
	for i, mapping := range mappings {
		next, err := s.projectMappingNotifications(ctx, userID, adminAccountID, mapping)
		if err != nil {
			return nil, err
		}
		projected[i] = next
	}
	return projected, nil
}

// Runtime notification failures are isolated from pricing and never persist a fallback.
func (s *Service) sendMappingNotification(ctx context.Context, userID, adminAccountID string, mapping GroupMapping, message string) {
	projected, err := s.projectMappingNotifications(ctx, userID, adminAccountID, mapping)
	if err != nil {
		log.Printf("[auto-pricing] notification recipient lookup unavailable workspace=%s", adminAccountID)
		return
	}
	if !projected.EnableAutoPricingNotify || len(projected.AutoPricingNotifyBotIDs) == 0 {
		return
	}
	if notifier, ok := s.botNotifier.(workspaceBotNotifier); ok {
		notifier.SendToWorkspaceBots(ctx, userID, adminAccountID, projected.AutoPricingNotifyBotIDs, message)
		return
	}
	s.botNotifier.SendToBots(ctx, userID, projected.AutoPricingNotifyBotIDs, message)
}

func (s *Service) mappingNotificationForResponse(ctx context.Context, userID, adminAccountID string, mapping GroupMapping) GroupMapping {
	projected, err := s.projectMappingNotifications(ctx, userID, adminAccountID, mapping)
	if err != nil {
		mapping.AutoPricingNotifyRecipientsUnavailable = true
		return mapping
	}
	projected.AutoPricingNotifyRecipientsUnavailable = false
	return projected
}
