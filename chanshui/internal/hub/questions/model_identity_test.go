package questions

import (
	"strconv"
	"strings"
	"testing"
)

func TestModelIdentityGradesDirectClaimsWithoutClosedCorrectAnswerList(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: "model_identity"}}}
	for _, tc := range []struct {
		answer string
		known  bool
		points int
	}{
		{"我是基于 GPT-5 的模型。", true, 0}, {"我的实际模型是 GPT-5.5。", true, 0}, {"GPT5.6-sol", true, 0},
		{"我是 GPT-6。", true, 5}, {"我是 OpenAI 的 **GPT-6.2 Sol**。", true, 5}, {"I am based on GPT_6.1_sol.", true, 5},
		{"我不知道具体型号。", true, 5}, {"不知道", true, 5}, {"无法确认我的具体型号。", true, 5}, {"I don't know my exact model.", true, 5},
		{"我是 OpenAI 的 ChatGPT。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。没有联网。", true, 5},
		{"当前会话没有提供我的底层模型型号，因此我无法确认。", true, 5},
		{"我是 ChatGPT。", false, 0},
		{"你问的是你自己的模型。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。", false, 0},
		{"没有向我提供你的具体型号，因此我无法准确确认。", false, 0},
		{"没有提供我的型号，因此我无法确认你的型号。", false, 0},
		{"例如没有提供我的底层模型名称，因此我无法准确确认。", false, 0},
		{"如果没有提供我的底层模型名称，因此我无法准确确认。", false, 0},
		{"我是 GPT-5。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。", true, 0},
		{"我是 GPT-5。\n很高兴帮助你。", true, 0}, {"我是 GPT-6，但无法确认具体部署版本。", true, 5},
		{"我不是GPT-5，无法确认具体型号。", false, 0}, {"别人说“我是GPT-5”。", false, 0},
		{"例如：我是GPT-5。", false, 0}, {"我可能是GPT-5，也可能是GPT-6。", false, 0},
		{"我是GPT-5。我是GPT-6。", false, 0}, {"我是GPT-5或GPT-6。", false, 0}, {"I am GPT-5 or GPT-6.", false, 0}, {"GPT-50", false, 0}, {"GPT-60", false, 0}, {"GPT-4", false, 0},
		{"我是GPT-5或Claude。", false, 0}, {"我是GPT-5或未知型号。", false, 0}, {"I am GPT-5 or unknown.", false, 0}, {"我是GPT-5-or-GPT-6。", false, 0}, {"我是GPT-5_or_GPT-6。", false, 0},
		{"“这是别人的回答。\n我是GPT-5。”", false, 0}, {"例如：“我不知道具体型号。”", false, 0}, {"I cannot confirm the price.", false, 0},
		{"我无法确认价格。", false, 0}, {"我不知道你的型号。", false, 0}, {"我不知道。", true, 5}, {"I cannot confirm my actual model.", true, 5},
		{"我是 GPT-5，无法确认当前部署的版本或别名。", true, 0}, {"I am GPT-6. I cannot confirm my deployment version or alias.", true, 5},
		{"价格是多少？无法确认。", false, 0}, {"I don't have access to", false, 0}, {"用户的回答如下：\n我是GPT-5。", false, 0}, {"用户的回答如下：\n我不知道自己的型号。", false, 0}, {"我无法确认自己的型号。请从接口核对。", true, 5},
		{"我是 GPT-5，或者 Claude。", false, 0}, {"I am GPT-5, or an unknown model.", false, 0}, {"我是 GPT-5。我的实际模型是 Claude。", false, 0},
		{"你好！我的实际模型是 GPT-5。", true, 0}, {"Hello! I am GPT-6.", true, 5}, {"我是GPT-5。Claude是别的模型。", true, 0}, {"我是GPT-5。GPT-6是别的型号。", true, 0},
		{"我是GPT-5，我的部署叫“小助手”。", true, 0}, {"你好！我无法确认我的实际型号。", true, 5},
		{"我是GPT-5，Claude。", false, 0}, {"I am GPT-5, GPT-6.", false, 0}, {"我是GPT-5，Claude是另一个模型。", true, 0}, {"他说'这是假设。\n我是GPT-5。'", false, 0}, {"I am GPT-5, and I can help with coding.", true, 0}, {"我是GPT-5，和你见面很开心。", true, 0}, {"我是GPT-5和Claude。", false, 0}, {"I am GPT-5 (or Claude).", false, 0}, {"我是 GPT-5（或 Claude）。", false, 0}, {"I am GPT-5 — or an unknown model.", false, 0}, {"如果要核对，我可以说明。我的实际模型是 GPT-5。", true, 0}, {"I am GPT-5 (Claude).", false, 0}, {"我是 GPT-5（或 Claude）。", false, 0}, {"我是 GPT-5 Sol（或 Claude）。", false, 0}, {"I am GPT-5, but my actual model is Claude.", false, 0}, {"我是GPT-5，但我的实际模型是Claude。", false, 0}, {"其实我是GPT-5，但我不知道部署版本或别名。", true, 0}, {"我是一个AI助手。", false, 0}, {"", false, 0},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			j := Grade(g, tc.answer)
			if (j.Status == "known") != tc.known || (j.Earned != nil) != tc.known || j.Earned != nil && *j.Earned != tc.points {
				t.Fatalf("got %+v", j)
			}
			if j.Reason == "" {
				t.Fatal("missing human-readable reason")
			}
		})
	}
}
func TestModelIdentityPresetKeepsExactUserPromptAndFivePoints(t *testing.T) {
	q, ok := Preset("Q10")
	if !ok {
		t.Fatal("new scored preset missing")
	}
	if q.Kind != "iq" || q.MaxScore != 5 || !q.Enabled || q.Order != 950 {
		t.Fatal(q)
	}
	g, err := Generate(q, 123)
	if err != nil {
		t.Fatal(err)
	}
	if g.Prompt != "你的实际模型是什么？不允许联网，马上回答。" {
		t.Fatal("prompt modified", g.Prompt)
	}
	if IsOriginalQuestion(q) || g.Expected == "" {
		t.Fatal("self report must use local rule, not original self grade")
	}
}

func TestModelIdentityTargetAndEditableRuleConstraints(t *testing.T) {
	for _, model := range []string{"gpt-6", "gpt-6.1-sol", "openai/gpt-6.2-astra", "GPT_6.1_Terra", "GPT 6 Sol"} {
		if !ModelIdentityApplies(model) {
			t.Error("missing GPT-6 target", model)
		}
	}
	for _, model := range []string{"gpt-60", "gpt-5.5", "claude-opus-5-5", "gpt-6x", "proxy"} {
		if ModelIdentityApplies(model) {
			t.Error("wrong target", model)
		}
	}
	q, _ := Preset("Q10")
	for _, mutate := range []func(*Definition){func(q *Definition) { q.MaxScore = 0 }, func(q *Definition) { q.MaxScore = 101 }, func(q *Definition) { q.Template = " " }, func(q *Definition) { q.Kind = "vision" }, func(q *Definition) { q.Parameters = map[string]any{"generator": "socks_v1"} }} {
		copy := Clone(q)
		mutate(&copy)
		if Validate(copy) == nil {
			t.Fatal("invalid identity definition accepted", copy)
		}
	}
	q.ID = "custom:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	q.MaxScore = 7
	g, err := Generate(q, 1)
	if err != nil {
		t.Fatal(err)
	}
	j := Grade(g, "我是GPT-6。")
	if j.Earned == nil || *j.Earned != 7 || g.Prompt != ModelIdentityPrompt {
		t.Fatal("custom scored copy changed rule", g, j)
	}
}

func TestModelIdentityLongReplyKeepsDirectStatements(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: "model_identity"}}}
	j := Grade(g, strings.Repeat("你好！我是GPT-6。\n", 16000))
	if j.Status != "known" || j.Earned == nil || *j.Earned != 5 {
		t.Fatal(j)
	}
}

func TestModelIdentityReportedAnswerStaysUnconfirmedAcrossSentences(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: "model_identity"}}}
	for _, answer := range []string{
		"以下是用户的回答。\n我是GPT-5。",
		"以下是用户的回答。\n我是GPT-6。",
		"以下是用户的回答。\n我不知道自己的型号。",
		"例如。\n我不知道自己的型号。",
		"例如。\n我是GPT-5。",
		"以下是用户的回答！我是GPT-5。",
		"Answer from someone else.\nI am GPT-5.",
		"For example.\nI cannot confirm my actual model.",
	} {
		t.Run(answer, func(t *testing.T) {
			j := Grade(g, answer)
			if j.Status != "unknown" || j.Earned != nil {
				t.Fatalf("reported answer received a definite grade: %+v", j)
			}
		})
	}
}

func BenchmarkModelIdentityManyClaims(b *testing.B) {
	for _, size := range []int{1000, 4000, 16000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			text := strings.Repeat("你好！我是GPT-6。\n", size)
			g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: "model_identity"}}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				j := Grade(g, text)
				if j.Status != "known" || j.Earned == nil || *j.Earned != 5 {
					b.Fatal(j)
				}
			}
		})
	}
}
