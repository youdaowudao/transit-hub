package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStrategyNotificationProjectionKeepsAmountsDisabledTelegramAndRefresh(t *testing.T) {
	service := NewService(nil, nil)
	service.notificationRepo = &fakeNotificationRepository{channels: NotificationChannelSettings{Telegram: []TelegramChannelSettings{{ID: "original", Enabled: false}, {ID: "other", Enabled: true}}}}
	original := StrategySettings{EnableBalanceWarning: true, DefaultBalanceThreshold: 700.125, BalanceNotifyBotIDs: []string{"retired", "original"}, EnableMultiplierAlert: true, MultiplierNotifyBotIDs: []string{"retired"}, EnableRefreshInterval: true, RefreshInterval: 90, EnableAutoChangeMultiplier: true, BalanceTemplate: legacyBalanceTemplateDefaults[0]}
	projected, err := service.projectStrategy(context.Background(), "user", "workspace", original)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected.BalanceNotifyBotIDs, []string{"original"}) || !projected.EnableBalanceWarning || !projected.BalanceNotifyRecipientsInvalid {
		t.Fatal("must keep original disabled Telegram reference and mixed warning")
	}
	if projected.EnableMultiplierAlert || len(projected.MultiplierNotifyBotIDs) != 0 || !projected.MultiplierNotifyRecipientsInvalid {
		t.Fatal("only removed references must close only their notification switch")
	}
	if !projected.EnableRefreshInterval || projected.RefreshInterval != 90 || !projected.EnableAutoChangeMultiplier || projected.DefaultBalanceThreshold != 700.125 {
		t.Fatal("unrelated business settings or amount changed")
	}
	if strings.Contains(projected.BalanceTemplate, "CNY") || strings.Contains(projected.BalanceTemplate, " 元") {
		t.Fatal("old default marker was exposed")
	}
	if !reflect.DeepEqual(original.BalanceNotifyBotIDs, []string{"retired", "original"}) {
		t.Fatal("read projection mutated stored slice")
	}
}

type unavailableNotificationRepo struct{}

func (unavailableNotificationRepo) GetNotificationChannels(context.Context, string, string) (NotificationChannelSettings, error) {
	return NotificationChannelSettings{}, errors.New("fixture unavailable")
}
func (unavailableNotificationRepo) SaveNotificationChannels(context.Context, string, string, NotificationChannelSettings) error {
	return nil
}

func TestNotificationProjectionFailureCannotBecomeEmptyRecipientSave(t *testing.T) {
	service := NewService(nil, nil)
	service.notificationRepo = unavailableNotificationRepo{}
	repo := &fakeStrategyRepository{}
	service.strategyRepo = repo
	service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "workspace"})
	_, err := service.SaveStrategy(context.Background(), "user", StrategySettings{EnableBalanceWarning: true, BalanceNotifyBotIDs: []string{"original"}})
	if err == nil || repo.savedUser != "" {
		t.Fatal("channel lookup failure must abort strategy persistence")
	}
}

func TestLegacyDefaultTemplateProjectionIsExactAndKeepsCustomContent(t *testing.T) {
	for _, legacy := range legacyBalanceTemplateDefaults {
		projected := projectDefaultBalanceTemplate(legacy)
		if hasLegacyTemplateMarkers(projected) || projected == legacy {
			t.Fatal("known old default must lose markers")
		}
		custom := legacy + "\ncustom addition"
		if projectDefaultBalanceTemplate(custom) != custom {
			t.Fatal("user edited template must remain byte-for-byte intact")
		}
	}
}

func TestEmailDefaultProjectionDoesNotOverwriteUserEditedBuiltIn(t *testing.T) {
	legacy := defaultMarketingEmailTemplate()
	legacy.HTMLBody = strings.Replace(legacy.HTMLBody, `lang="zh"`, `lang="zh-CN"`, 1)
	projected := projectBuiltInEmailTemplate(legacy)
	if projected.HTMLBody != builtInMarketingHTML {
		t.Fatal("old system email language must project as zh")
	}
	legacy.HTMLBody += "<!-- edited -->"
	if !reflect.DeepEqual(projectBuiltInEmailTemplate(legacy), legacy) {
		t.Fatal("edited builtin must remain intact")
	}
}

func TestNotificationCleanupPreviewPreservesNumbersUnknownFieldsAndBusinessState(t *testing.T) {
	rows := []CleanupRow{
		{Table: "notification_channel_settings", Before: json.RawMessage(`{"dingtalk":[{"id":"retired","secret":"never-log"}],"telegram":{"id":"original","enabled":false,"botToken":"never-log","chatId":"unchanged","futureBotFlag":{"preserve":true}},"futureFlag":true}`)},
		{Table: "strategy_settings", Before: json.RawMessage(`{"enableBalanceWarning":true,"balanceNotifyBotIds":["retired","original"],"enableMultiplierAlert":true,"multiplierNotifyBotIds":["retired"],"defaultBalanceThreshold":700.1234567890123456789,"enableRefreshInterval":true,"refreshInterval":90,"futureFlag":"untouched"}`)},
		{Table: "group_rate_campaigns", ID: "activity", Before: json.RawMessage(`{"enabled":true,"botIds":["retired"],"startTemplate":"custom ¥ content","futureFlag":true}`)},
		{Table: "my_site_states", Before: json.RawMessage(`[{"ownGroup":"business","enableAutoPricing":true,"enableAutoPricingNotify":true,"autoPricingNotifyBotIds":["retired"],"fixedIncrease":0.125,"maxMultiplier":7,"futureFlag":"untouched"}]`)},
	}
	before, _ := json.Marshal(rows)
	plan, err := PlanNotificationCleanup("user", "workspace", rows)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(rows)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preview mutated input")
	}
	if plan.Summary.ChannelsRemoved != 1 || plan.Summary.RecipientRefsRemoved != 4 || plan.Summary.NotificationSwitches != 3 || plan.Summary.RowsChanged != 4 || plan.Summary.CustomLegacyTemplates != 1 {
		t.Fatal("preview counts differ from expected business cases")
	}
	var projectedChannels map[string]json.RawMessage
	if err := json.Unmarshal(plan.Rows[0].After, &projectedChannels); err != nil {
		t.Fatal(err)
	}
	var projectedTelegram []map[string]json.RawMessage
	if err := json.Unmarshal(projectedChannels["telegram"], &projectedTelegram); err != nil {
		t.Fatal(err)
	}
	if string(projectedTelegram[0]["futureBotFlag"]) != `{"preserve":true}` || string(projectedTelegram[0]["botToken"]) != `"never-log"` || string(projectedTelegram[0]["id"]) != `"original"` {
		t.Fatal("cleanup must preserve every original Telegram field")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(plan.Rows[1].After, &value); err != nil {
		t.Fatal(err)
	}
	if string(value["defaultBalanceThreshold"]) != "700.1234567890123456789" || string(value["refreshInterval"]) != "90" || string(value["enableRefreshInterval"]) != "true" || string(value["futureFlag"]) != `"untouched"` {
		t.Fatal("cleanup changed amount precision or non-notification fields")
	}
	if string(value["balanceNotifyBotIds"]) != `["original"]` || string(value["enableBalanceWarning"]) != "true" || string(value["enableMultiplierAlert"]) != "false" {
		t.Fatal("cleanup changed mixed recipient semantics")
	}
	var mappings []map[string]json.RawMessage
	if err := json.Unmarshal(plan.Rows[3].After, &mappings); err != nil {
		t.Fatal(err)
	}
	if string(mappings[0]["enableAutoPricing"]) != "true" || string(mappings[0]["fixedIncrease"]) != "0.125" || string(mappings[0]["enableAutoPricingNotify"]) != "false" {
		t.Fatal("cleanup disabled business or changed multiplier")
	}
	second, err := PlanNotificationCleanup("user", "other-workspace", rows)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Digest == second.Digest {
		t.Fatal("approval digest must be workspace scoped")
	}
}

func TestNotificationCleanupLeavesCustomEmailAndTemplatesUntouched(t *testing.T) {
	template := defaultMarketingEmailTemplate()
	template.HTMLBody += "custom CNY"
	raw, _ := json.Marshal(template)
	rows := []CleanupRow{{Table: "email_templates", ID: template.ID, Before: raw}, {Table: "strategy_settings", Before: json.RawMessage(`{"balanceTemplate":"custom CNY ¥ content","defaultBalanceThreshold":100}`)}}
	plan, err := PlanNotificationCleanup("user", "workspace", rows)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.RowsChanged != 0 || plan.Summary.CustomLegacyTemplates != 2 {
		t.Fatal("custom records must be counted without rewriting them")
	}
}
