package questions

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var customID = regexp.MustCompile(`^custom:[0-9a-f]{32}$`)
var placeholder = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
var numText = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func Clone(q Definition) Definition {
	b, _ := json.Marshal(q)
	var out Definition
	_ = json.Unmarshal(b, &out)
	return out
}
func text(v any) string { s, _ := v.(string); return s }
func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) {
			return int(n), true
		}
	}
	return 0, false
}
func Int(p map[string]any, k string) int     { n, _ := integer(p[k]); return n }
func Text(p map[string]any, k string) string { return text(p[k]) }
func Strings(v any) []string {
	switch a := v.(type) {
	case []string:
		return a
	case []any:
		out := make([]string, 0, len(a))
		for _, v := range a {
			s, ok := v.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func objects(v any) []map[string]any {
	a, _ := v.([]any)
	out := []map[string]any{}
	for _, v := range a {
		m := object(v)
		if m == nil {
			return nil
		}
		out = append(out, m)
	}
	return out
}
func requiredTokens(t string, required []string, optional ...string) error {
	allow := map[string]bool{}
	for _, k := range append(append([]string{}, required...), optional...) {
		allow[k] = true
	}
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(t, -1) {
		if !allow[m[1]] {
			return fmt.Errorf("未知占位符 {{%s}}，请使用此题列出的字段", m[1])
		}
		seen[m[1]] = true
	}
	if strings.Contains(placeholder.ReplaceAllString(t, ""), "{{") || strings.Contains(placeholder.ReplaceAllString(t, ""), "}}") {
		return fmt.Errorf("占位符格式不完整，请使用双花括号")
	}
	for _, k := range required {
		if !seen[k] {
			return fmt.Errorf("题目缺少必需占位符 {{%s}}", k)
		}
	}
	return nil
}
func rangeInt(p map[string]any, key string, lo, hi int) error {
	n, ok := integer(p[key])
	if !ok || n < lo || n > hi {
		return fmt.Errorf("参数%s必须为%d～%d的整数", key, lo, hi)
	}
	return nil
}
func pairRange(p map[string]any, a, b string, lo, hi int) error {
	if e := rangeInt(p, a, lo, hi); e != nil {
		return e
	}
	if e := rangeInt(p, b, lo, hi); e != nil {
		return e
	}
	if Int(p, a) > Int(p, b) {
		return fmt.Errorf("%s不能大于%s", a, b)
	}
	return nil
}
func stringParameter(p map[string]any, key string) error {
	if _, ok := p[key].(string); !ok {
		return fmt.Errorf("参数%s必须为文字", key)
	}
	return nil
}
func objectKeys(p map[string]any, allowed ...string) error {
	for key := range p {
		ok := false
		for _, a := range allowed {
			if key == a {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("不支持嵌套字段%s，请只填写此题列出的内容", key)
		}
	}
	return nil
}
func maxLen(s, label string, n int) error {
	if len(s) > n {
		return fmt.Errorf("%s超过%d字节，请缩短", label, n)
	}
	return nil
}
func Validate(q Definition) error {
	preset, isPreset := Preset(q.ID)
	if !isPreset && !customID.MatchString(q.ID) {
		return fmt.Errorf("题目编号无效，请使用新增题目生成的编号")
	}
	if strings.TrimSpace(q.Title) == "" || utf8.RuneCountInString(q.Title) > 100 {
		return fmt.Errorf("题名不能为空且最多100字")
	}
	if q.Order < 0 || q.Order > 999999 {
		return fmt.Errorf("排序必须为0～999999")
	}
	if e := maxLen(q.Template, "题干", 8192); e != nil {
		return e
	}
	if q.Parameters == nil {
		return fmt.Errorf("缺少题目参数")
	}
	gen := Text(q.Parameters, "generator")
	kind := map[string]string{"static": "iq", "color_blind_v1": "iq", "candy_classic_v1": "iq", "candy_variant_v1": "iq", "socks_v1": "iq", "chart_v1": "vision", "event_v1": "knowledge", "tools_v1": "tools", "cache_v1": "cache", "injection_v1": "injection", "web_v1": "web", "thinking_v1": "thinking", "fingerprint_numeric_v1": "fingerprint_numeric", "fingerprint_behavior_v1": "fingerprint_behavior"}[gen]
	if kind == "" || kind != q.Kind {
		return fmt.Errorf("题型与已注册生成器不一致")
	}
	allowed := map[string][]string{"static": {}, "color_blind_v1": {"pairs"}, "candy_classic_v1": {"ca", "ct", "cm", "sa", "st", "sm"}, "candy_variant_v1": {"min", "max"}, "socks_v1": {"colorsMin", "colorsMax", "stockMin", "stockMax", "pairsMin", "pairsMax", "colors"}, "chart_v1": {"lines"}, "event_v1": {"date", "event", "choices", "knowAliases", "wrongAliases", "unknownAliases"}, "tools_v1": {"lhs", "rhs"}, "cache_v1": {"prefixHeading", "prefixLineTemplate", "turnTemplate"}, "injection_v1": {}, "web_v1": {"deepSeekQuery"}, "thinking_v1": {}, "fingerprint_numeric_v1": {"openings", "actions", "endings", "separatorHints"}, "fingerprint_behavior_v1": {"slot", "probeKind"}}
	for k := range q.Parameters {
		ok := k == "generator"
		for _, a := range allowed[gen] {
			ok = ok || a == k
		}
		if !ok {
			return fmt.Errorf("不支持参数%s，请保留此题列出的字段", k)
		}
	}
	if q.Kind == "iq" || q.Kind == "vision" {
		switch q.Judge.Type {
		case ModelIdentityJudge:
			if q.Kind != "iq" || gen != "static" || strings.TrimSpace(q.Template) == "" || q.MaxScore < 1 || q.MaxScore > 100 {
				return fmt.Errorf("模型自报检查只支持普通推理文字题，题干不能为空，满分必须为1～100")
			}
		case "legacy":
			if !isPreset || preset.Judge.Type != "legacy" || q.MaxScore != preset.MaxScore || q.Template != preset.Template || q.Judge.ExpectedText != preset.Judge.ExpectedText {
				return fmt.Errorf("原判分规则只支持原题干、答案与固定满分；修改后请选通用判题或人工查看")
			}
			if q.ID == "vision" && !reflect.DeepEqual(Clone(q).Parameters, Clone(preset).Parameters) {
				return fmt.Errorf("图内说明已修改，请改选通用数值判题或人工查看")
			}
			if q.ID == "Q6" && !reflect.DeepEqual(Clone(q).Parameters, Clone(preset).Parameters) {
				return fmt.Errorf("经典糖果库存已修改，请切换数值判题或人工查看")
			}
		case "manual":
			if q.MaxScore != 0 {
				return fmt.Errorf("人工查看题满分应为0，不参与自动得分")
			}
			if e := maxLen(q.Judge.ReferenceAnswer, "参考答案", 8192); e != nil {
				return e
			}
		case "text", "number", "keywords":
			if q.MaxScore < 1 || q.MaxScore > 100 {
				return fmt.Errorf("通用自动判题满分必须为1～100")
			}
			if q.Judge.ExpectedFrom != "" && q.Judge.ExpectedFrom != "generator" {
				return fmt.Errorf("答案来源无效")
			}
			if q.Judge.ExpectedFrom == "generator" && gen == "static" {
				return fmt.Errorf("普通文字题没有内置求解器，请填写答案")
			}
			if q.Judge.Type == "text" && q.Judge.ExpectedFrom == "" && (q.Judge.ExpectedText == "" || len(q.Judge.ExpectedText) > 1024) {
				return fmt.Errorf("文本标准答案必填且不超过1024字节")
			}
			if math.IsInf(q.Judge.ExpectedNumber, 0) || math.IsNaN(q.Judge.ExpectedNumber) {
				return fmt.Errorf("数值答案必须有限")
			}
			if q.Judge.Type == "keywords" {
				if q.Judge.Match != "any" && q.Judge.Match != "all" {
					return fmt.Errorf("关键词请选择任一或全部命中")
				}
				if len(q.Judge.Required) < 1 || len(q.Judge.Required) > 20 || len(q.Judge.Forbidden) > 20 {
					return fmt.Errorf("必需词1～20条，禁词最多20条")
				}
				seen := map[string]bool{}
				for _, v := range append(append([]string{}, q.Judge.Required...), q.Judge.Forbidden...) {
					key := strings.ToLower(strings.TrimSpace(v))
					if key == "" || utf8.RuneCountInString(v) > 64 || seen[key] {
						return fmt.Errorf("关键词不能空白、重复或超过64字")
					}
					seen[key] = true
				}
			}
		default:
			return fmt.Errorf("请选择文本、数值、关键词或人工判题")
		}
	} else if q.Judge.Type != "manual" || q.MaxScore != 0 {
		return fmt.Errorf("协议、知识和指纹项保留固定检测规则，请勿配置推理分值")
	}
	req := []string{}
	optional := []string{}
	switch gen {
	case "static", "thinking_v1", "fingerprint_behavior_v1":
	case "color_blind_v1":
		req = []string{"father", "daughter", "remark"}
		pairs := objects(q.Parameters["pairs"])
		if len(pairs) < 1 || len(pairs) > 32 {
			return fmt.Errorf("色盲数字对需要1～32组")
		}
		for _, p := range pairs {
			if e := objectKeys(p, "father", "daughter", "remark"); e != nil {
				return e
			}
			for _, k := range []string{"father", "daughter"} {
				v := Text(p, k)
				if len(v) != 2 || v[0] < '0' || v[0] > '9' || v[1] < '0' || v[1] > '9' {
					return fmt.Errorf("父亲/女儿数字必须恰好两位ASCII数字")
				}
			}
			if e := stringParameter(p, "remark"); e != nil {
				return e
			}
			if utf8.RuneCountInString(Text(p, "remark")) > 256 {
				return fmt.Errorf("数字对说明最多256字")
			}
		}
	case "candy_classic_v1":
		req = []string{"ca", "ct", "cm", "sa", "st", "sm"}
		for _, k := range req {
			if e := rangeInt(q.Parameters, k, 1, 20); e != nil {
				return e
			}
		}
	case "candy_variant_v1":
		req = []string{"ca", "ct", "cm", "sa", "st", "sm"}
		if e := pairRange(q.Parameters, "min", "max", 4, 10); e != nil {
			return e
		}
	case "socks_v1":
		req = []string{"color_count", "stocks", "pair_count"}
		for _, x := range [][4]string{{"colorsMin", "colorsMax", "3", "4"}, {"stockMin", "stockMax", "2", "7"}, {"pairsMin", "pairsMax", "2", "4"}} {
			lo, hi := 2, 7
			if x[0] == "colorsMin" {
				lo, hi = 3, 4
			}
			if x[0] == "pairsMin" {
				lo, hi = 2, 4
			}
			if e := pairRange(q.Parameters, x[0], x[1], lo, hi); e != nil {
				return e
			}
		}
		colors := Strings(q.Parameters["colors"])
		seen := map[string]bool{}
		if len(colors) < Int(q.Parameters, "colorsMax") || len(colors) > 6 {
			return fmt.Errorf("颜色名数量应覆盖最多颜色且不超过6")
		}
		for _, s := range colors {
			if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > 12 || seen[s] {
				return fmt.Errorf("颜色名不能为空、重复或超过12字")
			}
			seen[s] = true
		}
	case "chart_v1":
		lines := Strings(q.Parameters["lines"])
		if len(lines) != 6 {
			return fmt.Errorf("识图必须保持六行说明")
		}
		for i, line := range lines {
			r := []string{}
			if i == 1 || i == 2 {
				r = []string{"bar_count"}
			}
			if e := requiredTokens(line, r, "bar_count", "last_bar_id"); e != nil {
				return e
			}
		}
		if q.Template != "" {
			return fmt.Errorf("识图题请编辑图内六行说明，不另发文字题面")
		}
	case "event_v1":
		d := Text(q.Parameters, "date")
		t, e := time.Parse("2006-01-02", d)
		if e != nil || t.Year() < 1900 || t.Year() > 2100 {
			return fmt.Errorf("知识事件日期应为1900～2100年的合法日期")
		}
		if strings.TrimSpace(Text(q.Parameters, "event")) == "" || utf8.RuneCountInString(Text(q.Parameters, "event")) > 100 {
			return fmt.Errorf("事件名必填且最多100字")
		}
		cs := objects(q.Parameters["choices"])
		if len(cs) < 2 || len(cs) > 8 {
			return fmt.Errorf("知识题选项需要2～8个")
		}
		know, unknown := 0, 0
		seen := map[string]bool{}
		for _, c := range cs {
			if e := objectKeys(c, "text", "kind"); e != nil {
				return e
			}
			s := strings.ToLower(strings.TrimSpace(Text(c, "text")))
			if s == "" || len(s) > 1024 || seen[s] {
				return fmt.Errorf("知识题选项不能为空、重复或超过1024字节")
			}
			seen[s] = true
			switch Text(c, "kind") {
			case "know":
				know++
			case "unknown":
				unknown++
			case "wrong":
			default:
				return fmt.Errorf("选项类别必须为know/wrong/unknown")
			}
		}
		originalChoices := isPreset && q.ID == "knowledge:olympics" && reflect.DeepEqual(Clone(q).Parameters["choices"], Clone(preset).Parameters["choices"])
		if (know != 1 && !originalChoices) || unknown < 1 {
			return fmt.Errorf("知识题必须恰有一个正确选项，至少一个不知道选项")
		}
		for _, k := range []string{"knowAliases", "wrongAliases", "unknownAliases"} {
			a := Strings(q.Parameters[k])
			if a == nil || len(a) > 20 {
				return fmt.Errorf("知识答案别名必须为数组，最多20条")
			}
			for _, v := range a {
				if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 64 {
					return fmt.Errorf("知识答案别名不能为空或超过64字")
				}
			}
		}
	case "tools_v1":
		req = []string{"tool_name", "nonce", "lhs", "rhs"}
		for _, k := range []string{"lhs", "rhs"} {
			if e := rangeInt(q.Parameters, k, 1, 1000); e != nil {
				return e
			}
		}
	case "cache_v1":
		for _, k := range []string{"prefixHeading", "prefixLineTemplate", "turnTemplate"} {
			if e := stringParameter(q.Parameters, k); e != nil {
				return e
			}
		}
		if utf8.RuneCountInString(Text(q.Parameters, "prefixHeading")) > 256 || utf8.RuneCountInString(Text(q.Parameters, "prefixLineTemplate")) > 256 || len(Text(q.Parameters, "turnTemplate")) > 1024 {
			return fmt.Errorf("缓存说明或轮提示超过上限")
		}
		if e := requiredTokens(Text(q.Parameters, "prefixLineTemplate"), []string{"index"}); e != nil {
			return e
		}
		if e := requiredTokens(Text(q.Parameters, "turnTemplate"), []string{"turn"}); e != nil {
			return e
		}
		if q.Template != "" {
			return fmt.Errorf("缓存请编辑固定前缀及轮提示字段")
		}
	case "injection_v1":
		req = []string{"sample"}
		if len(q.Template) > 1024 {
			return fmt.Errorf("注入说明最多1024字节")
		}
	case "web_v1":
		if e := stringParameter(q.Parameters, "deepSeekQuery"); e != nil {
			return e
		}
		if strings.TrimSpace(Text(q.Parameters, "deepSeekQuery")) == "" {
			return fmt.Errorf("DeepSeek搜索文字不能为空")
		}
		req = []string{"today"}
		if len(Text(q.Parameters, "deepSeekQuery")) > 8192 {
			return fmt.Errorf("DeepSeek搜索文字过长")
		}
		if e := requiredTokens(Text(q.Parameters, "deepSeekQuery"), nil); e != nil {
			return e
		}
	case "fingerprint_numeric_v1":
		req = []string{"count", "min", "max"}
		optional = []string{"opening", "action", "ending", "separatorHint", "toolBan", "direct"}
		for _, k := range []string{"openings", "actions", "endings", "separatorHints"} {
			a := Strings(q.Parameters[k])
			if len(a) < 1 || len(a) > 16 {
				return fmt.Errorf("指纹片段%s需要1～16条", k)
			}
			for _, v := range a {
				if v == "" || utf8.RuneCountInString(v) > 256 {
					return fmt.Errorf("指纹片段不能为空或超过256字")
				}
			}
		}
	}
	if gen == "fingerprint_behavior_v1" {
		if e := rangeInt(q.Parameters, "slot", 0, 2); e != nil {
			return e
		}
		if Int(q.Parameters, "slot") < 0 || Int(q.Parameters, "slot") > 2 || Text(q.Parameters, "probeKind") != []string{"refusal-x2", "refusal-x2", "formatting"}[Int(q.Parameters, "slot")] {
			return fmt.Errorf("行为指纹槽位与特征类型固定，不可修改")
		}
	}
	if e := requiredTokens(q.Template, req, optional...); e != nil {
		return e
	}
	if q.Kind != "vision" && gen != "cache_v1" && strings.TrimSpace(q.Template) == "" {
		return fmt.Errorf("题干不能为空")
	}
	return nil
}
