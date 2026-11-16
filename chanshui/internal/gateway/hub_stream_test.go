package gateway

import (
	"fmt"
	"strings"
	"testing"
)

func TestHubStreamKeepsCompleteEvidenceWithoutRetainingFrames(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {\"type\":\"message_start\",\"message\":{\"content\":[],\"usage\":{\"input_tokens\":10}}}\n\n")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"abc\"}}\n\n")
	}
	b.WriteString("data: {\"type\":\"message_stop\"}\n\n")
	var m Metrics
	if e := parseStreamWithOptions(strings.NewReader(b.String()), &m, false); e != nil {
		t.Fatal(e)
	}
	if m.Answer != strings.Repeat("abc", 1000) || !m.StreamComplete || m.SSEFrames != nil || m.InputTokens != 10 {
		t.Fatal("Hub dropped evidence or retained frames")
	}
	var old Metrics
	if e := parseStream(strings.NewReader(b.String()), &old); e != nil || len(old.SSEFrames) == 0 {
		t.Fatal("standalone frame behavior changed")
	}
}
func TestHubCompatibleStreamBuilderPreservesCompleteAnswer(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"abc\"}}]}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	var m Metrics
	if e := parseCompatibleStreamWithOptions(strings.NewReader(b.String()), "chat", &m, true); e != nil || m.Answer != strings.Repeat("abc", 1000) || !m.StreamComplete {
		t.Fatal(e, m.Answer)
	}
}
