package knowledge

import (
	"fmt"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/integrity"
)

const ModelGroupMethod = "model_group"

type ModelGroup struct {
	Name  string
	Know  []string
	Newer []string
}

// GPT-6 Astra 的证明题：2026-11-23 单会话验证均答对（见 .cache/kb-v7/results3.csv phase=f1），
// 2026 年 1 月起的事件答「不知道」。均为现有默认题，不需要新增迁移。
var gpt6AstraKnow = []string{"japan_2025_diet", "nobel_2025_peace", "usopen_2025_men", "poland_2025_runoff"}

var modelGroups = map[string]ModelGroup{
	"gpt62sol":   {"GPT-6.2 Sol", []string{"bp_2026_chair_ouster", "musk_openai_2026_verdict", "streeting_2026_resignation", "anthropic_2026_spacex_compute"}, []string{"spacex_2026_first_trade", "worldcup_2026_final_goal"}},
	"gpt62astra": {"GPT-6.2 Astra", []string{"anthropic_2026_mythos_access", "openai_2026_tbpn_acquisition", "meta_2026_manus_blocked", "social_media_2026_addiction_verdict"}, []string{"pga_2026_winner", "kentucky_2026_derby"}},
	"gpt61terra": {"GPT-6.1 Terra", []string{"oscars_2026_cinematography", "iea_2026_reserves", "wbd_2026_bid", "milano_2026_gold_count"}, []string{"artemis_2026_crater", "kentucky_2026_derby"}},
	"gpt6sol":    {"GPT-6 Sol", []string{"artemis_2026_crater", "apple_2026_neo", "openai_2026_pentagon_deal", "superbowl_2026_score"}, []string{"hungary_2026_final_seats", "kentucky_2026_derby"}},
	"gpt6luna":   {"GPT-6 Luna", []string{"artemis_2026_crater", "wbd_2026_bid", "minnesota_2026_pretti", "brazil_2026_floods_minas"}, []string{"kentucky_2026_derby", "pga_2026_winner"}},
	"gpt61sol":   {"GPT-6.1 Sol", []string{"afcon_2026_goal", "minnesota_2026_pretti", "venezuela_2026_custody", "milano_2026_men_skate"}, []string{"hungary_2026_final_seats", "kentucky_2026_derby"}},
	"gpt6astra":  {"GPT-6 Astra", gpt6AstraKnow, []string{"venezuela_2026_custody", "afcon_2026_goal"}},
}

// SavedGroup restores the group recorded in a report's boundary review.
func SavedGroup(review map[string]any) (ModelGroup, bool) {
	ids := func(key string) []string {
		list, _ := review[key].([]any)
		out := make([]string, 0, len(list))
		for _, v := range list {
			if id, ok := v.(string); ok && id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	name, _ := review["group"].(string)
	know, newer := ids("know"), ids("newer")
	if name == "" || len(newer) == 0 {
		return ModelGroup{}, false
	}
	return ModelGroup{Name: name, Know: know, Newer: newer}, true
}

// KnownGroupName reports whether name is one of the built-in group names.
func KnownGroupName(name string) bool {
	for _, group := range modelGroups {
		if group.Name == name {
			return true
		}
	}
	return false
}

func GroupForModel(model string) (ModelGroup, bool) {
	group, ok := modelGroups[integrity.CanonModel(model)]
	return group, ok
}

func ModelGroupVerdict(group ModelGroup, rows []Row, protocol string, attempts int) Verdict {
	v := Verdict{Status: "unknown", Label: "型号核对未确认", Protocol: protocol, Attempts: attempts, Rows: rows,
		BoundaryReview: &BoundaryReview{Method: ModelGroupMethod, Status: "unconfirmed",
			Group: group.Name, Know: append([]string{}, group.Know...), Newer: append([]string{}, group.Newer...)}}
	byID := make(map[string]Row, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	var correct, newerCorrect, newerUnknown int
	var olderEvents, newerEvents []string
	for _, id := range append(append([]string{}, group.Know...), group.Newer...) {
		row, ok := byID[id]
		if !ok || row.Error != "" || row.Kind == kindError || row.Kind == kindInvalid || strings.TrimSpace(row.Answer) == "" {
			v.BoundaryReview.Reason = "invalid"
			v.Reason = "本轮有题目未完成或回答无效，请展开对应题目查看原因并补测。本项不计分。"
			if len(group.Know) == 0 {
				v.Reason += "该型号的证明题尚未补齐，暂无法确认。"
			}
			return v
		}
	}
	for _, id := range group.Know {
		row := byID[id]
		if row.Kind == kindKnow {
			correct++
		} else {
			olderEvents = append(olderEvents, row.Event)
		}
	}
	for _, id := range group.Newer {
		row := byID[id]
		if row.Kind == kindKnow {
			newerCorrect++
			newerEvents = append(newerEvents, row.Event)
		} else if row.Kind == kindUnknown {
			newerUnknown++
		}
	}
	v.Correct, v.Total = correct, len(group.Know)
	v.Reason = fmt.Sprintf("本轮核对 %s 题组：%d 道证明题答对 %d 道，%d 道更新型号题中 %d 道答对、%d 道回答不知道。", group.Name, len(group.Know), correct, len(group.Newer), newerCorrect, newerUnknown)
	switch {
	case len(group.Know) == 0:
		v.BoundaryReview.Reason = "missing_proof"
		v.Reason += "证明题尚未补齐，暂时无法确认是否符合该型号。本项不计分。"
		return v
	case newerCorrect > 0:
		v.Status, v.Label = "mismatch", "与该型号不符，答出了更新型号才知道的事件"
		v.Reason += "答对的更新型号题：" + strings.Join(newerEvents, "、") + "。"
	case correct <= len(group.Know)-2:
		v.Status, v.Label = "mismatch", "与该型号不符，知识更旧"
		v.Reason += "没有答对的证明题：" + strings.Join(olderEvents, "、") + "。"
	default:
		v.Status, v.Label = "matched", "符合"+group.Name
	}
	v.Reason += "本项不计分。"
	v.BoundaryReview.Status, v.BoundaryReview.Reason = "complete", "complete"
	return v
}
