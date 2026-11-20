package prefixcheck

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestPromptLibrary(t *testing.T) {
	prompts := Prompts()
	if len(prompts) != 10 {
		t.Fatalf("prompt count = %d; want 10", len(prompts))
	}
	for i, prompt := range prompts {
		t.Run(prompt.ID, func(t *testing.T) {
			metadata, text, ok := PromptText(prompt.ID)
			if !ok || metadata != prompt || text == "" || text != strings.TrimSpace(text) {
				t.Fatal("missing or invalid prompt text or metadata")
			}
			n, err := Count(text)
			if err != nil || n != prompt.Tokens {
				t.Fatalf("prompt count = %d, %v; want %d", n, err, prompt.Tokens)
			}
			for _, prev := range prompts[:i] {
				difference := prompt.Tokens - prev.Tokens
				if prompt.ID == prev.ID || max(difference, -difference) <= 2*MatchNear {
					t.Fatalf("duplicate or overlapping prompts: %q and %q", prompt.ID, prev.ID)
				}
			}
			raw, ok := PromptJSON(prompt.ID)
			var body map[string]json.RawMessage
			if !ok || len(raw) > 64<<10 || json.Unmarshal(raw, &body) != nil || len(body) != 6 {
				t.Fatalf("invalid prompt response, %d bytes", len(raw))
			}
			var displayed string
			if err := json.Unmarshal(body["text"], &displayed); err != nil || displayed != text {
				t.Fatal("response text differs from the embedded prompt")
			}
		})
	}
	if _, ok := PromptJSON("unknown"); ok {
		t.Fatal("unknown prompt returned a response")
	}
}

func TestMatchPrefixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		tokens int64
		id     string
		diff   int
		exact  bool
	}{
		{0, "", 0, false},
		{-5, "", 0, false},
		{7, "sub2api-placeholder", 0, true},
		{5410, "codex-gpt-6.2-sol", 0, true},
		{4378, "codex-gpt-5.5", 2, true},
		{4381, "codex-gpt-5.5", 5, false},
		{4385, "", 0, false},
		{18290, "", 0, false},
		{15, "sub2api-placeholder", 8, false},
		{5413, "codex-gpt-6.2-sol", 3, true},
		{5414, "codex-gpt-6.2-sol", 4, false},
		{5418, "codex-gpt-6.2-sol", 8, false},
		{5419, "", 0, false},
	} {
		got := MatchPrefix(tc.tokens)
		if tc.id == "" {
			if got != nil {
				t.Errorf("prefix %d matched %+v; want no match", tc.tokens, got)
			}
			continue
		}
		if got == nil || got.ID != tc.id || got.Diff != tc.diff || got.Exact != tc.exact {
			t.Errorf("prefix %d matched %+v; want %s diff=%d exact=%t", tc.tokens, got, tc.id, tc.diff, tc.exact)
		}
	}
	if PrefixOf(12) != 0 || PrefixOf(13) != 3 {
		t.Error("no-prefix threshold must be inclusive")
	}
}

func TestCanonAndExpectedFor(t *testing.T) {
	for _, tc := range []struct{ model, canon, id string }{
		{"gpt-6.2-sol", "gpt-6.2-sol", "codex-gpt-6.2-sol"},
		{"openai/GPT-6.2-SOL-high", "gpt-6.2-sol", "codex-gpt-6.2-sol"},
		{"gpt-6.2_sol", "gpt-6.2-sol", "codex-gpt-6.2-sol"},
		{"gpt6.2sol", "gpt-6.2-sol", "codex-gpt-6.2-sol"},
		{"gpt-6.2sol-xhigh", "gpt-6.2-sol", "codex-gpt-6.2-sol"},
		{"gpt-6-sol", "gpt-6-sol", "codex-gpt-5.5"},
		{"gpt-6-luna", "gpt-6-luna", "codex-gpt-5.5"},
		{"gpt-5.6-sol", "gpt-5.6-sol", "codex-gpt-5.5"},
		{"gpt-6", "gpt-6", "codex-gpt-6-astra"},
		{"gpt-6-astra", "gpt-6-astra", "codex-gpt-6-astra"},
		{"gpt-5.3-codex-spark", "gpt-5.3-codex-spark", "codex-default-gpt5"},
		{"codex-auto-review", "codex-auto-review", "codex-default-gpt5"},
		{"claude-opus-5-5", "claude-opus-5-5", ""},
		{"gpt-7-nova", "gpt-7-nova", ""},
		{"chat-latest", "chat-latest", ""},
		{"  openai/ChatGPT6.1___Terra--MINIMAL  ", "gpt-6.1-terra", "codex-gpt-6.1-terra"},
		{"gpt-6.2-astra-preview", "gpt-6.2-astra-preview", "codex-gpt-6.2-astra"},
		{"gpt-6.1-sol", "gpt-6.1-sol", "codex-gpt-6.1-sol"},
		{"gpt-5.2", "gpt-5.2", "codex-gpt-5.2"},
		{"gpt-5.1", "gpt-5.1", "codex-gpt-5.1"},
		{"gpt-5.4-low-low", "gpt-5.4-low", "codex-gpt-5.5"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := Canon(tc.model); got != tc.canon {
				t.Errorf("canonical name = %q; want %q", got, tc.canon)
			}
			want := []string{}
			if tc.id != "" {
				want = []string{tc.id}
			}
			if got := ExpectedFor(tc.model); !slices.Equal(got, want) {
				t.Errorf("expected prompts = %v; want %v", got, want)
			}
		})
	}
}
