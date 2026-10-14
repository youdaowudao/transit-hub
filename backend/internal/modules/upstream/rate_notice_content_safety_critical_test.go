package upstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
