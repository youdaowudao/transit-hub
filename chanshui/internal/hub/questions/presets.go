package questions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

const AnswerTail = "请在最后单独一行以“最终答案: ”开头给出短结论。"
const candyTemplate = "一盒糖果中，圆形的有苹果味 {{ca}} 颗、桃子味 {{ct}} 颗、西瓜味 {{cm}} 颗；星形的有苹果味 {{sa}} 颗、桃子味 {{st}} 颗、西瓜味 {{sm}} 颗。糖果只能看出形状，看不出口味。从中取糖，至少要取出多少颗，才能保证取出的糖中，要么既有圆形苹果味又有星形桃子味，要么既有圆形桃子味又有星形苹果味？（取糖时可以看到形状，因此你可以先取够想要的圆形数量，再取够想要的星形数量）"
const colorTemplate = "体检时用的红绿色盲检测图上印着一个两位数。色觉完全正常的父亲盯着图看，说图上是「{{father}}」，{{remark}}；可女儿却坚持说图上是「{{daughter}}」。父亲反复核对后确认女儿看错了，随后崩溃了。医生最终确诊：女儿是红绿色盲，而父亲色觉完全正常。本题为经典孟德尔遗传模型：红绿色盲是完全外显的伴X染色体隐性遗传；排除新发突变、染色体异常及诊断误差。\n请结合遗传学规律给出最终结论：女儿是亲生还是非亲生？"
const socksTemplate = "抽屉里混放着 {{color_count}} 种颜色的袜子：{{stocks}}。袜子不成对地乱放在一起，房间里一片漆黑，只能靠手感把袜子摸出来，分辨不出颜色。至少要摸出多少只袜子，才能保证其中一定有 {{pair_count}} 双同色的袜子？"
const toolsTemplate = "Call {{tool_name}} exactly once with nonce {{nonce}} and sum equal to {{lhs}}+{{rhs}}. Do not print the arguments as prose. After the tool result arrives, reply only with the exact confirmation token from its result."
const webTemplate = "Use web search once to find the current latest release on github.com/golang/go/releases as of {{today}}. Include the release tag and cite the source. Do not answer from memory."
const numericTemplate = "{{opening}}。{{action}} {{count}} 个 {{min}} 到 {{max}}（含端点）的整数。每个位置都要单独选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器；也不要先编写或运行代码。{{ending}}{{separatorHint}}直接从第一个取值开始输出，不要在序列前重复数量、范围或任务说明。"

// Presets enumerates every probe used by the frozen upstream execution path.
// Each returned value is independent: editing cannot mutate a future seed.
func Presets() []Definition {
	out := []Definition{}
	orderOffset := 0
	add := func(id, title, kind, template string, params map[string]any, max int, judge Judge) {
		out = append(out, Definition{ID: id, Title: title, Kind: kind, Template: template, Parameters: params, MaxScore: max, Judge: judge, Enabled: true, Order: (len(out)+1)*100 - orderOffset})
	}
	r := rand.New(rand.NewSource(1))
	for _, q := range []iq.Question{iq.GenPill(), iq.GenCarWash(), iq.GenBatBall(), iq.GenAnchor(), iq.GenBridge()} {
		add(q.ID, q.Title, "iq", strings.TrimSuffix(q.Prompt, "\n"+AnswerTail), map[string]any{"generator": "static"}, q.MaxScore, Judge{Type: "legacy", ExpectedText: q.Answer})
	}
	cb := iq.GenColorBlind(r)
	add(cb.ID, cb.Title, "iq", colorTemplate, map[string]any{"generator": "color_blind_v1", "pairs": []any{
		map[string]any{"father": "88", "daughter": "38", "remark": "第二个数字明明是上下两个圆圈拼成的 8，左半圈的圆点是红色、右半圈是绿色，他看得清清楚楚"},
		map[string]any{"father": "72", "daughter": "27", "remark": "那张图的红绿圆点排布他看得分明，两个数字一前一后根本不会看反"},
		map[string]any{"father": "54", "daughter": "45", "remark": "两个数字左右位置分明，红绿圆点的边界他看得很清楚"},
	}}, cb.MaxScore, Judge{Type: "legacy", ExpectedText: cb.Answer})
	add("Q6", "经典糖果", "iq", candyTemplate, map[string]any{"generator": "candy_classic_v1", "ca": 7, "ct": 9, "cm": 8, "sa": 7, "st": 6, "sm": 4}, 16, Judge{Type: "legacy", ExpectedFrom: "generator"})
	add("Q7", "袜子配对", "iq", socksTemplate, map[string]any{"generator": "socks_v1", "colorsMin": 3, "colorsMax": 4, "stockMin": 2, "stockMax": 7, "pairsMin": 2, "pairsMax": 4, "colors": []string{"黑", "白", "灰", "蓝", "红", "棕"}}, 16, Judge{Type: "legacy", ExpectedFrom: "generator"})
	add("Q8", "糖果变体", "iq", candyTemplate, map[string]any{"generator": "candy_variant_v1", "min": 4, "max": 10}, 16, Judge{Type: "legacy", ExpectedFrom: "generator"})
	for i := range out {
		out[i].Order = int(out[i].ID[1]-'0') * 100
	}
	add("vision", "K线读数", "vision", "", map[string]any{"generator": "chart_v1", "lines": iq.ChartPanelTemplates()}, 20, Judge{Type: "legacy", ExpectedFrom: "generator"})
	events := ModelKnowledge()
	out = append(out, events...)
	orderOffset = (len(CurrentKnowledge()) - len(knowledge.Anchors)) * 100
	add("tools", "工具调用检测", "tools", toolsTemplate, map[string]any{"generator": "tools_v1", "lhs": 17, "rhs": 25}, 0, Judge{Type: "manual"})
	add("cache", "缓存读写验证", "cache", "", map[string]any{"generator": "cache_v1", "prefixHeading": "Stable agent brief. Keep this document byte-for-byte identical across turns.", "prefixLineTemplate": "Cache prefix sentence {{index}} remains byte for byte identical.", "turnTemplate": "Continue the same document. Do not rewrite earlier turns. Reply with only noted-{{turn}}."}, 0, Judge{Type: "manual"})
	out[len(out)-1].Enabled = false
	add("injection", "注入大小", "injection", "{{sample}}", map[string]any{"generator": "injection_v1"}, 0, Judge{Type: "manual"})
	add("web", "联网检测", "web", webTemplate, map[string]any{"generator": "web_v1", "deepSeekQuery": "DeepSeek V4 模型 发布"}, 0, Judge{Type: "manual"})
	add("thinking", "思维摘要检测", "thinking", "求最小正整数 n，使 n 除以 17 余 5，除以 19 余 7，除以 23 余 11。请推导并核验，最后给出数值。", map[string]any{"generator": "thinking_v1"}, 0, Judge{Type: "manual"})
	templates := fingerprint.HubChallengeTemplates()
	add("fingerprint:numeric", "数字指纹", "fingerprint_numeric", numericTemplate, map[string]any{"generator": "fingerprint_numeric_v1", "openings": templates.Openings, "actions": templates.Actions, "endings": templates.Endings, "separatorHints": templates.SeparatorHints}, 0, Judge{Type: "manual"})
	for i, p := range fingerprint.BehaviorProbes {
		add(fmt.Sprintf("fingerprint:behavior:%d", i+1), fmt.Sprintf("行为指纹 %d", i+1), "fingerprint_behavior", p.Prompt, map[string]any{"generator": "fingerprint_behavior_v1", "slot": i, "probeKind": p.Kind}, 0, Judge{Type: "manual"})
	}
	out = append(out, Definition{ID: "Q10", Title: "模型自报检查", Kind: "iq", Enabled: true, Order: 950, MaxScore: 5, Template: ModelIdentityPrompt, Parameters: map[string]any{"generator": "static"}, Judge: Judge{Type: ModelIdentityJudge}})
	return out
}
func Preset(id string) (Definition, bool) {
	if strings.HasPrefix(id, "knowledge:") {
		for _, bank := range []func() []Definition{ModelKnowledge, CurrentKnowledge, PreviousKnowledge, LegacyKnowledge} {
			for _, q := range bank() {
				if q.ID == id {
					return q, true
				}
			}
		}
		return Definition{}, false
	}
	for _, q := range Presets() {
		if q.ID == id {
			return q, true
		}
	}
	return Definition{}, false
}

const firstKnowledgeOrder = 1100

// LegacyKnowledge returns the exact original definitions for historical snapshots and migration.
func LegacyKnowledge() []Definition {
	defs := make([]Definition, 0, len(knowledge.Anchors))
	for i, p := range knowledge.Anchors {
		defs = append(defs, knowledgeDefinition(p, firstKnowledgeOrder+i*100))
	}
	return defs
}

// CurrentKnowledge returns the refreshed defaults, placing supplemental dates in existing gaps.
func CurrentKnowledge() []Definition {
	return knowledgeDefinitions(knowledge.CurrentAnchors())
}

// ModelKnowledge 保留 v6 题目的原定义，仅为新增题分配顺序。
func ModelKnowledge() []Definition {
	defs := CurrentKnowledge()
	known := map[string]bool{}
	for _, q := range defs {
		known[q.ID] = true
	}
	for _, p := range knowledge.ModelAnchors() {
		if !known["knowledge:"+p.ID] {
			defs = append(defs, knowledgeDefinition(p, 5000+len(defs)))
		}
	}
	return defs
}

// ModelAdditionIDs 是 v7 新增的默认题，不占用原有 40 道题的额度。
var modelAdditionIDs = func() []string {
	old := map[string]bool{}
	for _, q := range CurrentKnowledge() {
		old[q.ID] = true
	}
	var ids []string
	for _, q := range ModelKnowledge() {
		if !old[q.ID] {
			ids = append(ids, q.ID)
		}
	}
	return ids
}()

func ModelAdditionIDs() []string {
	return append([]string(nil), modelAdditionIDs...)
}

func IsModelAddition(id string) bool {
	for _, candidate := range modelAdditionIDs {
		if candidate == id {
			return true
		}
	}
	return false
}

func IsUntouchedModelAddition(q Definition) bool {
	if q.Kind != "knowledge" || q.Revision != 1 || !IsModelAddition(q.ID) {
		return false
	}
	preset, ok := Preset(q.ID)
	if !ok {
		return false
	}
	q.Revision = 0
	actual, _ := json.Marshal(q)
	expected, _ := json.Marshal(preset)
	return string(actual) == string(expected)
}

// PreviousKnowledge returns the exact v5 defaults and order for migration and saved reports.
func PreviousKnowledge() []Definition {
	return knowledgeDefinitions(knowledge.PreviousAnchors())
}

func knowledgeDefinitions(anchors []knowledge.Probe) []Definition {
	defs := make([]Definition, 0, len(anchors))
	nextReplacement, gap := firstKnowledgeOrder, 0
	for _, p := range anchors {
		if knowledge.IsSupplement(p.ID) {
			gap += 10
			defs = append(defs, knowledgeDefinition(p, nextReplacement-100+gap))
			continue
		}
		defs = append(defs, knowledgeDefinition(p, nextReplacement))
		nextReplacement += 100
		gap = 0
	}
	return defs
}

func knowledgeDefinition(p knowledge.Probe, order int) Definition {
	choices := make([]any, 0, len(p.Choices))
	for _, c := range p.Choices {
		choices = append(choices, map[string]any{"text": c.Text, "kind": c.Kind})
	}
	return Definition{
		ID: "knowledge:" + p.ID, Title: p.Event, Kind: "knowledge", Template: p.Question,
		Parameters: map[string]any{
			"generator": "event_v1", "date": p.HappenedAt, "event": p.Event, "choices": choices,
			"knowAliases": []string{}, "wrongAliases": []string{}, "unknownAliases": []string{},
		},
		Judge: Judge{Type: "manual"}, Enabled: true, Order: order,
	}
}
