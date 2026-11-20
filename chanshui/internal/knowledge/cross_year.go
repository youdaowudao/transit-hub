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
	s := newSearch()
	s.crossYear = true
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

func InterruptCrossYear(v Verdict) Verdict {
	if len(v.Rows) == 0 {
		v.Reason = ""
	}
	return unconfirmCrossYear(v, "interrupted")
}

type eventBounds struct {
	low, upper, peer Row
	conflict         bool
}

func eventDate(date string) time.Time {
	at, _ := time.Parse(dateLayout, date)
	return at
}

func dateBounds(rows []Row) eventBounds {
	var bounds eventBounds
	ordered := append([]Row(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].HappenedAt != ordered[j].HappenedAt {
			return ordered[i].HappenedAt < ordered[j].HappenedAt
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, row := range ordered {
		if row.Kind == kindKnow && row.Error == "" && row.HappenedAt > bounds.low.HappenedAt {
			bounds.low = row
		}
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

func currentEventTopic(id string) string {
	switch id {
	case "ao_2025_zverev", "icc_2025_india", "open_2025_golf", "usopen_2025_men", "ao_2026_women",
		"milano_2026_men_skate", "milano_2026_men_hockey", "london_2026_marathon", "europa_2026_final",
		"ucl_2026_final", "worldcup_2026_france_paraguay", "worldcup_2026_boot":
		return "sports"
	case "merz_2025_ballot", "poland_2025_runoff", "czech_2025_chamber", "bolivia_2025_runoff",
		"virginia_2025_governor", "hungary_2026_vote":
		return "election"
	case "oscars_2026_picture", "eurovision_2026_final":
		return "arts"
	default:
		return "public_affairs"
	}
}

func nextCrossYear(bank []Probe, rows []Row, today time.Time) (Probe, bool) {
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
		for _, p := range available {
			if p.ID == id {
				return p, true
			}
		}
	}

	bounds := dateBounds(rows)
	low, upper := eventDate(bounds.low.HappenedAt), eventDate(bounds.upper.HappenedAt)
	if bounds.conflict && !hasNearbyEvidence(rows, bounds.low) {
		topic := currentEventTopic(bounds.low.ID)
		if p, ok := closestEvent(available, low, func(p Probe, at time.Time) bool {
			return !at.Equal(low) && at.Sub(low).Abs() <= confirmationSpan && currentEventTopic(p.ID) != topic
		}); ok {
			return p, true
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

func hasNearbyEvidence(rows []Row, low Row) bool {
	at := eventDate(low.HappenedAt)
	topic := currentEventTopic(low.ID)
	for _, row := range rows {
		date := eventDate(row.HappenedAt)
		if date.Equal(at) || date.Sub(at).Abs() > confirmationSpan || currentEventTopic(row.ID) == topic {
			continue
		}
		if (row.Kind == kindKnow && row.Error == "") || (date.After(at) && explicitUnknown(row)) {
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
	bounds := dateBounds(v.Rows)
	var asked, known, unknown int
	dates := make(map[string]bool)
	for _, row := range v.Rows {
		if !strings.HasPrefix(row.HappenedAt, "2026-") {
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
		latest := "本轮没有题目答对。"
		if bounds.low.ID != "" {
			latest = "最近答对的事件发生在 " + bounds.low.HappenedAt + "。"
		}
		v.Reason = fmt.Sprintf("本轮记录了 %d 道事件题结果，其中 2026 年 %d 道，%d 道答对，%d 道明确回答不知道。%s本项不计分。", len(v.Rows), asked, known, unknown, latest)
	}
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
	case bounds.conflict:
		return unconfirmCrossYear(v, "conflict")
	case bounds.low.ID == "" || bounds.upper.ID == "":
		return unconfirmCrossYear(v, "no_anchor")
	case limit:
		return unconfirmCrossYear(v, "limit")
	default:
		return unconfirmCrossYear(v, "no_question")
	}
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
