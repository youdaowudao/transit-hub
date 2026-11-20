package api

import "testing"

func TestIQSystemPromptPreservesOriginalText(t *testing.T) {
	const original = "禁止使用任何工具。直接给出结论，不要展开推导过程；回答最后单独一行：最终答案: <短结论>。"
	if IQSystemPrompt != original {
		t.Errorf("system prompt = %q; want %q", IQSystemPrompt, original)
	}
}
