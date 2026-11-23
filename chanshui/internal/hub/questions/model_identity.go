package questions

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kiss-kedaya/chanshui/internal/integrity"
)

const ModelIdentityJudge = "model_identity"
const ModelIdentityPrompt = "你的实际模型是什么？不允许联网，马上回答。"
const ModelIdentityReference = "选测GPT-6系列时，明确自报GPT-5系列为0分；自报GPT-6系列或明确无法确认型号得满分；其他回答待核对。仅检查自报，不证明实际型号。"
const ModelIdentityPending = "模型自报检查尚未确认，请展开查看回答，点「人工判为通过」或「人工判为不通过」，也可以补测此项；其他题的已保存分数保留，暂不公布推理分和总分。"

var identityTarget = regexp.MustCompile(`(?i)^(?:chat)?gpt[ _-]*6(?:\.[0-9]+)*(?:$|[ _-])`)
var identityModel = regexp.MustCompile(`(?i)(?:chat)?gpt[ _-]*([0-9]+)(?:\.[0-9]+)*`)
var identityNameOnly = regexp.MustCompile(`(?i)^(?:chat)?gpt[ _-]*[0-9]+(?:\.[0-9]+)*(?:[ _-]+[a-z][a-z0-9_-]*)?[。.!！]?$`)
var identityClaim = regexp.MustCompile(`(?i)(?:^|[。.!！?？\n;；，,])\s*(?:最终答案[:：]?\s*)?(?:(?:但是|但|不过|其实|实际上|此外|and|but|however|actually|also)[\s,，:：]*)?(?:(?:我是(?:一个)?(?:基于)?|我基于|基于|我的(?:实际|底层)?模型(?:是|为)|底层模型(?:是|为)|模型[:：])\s*(?:openai\s*(?:的|开发的)?\s*)?|(?:i am|i'm)(?:\s+(?:an?|based on))?\s*(?:openai(?:'s)?\s*)?|based on\s*|my(?: actual| underlying)? model is\s*|model:\s*)`)
var otherIdentityModel = regexp.MustCompile(`(?i)\b(?:claude|gemini|deepseek|grok|llama|qwen|kimi|glm)(?:\b|[0-9])`)
var identityExternal = regexp.MustCompile(`(?i)例如|比如|举例|示例|别人|他人|对方|转述|假设|用户(?:的)?回答|用户说|他(?:的)?回答|他说|someone|for example|e\.g\.|(?:response|answer)\s+(?:from|by)`)
var identityConditional = regexp.MustCompile(`如果`)
var identityAlternation = regexp.MustCompile(`(?i)^[\s,，。.!！?？;；_-]*(?:(?:sol|astra|terra|luna|mini|nano|pro)[ _-]+)?(?:或|还是|或者|[/|&+、]|or(?:$|[ _-])|(?:以及|和|与|and[ _-]+(?:an?[ _-]+)?)[ _-]*(?:(?:chat)?gpt|claude|gemini|deepseek|grok|llama|qwen|kimi|glm|unknown|another[ _-]+model|other[ _-]+model|未知|(?:另一个|另一种|其他|其它)模型))`)
var identityBareList = regexp.MustCompile(`(?i)^[ \t]*[,，、/&+]?[ \t]*(?:(?:chat)?gpt[ _-]*[0-9]+(?:\.[0-9]+)*|claude|gemini|deepseek|grok|llama|qwen|kimi|glm)(?:[ _-]+[a-z0-9][a-z0-9_.-]*)?[ \t]*(?:[。.!！?？;；\n)]|）|\]|\}|】|$)`)

var identityUncertain = regexp.MustCompile(`(?i)(?:无法|不能|没法|无从|难以)\p{Han}{0,4}(?:确认|确定|核实|核对|得知|获知|知道|判断|验证|核验)|不确定|不清楚|不知道|\b(?:(?:cannot|can't|unable\s+to)\s+(?:[a-z]+\s+){0,2}(?:confirm|determine|verify|know)|don't\s+know|do\s+not\s+know|not\s+sure|unsure|uncertain|no\s+way\s+to\s+(?:know|verify|confirm))\b`)
var identityReportedUncertainty = regexp.MustCompile(`(?i)(?:助手|模型|有人|她|他)\s*(?:说|回答|表示|答道)|\b(?:assistant|model)\s+(?:said|reported|replied|answered)\b`)
var identityEnglishConditional = regexp.MustCompile(`(?i)\b(?:if|unless)\b`)

// ModelIdentityApplies is shared by execution and aggregation, even for an item
// that never started. The provider prefix does not change the requested model.
func ModelIdentityApplies(model string) bool {
	if integrity.ModelFamily(model) != "gpt" {
		return false
	}
	if i := strings.IndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	return identityTarget.MatchString(strings.TrimSpace(model))
}

// Quoted text cannot establish the respondent's identity. Blank the quoted
// span, preserving unrelated, unquoted self declarations and their order.
func unquotedIdentity(normalized, original string) string {
	runes := []rune(original)
	var close rune
	lineStart := true
	block := false
	for i, r := range runes {
		if r == '\n' {
			lineStart = true
			block = false
		}
		if block {
			runes[i] = ' '
			continue
		}
		if close != 0 {
			runes[i] = ' '
			if r == close {
				close = 0
			}
			continue
		}
		if lineStart && r == '>' {
			block = true
			runes[i] = ' '
			continue
		}
		if !unicode.IsSpace(r) {
			lineStart = false
		}
		switch r {
		case '"', '`':
			close = r
		case '“':
			close = '”'
		case '「':
			close = '」'
		case '『':
			close = '』'
		case '\'':
			if i == 0 || !(runes[i-1] >= 'a' && runes[i-1] <= 'z' || runes[i-1] >= 'A' && runes[i-1] <= 'Z' || runes[i-1] >= '0' && runes[i-1] <= '9') {
				close = r
			}
		}
		if close != 0 {
			runes[i] = ' '
		}
	}
	// Project the original scanner's exclusions onto the normalized text.
	// Removed runes have no output position.
	out := []rune(normalized)
	originalPosition, position := 0, 0
	for _, r := range original {
		if normalizeIdentityRune(r) >= 0 {
			if runes[originalPosition] == ' ' {
				out[position] = ' '
			}
			position++
		}
		originalPosition++
	}
	return string(out)
}

// Locate declarations and local sentence context once. Queries use indexes,
// not repeated scans of the prefix of an arbitrarily long upstream reply.
func identityPositions(s string) (map[int]bool, func(int, bool) bool) {
	external := identityExternal.FindAllStringIndex(s, -1)
	conditional := identityConditional.FindAllStringIndex(s, -1)
	englishConditional := identityEnglishConditional.FindStringIndex(s)
	reported := identityReportedUncertainty.FindStringIndex(s)
	breaks := []int{}
	marker := 0
	for offset, r := range s {
		for marker < len(external) && external[marker][1] <= offset {
			marker++
		}
		insideMarker := marker < len(external) && external[marker][0] <= offset
		if insideMarker || !strings.ContainsRune("。.!！?？;；", r) {
			continue
		}
		if r == '.' && offset > 0 && offset+1 < len(s) && s[offset-1] >= '0' && s[offset-1] <= '9' && s[offset+1] >= '0' && s[offset+1] <= '9' {
			continue
		}
		breaks = append(breaks, offset+utf8.RuneLen(r))
	}
	clear := func(offset int, wholeSentence bool) bool {
		boundary := sort.SearchInts(breaks, offset+1)
		start, end := 0, offset
		if boundary > 0 {
			start = breaks[boundary-1]
		}
		if wholeSentence {
			end = len(s)
			if boundary < len(breaks) {
				end = breaks[boundary]
			}
			if reported != nil && reported[0] < end {
				return false
			}
			// A period after an English condition can be an abbreviation.
			// Keep the reply open for manual review when that context is present.
			if englishConditional != nil && englishConditional[0] < end {
				return false
			}
		}
		// A reporting marker can introduce later sentences.
		if len(external) > 0 && external[0][0] < end {
			return false
		}
		i := sort.Search(len(conditional), func(i int) bool { return conditional[i][1] > end }) - 1
		return i < 0 || conditional[i][0] < start
	}
	direct := map[int]bool{}
	for _, loc := range identityClaim.FindAllStringIndex(s, -1) {
		direct[loc[1]] = clear(loc[1], false)
	}
	return direct, clear
}
func identityChoice(tail string) bool {
	if identityAlternation.MatchString(tail) || identityBareList.MatchString(tail) {
		return true
	}
	delimiter := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }
	tail = strings.TrimLeftFunc(tail, delimiter)
	for _, alias := range []string{"sol", "astra", "terra", "luna", "mini", "nano", "pro"} {
		if len(tail) < len(alias) || !strings.EqualFold(tail[:len(alias)], alias) {
			continue
		}
		rest := tail[len(alias):]
		if rest == "" {
			tail = ""
			break
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if delimiter(r) {
			tail = strings.TrimLeftFunc(rest, delimiter)
			break
		}
	}
	return identityAlternation.MatchString(tail) || identityBareList.MatchString(tail)
}
func normalizeIdentityRune(r rune) rune {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
		return -1
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212', '\ufe58', '\ufe63', '\uff0d':
		return '-'
	case '\u00a0', '\u2007', '\u202f', '\u3000':
		return ' '
	}
	if r >= '\uff10' && r <= '\uff19' || r >= '\uff21' && r <= '\uff3a' || r >= '\uff41' && r <= '\uff5a' {
		return r - 0xfee0
	}
	return r
}
func gradeModelIdentity(g Generated, answer string) Judgment {
	j := Judgment{Status: "unknown", Max: g.Definition.MaxScore, Tags: []string{}, Reason: "自报内容无法按本题规则确认，未给出确定分数；请查看完整回答，必要时在本报告补测此项。"}
	raw := strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "’", "'", "‘", "'", "：", ":").Replace(answer))
	normalized := strings.Map(normalizeIdentityRune, raw)
	// Normalize before removing quotes, then derive every byte index from s.
	s := unquotedIdentity(normalized, raw)
	direct, clearPrefix := identityPositions(s)
	mentions := identityModel.FindAllStringSubmatchIndex(s, -1)
	other := otherIdentityModel.FindAllStringIndex(s, -1)
	otherDeclared := false
	for _, m := range other {
		otherDeclared = otherDeclared || direct[m[0]]
	}
	if otherDeclared {
		return j
	}
	claims := map[string]bool{}
	for _, m := range mentions {
		bare := len(mentions) == 1 && len(other) == 0 && identityNameOnly.MatchString(strings.TrimSpace(s))
		if bare || direct[m[0]] {
			// Only choices immediately attached to a declared model are ambiguous;
			// an unrelated "version or alias" later in the reply is not a conflict.
			if identityChoice(s[m[1]:]) {
				return j
			}
			claims[s[m[2]:m[3]]] = true
		}
	}
	if len(claims) > 1 {
		return j
	}
	points := -1
	if claims["5"] {
		points = 0
		j.Reason = "自报型号异常：选测GPT-6系列，但回答自称GPT-5系列，本题0分；请结合完整回答和其他检测核查上游型号。"
	}
	if claims["6"] {
		points = g.Definition.MaxScore
		j.Reason = "未发现自报型号异常，按本题规则得满分；自报不证明实际型号。"
	}
	if len(claims) == 0 && len(mentions) == 0 && len(other) == 0 {
		for _, match := range identityUncertain.FindAllStringIndex(s, -1) {
			if clearPrefix(match[0], true) {
				points = g.Definition.MaxScore
				j.Reason = "未确认具体型号，按本题规则通过并得满分；不证明实际型号。"
				break
			}
		}
	}
	if points >= 0 {
		j.Status, j.Earned = "known", &points
	}
	return j
}
