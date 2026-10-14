package upstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
)

func TestRateNoticeDisplayUsesConfirmedPageCopy(t *testing.T) {
	multiplier := 2.0
	site := Site{ID: "site-copy", UserID: "user-copy", AdminAccountID: "workspace-copy", Name: "验收网站"}
	group := GroupInfo{ID: "group-copy", Name: "验收分组", Multiplier: &multiplier}
	bindings := []RateNoticeBinding{{AccountID: "7", AccountName: "登记账号", KeyID: "key-copy", AdminPlatform: "sub2api", Status: "disabled"}}
	observed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		bindings    []RateNoticeBinding
		assignments map[string]RateNoticeKeyAssignment
		level       notifications.Level
		title       string
		copy        string
	}{
		{"registered-known", bindings, map[string]RateNoticeKeyAssignment{"key-copy": {Confirmed: true, GroupID: group.ID}}, notifications.LevelUrgent, "已登记主站接入的上游倍率发生变化", "验收网站·验收分组：上游倍率1×→2×。请检查成本、定价和调度安排；查看或确认不会修改主站。"},
		{"registered-unknown", bindings, nil, notifications.LevelUrgent, "已登记主站接入的上游倍率发生变化", "这个网站已有登记的主站接入，但当前无法确认受影响范围，请进入分组健康核查。"},
		{"unmatched", nil, nil, notifications.LevelNormal, "", "上游倍率发生变化；未发现这个分组的已登记主站接入。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := rateChangeNotice(site, group, 1, observed, test.bindings, test.assignments)
			if err != nil {
				t.Fatal(err)
			}
			if input.Level != test.level {
				t.Fatalf("classification changed: got %d", input.Level)
			}
			if test.title != "" && input.Content.Title != test.title {
				t.Fatalf("confirmed title missing: %q", input.Content.Title)
			}
			if !strings.Contains(input.Content.Summary, test.copy) {
				t.Fatalf("confirmed visible copy missing: %q", input.Content.Summary)
			}
			if input.SubjectID != site.ID || input.Content.Action == nil || input.Content.Action.SiteID != site.ID || input.Content.Action.FocusGroupID != group.ID {
				t.Fatal("page copy must keep original business identity and fixed action")
			}
			fixture := notifications.Notice{ID: "copy-" + test.name, AdminAccountID: site.AdminAccountID, Kind: input.Kind, SubjectType: input.SubjectType, SubjectID: input.SubjectID, Level: input.Level, DiscoveredAt: observed, CreatedAt: observed, Content: input.Content}
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("[notification-copy-fixture]%s", raw)
		})
	}
}

func TestRateNoticeDisplayDoesNotKeepQuoteOrNestedURLCredentials(t *testing.T) {
	for _, unsafeName := range []string{
		"https://viewer:prefix'fixture-producer-tail@example.test/rates",
		"https://viewer:prefix\"fixture-producer-tail@example.test/rates",
		"https://safe.example/rates?next=https://viewer:fixture-producer-tail@example.test/child",
	} {
		for _, field := range []string{"site", "group"} {
			t.Run(field+"-"+unsafeName[:12], func(t *testing.T) {
				multiplier := 2.0
				site := Site{ID: "site-a", UserID: "user", AdminAccountID: "workspace-a", Name: "正常网站"}
				group := GroupInfo{ID: "7", Name: "正常分组", Multiplier: &multiplier}
				if field == "site" {
					site.Name = unsafeName
				} else {
					group.Name = unsafeName
				}
				input, err := rateChangeNotice(site, group, 1, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil)
				if err != nil {
					t.Fatal("known safe destination should retain a readable rate-change fact")
				}
				raw, err := json.Marshal(input.Content)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "fixture-producer-tail") {
					t.Fatal("actual rate-change producer left a URL credential suffix in immutable notification content")
				}
				if input.SubjectID != "site-a" || input.Content.Action == nil || input.Content.Action.SiteID != "site-a" || input.Content.Action.FocusGroupID != "7" {
					t.Fatal("masking display names must preserve complete existing destination identifiers")
				}
			})
		}
	}
}
