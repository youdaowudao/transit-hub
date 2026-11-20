package prefixcheck

import "testing"

func TestTokenCountOffline(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"sample", "x", 1},
		{"identity", "你的实际模型是什么？不允许联网，马上回答。", 12},
		{"system", "禁止使用任何工具。直接给出结论，不要展开推导过程；回答最后单独一行：最终答案: <短结论>。", 33},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Count(tc.text)
			if err != nil || got != tc.want {
				t.Fatalf("Count = %d, %v; want %d, nil", got, err, tc.want)
			}
		})
	}
	if got, err := Count("<|endoftext|>"); err != nil || got <= 1 {
		t.Fatalf("special marker counted as ordinary text = %d, %v", got, err)
	}
}
