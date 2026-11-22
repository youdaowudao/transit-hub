package prefixcheck

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

func fixtureSummary(raw json.RawMessage) ([]KnowledgeRow, json.RawMessage, bool) {
	var summary map[string]any
	object(raw, &summary)
	var rows []KnowledgeRow
	if knowledge, ok := summary["knowledge"].(map[string]any); ok {
		data, _ := knowledge["rows"].([]any)
		for _, row := range data {
			values, _ := row.(map[string]any)
			id, idOK := values["id"].(string)
			kind, kindOK := values["kind"].(string)
			event, _ := values["event"].(string)
			rawDate, _ := values["happened_at"].(string)
			if date, ok := KnowledgeDate(rawDate); idOK && kindOK && ok {
				rows = append(rows, KnowledgeRow{id, event, kind, date})
			}
		}
	}
	var vision json.RawMessage
	parts, _ := summary["parts"].([]any)
	for _, part := range parts {
		if values, ok := part.(map[string]any); ok && values["key"] == "vision" {
			vision, _ = json.Marshal(values)
			break
		}
	}
	return rows, vision, summary["singleAsk"] == true
}

func fixtureIdentity(item fixtureItem) *IdentityInput {
	identity := &IdentityInput{ItemID: item.ItemID, OrderIndex: item.Order}
	var data map[string]any
	object(item.Result, &data)
	judgment, _ := data["judgment"].(map[string]any)
	identity.Status, _ = judgment["status"].(string)
	if earned, ok := integer(judgment["earned"]); ok {
		identity.Earned = &earned
	}
	if upper, ok := integer(judgment["max"]); ok {
		identity.Max = &upper
	}
	return identity
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		return r
	}, s)
}

func fixtureCrossInput(report fixtureReport, reports []fixtureReport, analyses map[string]fixtureResult) CrossInput {
	r := ReportInput{ReportID: report.ReportID, AccountID: report.AccountID, Model: report.Model, Status: report.Status,
		CreatedAtMs: report.CreatedAtMs, UpdatedAtMs: report.UpdatedAtMs, Prefix: analyses[report.ReportID].Analysis,
	}
	r.KnowledgeRows, r.Vision, _ = fixtureSummary(report.Summary)
	in := CrossInput{Report: r}
	items := slices.Clone(report.Items)
	sort.Slice(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	for _, item := range items {
		in.Items = append(in.Items, ItemIndex{item.ItemID, item.Order, item.CompletedAtMs})
		if item.Kind == "injection" {
			_, injection, _ := Extract(item.stored())
			in.Injections = append(in.Injections, *injection)
		}
		if item.JudgeType == "model_identity" && in.Identity == nil {
			in.Identity = fixtureIdentity(item)
		}
	}
	distance := func(at int64) int64 {
		d := at - report.CreatedAtMs
		return max(d, -d)
	}
	for _, peer := range reports {
		if peer.ReportID == report.ReportID || peer.AccountID == report.AccountID || asciiLower(peer.Model) != asciiLower(report.Model) ||
			(peer.Status != "done" && peer.Status != "partial") || distance(peer.CreatedAtMs) > PeerWindow.Milliseconds() {
			continue
		}
		rows, vision, single := fixtureSummary(peer.Summary)
		if !single {
			in.Peers = append(in.Peers, PeerInput{ReportID: peer.ReportID, AccountName: peer.AccountName, Status: peer.Status,
				CreatedAtMs: peer.CreatedAtMs, Prefix: analyses[peer.ReportID].Analysis, KnowledgeRows: rows, Vision: vision})
		}
	}
	sort.Slice(in.Peers, func(i, j int) bool {
		a, b := distance(in.Peers[i].CreatedAtMs), distance(in.Peers[j].CreatedAtMs)
		if a != b {
			return a < b
		}
		return in.Peers[i].ReportID < in.Peers[j].ReportID
	})
	in.Peers = in.Peers[:min(len(in.Peers), PeerLimit)]
	return in
}

func TestCrossCheckFixture(t *testing.T) {
	reports, expected, without := readFixture(t)
	for _, report := range reports {
		t.Run(report.ReportID, func(t *testing.T) {
			requireFixtureJSON(t, expected[report.ReportID].CrossCheck, CrossCheck(fixtureCrossInput(report, reports, expected)))
			requireFixtureJSON(t, without[report.ReportID].CrossCheck, CrossCheck(fixtureCrossInput(report, reports, nil)))
		})
	}
}

func prefixJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCrossPrefixTypeAndTime(t *testing.T) {
	in := CrossInput{Report: ReportInput{Status: "done"}, Items: []ItemIndex{{"q", 1, 6}}}
	const ok = `{"version":1,"status":"ok","tokenizer":"o200k_base","computedAtMs":6,"reference":null,"` +
		`referenceReason":"本报告没有注入检测结果","overheads":[],"checked":0,"skipped":1}`
	for _, tc := range []struct{ name, raw, status, reason string }{
		{"absent", "", "none", ""},
		{"null", "null", "invalid", ""},
		{"encoded object", `"{}"`, "invalid", ""},
		{"wrong status", strings.Replace(ok, `"ok"`, "false", 1), "invalid", ""},
		{"boolean version", strings.Replace(ok, "version\":1", "version\":true", 1), "invalid", ""},
		{"decimal version", strings.Replace(ok, "version\":1", "version\":1.0", 1), "invalid", ""},
		{"missing timestamp", strings.Replace(ok, `"computedAtMs":6,`, "", 1), "invalid", ""},
		{"checked null", strings.Replace(ok, `"checked":0`, `"checked":null`, 1), "invalid", ""},
		{"reference without ID", strings.Replace(ok, `"reference":null`, `"reference":{"orderIndex":0,"aTotal":5,"aPromptTokens":1,"overhead":4}`, 1), "invalid", ""},
		{"overheads null", strings.Replace(ok, `"overheads":[]`, `"overheads":null`, 1), "invalid", ""},
		{"overheads wrong tuple size", strings.Replace(ok, `"overheads":[]`, `"overheads":[[0,1,2]]`, 1), "invalid", ""},
		{"unavailable", `{"version":1,"status":"unavailable","reason":"未分词"}`, "unavailable", "未分词"},
		{"stale", strings.Replace(ok, `"computedAtMs":6`, `"computedAtMs":5`, 1), "stale", ""},
		{"same ms", ok, "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in.Report.Prefix = nil
			if tc.raw != "" {
				in.Report.Prefix = json.RawMessage(tc.raw)
			}
			got := CrossCheck(in)
			if got.PrefixStatus != tc.status || got.PrefixReason != tc.reason || got.PrefixStale != (tc.status == "stale") ||
				got.PerItemChecked != (tc.status == "ok") || (got.Groups != nil) != (tc.status == "ok") {
				t.Fatalf("got %+v; want prefix %s, %q", got, tc.status, tc.reason)
			}
		})
	}
}

func TestCrossPrefixConsistency(t *testing.T) {
	fresh := func() (CrossInput, Analysis) {
		return CrossInput{
				Report:     ReportInput{Status: "done"},
				Items:      []ItemIndex{{"Q0", 0, 0}, {"Q1", 1, 3}, {"injection", 3, 6}},
				Injections: []InjectionItem{{ItemID: "injection", OrderIndex: 3, Points: []Point{{N: 1, Input: 5, Total: 5}}}},
			}, Analysis{Version: 1, Status: "ok", Tokenizer: TokenizerName, ComputedAtMs: 6,
				Reference: &Reference{ItemID: "injection", OrderIndex: 3, ATotal: 5, APromptTokens: 1, Overhead: 4},
				Overheads: [][2]int64{{0, 20}, {1, 21}}, Checked: 2}
	}
	for _, tc := range []struct {
		name, want, reason string
		edit               func(*CrossInput, *Analysis)
	}{
		{"consistent", "ok", "", func(*CrossInput, *Analysis) {}},
		{"no injections with reference", "invalid", noInjection, func(in *CrossInput, p *Analysis) {
			in.Items, in.Injections = in.Items[:2], nil
		}},
		{"multiple injections with reference", "invalid", multipleInjections, func(in *CrossInput, p *Analysis) {
			in.Items = append(in.Items, ItemIndex{"other", 4, 0})
			in.Injections = append(in.Injections, InjectionItem{ItemID: "other", OrderIndex: 4})
		}},
		{"multiple injections properly absent", "ok", multipleInjections, func(in *CrossInput, p *Analysis) {
			in.Items = append(in.Items, ItemIndex{"other", 4, 0})
			in.Injections = append(in.Injections, InjectionItem{ItemID: "other", OrderIndex: 4})
			p.Reference, p.ReferenceReason = nil, multipleInjections
		}},
		{"wrong reference ID", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.ItemID = "other" }},
		{"wrong reference order", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.OrderIndex = 0 }},
		{"wrong A total", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.ATotal, p.Reference.Overhead = 6, 5 }},
		{"wrong overhead", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.Overhead = 4000 }},
		{"wrong zero A count", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.APromptTokens, p.Reference.Overhead = 0, 5 }},
		{"wrong default A count", "invalid", "", func(in *CrossInput, p *Analysis) { p.Reference.APromptTokens, p.Reference.Overhead = 2, 3 }},
		{"valid custom count", "ok", "", func(in *CrossInput, p *Analysis) {
			in.Injections[0].Template = "说明：{{sample}}"
			p.Reference.APromptTokens, p.Reference.Overhead = 3, 2
		}},
		{"invalid custom count", "invalid", oldCustomTemplate, func(in *CrossInput, p *Analysis) {
			in.Injections[0].Template = "说明：{{sample}}"
			p.Reference.APromptTokens, p.Reference.Overhead = 0, 5
		}},
		{"invalid order", "invalid", "", func(in *CrossInput, p *Analysis) { p.Overheads[1][0] = 7 }},
		{"injection order", "invalid", "", func(in *CrossInput, p *Analysis) { p.Overheads[1][0] = 3 }},
		{"duplicate order", "invalid", "", func(in *CrossInput, p *Analysis) { p.Overheads[1][0] = 0 }},
		{"descending order", "invalid", "", func(in *CrossInput, p *Analysis) { p.Overheads[0], p.Overheads[1] = p.Overheads[1], p.Overheads[0] }},
		{"wrong checked", "invalid", "", func(in *CrossInput, p *Analysis) { p.Checked = 3 }},
		{"wrong skipped", "invalid", "", func(in *CrossInput, p *Analysis) { p.Skipped = 1 }},
		{"wrong tokenizer", "invalid", "", func(in *CrossInput, p *Analysis) { p.Tokenizer = "cl100k_base" }},
		{"lying missing points", "invalid", "", func(in *CrossInput, p *Analysis) {
			p.Reference, p.ReferenceReason = nil, noInjectionUsage
		}},
		{"empty reason for no reference", "invalid", noInjection, func(in *CrossInput, p *Analysis) {
			in.Items, in.Injections, p.Reference = in.Items[:2], nil, nil
		}},
		{"proper no injection reason", "ok", noInjection, func(in *CrossInput, p *Analysis) {
			in.Items, in.Injections, p.Reference, p.ReferenceReason = in.Items[:2], nil, nil, noInjection
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, analysis := fresh()
			tc.edit(&in, &analysis)
			in.Report.Prefix = prefixJSON(t, analysis)
			got := CrossCheck(in)
			if got.PrefixStatus != tc.want || got.ReferenceUnavailableReason != tc.reason {
				t.Fatalf("got %s, %q; want %s, %q", got.PrefixStatus, got.ReferenceUnavailableReason, tc.want, tc.reason)
			}
		})
	}
}

func TestCrossGroupingAndSelfReport(t *testing.T) {
	for _, tc := range []struct {
		overhead [][2]int64
		byPrompt bool
		groups   int
	}{
		{[][2]int64{{1, 5420}}, false, 1},
		{[][2]int64{{26, 1445}}, true, 1},
		{[][2]int64{{26, 5420}}, false, 1},
		{[][2]int64{{1, 1445}, {26, 1451}}, true, 1},
		{[][2]int64{{1, 1445}, {26, 1452}}, true, 2},
		{[][2]int64{}, false, 0},
	} {
		zero := int64(0)
		in := CrossInput{
			Report:     ReportInput{Model: "gpt-6.2-sol", Status: "done"},
			Items:      []ItemIndex{{"injection", 0, 0}, {"Q1", 1, 0}, {"Q10", 26, 0}},
			Identity:   &IdentityInput{ItemID: "Q10", OrderIndex: 26, Status: "known", Earned: &zero},
			Injections: []InjectionItem{{ItemID: "injection", Points: []Point{{N: 1, Input: 1446, Total: 1446}}}},
		}
		ref := &Reference{ItemID: "injection", ATotal: 1446, APromptTokens: 1, Overhead: 1445}
		in.Report.Prefix = prefixJSON(t, Analysis{Version: 1, Status: "ok", Tokenizer: TokenizerName, Reference: ref,
			Overheads: tc.overhead, Checked: len(tc.overhead), Skipped: 2 - len(tc.overhead)})
		got := CrossCheck(in)
		if !got.PerItemChecked || got.SelfReport.ByPrompt != tc.byPrompt || len(got.Groups) != tc.groups || !slices.Contains(got.Codes, "self_report_gpt5") {
			t.Errorf("overheads %v gave %+v", tc.overhead, got)
		}
		for _, group := range got.Groups {
			for _, item := range group.Items {
				if (item.OrderIndex == 26 && item.ItemID != "Q10") || (item.OrderIndex == 1 && item.ItemID != "Q1") {
					t.Errorf("wrong identity for order: %+v", item)
				}
			}
		}
		in.Report.Prefix = nil
		if old := CrossCheck(in); old.PerItemChecked || !old.SelfReport.ByPrompt || old.Reference.Match.Identity != "GPT-5" {
			t.Error("old report must source the self-report prompt match from the injection sample")
		}
	}
	for _, bad := range []string{`false`, `null`, `"0"`, `0.0`, `-1`, `9007199254740992`} {
		identity := fixtureIdentity(fixtureItem{ItemID: "Q10", Result: json.RawMessage(`{"judgment":{"status":"known","earned":` + bad + `,"max":5}}`)})
		got := CrossCheck(CrossInput{Report: ReportInput{Status: "done"}, Identity: identity})
		if got.Grade != "E" || slices.Contains(got.Codes, "self_report_gpt5") {
			t.Errorf("invalid earned %s gave grade %s and codes %v", bad, got.Grade, got.Codes)
		}
	}
	zero := int64(0)
	na := CrossCheck(CrossInput{Report: ReportInput{Status: "done"}, Identity: &IdentityInput{Status: "not_applicable", Earned: &zero}})
	if na.Grade != "E" || na.SelfReport.Status != "not_applicable" || len(na.Codes) != 0 {
		t.Fatalf("not_applicable gave %+v", na)
	}
}

func TestCrossCheckManualIdentityIsNotModelEvidence(t *testing.T) {
	for _, earned := range []int{0, 5} {
		var input IdentityInput
		err := json.Unmarshal([]byte(fmt.Sprintf(`{"ItemID":"Q10","Status":"known","Earned":%d,"Max":5,"Source":"manual"}`, earned)), &input)
		if err != nil {
			t.Fatal(err)
		}
		got := CrossCheck(CrossInput{Report: ReportInput{Model: "gpt-6.2-sol", Status: "done"}, Identity: &input})
		if got.Grade != "E" || slices.Contains(got.Codes, "self_report_gpt5") || got.SelfReport.ByPrompt || !got.SelfReport.Present {
			t.Fatalf("human score %d must be displayed without being used as model evidence: %+v", earned, got)
		}
	}
}

func TestCrossPromptMatchesAndGrades(t *testing.T) {
	for _, tc := range []struct {
		model     string
		total     int64
		overheads [][2]int64
		ids       []string
		grade     string
	}{
		{"gpt-6.2-sol", 4396, [][2]int64{{1, 4390}}, []string{"codex-gpt-5.5"}, "C"},
		{"claude-opus-5-5", 6, [][2]int64{{1, 17}}, []string{"sub2api-placeholder"}, "C"},
		{"claude-opus-5-5", 6, [][2]int64{{1, 4390}}, []string{"codex-gpt-5.5"}, "A"},
		{"gpt-6.2-sol", 6, [][2]int64{{1, 4382}, {2, 4390}}, []string{"codex-gpt-5.5", "codex-gpt-5.5"}, "C"},
		{"gpt-6.2-sol", 7, [][2]int64{{1, 5420}}, []string{"codex-gpt-6.2-sol"}, "A"},
	} {
		in := CrossInput{Report: ReportInput{Status: "done", Model: tc.model}, Items: []ItemIndex{{"injection", 0, 0}},
			Injections: []InjectionItem{{ItemID: "injection", Points: []Point{{N: 1, Total: tc.total}}}}}
		for _, pair := range tc.overheads {
			in.Items = append(in.Items, ItemIndex{fmt.Sprint(pair[0]), int(pair[0]), 0})
		}
		in.Report.Prefix = prefixJSON(t, Analysis{Version: 1, Status: "ok", Tokenizer: TokenizerName, Checked: len(tc.overheads),
			Overheads: tc.overheads, Reference: &Reference{ItemID: "injection", ATotal: tc.total, APromptTokens: 1, Overhead: tc.total - 1}})
		r := CrossCheck(in)
		if !r.PerItemChecked || r.Grade != tc.grade || r.Reference == nil || r.Reference.Match != nil || len(r.Groups) != len(tc.ids) {
			t.Fatalf("got %+v, want grade %s with groups %v", r, tc.grade, tc.ids)
		}
		for i, group := range r.Groups {
			if group.Match == nil || group.Match.ID != tc.ids[i] || group.Overhead != tc.overheads[i][1] {
				t.Errorf("wrong group %d: %+v", i, group)
			}
		}
	}
	unfinished := CrossCheck(CrossInput{Report: ReportInput{Status: "running"}, Injections: []InjectionItem{{ItemID: "injection", Points: []Point{{Total: 40}}}}})
	if unfinished.Grade != "E" || unfinished.Terminal || unfinished.Reference == nil {
		t.Fatalf("unfinished report = %+v", unfinished)
	}
}

func TestCrossKnowledgeVisionAndResultLimit(t *testing.T) {
	rows, _, _ := fixtureSummary(json.RawMessage(`{"knowledge":{"rows":[{"id":"a","kind":"unknown","happened_at":"2025-13-40"},{"id":"b","kind":"know","happened_at":"坏日期"},{"id":"c","kind":"unknown","happened_at":"2025-01-02T12:00:00Z"}]}}`))
	if len(rows) != 1 || rows[0].HappenedAt != "2025-01-02" {
		t.Fatalf("bad date handling = %+v", rows)
	}
	const full = `{"applicable":true,"known":true,"points":5,"max_points":5}`
	const zero = `{"applicable":true,"known":true,"points":0,"max_points":5}`
	knowing := func(id, late string) PeerInput {
		return PeerInput{ReportID: id, Vision: json.RawMessage(full), KnowledgeRows: []KnowledgeRow{
			{ID: "c", Kind: "know", HappenedAt: "2025-01-02"},
			{ID: "late", Kind: "know", HappenedAt: late},
		}}
	}
	in := CrossInput{Report: ReportInput{Status: "done", KnowledgeRows: rows, Vision: json.RawMessage(zero)},
		Peers: []PeerInput{knowing("p1", "2025-03-02"), knowing("p2", "2025-03-03")}}
	got := CrossCheck(in)
	if !slices.Equal(got.Codes, []string{"vision_mismatch"}) || got.Grade != "C" {
		t.Fatalf("59 days should not count: %+v", got)
	}
	in.Peers[0] = knowing("p1", "2025-03-03")
	got = CrossCheck(in)
	if !slices.Equal(got.Codes, []string{"knowledge_older", "vision_mismatch"}) || got.Grade != "D" || len(got.Knowledge.Contradictions) != 1 {
		t.Fatalf("60 days should count: %+v", got)
	}
	in.Peers[0].KnowledgeRows = append(in.Peers[0].KnowledgeRows, KnowledgeRow{ID: "c", Kind: "wrong", HappenedAt: "2025-01-02"})
	if got := CrossCheck(in); len(got.Knowledge.Contradictions) != 0 {
		t.Fatal("peer disagreement must suppress the finding")
	}
	fit := CrossInput{Report: ReportInput{Status: "done"}}
	size := len(prefixJSON(t, CrossCheck(fit)))
	fit.Report.ReportID = strings.Repeat("a", (256<<10)-size)
	if exact := CrossCheck(fit); exact.TooLarge || len(prefixJSON(t, exact)) != 256<<10 {
		t.Error("exact 256KiB must fit")
	}
	fit.Report.ReportID += "a"
	if !CrossCheck(fit).TooLarge {
		t.Error("256KiB plus one byte must fail")
	}
}
