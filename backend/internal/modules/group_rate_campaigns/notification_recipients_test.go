package group_rate_campaigns

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
	panic("current workspace path must not be used")
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

func TestCampaignWithRetiredNotificationStillRunsAtOriginalMultiplier(t *testing.T) {
	repository := newFakeRepository()
	operator := &fakeOperator{groups: adminGroups()}
	notifier := &notificationProjectionFixture{}
	service := &Service{repository: repository, operator: operator, notifier: notifier, config: Config{DefaultNotifyBotIDs: []string{"original"}}}
	request := CreateCampaignRequest{Name: "activity", Selection: Selection{Mode: SelectionManual, Groups: []SelectionGroupRef{{GroupName: "vip", CampaignMultiplier: floatPtr(0.6)}}}, Adjustment: Adjustment{Mode: AdjustmentSet}, Schedule: Schedule{StartMode: StartNow, EndMode: EndManual}, Notify: Notify{Enabled: true, BotIDs: []string{"retired"}, StartTemplate: "start"}}
	detail, err := service.Create(context.Background(), "user", "stored-workspace", request)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != StatusRunning || operator.updateCalls != 1 || operator.updates[0].multiplier != 0.6 {
		t.Fatal("retired notifications must not stop the campaign or alter its multiplier")
	}
	if detail.Notify.Enabled || len(detail.Notify.BotIDs) != 0 || !detail.Notify.RecipientsInvalid || notifier.sentWorkspace != "" {
		t.Fatal("old-only references must not fall back to another configured Telegram bot")
	}
	mixed, err := service.projectNotify(context.Background(), "user", "stored-workspace", Notify{Enabled: true, BotIDs: []string{"retired", "original"}})
	if err != nil {
		t.Fatal(err)
	}
	if !mixed.Enabled || !mixed.RecipientsInvalid || !reflect.DeepEqual(mixed.BotIDs, []string{"original"}) {
		t.Fatal("mixed references must retain only the original Telegram recipient")
	}
	service.sendWorkspaceNotification(context.Background(), &Campaign{UserID: "user", AdminAccountID: "stored-workspace", Notify: mixed}, "message")
	if notifier.sentWorkspace != "stored-workspace" || !reflect.DeepEqual(notifier.sentIDs, []string{"original"}) {
		t.Fatal("campaign notification changed workspace or recipient")
	}
}

func TestCampaignRecipientLookupFailureAbortsCreateBeforePersistence(t *testing.T) {
	repository := newFakeRepository()
	service := &Service{repository: repository, notifier: &notificationProjectionFixture{fail: true}}
	_, err := service.Create(context.Background(), "user", "workspace", CreateCampaignRequest{Notify: Notify{Enabled: true, BotIDs: []string{"original"}}})
	if err == nil || len(repository.campaigns) != 0 {
		t.Fatal("failed recipient lookup must never persist an empty replacement")
	}
}

func TestCampaignCreateDoesNotPersistClientNotificationUnavailableWarning(t *testing.T) {
	repository := newFakeRepository()
	service := &Service{repository: repository, notifier: &notificationProjectionFixture{}}
	request := CreateCampaignRequest{
		Name:       "activity",
		Selection:  Selection{Mode: SelectionManual, Groups: []SelectionGroupRef{{GroupName: "vip", CampaignMultiplier: floatPtr(0.6)}}},
		Adjustment: Adjustment{Mode: AdjustmentSet},
		Schedule:   Schedule{StartMode: StartDraft, EndMode: EndManual},
		Notify:     Notify{Enabled: true, BotIDs: []string{"original"}, StartTemplate: "custom start", EndTemplate: "custom end", RecipientsUnavailable: true},
	}
	detail, err := service.Create(context.Background(), "user", "workspace", request)
	if err != nil {
		t.Fatal(err)
	}
	stored := repository.campaigns[detail.ID]
	if stored.Notify.RecipientsUnavailable || detail.Notify.RecipientsUnavailable {
		t.Fatal("a successful validation must clear the temporary client warning before persistence")
	}
	if !stored.Notify.Enabled || !reflect.DeepEqual(stored.Notify.BotIDs, []string{"original"}) || stored.Notify.StartTemplate != "custom start" || stored.Notify.EndTemplate != "custom end" {
		t.Fatal("clearing the warning must preserve the original notification configuration")
	}
}
