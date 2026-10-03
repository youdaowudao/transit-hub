package my_sites

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type notificationProjectionFixture struct {
	fail          bool
	sentWorkspace string
	sentIDs       []string
}

func (*notificationProjectionFixture) SendToBots(context.Context, string, []string, string) {
	panic("current workspace notification path must not be used")
}
func (f *notificationProjectionFixture) SendToWorkspaceBots(_ context.Context, _ string, workspace string, ids []string, _ string) {
	f.sentWorkspace = workspace
	f.sentIDs = ids
}
func (f *notificationProjectionFixture) ProjectNotificationRecipients(_ context.Context, _ string, _ string, ids []string) ([]string, bool, error) {
	if f.fail {
		return nil, false, errors.New("fixture unavailable")
	}
	valid := []string{}
	invalid := false
	for _, id := range ids {
		if id == "original" {
			valid = append(valid, id)
		} else {
			invalid = true
		}
	}
	return valid, invalid, nil
}

func TestMappingNotificationProjectionPreservesBusinessAndArrayShape(t *testing.T) {
	fixture := &notificationProjectionFixture{}
	service := &Service{botNotifier: fixture}
	original := GroupMapping{OwnGroup: "business", EnableAutoPricing: true, FixedIncrease: 0.125, UpstreamTargets: []UpstreamGroupRef{}, EnableAutoPricingNotify: true, AutoPricingNotifyBotIDs: []string{"retired", "original"}, AutoPricingNotifyTemplate: "custom"}
	mixed, err := service.projectMappingNotifications(context.Background(), "user", "stored-workspace", original)
	if err != nil {
		t.Fatal(err)
	}
	if !mixed.EnableAutoPricing || !mixed.EnableAutoPricingNotify || mixed.FixedIncrease != 0.125 || !mixed.AutoPricingNotifyRecipientsInvalid || !reflect.DeepEqual(mixed.AutoPricingNotifyBotIDs, []string{"original"}) {
		t.Fatal("mixed projection changed business settings or recipients")
	}
	original.AutoPricingNotifyBotIDs = []string{"retired"}
	projected, err := service.projectMappingList(context.Background(), "user", "stored-workspace", []GroupMapping{original})
	if err != nil {
		t.Fatal(err)
	}
	if !projected[0].EnableAutoPricing || projected[0].EnableAutoPricingNotify || !projected[0].AutoPricingNotifyRecipientsInvalid || projected[0].UpstreamTargets == nil {
		t.Fatal("old-only notifications must close notification while keeping business and response array")
	}
	service.sendMappingNotification(context.Background(), "user", "stored-workspace", mixed, "message")
	if fixture.sentWorkspace != "stored-workspace" || !reflect.DeepEqual(fixture.sentIDs, []string{"original"}) {
		t.Fatal("notification was sent to a different workspace or recipient")
	}
	fixture.fail = true
	if _, err := service.projectMappingNotifications(context.Background(), "user", "stored-workspace", original); err == nil {
		t.Fatal("recipient lookup failure must surface without replacing IDs")
	}
}
