package prefixcheck

import (
	"embed"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

const (
	PrefixFraming      = 10
	NoPrefixMax        = 12
	SameRouteTolerance = 6
	MatchExact         = 3
	MatchNear          = 8
)

type Prompt struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Tokens   int    `json:"tokens"`
	Source   string `json:"source"`
	File     string `json:"file"`
}

type Match struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Tokens   int    `json:"tokens"`
	Diff     int    `json:"diff"`
	Exact    bool   `json:"exact"`
}

//go:embed library
var libraryFS embed.FS

var loadPrompts = sync.OnceValue(func() []Prompt {
	raw, err := libraryFS.ReadFile("library/manifest.json")
	if err != nil {
		return nil
	}
	var prompts []Prompt
	if json.Unmarshal(raw, &prompts) != nil {
		return nil
	}
	return prompts
})

func Prompts() []Prompt {
	return slices.Clone(loadPrompts())
}

func PromptText(id string) (Prompt, string, bool) {
	for _, prompt := range loadPrompts() {
		if prompt.ID == id {
			raw, err := libraryFS.ReadFile(path.Join("library", prompt.File))
			return prompt, string(raw), err == nil
		}
	}
	return Prompt{}, "", false
}

func PromptJSON(id string) ([]byte, bool) {
	prompt, text, ok := PromptText(id)
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Identity string `json:"identity"`
		Tokens   int    `json:"tokens"`
		Source   string `json:"source"`
		Text     string `json:"text"`
	}{prompt.ID, prompt.Name, prompt.Identity, prompt.Tokens, prompt.Source, text})
	return raw, err == nil
}

func PrefixOf(overhead int64) int64 {
	if overhead <= NoPrefixMax {
		return 0
	}
	return overhead - PrefixFraming
}

func MatchPrefix(prefixTokens int64) *Match {
	if prefixTokens <= 0 {
		return nil
	}
	var best *Match
	bestDistance := int64(MatchNear + 1)
	for _, prompt := range loadPrompts() {
		diff := prefixTokens - int64(prompt.Tokens)
		distance := max(diff, -diff)
		if distance < bestDistance {
			bestDistance = distance
			best = &Match{
				ID: prompt.ID, Name: prompt.Name, Identity: prompt.Identity,
				Tokens: prompt.Tokens, Diff: int(diff), Exact: distance <= MatchExact,
			}
		}
	}
	return best
}

var (
	dashes  = regexp.MustCompile(`-{2,}`)
	variant = regexp.MustCompile(`^(?:chat)?gpt-?(\p{Nd}+(?:\.\p{Nd}+)?)-?(sol|astra|terra|luna)(-.*)?$`)
	effort  = regexp.MustCompile(`-(none|minimal|low|medium|high|xhigh)$`)
)

func Canon(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	name = name[strings.LastIndexByte(name, '/')+1:]
	name = strings.Map(func(r rune) rune {
		if r == '_' || unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, name)
	name = dashes.ReplaceAllString(name, "-")
	name = variant.ReplaceAllString(name, "gpt-${1}-${2}${3}")
	return effort.ReplaceAllString(name, "")
}

func ExpectedFor(model string) []string {
	name := Canon(model)
	if strings.Contains(name, "codex") {
		return []string{"codex-default-gpt5"}
	}
	is := func(base string) bool {
		return name == base || strings.HasPrefix(name, base+"-")
	}
	for _, base := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.1-sol", "gpt-6.1-terra"} {
		if is(base) {
			return []string{"codex-" + base}
		}
	}
	if name == "gpt-6" || is("gpt-6-astra") {
		return []string{"codex-gpt-6-astra"}
	}
	for _, base := range []string{"gpt-5.2", "gpt-5.1"} {
		if is(base) {
			return []string{"codex-" + base}
		}
	}
	for _, base := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.6", "gpt-6-sol", "gpt-6-luna"} {
		if is(base) {
			return []string{"codex-gpt-5.5"}
		}
	}
	return []string{}
}
