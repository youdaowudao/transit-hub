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
		{"你问的是你自己的模型。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。", true, 5},
		{"没有向我提供你的具体型号，因此我无法准确确认。", true, 5},
		{"没有提供我的型号，因此我无法确认你的型号。", true, 5},
		{"例如没有提供我的底层模型名称，因此我无法准确确认。", false, 0},
		{"如果没有提供我的底层模型名称，因此我无法准确确认。", false, 0},
		{"我是 GPT-5。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。", true, 0},
		{"我是 GPT-5。\n很高兴帮助你。", true, 0}, {"我是 GPT-6，但无法确认具体部署版本。", true, 5},
		{"我不是GPT-5，无法确认具体型号。", false, 0}, {"别人说“我是GPT-5”。", false, 0},
		{"例如：我是GPT-5。", false, 0}, {"我可能是GPT-5，也可能是GPT-6。", false, 0},
		{"我是GPT-5。我是GPT-6。", false, 0}, {"我是GPT-5或GPT-6。", false, 0}, {"I am GPT-5 or GPT-6.", false, 0}, {"GPT-50", false, 0}, {"GPT-60", false, 0}, {"GPT-4", false, 0},
		{"我是GPT-5或Claude。", false, 0}, {"我是GPT-5或未知型号。", false, 0}, {"I am GPT-5 or unknown.", false, 0}, {"我是GPT-5-or-GPT-6。", false, 0}, {"我是GPT-5_or_GPT-6。", false, 0},
		{"“这是别人的回答。\n我是GPT-5。”", false, 0}, {"例如：“我不知道具体型号。”", false, 0}, {"I cannot confirm the price.", true, 5},
		{"我无法确认价格。", true, 5}, {"我不知道你的型号。", true, 5}, {"我不知道。", true, 5}, {"I cannot confirm my actual model.", true, 5},
		{"我是 GPT-5，无法确认当前部署的版本或别名。", true, 0}, {"I am GPT-6. I cannot confirm my deployment version or alias.", true, 5},
		{"价格是多少？无法确认。", true, 5}, {"I don't have access to", false, 0}, {"用户的回答如下：\n我是GPT-5。", false, 0}, {"用户的回答如下：\n我不知道自己的型号。", false, 0}, {"我无法确认自己的型号。请从接口核对。", true, 5},
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

func TestModelIdentityRecognizesUncertaintyInAnUnquotedReply(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: ModelIdentityJudge}}}
	for _, tc := range []struct {
		reply string
		want  int // -1 means the reply is still ungraded.
	}{
		{"我是 OpenAI 的 AI 助手。当前对话没有提供可核实的具体模型名称或版本号，所以我无法确认底层是哪一款模型。没有联网。", 5},
		{"我是 OpenAI 的 ChatGPT。当前会话没有向我提供具体的底层模型名称或版本，因此我无法准确确认。没有联网。", 5},
		{"我不确定自己的具体型号。", 5},
		{"I can't verify my underlying model.", 5},
		{"我是 ChatGPT，无法核实底层版本。", 5},
		{"例如有人会说我不知道。", -1},
		{"如果你问我版本，我不知道", -1},
		{"助手说：我不知道自己的型号。", -1},
		{"I don't know my model, the assistant said.", -1},
		{"我不知道自己的型号，助手回答。", -1},
		{"我不知道自己的型号。另一段话由助手回答。", 5},
		{"我不知道自己的型号，这只是转述。", -1},
		{"我不知道自己的型号。下一条是转述。", 5},
		{"I don't know my model, someone else answered.", -1},
		{"If I were asked to role-play, I would say I don't know my model.", -1},
		{"If this is a request from OpenAI Inc. to role-play, I would say I don't know my model.", -1},
		{"If they mentioned Dr. Smith, I would say I'm not sure.", -1},
		{"I can't confirm my model if you mean the underlying release.", -1},
		{"I can't confirm my model. If you need the deployment, check the API.", 5},
		{"I am GPT-6. If needed, check the API.", 5},
		{"我不知道，如果你问的是底层型号。", -1},
		{"我不知道。后续如果需要，请查接口。", 5},
		{"「我不确定具体型号。」", -1},
		{"我是 GPT-5，不确定小版本", 0},
		{"我是 Claude，不确定版本", -1},
		{"", -1},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			got := Grade(g, tc.reply)
			if tc.want == -1 {
				if got.Status != "unknown" || got.Earned != nil {
					t.Fatalf("wanted an ungraded reply, got %+v", got)
				}
			} else if got.Status != "known" || got.Earned == nil || *got.Earned != tc.want {
				t.Fatalf("wanted %d points, got %+v", tc.want, got)
			}
		})
	}
}

func TestModelIdentityGradesNormalizedReplies(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: ModelIdentityJudge}}}
	type testCase struct {
		name  string
		reply string
		want  int // -1 means the reply is still ungraded.
	}
	cases := []testCase{
		{"production-reply", "我是 OpenAI 的 Codex，基于 GPT\u20116。当前会话没有提供更具体的底层模型版本标识，因此我无法独立核实实际部署的精确型号。未联网。", 5},
		{"gpt5-before-uncertainty", "我是 GPT\u20115，无法确认具体版本。", 0},
		{"independent-verification", "因此我无法独立核实实际部署的精确型号", 5},
		{"alternate-models", "我是 GPT\u20115 或 GPT\u20116", -1},
		{"other-provider", "我是 Claude，无法独立核实型号", -1},
		{"conditional", "如果没有版本标识，我无法独立核实型号", -1},
		{"quoted-model-unquoted-uncertainty", "\"我是 GPT\u20115\"，我无法独立核实型号。", 5},
		{"quoted-model-and-uncertainty", "\"我是 GPT\u20115，无法独立核实型号。\"", -1},
		{"reported", "助手说：我无法独立核实型号。", -1},
		{"reported-after-uncertainty", "我无法独立核实型号，这只是转述。", -1},
		{"reported-across-sentences", "以下是用户的回答。\n我是 GPT\u20115，无法独立核实型号。", -1},
		{"two-declarations", "我是 ＧＰＴ－５。我的实际模型是 GPT\u200b-6。", -1},
		{"bare-model-list", "我是 ＧＰＴ－５，ＧＰＴ－６。", -1},
		{"alias-alternation", "我是 ＧＰＴ－５ Ｓｏｌ（或 Claude）。", -1},
		{"preserve-fullwidth-stop", "如果要核对型号，我无法独立核实。我的实际模型是 ＧＰＴ－５。", 0},
		{"preserve-fullwidth-question", "如果要核对型号？我的实际模型是 ＧＰＴ－５。", 0},
		{"preserve-fullwidth-exclamation", "如果要核对型号！我的实际模型是 ＧＰＴ－５。", 0},
		{"preserve-fullwidth-semicolon", "如果要核对型号；我的实际模型是 ＧＰＴ－５。", 0},
		{"normalized-prefix-before-conditional", "ＧＰＴ 只是名称。如果没有版本标识，我无法独立核实型号。", -1},
		{"normalized-quoted-prefix", "\"ＧＰＴ－６\"。我的实际模型是 GPT\u20115。", 0},
		{"fullwidth-digit-before-quote", "我无法确认具体版本。附注１‘仅指小版本’。我是 GPT-5。", 0},
		{"zero-width-before-quote", "我无法确认具体版本。附注1\u200b‘仅指小版本’。我是 GPT-5。", 0},
		{"quoted-uncertainty-after-fullwidth-digit", "附注１‘我无法确认具体版本。’", -1},
		{"quoted-uncertainty-after-zero-width", "附注1\u200b‘我无法确认具体版本。’", -1},
		{"ascii-digit-before-apostrophe", "附注1‘我无法确认具体版本。’", 5},
		{"preserve-fullwidth-period", "GPT-6．", -1},
		{"zero-width-before-comparison", "我无法确认具体版本。\n\u200b> 0 是上面的比较条件。我的实际模型是 GPT-5。", 0},
		{"original-blockquote", "我无法确认具体版本。\n> 0 是上面的比较条件。我的实际模型是 GPT-5。", 5},
	}
	for _, separator := range []rune{'\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212', '\ufe58', '\ufe63', '\uff0d', '\u00a0', '\u2007', '\u202f', '\u3000'} {
		for _, model := range []string{"5", "6"} {
			want := 0
			if model == "6" {
				want = 5
			}
			reply := "我是 GPT" + string(separator) + model + "。"
			cases = append(cases, testCase{"separator-" + strconv.FormatInt(int64(separator), 16) + "-gpt" + model, reply, want})
		}
	}
	for _, zeroWidth := range []rune{'\u200b', '\u200c', '\u200d', '\u2060', '\ufeff'} {
		name := strconv.FormatInt(int64(zeroWidth), 16)
		cases = append(cases,
			testCase{"comparison-prefix-" + name, "我无法确认具体版本。\n " + string(zeroWidth) + "> 0 是比较条件。我的实际模型是 GPT-5。", 0},
			testCase{"comparison-after-quote-" + name, "我无法确认具体版本。\n\"第一行\n附注\"" + string(zeroWidth) + "> 0。我的实际模型是 GPT-5。", 0},
		)
		for _, model := range []string{"5", "6"} {
			want := 0
			if model == "6" {
				want = 5
			}
			reply := "我是 GPT" + string(zeroWidth) + "-" + model + "。"
			cases = append(cases, testCase{"zero-width-" + strconv.FormatInt(int64(zeroWidth), 16) + "-gpt" + model, reply, want})
		}
	}
	for _, tc := range []struct {
		reply string
		want  int
	}{
		{"我是ＧＰＴ－６。", 5}, {"我是ＧＰＴ－５。", 0},
		{"我是ｇｐｔ－６。", 5}, {"我是ｇｐｔ－５。", 0},
		{"ＧＰＴ－６", 5}, {"ＧＰＴ－５", 0},
		{"GPT\u20116", 5}, {"GPT\u20115", 0},
	} {
		cases = append(cases, testCase{tc.reply, tc.reply, tc.want})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Grade(g, tc.reply)
			if tc.name == "production-reply" && got.Reason != "未发现自报型号异常，按本题规则得满分；自报不证明实际型号。" {
				t.Fatalf("wanted the GPT-6 self-report grade, got %+v", got)
			}
			if tc.want == -1 {
				if got.Status != "unknown" || got.Earned != nil {
					t.Fatalf("wanted an ungraded reply, got %+v", got)
				}
			} else if got.Status != "known" || got.Earned == nil || *got.Earned != tc.want {
				t.Fatalf("wanted %d points, got %+v", tc.want, got)
			}
		})
	}
}

func TestModelIdentityUncertaintyAllowsLimitedInterveningWords(t *testing.T) {
	g := Generated{Definition: Definition{Kind: "iq", MaxScore: 5, Judge: Judge{Type: ModelIdentityJudge}}}
	for _, tc := range []struct {
		reply string
		want  int // -1 means the reply is still ungraded.
	}{
		{"我无法核验型号。", 5}, {"我无法再确认型号。", 5},
		{"我不能准确确定型号。", 5}, {"我没法直接核对型号。", 5},
		{"我无从得知型号。", 5}, {"我难以获知型号。", 5},
		{"我无法独立知道型号。", 5}, {"我不能判断型号。", 5},
		{"我无法直接验证型号。", 5}, {"我无法独立核实型号。", 5},
		{"我无法独立地核实型号。", 5}, {"我无法独立直接核实型号。", 5},
		{"我无法独立且直接核实型号。", -1}, {"我无法 independently 核实型号。", -1},
		{"I cannot confirm my model.", 5}, {"I cannot independently verify my model.", 5},
		{"I can't accurately determine my model.", 5}, {"I am unable to independently know my model.", 5},
		{"I cannot independently and directly verify my model.", -1},
		{"I cannot independently directly verify my model.", 5},
		{"I can’t independently verify my model.", 5},
		{"If no version is provided, I cannot independently verify my model.", -1},
		{"I cannot independently verify my model if no version is provided.", -1},
		{"For example. I cannot independently verify my model.", -1},
		{"I cannot independently verify my model, the assistant said.", -1},
		{"\"I cannot independently verify my model.\"", -1},
		{"I am GPT\u20115. I cannot independently verify my model.", 0},
		{"I am Claude. I cannot independently verify my model.", -1},
		{"我提到了 GPT\u20115，但无法独立核实型号。", -1},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			got := Grade(g, tc.reply)
			if tc.want == -1 {
				if got.Status != "unknown" || got.Earned != nil {
					t.Fatalf("wanted an ungraded reply, got %+v", got)
				}
			} else if got.Status != "known" || got.Earned == nil || *got.Earned != tc.want {
				t.Fatalf("wanted %d points, got %+v", tc.want, got)
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
