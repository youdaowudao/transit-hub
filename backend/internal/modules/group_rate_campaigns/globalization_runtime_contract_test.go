package group_rate_campaigns

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type globalizationCampaignNotifier struct {
	fail      bool
	failAfter int
	reads     int
}

func (*globalizationCampaignNotifier) SendToBots(context.Context, string, []string, string) {
	panic("campaign notifications must use the stored workspace")
}

func (*globalizationCampaignNotifier) SendToWorkspaceBots(context.Context, string, string, []string, string) {
}

func (f *globalizationCampaignNotifier) ProjectNotificationRecipients(_ context.Context, _, _ string, ids []string) ([]string, bool, error) {
	f.reads++
	if f.fail || (f.failAfter > 0 && f.reads >= f.failAfter) {
		return nil, false, errors.New("isolated notification lookup unavailable")
	}
	return append([]string(nil), ids...), false, nil
}

type globalizationCampaignRepository struct{ *fakeRepository }

func (f *globalizationCampaignRepository) List(_ context.Context, userID, workspace string, _ ListQuery) ([]Campaign, int, error) {
	rows := []Campaign{}
	for _, campaign := range f.campaigns {
		if campaign.UserID == userID && campaign.AdminAccountID == workspace {
			rows = append(rows, *campaign)
		}
	}
	return rows, len(rows), nil
}

func globalizationCampaignRequest(start StartMode) CreateCampaignRequest {
	return CreateCampaignRequest{
		Name:       "isolated notification runtime",
		Selection:  Selection{Mode: SelectionManual, Groups: []SelectionGroupRef{{GroupName: "vip", CampaignMultiplier: floatPtr(0.6)}}},
		Adjustment: Adjustment{Mode: AdjustmentSet},
		Schedule:   Schedule{StartMode: start, EndMode: EndManual},
		Notify:     Notify{Enabled: true, BotIDs: []string{"original"}, StartTemplate: "custom start", EndTemplate: "custom end"},
	}
}

func assertGlobalizationCampaignResponse(t *testing.T, detail CampaignDetail, stored *Campaign) {
	t.Helper()
	if !detail.Notify.Enabled || !reflect.DeepEqual(detail.Notify.BotIDs, []string{"original"}) || detail.Notify.StartTemplate != "custom start" || detail.Notify.EndTemplate != "custom end" {
		t.Fatal("notification lookup failure must preserve original response configuration")
	}
	raw, _ := json.Marshal(detail.Notify)
	var response map[string]any
	if json.Unmarshal(raw, &response) != nil || response["recipientsUnavailable"] != true {
		t.Fatal("notification failure must be reported independently of the business result")
	}
	raw, _ = json.Marshal(stored.Notify)
	if json.Unmarshal(raw, &response) != nil {
		t.Fatal("invalid stored notification fixture")
	}
	// Decode into a fresh map: an absent field must not inherit the response flag.
	response = nil
	_ = json.Unmarshal(raw, &response)
	if response["recipientsUnavailable"] == true || !stored.Notify.Enabled || !reflect.DeepEqual(stored.Notify.BotIDs, []string{"original"}) {
		t.Fatal("temporary lookup failure must not alter persisted notification settings")
	}
}

func TestGlobalizationCampaignNotificationFailureKeepsCompletedOperationsVisible(t *testing.T) {
	for _, operation := range []string{"start", "end", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			repository := &globalizationCampaignRepository{newFakeRepository()}
			operator := &fakeOperator{groups: adminGroups()}
			notifier := &globalizationCampaignNotifier{}
			service := &Service{repository: repository, operator: operator, notifier: notifier}
			start := StartDraft
			if operation == "end" {
				start = StartNow
			}
			created, err := service.Create(context.Background(), "user", "workspace", globalizationCampaignRequest(start))
			if err != nil {
				t.Fatal(err)
			}
			before := operator.updateCalls
			if operation == "end" {
				operator.groups[1].Multiplier = floatPtr(0.6)
			}
			notifier.fail = true
			var detail CampaignDetail
			var expectedStatus string
			switch operation {
			case "start":
				detail, err = service.StartNow(context.Background(), "user", "workspace", created.ID)
				expectedStatus = StatusRunning
				if operator.updateCalls != before+1 || operator.updates[len(operator.updates)-1].multiplier != 0.6 {
					t.Fatal("start did not apply the unchanged campaign multiplier")
				}
			case "end":
				detail, err = service.End(context.Background(), "user", "workspace", created.ID)
				expectedStatus = StatusEnded
				if operator.updateCalls != before+1 || operator.updates[len(operator.updates)-1].multiplier != 2.0 {
					t.Fatal("end did not restore the original multiplier")
				}
			case "cancel":
				detail, err = service.Cancel(context.Background(), "user", "workspace", created.ID)
				expectedStatus = StatusCancelled
				if operator.updateCalls != before {
					t.Fatal("cancel must not write any multiplier")
				}
			}
			stored := repository.campaigns[created.ID]
			if string(stored.Status) != expectedStatus {
				t.Fatal("business operation did not persist its expected state")
			}
			if err != nil || string(detail.Status) != expectedStatus {
				t.Fatalf("completed operation must remain successful despite notification lookup: %v", err)
			}
			assertGlobalizationCampaignResponse(t, detail, stored)
		})
	}
}

func TestGlobalizationCampaignNotificationFailureKeepsReadAndRecoveryEntrancesVisible(t *testing.T) {
	repository := &globalizationCampaignRepository{newFakeRepository()}
	notifier := &globalizationCampaignNotifier{}
	service := &Service{repository: repository, operator: &fakeOperator{groups: adminGroups()}, notifier: notifier, config: Config{NotifyEnabledDefault: true, DefaultNotifyBotIDs: []string{"original"}}}
	created, err := service.Create(context.Background(), "user", "workspace", globalizationCampaignRequest(StartDraft))
	if err != nil {
		t.Fatal(err)
	}
	notifier.fail = true
	t.Run("get", func(t *testing.T) {
		detail, err := service.Get(context.Background(), "user", "workspace", created.ID)
		if err != nil {
			t.Fatalf("notification failure must not hide a campaign detail: %v", err)
		}
		assertGlobalizationCampaignResponse(t, detail, repository.campaigns[created.ID])
	})
	t.Run("list", func(t *testing.T) {
		list, err := service.List(context.Background(), "user", "workspace", ListQuery{})
		if err != nil || len(list.Items) != 1 || list.Items[0].ID != created.ID || !list.Items[0].NotifyEnabled {
			t.Fatalf("notification failure must not hide the list or recovery entrance: %v", err)
		}
		raw, _ := json.Marshal(list.Items[0])
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		if item["notifyRecipientsUnavailable"] != true {
			t.Fatal("list must distinguish unavailable notifications")
		}
		raw, _ = json.Marshal(list.Defaults)
		item = nil
		_ = json.Unmarshal(raw, &item)
		if item["recipientsUnavailable"] != true || !list.Defaults.Enabled || !reflect.DeepEqual(list.Defaults.BotIDs, []string{"original"}) {
			t.Fatal("unavailable defaults must retain original selection with a separate warning")
		}
	})
}

func TestGlobalizationCampaignCreateSurvivesOnlyTheFinalNotificationReadFailure(t *testing.T) {
	repository := &globalizationCampaignRepository{newFakeRepository()}
	operator := &fakeOperator{groups: adminGroups()}
	notifier := &globalizationCampaignNotifier{failAfter: 2}
	service := &Service{repository: repository, operator: operator, notifier: notifier}
	detail, err := service.Create(context.Background(), "user", "workspace", globalizationCampaignRequest(StartNow))
	if operator.updateCalls != 1 || operator.updates[0].multiplier != 0.6 || len(repository.campaigns) != 1 {
		t.Fatal("validated create must apply and persist its original business operation")
	}
	if err != nil || detail.Status != StatusRunning {
		t.Fatalf("late notification lookup must not report a completed creation as failed: %v", err)
	}
	assertGlobalizationCampaignResponse(t, detail, repository.campaigns[detail.ID])
}
