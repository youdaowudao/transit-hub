package prefixcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

const testSystemPrompt = "禁止使用任何工具。直接给出结论，不要展开推导过程；回答最后单独一行：最终答案: <短结论>。"

type fixtureItem struct {
	ItemID, Kind, JudgeType, Prompt, Template string
	Order                                     int
	CompletedAtMs                             int64
	ImageAvailable                            bool
	Result                                    json.RawMessage
}

func (f fixtureItem) stored() StoredItem {
	return StoredItem{
		ItemID: f.ItemID, Kind: f.Kind, JudgeType: f.JudgeType, OrderIndex: f.Order,
		Prompt: f.Prompt, Template: f.Template, ImageAvailable: f.ImageAvailable, Result: f.Result,
	}
}

type fixtureReport struct {
	ReportID, AccountID, AccountName, Model, Status string
	CreatedAtMs, UpdatedAtMs                        int64
	Request, Summary                                json.RawMessage
	Items                                           []fixtureItem
}

type fixtureResult struct{ Analysis, CrossCheck json.RawMessage }

func readFixture(t *testing.T) (reports []fixtureReport, expected, without map[string]fixtureResult) {
	t.Helper()
	var fixture struct {
		Reports                          []fixtureReport
		Expected, ExpectedWithoutPerItem map[string]fixtureResult
	}
	raw, err := os.ReadFile("testdata/prefixcheck-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Reports) != 14 {
		t.Fatalf("want 14 fixture reports, got %d", len(fixture.Reports))
	}
	return fixture.Reports, fixture.Expected, fixture.ExpectedWithoutPerItem
}

func requireFixtureJSON(t *testing.T, want json.RawMessage, value any) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var gotObject, wantObject map[string]any
	if !object(got, &gotObject) || !object(want, &wantObject) {
		t.Fatal("fixture comparison requires two JSON objects")
	}
	if !reflect.DeepEqual(gotObject, wantObject) {
		for field, expected := range wantObject {
			if !reflect.DeepEqual(gotObject[field], expected) {
				t.Errorf("%s = %#v; want %#v", field, gotObject[field], expected)
			}
		}
		for field := range gotObject {
			if _, exists := wantObject[field]; !exists {
				t.Errorf("unexpected field %q", field)
			}
		}
	}
}

func TestAnalyzeFixture(t *testing.T) {
	reports, expected, _ := readFixture(t)
	for _, report := range reports {
		t.Run(report.ReportID, func(t *testing.T) {
			sort.Slice(report.Items, func(i, j int) bool { return report.Items[i].Order < report.Items[j].Order })
			var candidates []Candidate
			var injections []InjectionItem
			skipped := 0
			for _, item := range report.Items {
				candidate, injection, ignore := Extract(item.stored())
				switch {
				case candidate != nil:
					candidates = append(candidates, *candidate)
				case injection != nil:
					injections = append(injections, *injection)
				case ignore:
					skipped++
				default:
					t.Fatal("item had no extraction result")
				}
			}
			got, err := Analyze(context.Background(), candidates, injections, skipped, testSystemPrompt, Count)
			if err != nil || got.ComputedAtMs == 0 {
				t.Fatalf("Analyze returned %+v, %v", got, err)
			}
			got.ComputedAtMs = 0
			requireFixtureJSON(t, expected[report.ReportID].Analysis, got)
		})
	}
}

func TestExtractUsageBoundaries(t *testing.T) {
	for _, tc := range []struct {
		fields string
		valid  bool
		total  int64
	}{
		{`"UsagePresent":true,"Status":200,"InputTokens":20`, true, 20},
		{`"UsagePresent":true,"InputTokens":20`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":-1`, false, 0},
		{`"UsagePresent":false,"Status":200,"InputTokens":20`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":true`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":20,"CacheRead":""`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":20.0`, false, 0},
		{`"UsagePresent":true,"Status":"200","InputTokens":20`, false, 0},
		{`"UsagePresent":true,"Status":200.0,"InputTokens":20`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":20,"CacheRead":null`, true, 20},
		{`"UsagePresent":true,"Status":200,"InputTokens":9007199254740992`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":9007199254740990,"CacheRead":2`, false, 0},
		{`"UsagePresent":true,"Status":200,"InputTokens":9007199254740991`, true, maxSafeInteger},
		{`"UsagePresent":true,"Status":200,"InputTokens":20,"CacheRead":1,"CacheCreation5m":2,"CacheCreation1h":3`, true, 26},
	} {
		input := StoredItem{Kind: "iq", OrderIndex: 5, Result: json.RawMessage(`{"metrics":{` + tc.fields + `},"evidence":{"prompt":"题面"}}`)}
		got, inj, skipped := Extract(input)
		if skipped == tc.valid || inj != nil || (got != nil) != tc.valid {
			t.Errorf("extract %s = %+v, %+v, %v; valid=%v", tc.fields, got, inj, skipped, tc.valid)
		}
		if got != nil && (got.OrderIndex != 5 || got.Text != "题面" || got.HasSystem || got.Total != tc.total) {
			t.Errorf("extract %s had wrong candidate: %+v", tc.fields, got)
		}
	}
}

func TestExtractPromptEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, kind, prompt string
		image, valid       bool
	}{
		{"tools", "tools", "题面", false, false},
		{"image", "iq", "题面", true, false},
		{"empty", "iq", "", false, false},
		{"long Chinese", "iq", strings.Repeat("长", 6000), false, false},
		{"redacted prompt", "iq", "题面" + redactedPromptText, false, false},
		{"limit", "iq", strings.Repeat("x", 16384), false, true},
		{"over limit", "iq", strings.Repeat("x", 16385), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := fmt.Sprintf(`{"metrics":{"UsagePresent":true,"Status":200,"InputTokens":20},"evidence":{"prompt":%q}}`, tc.prompt)
			got, _, skipped := Extract(StoredItem{Kind: tc.kind, Prompt: "fallback", ImageAvailable: tc.image, Result: json.RawMessage(result)})
			if (got != nil) != tc.valid || skipped == tc.valid {
				t.Fatalf("candidate=%v skipped=%v; want valid=%v", got != nil, skipped, tc.valid)
			}
		})
	}
	for _, tc := range []struct {
		result, text string
		system       bool
	}{
		{`{"evidence":{"prompt":"题面","metrics":{"Status":200,"UsagePresent":true}}}`, "题面", true},
		{`{"evidence":{"prompt":[],"metrics":{"Status":200,"UsagePresent":true}},"metrics":{"Status":200,"UsagePresent":true}}`, "fallback", false},
		{`{"evidence":{"prompt":"题面","metrics":"{}"},"metrics":{"Status":200,"UsagePresent":true}}`, "题面", false},
	} {
		got, _, _ := Extract(StoredItem{Kind: "iq", Prompt: "fallback", Result: json.RawMessage(tc.result)})
		if got == nil || got.Text != tc.text || got.HasSystem != tc.system {
			t.Errorf("candidate from %s: %+v", tc.result, got)
		}
	}
}

func TestExtractInjectionPoints(t *testing.T) {
	const a = `{"n":1,"input":5,"total":5}`
	const b = `{"n":1599,"input":900,"total":900}`
	for _, tc := range []struct {
		points, template string
		count            int
	}{
		{`[` + a + `,` + b + `]`, "", 2},
		{`[` + b + `,` + a + `]`, "", 0},
		{`[` + a + `,{"n":1598,"input":900,"total":900}]`, "", 1},
		{`[{"n":1,"input":5,"total":4},` + b + `]`, "", 0},
		{`[{"N":1,"Input":5,"Total":5},` + b + `]`, "", 0},
		{`[` + a + `,{"N":1599,"Input":900,"Total":900}]`, "", 1},
		{`[{"n":1,"input":1,"total":9007199254740992},` + b + `]`, "", 0},
		{`{"n":1,"input":5,"total":5}`, "", 0},
		{`[]`, "", 0},
		{`[{"n":6,"input":20,"total":30},{"n":3202,"input":1000,"total":1000}]`, "中{{sample}}+{{sample}}", 2},
	} {
		result := json.RawMessage(`{"error":false,"evidence":{"points":` + tc.points + `,"reliable":false,"error":"failed"}}`)
		_, inj, skipped := Extract(StoredItem{Kind: "injection", ItemID: "A", OrderIndex: 3, Template: tc.template, Result: result})
		if skipped || inj == nil || len(inj.Points) != tc.count || inj.Error != "failed" || inj.Reliable == nil || *inj.Reliable {
			t.Errorf("points %s: got %+v, skipped=%v; want %d usable points", tc.points, inj, skipped, tc.count)
		}
	}
}

func TestAnalyzeReferenceAndCancellation(t *testing.T) {
	inj := InjectionItem{ItemID: "injection", OrderIndex: 3, Points: []Point{{N: 1, Input: 5, Total: 5}}}
	for _, tc := range []struct {
		injections []InjectionItem
		reason     string
	}{
		{nil, noInjection},
		{[]InjectionItem{inj, inj}, multipleInjections},
		{[]InjectionItem{{ItemID: "injection", OrderIndex: 3}}, noInjectionUsage},
		{[]InjectionItem{inj}, ""},
	} {
		out, err := Analyze(context.Background(), nil, tc.injections, 2, testSystemPrompt, Count)
		if err != nil || out.ReferenceReason != tc.reason || (out.Reference == nil) != (tc.reason != "") || out.Overheads == nil {
			t.Errorf("reference = %+v, %v; want reason %q", out, err, tc.reason)
		}
		if out.Reference != nil && *out.Reference != (Reference{ItemID: "injection", OrderIndex: 3, ATotal: 5, APromptTokens: 1, Overhead: 4}) {
			t.Errorf("incorrect reference: %+v", out.Reference)
		}
	}
	for _, cancelAt := range []int{3, 4} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		candidates := []Candidate{{OrderIndex: 1, Text: "q1", HasSystem: true, Total: 100}, {OrderIndex: 2, Text: "q2", Total: 100}}
		out, err := Analyze(ctx, candidates, []InjectionItem{inj}, 0, testSystemPrompt, func(string) (int, error) {
			calls++
			if calls == cancelAt {
				cancel()
			}
			return 1, nil
		})
		cancel()
		if calls != cancelAt || !errors.Is(err, context.Canceled) || out.Overheads != nil || out.Reference != nil {
			t.Errorf("cancel at %d: got calls=%d %+v %v", cancelAt, calls, out, err)
		}
	}
}

func TestAnalysisSizeAndReasonLimit(t *testing.T) {
	out := Analysis{
		Version: 1, Status: "ok", Tokenizer: TokenizerName, ComputedAtMs: maxSafeInteger,
		Reference: &Reference{ItemID: "custom:" + strings.Repeat("a", 32), OrderIndex: maxSafeInteger, ATotal: maxSafeInteger, APromptTokens: maxSafeInteger, Overhead: maxSafeInteger},
		Overheads: make([][2]int64, 128), Checked: 128,
	}
	for i := range out.Overheads {
		out.Overheads[i] = [2]int64{int64(i), -maxSafeInteger}
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 4096 {
		t.Fatalf("analysis serialized to %d bytes (%v)", len(raw), err)
	}
	for _, s := range []string{strings.Repeat("中", 3500), strings.Repeat("a", 512), strings.Repeat("a", 513)} {
		got := LimitReason(s)
		if !utf8.ValidString(got) || len(got) > 512 || (len(s) == 512 && got != s) || (len(s) > 512 && !strings.HasSuffix(got, "…")) {
			t.Errorf("incorrect reason limit for %d input bytes, got %d bytes", len(s), len(got))
		}
	}
}
