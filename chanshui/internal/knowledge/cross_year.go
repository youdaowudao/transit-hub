package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
)

const (
	CrossYearMethod  = "cross_year"
	unconfirmedDate  = "知识截止日期未确认"
	confirmationSpan = 30 * 24 * time.Hour
	dateLayout       = "2006-01-02"
)

var openingEvents = [...]string{"virginia_2025_governor", "ao_2026_women", "hungary_2026_vote"}

func CheckCrossYear(ctx context.Context, c gateway.Client, model string, timeout int) (Verdict, error) {
	return checkCrossYear(ctx, c, model, timeout, false)
}

// CheckPreviousCrossYear runs the v5 rules for reports with the frozen v5 bank.
func CheckPreviousCrossYear(ctx context.Context, c gateway.Client, model string, timeout int) (Verdict, error) {
	return checkCrossYear(ctx, c, model, timeout, true)
}

func checkCrossYear(ctx context.Context, c gateway.Client, model string, timeout int, previous bool) (Verdict, error) {
	s := newSearch()
	s.crossYear, s.previousCrossYear = true, previous
	return check(ctx, c, model, timeout, s)
}

func InitialCrossYear() Verdict {
	return Verdict{
		Status:         "unknown",
		Label:          unconfirmedDate,
		Reason:         "知识结论尚未发布，可查看检测状态和已保存回答。",
		BoundaryReview: &BoundaryReview{Method: CrossYearMethod, Status: "not_needed", Reason: "no_anchor"},
	}
}

// CrossYearFromRows re-evaluates saved observations after an explicit retry.
func CrossYearFromRows(rows []Row, protocol string, attempts int) Verdict {
	return crossYearVerdict(FromRows(rows, protocol, attempts), false)
}

// PreviousCrossYearFromRows keeps the v5 conclusions when its saved observations change.
func PreviousCrossYearFromRows(rows []Row, protocol string, attempts int) Verdict {
	return crossYearResult(FromRows(rows, protocol, attempts), false, true)
}

func InterruptCrossYear(v Verdict) Verdict {
	if len(v.Rows) == 0 {
		v.Reason = ""
	}
	return unconfirmCrossYear(v, "interrupted")
}

type eventBounds struct {
	low, upper, peer Row
	conflict         bool

	// The current search uses these to revisit unsupported answers and cover skipped months.
	latest  Row
	guessed []Row
}

func eventDate(date string) time.Time {
	at, _ := time.Parse(dateLayout, date)
	return at
}

func dateBounds(rows []Row) eventBounds {
	return crossYearBounds(rows, false)
}

func crossYearBounds(rows []Row, previous bool) eventBounds {
	var bounds eventBounds
	ordered := append([]Row(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].HappenedAt != ordered[j].HappenedAt {
			return ordered[i].HappenedAt < ordered[j].HappenedAt
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, row := range ordered {
		if previous {
			if row.Kind == kindKnow && row.Error == "" && row.HappenedAt > bounds.low.HappenedAt {
				bounds.low = row
			}
			continue
		}
		if !correctEvent(row) {
			continue
		}
		if row.HappenedAt > bounds.latest.HappenedAt {
			bounds.latest = row
		}
		if is2026Event(row.HappenedAt) {
			if supportedEvent(row, ordered) {
				if row.HappenedAt > bounds.low.HappenedAt {
					bounds.low = row
				}
			} else {
				bounds.guessed = append(bounds.guessed, row)
			}
		}
	}
	if !previous && !is2026Event(bounds.latest.HappenedAt) {
		bounds.low = bounds.latest
	}
	for _, row := range ordered {
		if !explicitUnknown(row) {
			continue
		}
		if row.HappenedAt <= bounds.low.HappenedAt {
			bounds.conflict = true
			continue
		}
		if bounds.upper.ID == "" {
			bounds.upper = row
			continue
		}
		if row.HappenedAt > bounds.upper.HappenedAt && eventDate(row.HappenedAt).Sub(eventDate(bounds.upper.HappenedAt)) <= confirmationSpan {
			bounds.peer = row
			break
		}
	}
	return bounds
}

func is2026Event(date string) bool { return strings.HasPrefix(date, "2026-") }

func correctEvent(row Row) bool {
	return row.Kind == kindKnow && row.Error == "" && !eventDate(row.HappenedAt).IsZero()
}

func differentNearbyEvent(at time.Time, topic string, other time.Time, otherID string) bool {
	return !other.IsZero() && !at.Equal(other) && at.Sub(other).Abs() <= confirmationSpan && currentEventTopic(otherID) != topic
}

func supportedEvent(row Row, rows []Row) bool {
	at, topic := eventDate(row.HappenedAt), currentEventTopic(row.ID)
	for _, other := range rows {
		if correctEvent(other) && differentNearbyEvent(at, topic, eventDate(other.HappenedAt), other.ID) {
			return true
		}
	}
	return false
}

func currentEventTopic(id string) string {
	switch id {
	case "ao_2025_zverev", "icc_2025_india", "open_2025_golf", "usopen_2025_men", "ao_2026_women",
		"milano_2026_men_skate", "milano_2026_men_hockey", "london_2026_marathon", "europa_2026_final",
		"ucl_2026_final", "worldcup_2026_france_paraguay", "worldcup_2026_boot", "wbc_2026_final", "worldcup_2026_final_goal",
		"afcon_2026_goal", "superbowl_2026_score", "milano_2026_gold_count", "kentucky_2026_derby", "pga_2026_winner":
		return "sports"
	case "merz_2025_ballot", "poland_2025_runoff", "czech_2025_chamber", "bolivia_2025_runoff",
		"virginia_2025_governor", "hungary_2026_vote", "canada_2025_election", "japan_2025_diet", "hungary_2026_final_seats":
		return "election"
	case "oscars_2026_picture", "eurovision_2026_final", "oscars_2026_cinematography":
		return "arts"
	case "artemis_2026_crater", "hondius_2026":
		return "science"
	case "us_2025_tariff_pause", "nobel_2025_peace", "minnesota_2026_pretti", "wbd_2026_bid", "iea_2026_reserves", "spacex_2026_first_trade":
		// Public affairs, the default topic for unlisted events as well.
	}
	return "public_affairs"
}

func nextCrossYear(bank []Probe, rows []Row, today time.Time) (Probe, bool) {
	return nextCrossYearQuestion(bank, rows, today, false)
}

func nextPreviousCrossYear(bank []Probe, rows []Row, today time.Time) (Probe, bool) {
	return nextCrossYearQuestion(bank, rows, today, true)
}

func nextCrossYearQuestion(bank []Probe, rows []Row, today time.Time, previous bool) (Probe, bool) {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		seen[row.ID] = true
	}
	var eligible, available []Probe
	for _, p := range bank {
		if at := p.at(); at.IsZero() || at.After(today) {
			continue
		}
		eligible = append(eligible, p)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].HappenedAt != eligible[j].HappenedAt {
			return eligible[i].HappenedAt < eligible[j].HappenedAt
		}
		return eligible[i].ID < eligible[j].ID
	})
	for _, p := range eligible {
		if !seen[p.ID] {
			available = append(available, p)
		}
	}
	if len(available) == 0 {
		return Probe{}, false
	}
	for _, id := range openingEvents {
		var replacement string
		if !previous {
			replacement = eventReplacements[id].ID
		}
		for _, p := range available {
			if p.ID == id || (!previous && p.ID == replacement) {
				return p, true
			}
		}
	}

	bounds := crossYearBounds(rows, previous)
	low, upper := eventDate(bounds.low.HappenedAt), eventDate(bounds.upper.HappenedAt)
	if previous && bounds.conflict && !hasPreviousNearbyEvidence(rows, bounds.low) {
		topic := currentEventTopic(bounds.low.ID)
		if p, ok := closestEvent(available, low, func(p Probe, at time.Time) bool {
			return differentNearbyEvent(low, topic, at, p.ID)
		}); ok {
			return p, true
		}
	}
	for i := len(bounds.guessed) - 1; i >= 0; i-- {
		row := bounds.guessed[i]
		date, topic := eventDate(row.HappenedAt), currentEventTopic(row.ID)
		if p, ok := closestEvent(available, date, func(p Probe, at time.Time) bool {
			return differentNearbyEvent(date, topic, at, p.ID)
		}); ok {
			return p, true
		}
	}
	if is2026Event(bounds.latest.HappenedAt) {
		seenMonths := make(map[string]bool)
		for _, row := range rows {
			if !eventDate(row.HappenedAt).IsZero() {
				seenMonths[row.HappenedAt[:7]] = true
			}
		}
		for _, p := range available {
			month := p.HappenedAt[:7]
			if is2026Event(p.HappenedAt) && month <= bounds.latest.HappenedAt[:7] && !seenMonths[month] {
				return p, true
			}
		}
	}
	if !low.IsZero() && !upper.IsZero() && bounds.peer.ID == "" {
		for _, p := range available {
			if at := p.at(); at.After(upper) && at.Sub(upper) <= confirmationSpan {
				return p, true
			}
		}
	}

	left, right := low, upper
	if left.IsZero() {
		left = eligible[0].at()
	}
	if right.IsZero() {
		right = eligible[len(eligible)-1].at()
	}
	return closestEvent(available, left.Add(right.Sub(left)/2), func(_ Probe, at time.Time) bool {
		return (low.IsZero() || at.After(low)) && (upper.IsZero() || at.Before(upper))
	})
}

// The v5 conflict check also counted a later explicit unknown as nearby evidence.
func hasPreviousNearbyEvidence(rows []Row, low Row) bool {
	at, topic := eventDate(low.HappenedAt), currentEventTopic(low.ID)
	for _, row := range rows {
		date := eventDate(row.HappenedAt)
		if differentNearbyEvent(at, topic, date, row.ID) &&
			((row.Kind == kindKnow && row.Error == "") || (date.After(at) && explicitUnknown(row))) {
			return true
		}
	}
	return false
}

func closestEvent(available []Probe, target time.Time, eligible func(Probe, time.Time) bool) (Probe, bool) {
	var closest Probe
	var distance time.Duration
	for _, p := range available {
		at := p.at()
		if !eligible(p, at) {
			continue
		}
		d := at.Sub(target).Abs()
		if closest.ID == "" || d < distance {
			closest, distance = p, d
		}
	}
	return closest, closest.ID != ""
}

func crossYearVerdict(v Verdict, limit bool) Verdict {
	return crossYearResult(v, limit, false)
}

func crossYearResult(v Verdict, limit, previous bool) Verdict {
	bounds := crossYearBounds(v.Rows, previous)
	v.Guessed = bounds.guessed
	var asked, known, unknown int
	dates := make(map[string]bool)
	for _, row := range v.Rows {
		if !is2026Event(row.HappenedAt) {
			continue
		}
		asked++
		if row.Error != "" {
			continue
		}
		switch row.Kind {
		case kindKnow, kindUnknown, kindWrong:
			dates[row.HappenedAt] = true
		}
		if row.Kind == kindKnow {
			known++
		}
		if explicitUnknown(row) {
			unknown++
		}
	}
	v.Reason = InitialCrossYear().Reason
	if len(v.Rows) > 0 {
		latest := ""
		if bounds.low.ID != "" {
			latest = "最近答对的事件发生在 " + bounds.low.HappenedAt + "。"
		} else if bounds.latest.ID == "" {
			latest = "本轮没有题目答对。"
		}
		v.Reason = fmt.Sprintf("本轮记录了 %d 道事件题结果，其中 2026 年 %d 道，%d 道答对，%d 道明确回答不知道。%s本项不计分。", len(v.Rows), asked, known, unknown, latest)
	}
	reason := "no_question"
	switch {
	case v.Status == "invalid" || v.WebSuspect != "":
		return unconfirmCrossYear(v, "invalid")
	case v.Status == "partial" || v.Status == "error":
		return unconfirmCrossYear(v, "main_error")
	case len(dates) < 2:
		return unconfirmCrossYear(v, "coverage_missing")
	case bounds.low.ID != "" && bounds.upper.ID != "" && bounds.peer.ID != "":
		v.Status = "estimated"
		v.Cutoff = bounds.low.HappenedAt
		v.Label = fmt.Sprintf("本轮事件题估计范围 %s 至 %s", bounds.low.HappenedAt, bounds.upper.HappenedAt)
		v.BoundaryReview = &BoundaryReview{
			Method: CrossYearMethod, Status: "two_unknown", Reason: "two_unknown",
			From: bounds.low.HappenedAt, To: bounds.upper.HappenedAt, AnchorID: bounds.upper.ID, CheckID: bounds.peer.ID,
		}
		return v
	case previous && bounds.conflict:
		reason = "conflict"
	case bounds.low.ID == "" || bounds.upper.ID == "":
		reason = "no_anchor"
	case bounds.conflict:
		reason = "conflict"
	case limit:
		reason = "limit"
	}
	v = unconfirmCrossYear(v, reason)
	if !previous && bounds.low.ID != "" && bounds.upper.ID != "" {
		v.BoundaryReview.From, v.BoundaryReview.To = bounds.low.HappenedAt, bounds.upper.HappenedAt
		v.Label = fmt.Sprintf("初步区间 %s 至 %s", v.BoundaryReview.From, v.BoundaryReview.To)
	}
	return v
}

func unconfirmCrossYear(v Verdict, reason string) Verdict {
	if v.Status == "estimated" || v.Status == "" {
		v.Status = "unknown"
	}
	v.Cutoff = ""
	v.Label = unconfirmedDate
	v.BoundaryReview = &BoundaryReview{Method: CrossYearMethod, Status: "unconfirmed", Reason: reason}
	return v
}
