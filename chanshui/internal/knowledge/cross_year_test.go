package knowledge

import (
	"context"
	"encoding/json"
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

func historicalCrossYearBank() []Probe {
	return append(PreviousAnchors(),
		Probe{ID: "fixture_january_news", HappenedAt: "2026-01-30"},
		Probe{ID: "fixture_february_news", HappenedAt: "2026-02-08"},
	)
}

func TestCrossYearSuppressesAnOtherwiseSupportedRangeWhenWebIsSuspected(t *testing.T) {
	previousNow := nowFn
	t.Cleanup(func() { nowFn = previousNow })
	nowFn = func() time.Time { return eventDate("2026-07-20") }
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
			if p.HappenedAt <= "2026-06-12" {
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
	if bounds.low.HappenedAt != "2026-06-12" || bounds.peer.ID == "" {
		t.Fatalf("fixture did not establish a supported candidate before the web check: %+v", bounds)
	}
	if v.WebSuspect == "" || v.Cutoff != "" || v.BoundaryReview.To != "" || v.BoundaryReview.Reason != "invalid" {
		t.Fatalf("web suspicion did not suppress candidate range: %+v", v)
	}
}

func TestCrossYearChecksDifferentTopicAfterLaterCorrectAnswer(t *testing.T) {
	bank := historicalCrossYearBank()
	today := eventDate("2026-08-01")
	row := func(id, kind string) Row {
		for _, p := range bank {
			if p.ID == id {
				return Row{ID: id, HappenedAt: p.HappenedAt, Kind: kind, Answer: "不知道"}
			}
		}
		t.Fatal("missing fixture event", id)
		return Row{}
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
	if !ok || p.ID != "fixture_january_news" {
		t.Fatalf("did not continue checking unsupported answers before the upper bound: %+v", p)
	}

	// On a UTC day before the April question, neither it nor later events are eligible.
	p, ok = nextCrossYear(bank, rows[:2], eventDate("2026-02-01"))
	if !ok || p.ID != "fixture_january_news" {
		t.Fatalf("should ask the only eligible cross-check before future events: %+v", p)
	}
	if p, ok = nextCrossYear(bank, append(rows[:2:2], row("fixture_january_news", kindUnknown)), eventDate("2026-02-01")); ok && p.HappenedAt > "2026-02-01" {
		t.Fatalf("asked a future question: %+v", p)
	}
}

func TestCrossYearVerdictRequiresActualNearbyEvidence(t *testing.T) {
	known := Row{ID: "hungary_2026_vote", HappenedAt: "2026-04-12", Kind: kindKnow}
	support := Row{ID: "wbc_2026_final", HappenedAt: "2026-03-17", Kind: kindKnow}
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
			got := crossYearVerdict(Verdict{Status: "estimated", Rows: []Row{support, known, upper, peer}}, tc.limit)
			if got.BoundaryReview.Reason != tc.want || (got.Cutoff != "") != tc.wantRange {
				t.Fatalf("got %+v with review %+v", got, got.BoundaryReview)
			}
			if tc.wantRange && (got.Status != "estimated" || got.BoundaryReview.From != known.HappenedAt || got.BoundaryReview.To != upper.HappenedAt) {
				t.Fatalf("confirmed range did not use the closest unknown: %+v", got.BoundaryReview)
			}
			if !tc.wantRange && (got.Status != "unknown" || got.BoundaryReview.Status != "unconfirmed" || got.BoundaryReview.To != upper.HappenedAt || !strings.HasPrefix(got.Label, "初步区间 ")) {
				t.Fatalf("unconfirmed report lost the preliminary interval or treated it as an estimate: %+v", got)
			}
		})
	}
	peer := Row{ID: "peer", HappenedAt: "2026-05-16", Kind: kindUnknown, Answer: "不确定"}
	got := crossYearVerdict(Verdict{Rows: []Row{early, support, known, upper, peer}}, false)
	if got.Cutoff != known.HappenedAt || got.BoundaryReview.AnchorID != upper.ID || len(got.Rows) != 5 {
		t.Fatalf("earlier gap hid the later supported estimate: %+v", got)
	}
	interrupted := InterruptCrossYear(got)
	if interrupted.Status != "unknown" || interrupted.Cutoff != "" || interrupted.BoundaryReview.Reason != "interrupted" ||
		interrupted.BoundaryReview.To != "" || len(interrupted.Rows) != 5 {
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

func TestCrossYearRechecksAndCoversMissingMonths(t *testing.T) {
	rows := []Row{
		{ID: "virginia_2025_governor", HappenedAt: "2025-11-04", Kind: kindKnow},
		{ID: "ao_2026_women", HappenedAt: "2026-01-31", Kind: kindKnow},
		{ID: "hungary_2026_vote", HappenedAt: "2026-04-12", Kind: kindUnknown, Answer: "不知道"},
		{ID: "london_2026_marathon", HappenedAt: "2026-04-26", Kind: kindKnow},
		{ID: "eurovision_2026_final", HappenedAt: "2026-05-16", Kind: kindUnknown, Answer: "不知道"},
		{ID: "europa_2026_final", HappenedAt: "2026-05-20", Kind: kindUnknown, Answer: "不知道"},
	}
	bank := historicalCrossYearBank()
	for i, month := range []string{"2026-01", "2026-02", "2026-03"} {
		p, ok := nextCrossYear(bank, rows, eventDate("2026-05-20"))
		if !ok || !strings.HasPrefix(p.HappenedAt, month) || i == 0 && currentEventTopic(p.ID) == "sports" {
			t.Fatalf("expected a January cross-check, then February and March coverage; wanted %s, got %+v", month, p)
		}
		row := Row{ID: p.ID, HappenedAt: p.HappenedAt, Event: p.Event, Kind: kindUnknown, Answer: "不知道"}
		if i == 0 {
			row.Kind, row.Answer = kindKnow, "正确答案"
		}
		rows = append(rows, row)
	}
	peer, ok := nextCrossYear(bank, rows, eventDate("2026-05-20"))
	if !ok || peer.ID != "milano_2026_men_skate" {
		t.Fatalf("expected first nearby upper-bound check, got %+v", peer)
	}
	rows = append(rows, Row{ID: peer.ID, HappenedAt: peer.HappenedAt, Kind: kindUnknown, Answer: "不知道"})
	got := crossYearVerdict(Verdict{Rows: rows}, false)
	if got.Cutoff != "2026-01-31" || got.BoundaryReview.From != "2026-01-31" {
		t.Fatalf("lone April answer shifted the cutoff: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct{ Guessed []Row }
	if err = json.Unmarshal(encoded, &stored); err != nil || len(stored.Guessed) != 1 || stored.Guessed[0].ID != "london_2026_marathon" {
		t.Fatalf("lone April answer missing from saved possible guesses: %+v (%v)", stored.Guessed, err)
	}
}

func TestCrossYearDoesNotUseALaterUnknownAsSupport(t *testing.T) {
	rows := []Row{
		{ID: "london_2026_marathon", HappenedAt: "2026-04-26", Kind: kindKnow},
		{ID: "eurovision_2026_final", HappenedAt: "2026-05-16", Kind: kindUnknown, Answer: "不知道"},
		{ID: "europa_2026_final", HappenedAt: "2026-05-20", Kind: kindUnknown, Answer: "不知道"},
	}
	got := crossYearVerdict(Verdict{Rows: rows}, false)
	if got.Cutoff != "" || got.BoundaryReview.From != "" || dateBounds(rows).low.ID != "" {
		t.Fatalf("published an unsupported lower bound: %+v", got)
	}
}

func TestCrossYearPreviewsAnUnconfirmedRangeWhenTheNextUnknownIsTooFar(t *testing.T) {
	rows := []Row{
		{ID: "poland_2025_runoff", HappenedAt: "2025-06-01", Kind: kindKnow},
		{ID: "usopen_2025_men", HappenedAt: "2025-09-07", Kind: kindUnknown, Answer: "不知道"},
		{ID: "czech_2025_chamber", HappenedAt: "2025-10-04", Kind: kindKnow},
		{ID: "bolivia_2025_runoff", HappenedAt: "2025-10-19", Kind: kindKnow},
		{ID: "virginia_2025_governor", HappenedAt: "2025-11-04", Kind: kindUnknown, Answer: "不知道"},
		{ID: "ao_2026_women", HappenedAt: "2026-01-31", Kind: kindUnknown, Answer: "不知道"},
		{ID: "hungary_2026_vote", HappenedAt: "2026-04-12", Kind: kindUnknown, Answer: "不知道"},
	}
	got := crossYearVerdict(Verdict{Rows: rows}, false)
	if got.Status != "unknown" || got.Cutoff != "" || got.Label != "初步区间 2025-10-19 至 2025-11-04" ||
		got.BoundaryReview.Status != "unconfirmed" || got.BoundaryReview.From != "2025-10-19" || got.BoundaryReview.To != "2025-11-04" {
		t.Fatalf("missing or incorrectly confirmed preliminary range: %+v, %+v", got, got.BoundaryReview)
	}
	stopped := InterruptCrossYear(got)
	if stopped.Label != unconfirmedDate || stopped.BoundaryReview.From != "" || stopped.BoundaryReview.To != "" {
		t.Fatalf("stopped run retained a preliminary range: %+v", stopped)
	}
}

func TestCrossYearSupportedLowerBoundWithoutLaterUnknownReportsMissingAnchor(t *testing.T) {
	rows := []Row{
		{ID: "virginia_2025_governor", HappenedAt: "2025-11-04", Kind: kindUnknown, Answer: "不知道"},
		{ID: "minnesota_2026_pretti", HappenedAt: "2026-01-24", Kind: kindKnow},
		{ID: "afcon_2026_goal", HappenedAt: "2026-01-18", Kind: kindKnow},
	}
	got := crossYearVerdict(Verdict{Rows: rows}, false)
	if got.Cutoff != "" || got.BoundaryReview.Reason != "no_anchor" || len(got.Guessed) != 0 {
		t.Fatalf("supported answers were described as unconfirmed guesses: %+v, %+v", got, got.BoundaryReview)
	}
}

func TestCrossYearMonthDiversityAndRequestLimit(t *testing.T) {
	bank := CurrentAnchors()
	topics := map[string]map[string]bool{}
	for _, p := range bank {
		month := p.HappenedAt[:7]
		if topics[month] == nil {
			topics[month] = map[string]bool{}
		}
		topics[month][currentEventTopic(p.ID)] = true
	}
	for month := 1; month <= 5; month++ {
		key := fmt.Sprintf("2026-%02d", month)
		if len(topics[key]) < 2 {
			t.Errorf("expected at least two topics in %s, got %v", key, topics[key])
		}
	}
	previousNow := nowFn
	t.Cleanup(func() { nowFn = previousNow })
	nowFn = func() time.Time { return eventDate("2026-11-21") }
	bothBanks := append(bank, PreviousAnchors()...)
	seen := map[string]bool{}
	transport := crossYearTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		for _, p := range bothBanks {
			if !strings.Contains(string(body), p.Question) {
				continue
			}
			if seen[p.ID] {
				t.Errorf("repeated event %s", p.ID)
			}
			seen[p.ID] = true
			for _, option := range p.Choices {
				if option.Kind == kindWrong {
					payload := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, fmt.Sprintf(`{"choice":%q}`, option.Letter))
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
				}
			}
		}
		return nil, fmt.Errorf("unexpected event prompt")
	})
	client := gateway.Client{BaseURL: "https://fixture.invalid", Mode: "chat", NoRetry: true, HTTP: &http.Client{Transport: transport}}
	for _, tc := range []struct {
		name string
		run  func(context.Context, gateway.Client, string, int) (Verdict, error)
		want int
	}{{"cross-year", CheckCrossYear, 16}, {"old", CheckCurrent, 10}} {
		t.Run(tc.name, func(t *testing.T) {
			clear(seen)
			got, err := tc.run(t.Context(), client, "fixture", 1)
			if err != nil || len(got.Rows) != tc.want || len(seen) != tc.want {
				t.Fatalf("wanted %d distinct requests, got %d calls and %d rows (%v)", tc.want, len(seen), len(got.Rows), err)
			}
		})
	}
}
