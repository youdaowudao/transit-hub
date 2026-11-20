package gateway

import (
	"fmt"
	"strings"
	"testing"
)

func TestVerifiedCompatibleCacheUsage(t *testing.T) {
	valid := func(n int) CacheCount { return CacheCount{Value: n, State: CacheCountValid} }
	missing, invalid := CacheCount{}, CacheCount{State: CacheCountInvalid}
	for _, tc := range []struct {
		name, usage string
		want        CacheUsage
	}{
		{"nested zero wins", `{"prompt_tokens":200,"input_tokens":400,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"prompt_tokens_details":{"cached_tokens":80},"cache_write_tokens":30}`, CacheUsage{valid(0), valid(0), valid(200)}},
		{"invalid nested fields block fallbacks", `{"input_tokens":200,"input_tokens_details":{"cached_tokens":null,"cache_write_tokens":"0"},"cached_tokens":80,"cache_write_tokens":30}`, CacheUsage{invalid, invalid, valid(200)}},
		{"nested write alias ordering", `{"input_tokens_details":{"cache_creation_tokens":50},"prompt_tokens_details":{"cache_write_tokens":40},"cache_creation_input_tokens":60}`, CacheUsage{missing, valid(40), missing}},
		{"first present top-level read wins", `{"cache_read_input_tokens":null,"cache_read_tokens":30,"cached_tokens":60}`, CacheUsage{invalid, missing, missing}},
		{"first positive aggregate wins", `{"cache_write_tokens":0,"cache_creation_input_tokens":50,"cache_write_input_tokens":80,"cache_creation":{"ephemeral_5m_input_tokens":10}}`, CacheUsage{missing, valid(50), missing}},
		{"top-level zero is reported", `{"cache_write_tokens":0,"cache_creation_input_tokens":false,"prompt_cache_miss_tokens":30}`, CacheUsage{missing, valid(0), missing}},
		{"first top-level invalid blocks fallback", `{"cache_write_tokens":null,"cache_creation_input_tokens":0,"prompt_cache_miss_tokens":30}`, CacheUsage{missing, invalid, missing}},
		{"split sum wins", `{"cache_write_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":15}}`, CacheUsage{missing, valid(20), missing}},
		{"malformed split blocks aggregate", `{"cache_write_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":15}}`, CacheUsage{missing, invalid, missing}},
		{"null split blocks fallback", `{"cache_creation":null,"prompt_cache_miss_tokens":25}`, CacheUsage{missing, invalid, missing}},
		{"empty split allows fallback", `{"cache_creation":{},"input_tokens_details":false,"prompt_cache_hit_tokens":20,"prompt_cache_miss_tokens":25}`, CacheUsage{valid(20), valid(25), missing}},
		{"fractional and negative counts are invalid", `{"prompt_tokens":20,"cache_read_tokens":0.5,"cache_write_tokens":-2}`, CacheUsage{invalid, invalid, valid(20)}},
		{"explicit null total is invalid", `{"prompt_tokens":null,"input_tokens":30}`, CacheUsage{missing, missing, invalid}},
		{"safe integer ceiling", fmt.Sprintf(`{"cached_tokens":%d,"prompt_tokens":%d}`, CacheCountLimit, CacheCountLimit), CacheUsage{valid(CacheCountLimit), missing, valid(CacheCountLimit)}},
		{"unsafe counts", `{"cached_tokens":9007199254740992,"prompt_tokens":9007199254740993}`, CacheUsage{invalid, missing, invalid}},
		{"split sum overflow", fmt.Sprintf(`{"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":1}}`, CacheCountLimit), CacheUsage{missing, invalid, missing}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Metrics{CacheUsage: &CacheUsage{}}
			raw := []byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":` + tc.usage + `}`)
			if err := parseCompatibleJSON(raw, "chat", &m); err != nil {
				t.Fatal(err)
			}
			if *m.CacheUsage != tc.want {
				t.Fatalf("cache evidence = %+v, want %+v", *m.CacheUsage, tc.want)
			}
		})
	}
}

func TestVerifiedCompatibleStreamCacheUsage(t *testing.T) {
	chat := `data: {"usage":{"prompt_tokens":400,"cached_tokens":298,"cache_write_tokens":100}}` + "\n\n" +
		`data: {"usage":{"cached_tokens":0,"cache_write_tokens":null}}` + "\n\n" +
		`data: {"usage":{"completion_tokens":5}}` + "\n\n" + "data: [DONE]\n\n"
	responses := `data: {"type":"response.output_text.delta","delta":"ok","usage":{"input_tokens":900,"cached_tokens":800,"cache_write_tokens":99}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":450,"cached_tokens":0},"output":[]}}` + "\n\n"
	for _, tc := range []struct {
		protocol, body string
		want           CacheUsage
	}{
		{"chat", chat, CacheUsage{CacheCount{0, CacheCountValid}, CacheCount{State: CacheCountInvalid}, CacheCount{400, CacheCountValid}}},
		{"responses", responses, CacheUsage{CacheCount{0, CacheCountValid}, CacheCount{}, CacheCount{450, CacheCountValid}}},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			m := Metrics{CacheUsage: &CacheUsage{}}
			if err := parseCompatibleStream(strings.NewReader(tc.body), tc.protocol, &m); err != nil {
				t.Fatal(err)
			}
			if *m.CacheUsage != tc.want {
				t.Fatalf("cache evidence = %+v, want %+v", *m.CacheUsage, tc.want)
			}
		})
	}
}

func TestVerifiedMessagesCacheUsageAndPlaceholder(t *testing.T) {
	start := `data: {"type":"message_start","message":{"content":[],"usage":{"input_tokens":37,"cache_read_input_tokens":900,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":12,"ephemeral_1h_input_tokens":15}}}}` + "\n\n"
	middle := `data: {"type":"message_delta","usage":{"cache_creation_input_tokens":51,"cache_creation":{"ephemeral_5m_input_tokens":32,"ephemeral_1h_input_tokens":44}}}` + "\n\n"
	valid := func(n int) CacheCount { return CacheCount{Value: n, State: CacheCountValid} }
	invalid := CacheCount{State: CacheCountInvalid}
	for _, tc := range []struct {
		name, delta string
		want        CacheUsage
	}{
		{"valid placeholder keeps read and write", `{"cache_read_input_tokens":0,"cache_creation_input_tokens":3,"output_tokens":6}`, CacheUsage{valid(900), valid(76), valid(37)}},
		{"invalid write survives placeholder guard", `{"cache_read_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":null}}`, CacheUsage{valid(900), invalid, valid(37)}},
		{"invalid read replaces previous positive", `{"cache_read_input_tokens":null,"input_tokens":41}`, CacheUsage{invalid, valid(76), valid(41)}},
		{"write merges within the event only", `{"cache_creation_input_tokens":40}`, CacheUsage{valid(900), valid(40), valid(37)}},
		{"aggregate and split from the same event", `{"cache_creation_input_tokens":53,"cache_creation":{"ephemeral_5m_input_tokens":20}}`, CacheUsage{valid(900), valid(53), valid(37)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := start + middle + `data: {"type":"message_delta","usage":` + tc.delta + "}\n\ndata: {\"type\":\"message_stop\"}\n\n"
			m := Metrics{CacheUsage: &CacheUsage{}}
			if err := parseStream(strings.NewReader(body), &m); err != nil {
				t.Fatal(err)
			}
			if *m.CacheUsage != tc.want {
				t.Fatalf("cache evidence = %+v, want %+v", *m.CacheUsage, tc.want)
			}
		})
	}
}
