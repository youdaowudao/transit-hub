package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestModelGroupsMatchLatestSpecification(t *testing.T) {
	tests := []struct {
		model       string
		know, newer []string
	}{
		{"gpt-6.2-sol", []string{"bp_2026_chair_ouster", "musk_openai_2026_verdict", "streeting_2026_resignation", "anthropic_2026_spacex_compute"}, []string{"spacex_2026_first_trade", "worldcup_2026_final_goal"}},
		{"gpt-6.2-astra", []string{"anthropic_2026_mythos_access", "openai_2026_tbpn_acquisition", "meta_2026_manus_blocked", "social_media_2026_addiction_verdict"}, []string{"pga_2026_winner", "kentucky_2026_derby"}},
		{"gpt-6.1-terra", []string{"oscars_2026_cinematography", "iea_2026_reserves", "wbd_2026_bid", "milano_2026_gold_count"}, []string{"artemis_2026_crater", "kentucky_2026_derby"}},
		{"gpt-6-sol", []string{"artemis_2026_crater", "apple_2026_neo", "openai_2026_pentagon_deal", "superbowl_2026_score"}, []string{"hungary_2026_final_seats", "kentucky_2026_derby"}},
		{"gpt-6-luna", []string{"artemis_2026_crater", "wbd_2026_bid", "minnesota_2026_pretti", "brazil_2026_floods_minas"}, []string{"kentucky_2026_derby", "pga_2026_winner"}},
		{"gpt-6.1-sol", []string{"afcon_2026_goal", "minnesota_2026_pretti", "venezuela_2026_custody", "milano_2026_men_skate"}, []string{"hungary_2026_final_seats", "kentucky_2026_derby"}},
		{"gpt-6-astra", []string{"japan_2025_diet", "nobel_2025_peace", "usopen_2025_men", "poland_2025_runoff"}, []string{"venezuela_2026_custody", "afcon_2026_goal"}},
	}
	for _, tc := range tests {
		group, ok := GroupForModel(tc.model)
		if !ok || !reflect.DeepEqual(group.Know, tc.know) || !reflect.DeepEqual(group.Newer, tc.newer) {
			t.Fatalf("%s 题组与规格不一致：%+v", tc.model, group)
		}
		for _, id := range append(append([]string{}, group.Know...), group.Newer...) {
			if _, ok := Anchor(id); !ok {
				t.Fatalf("%s 缺少题目 %s", tc.model, id)
			}
		}
	}
}

func TestModelGroupVerdicts(t *testing.T) {
	group := ModelGroup{Name: "测试型号", Know: []string{"k1", "k2", "k3", "k4"}, Newer: []string{"n1", "n2"}}
	row := func(id, kind string) Row { return Row{ID: id, Event: id, Kind: kind, Answer: kind} }
	for _, tc := range []struct {
		name, label string
		rows        []Row
	}{
		{"符合", "符合测试型号", []Row{row("k1", "know"), row("k2", "know"), row("k3", "know"), row("k4", "unknown"), row("n1", "unknown"), row("n2", "unknown")}},
		{"知识更旧", "知识更旧", []Row{row("k1", "know"), row("k2", "know"), row("k3", "wrong"), row("k4", "unknown"), row("n1", "unknown"), row("n2", "unknown")}},
		{"答出更新事件", "答出了更新型号才知道的事件", []Row{row("k1", "know"), row("k2", "know"), row("k3", "know"), row("k4", "know"), row("n1", "know"), row("n2", "unknown")}},
		{"请求失败", "未确认", []Row{row("k1", "error"), row("k2", "know"), row("k3", "know"), row("k4", "know"), row("n1", "unknown"), row("n2", "unknown")}},
		{"无效回答", "未确认", []Row{row("k1", "invalid"), row("k2", "know"), row("k3", "know"), row("k4", "know"), row("n1", "unknown"), row("n2", "unknown")}},
		{"缺少回答", "未确认", []Row{row("k1", "know")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelGroupVerdict(group, tc.rows, "chat", len(tc.rows))
			if !strings.Contains(got.Label, tc.label) {
				t.Fatalf("结果 %q，期望包含 %q", got.Label, tc.label)
			}
			if got.BoundaryReview == nil || got.BoundaryReview.Method != ModelGroupMethod {
				t.Fatalf("缺少型号核对标记：%+v", got.BoundaryReview)
			}
		})
	}
}

func TestEmptyProofGroupNeverMatches(t *testing.T) {
	group := ModelGroup{Name: "空证明题型号", Newer: []string{"venezuela_2026_custody", "afcon_2026_goal"}}
	for _, kind := range []string{"know", "unknown"} {
		rows := []Row{{ID: group.Newer[0], Kind: kind, Answer: kind}, {ID: group.Newer[1], Kind: kind, Answer: kind}}
		got := ModelGroupVerdict(group, rows, "chat", 2)
		if got.Status != "unknown" || !strings.Contains(got.Reason, "证明题尚未补齐") {
			t.Fatalf("空证明题组不得判定符合或不符：%+v", got)
		}
	}
}

func TestModelAnswerRequiresRecognizableChoiceAndRejectsWeb(t *testing.T) {
	p, ok := Anchor("superbowl_2026_score")
	if !ok {
		t.Fatal("缺少事件题")
	}
	for _, tc := range []struct {
		answer, kind string
		web          bool
	}{
		{`{"choice":"C"}`, "know", false},
		{`{"choice":"A"}`, "wrong", false},
		{`{"choice":"D"}`, "unknown", false},
		{"不知道", "unknown", false},
		{"A", "wrong", false},
		{`{"choice":"E"}`, "invalid", false},
		{`{"choice":"E","note":"不知道"}`, "invalid", false},
		{`{"other":"不知道"}`, "invalid", false},
		{`{"choice":"A"}{"choice":"D"}`, "invalid", false},
		{`{"choice":"D"}{"choice":"A"}`, "invalid", false},
		{`{"choice":"A"}{"choice":"A"}`, "invalid", false},
		{`{"choice":"\u0043"}`, "know", false},
		{`{"choice":"D"}{"\u0063hoice":"C"}`, "invalid", false},
		{`{"choice":"D","choice":"C"}`, "invalid", false},
		{"{\"choice\":\"A\"}\n最终答案: D", "invalid", false},
		{"无法理解的内容", "invalid", false},
		{"```json\n{\"choice\":\"C\"}\n```", "know", false},
		{"```\n{\"choice\":\"D\"}\n```", "unknown", false},
		{"```json\n{\"choice\":\"A\"}{\"choice\":\"D\"}\n```", "invalid", false},
		{`{"choice":"C"}`, "invalid", true},
	} {
		if row := ObserveModel(p, tc.answer, "", tc.web); row.Kind != tc.kind {
			t.Errorf("回答 %q、联网 %v：得到 %s，期望 %s", tc.answer, tc.web, row.Kind, tc.kind)
		}
	}
}

func TestSavedGroupOverridesCurrentDefinition(t *testing.T) {
	saved := ModelGroup{Name: "旧题组", Know: []string{"k1", "k2"}, Newer: []string{"n1", "n2"}}
	raw, _ := json.Marshal(ModelGroupVerdict(saved, nil, "chat", 0).BoundaryReview)
	var review map[string]any
	_ = json.Unmarshal(raw, &review)
	got, ok := SavedGroup(review)
	if !ok || !reflect.DeepEqual(got, saved) {
		t.Fatalf("保存的题组无法还原：%+v", got)
	}
	if _, ok := SavedGroup(map[string]any{"method": ModelGroupMethod}); ok {
		t.Fatal("没有保存题组的旧报告应回退到当前题组")
	}
}
