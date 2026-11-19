package knowledge

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
)

type crossYearTransport func(*http.Request) (*http.Response, error)

func (f crossYearTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCrossYearSuppressesAnOtherwiseSupportedRangeWhenWebIsSuspected(t *testing.T) {
	previousNow := nowFn
	t.Cleanup(func() { nowFn = previousNow })
	nowFn = func() time.Time { return eventDate("2026-06-25") }
	bank := CurrentAnchors()
	transport := crossYearTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		for _, p := range bank {
			if !strings.Contains(string(body), p.Question) {
				continue
			}
			answer := "D"
			if p.HappenedAt <= "2026-05-18" {
				for _, choice := range p.Choices {
					if choice.Kind == kindKnow {
						answer = choice.Letter
						break
					}
				}
			}
			content := fmt.Sprintf(`{"choice":%q}`, answer)
			payload := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
		}
		return nil, fmt.Errorf("unexpected event prompt")
	})
	v, err := CheckCrossYear(t.Context(), gateway.Client{
		BaseURL: "https://fixture.invalid", Mode: "chat", NoRetry: true, HTTP: &http.Client{Transport: transport},
	}, "fixture", 1)
	if err != nil {
		t.Fatal(err)
	}
	bounds := dateBounds(v.Rows)
	if bounds.low.HappenedAt != "2026-05-16" || bounds.peer.ID == "" {
		t.Fatalf("fixture did not establish a supported candidate before the web check: %+v", bounds)
	}
	if v.WebSuspect == "" || v.Cutoff != "" || v.BoundaryReview.To != "" || v.BoundaryReview.Reason != "invalid" {
		t.Fatalf("web suspicion did not suppress candidate range: %+v", v)
	}
}

func TestCrossYearChecksDifferentTopicAfterLaterCorrectAnswer(t *testing.T) {
	bank := CurrentAnchors()
	today := eventDate("2026-08-01")
	row := func(id, kind string) Row {
		p, ok := Anchor(id)
		if !ok {
			t.Fatal("missing default event", id)
		}
		return Row{ID: id, HappenedAt: p.HappenedAt, Kind: kind, Answer: "不知道"}
	}
	rows := []Row{
		row("virginia_2025_governor", kindUnknown),
		row("ao_2026_women", kindKnow),
		row("hungary_2026_vote", kindUnknown),
		row("london_2026_marathon", kindKnow),
	}
	p, ok := nextCrossYear(bank, rows, today)
	if !ok || p.ID != "eurovision_2026_final" {
		t.Fatalf("old unknown excused a separate check around April 26: %+v", p)
	}
	rows = append(rows, row("eurovision_2026_final", kindUnknown))
	p, ok = nextCrossYear(bank, rows, today)
	if !ok || p.ID != "europa_2026_final" {
		t.Fatalf("did not reuse the new response before checking the upper bound: %+v", p)
	}

	// On a UTC day before the April question, neither it nor later events are eligible.
	p, ok = nextCrossYear(bank, rows[:2], eventDate("2026-02-01"))
	if ok {
		t.Fatalf("asked a future question: %+v", p)
	}
}

func TestCrossYearVerdictRequiresActualNearbyEvidence(t *testing.T) {
	known := Row{ID: "event_known", HappenedAt: "2026-04-12", Kind: kindKnow}
	early := Row{ID: "early_unknown", HappenedAt: "2025-11-04", Kind: kindUnknown, Answer: "不知道"}
	upper := Row{ID: "upper_unknown", HappenedAt: "2026-04-26", Kind: kindUnknown, Answer: "不知道"}
	for _, tc := range []struct {
		name, date, response, want string
		wantRange, limit           bool
	}{
		{"30 days including the last question", "2026-05-26", "不知道", "two_unknown", true, true},
		{"31 days is too far", "2026-05-27", "不知道", "no_question", false, false},
		{"same date is not an independent boundary", "2026-04-26", "不知道", "no_question", false, false},
		{"indirect reply is not explicit", "2026-05-16", "我没有可靠的信息支持任何选项", "no_question", false, false},
		{"still unsupported at the limit", "2026-05-27", "不知道", "limit", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := Row{ID: "peer", HappenedAt: tc.date, Kind: kindUnknown, Answer: tc.response}
			got := crossYearVerdict(Verdict{Status: "estimated", Rows: []Row{known, upper, peer}}, tc.limit)
			if got.BoundaryReview.Reason != tc.want || (got.Cutoff != "") != tc.wantRange {
				t.Fatalf("got %+v with review %+v", got, got.BoundaryReview)
			}
			if tc.wantRange && (got.Status != "estimated" || got.BoundaryReview.From != known.HappenedAt || got.BoundaryReview.To != upper.HappenedAt) {
				t.Fatalf("confirmed range did not use the closest unknown: %+v", got.BoundaryReview)
			}
			if !tc.wantRange && (got.Status != "unknown" || got.BoundaryReview.To != "") {
				t.Fatalf("unconfirmed report exposed an estimate: %+v", got)
			}
		})
	}
	peer := Row{ID: "peer", HappenedAt: "2026-05-16", Kind: kindUnknown, Answer: "不确定"}
	got := crossYearVerdict(Verdict{Rows: []Row{early, known, upper, peer}}, false)
	if got.Cutoff != known.HappenedAt || got.BoundaryReview.AnchorID != upper.ID || len(got.Rows) != 4 {
		t.Fatalf("earlier gap hid the later supported estimate: %+v", got)
	}
	interrupted := InterruptCrossYear(got)
	if interrupted.Status != "unknown" || interrupted.Cutoff != "" || interrupted.BoundaryReview.Reason != "interrupted" ||
		interrupted.BoundaryReview.To != "" || len(interrupted.Rows) != 4 {
		t.Fatalf("interrupted report retained an estimate or lost saved observations: %+v", interrupted)
	}
	older := []Row{
		{ID: "old_known", HappenedAt: "2025-09-07", Kind: kindKnow},
		{ID: "old_unknown", HappenedAt: "2025-10-04", Kind: kindUnknown, Answer: "不知道"},
		{ID: "old_peer", HappenedAt: "2025-10-19", Kind: kindUnknown, Answer: "不知道"},
		upper,
	}
	got = crossYearVerdict(Verdict{Rows: older}, false)
	if got.BoundaryReview.Reason != "coverage_missing" || got.Cutoff != "" {
		t.Fatalf("first partial retry published a range with only one 2026 result: %+v", got)
	}
}
