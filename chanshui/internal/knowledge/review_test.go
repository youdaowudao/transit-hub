package knowledge

import (
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"testing"
)

func TestBoundaryReviewNeedsIndependentExplicitAnswers(t *testing.T) {
	var probes []Probe
	for _, p := range CurrentAnchors() {
		if p.HappenedAt >= "2026-05-19" && p.HappenedAt <= "2026-06-01" {
			probes = append(probes, p)
		}
	}
	if len(probes) != 2 {
		t.Fatal("expected two frozen events")
	}
	for _, tc := range []struct{ answer, status, reason string }{
		{"不知道", "two_unknown", "two_unknown"},
		{"", "unconfirmed", "answer_missing"},
		{"我没有可靠信息", "unconfirmed", "answer_missing"},
	} {
		rows := []Row{Observe(probes[0], "不知道", ""), Observe(probes[1], tc.answer, "")}
		v := FromRows(rows, "chat", 2)
		v, _ = reviewBoundary(v, false, false)
		if v.BoundaryReview == nil || v.BoundaryReview.Status != tc.status || v.BoundaryReview.Reason != tc.reason {
			t.Fatalf("answer=%q: %+v", tc.answer, v.BoundaryReview)
		}
	}
}

func TestBoundaryReviewMissingWindowAndNoExtraRetryRequests(t *testing.T) {
	for _, p := range CurrentAnchors() {
		if p.HappenedAt != "2026-04-26" {
			continue
		}
		v := FromRows([]Row{Observe(p, "不知道", "")}, "chat", 1)
		v, next := reviewBoundary(v, false, true)
		if next != nil || v.BoundaryReview.Reason != "no_question" {
			t.Fatalf("%+v, %v", v, next)
		}
		old := v.BoundaryReview
		v = FromRows([]Row{Observe(p, "不知道", "")}, "chat", 2)
		v = ReviewAfterRetry(v, old)
		if v.BoundaryReview.Reason != "no_question" {
			t.Fatal(v.BoundaryReview)
		}
	}
}

func TestBoundaryBudgetConflictAndRetryAnchor(t *testing.T) {
	var first, second Probe
	for _, p := range CurrentAnchors() {
		if p.HappenedAt == "2026-05-20" {
			first = p
		}
		if p.HappenedAt == "2026-05-30" {
			second = p
		}
	}
	anchor := Observe(second, "不知道", "")
	initial := FromRows([]Row{anchor}, "chat", 1)
	v, next := reviewBoundary(initial, false, true)
	if next == nil || next.ID != first.ID {
		t.Fatal("missing nearest independent question", v)
	}
	rows := []Row{anchor, Observe(first, "不知道", "")}
	done, _ := evaluateBoundary(FromRows(rows, "chat", 2), false, false, first.ID)
	if done.BoundaryReview.AnchorID != second.ID || done.BoundaryReview.CheckID != first.ID || done.BoundaryReview.Status != "two_unknown" {
		t.Fatal("second question changed the main anchor", done)
	}
	retried := ReviewAfterRetry(FromRows(rows, "chat", 3), done.BoundaryReview)
	if retried.BoundaryReview.Status != "two_unknown" {
		t.Fatal("unchanged retry invalidated the anchor", retried)
	}
	changed := ReviewAfterRetry(FromRows([]Row{Observe(first, "不知道", "")}, "chat", 3), done.BoundaryReview)
	if changed.BoundaryReview.Reason != "observations_changed" {
		t.Fatal(changed)
	}
	initial.Total = 10
	limited, next := reviewBoundary(initial, false, true)
	if next != nil || limited.BoundaryReview.Reason != "limit" {
		t.Fatal("budget exceeded", limited)
	}
	stopped, next := reviewBoundary(initial, true, true)
	if next != nil || stopped.BoundaryReview.Reason != "interrupted" {
		t.Fatal("asked after cancellation", stopped)
	}
	initial.WebSuspect = "原近期事件原因"
	suspect, next := reviewBoundary(initial, false, true)
	if next != nil || suspect.BoundaryReview.Reason != "invalid" || suspect.WebSuspect != initial.WebSuspect {
		t.Fatal(suspect)
	}
}

func TestReviewRejectsMissingBodyAndTruncation(t *testing.T) {
	for _, m := range []gateway.Metrics{{Answer: ""}, {Answer: "不知道", StopReason: "max_tokens"}, {Answer: "不知道", StopReason: "length"}} {
		if ReviewResponseError(m, nil) == "" {
			t.Fatal("invalid response accepted", m)
		}
	}
	if ReviewResponseError(gateway.Metrics{Answer: "不知道"}, nil) != "" {
		t.Fatal("explicit answer rejected")
	}
	for _, name := range []string{"gpt-6-astra", "openai/gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.1-terra"} {
		if !ReviewModel(name) {
			t.Fatal(name)
		}
	}
	if ReviewModel("claude-opus-5") || ReviewModel("gpt-6.2-other") {
		t.Fatal("expanded scope")
	}
}

func TestRetryKeepsDirectFailureReason(t *testing.T) {
	var a, b Probe
	for _, p := range CurrentAnchors() {
		if p.HappenedAt == "2026-05-20" {
			a = p
		}
		if p.HappenedAt == "2026-05-30" {
			b = p
		}
	}
	previous := &BoundaryReview{AnchorID: a.ID, CheckID: b.ID, From: "2026-05-19", To: "2026-06-01", Status: "two_unknown", Reason: "two_unknown"}
	for _, tc := range []struct{ failure, want string }{
		{"上游连接中断", "check_error"}, {"上游返回的正文答案为空，本题未完成", "answer_missing"},
	} {
		v := FromRows([]Row{Observe(a, "不知道", ""), Observe(b, "", tc.failure)}, "chat", 2)
		got := ReviewAfterRetry(v, previous)
		if got.BoundaryReview.Reason != tc.want || got.BoundaryReview.CheckID != b.ID {
			t.Fatal(got)
		}
	}
	v := FromRows([]Row{Observe(a, "不知道", ""), Observe(b, "", "上游连接中断")}, "chat", 2)
	v.WebSuspect = "原实际事件与时间"
	if got := ReviewAfterRetry(v, previous); got.BoundaryReview.Reason != "invalid" {
		t.Fatal(got)
	}
}
