package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/integrity"
)

// BoundaryReview records evidence within a date window, never a training date.
type BoundaryReview struct {
	Method   string `json:"method,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	AnchorID string `json:"anchor_id,omitempty"`
	CheckID  string `json:"check_id,omitempty"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

func ReviewModel(model string) bool {
	switch integrity.CanonModel(model) {
	case "gpt6sol", "gpt6astra", "gpt61sol", "gpt6luna", "gpt61terra", "gpt62astra", "gpt62sol":
		return true
	}
	return false
}

// ReviewResponseError is shared by the scoped search and its item observer.
func ReviewResponseError(m gateway.Metrics, err error) string {
	if err != nil {
		return err.Error()
	}
	if m.Err != "" {
		return m.Err
	}
	if m.StopReason == "length" || m.StopReason == "max_tokens" {
		return "上游输出被长度预算截断，本题未完成，请查看响应记录后补测此项"
	}
	if strings.TrimSpace(m.Answer) == "" {
		return "上游返回的正文答案为空，本题未完成，请查看响应记录后补测此项"
	}
	return ""
}

func CheckCurrentReviewed(ctx context.Context, c gateway.Client, model string, timeout int) (Verdict, error) {
	s := newSearch()
	s.current, s.review = true, true
	return check(ctx, c, model, timeout, s)
}

func explicitUnknown(row Row) bool {
	if row.Kind != kindUnknown || row.Error != "" {
		return false
	}
	text := strings.TrimRight(strings.TrimSpace(row.Answer), "。.")
	if text == "不知道" || text == "不确定" {
		return true
	}
	p, ok := Anchor(row.ID)
	if !ok {
		return false
	}
	for _, ch := range p.Choices {
		if ch.Kind == kindUnknown && text == ch.Letter+" "+ch.Text {
			return true
		}
	}
	return false
}

func setReview(v Verdict, r BoundaryReview) Verdict {
	v.BoundaryReview = &r
	if r.Status == "two_unknown" {
		v.Label = "这两道事件均未确认"
	}
	if r.Status == "unconfirmed" {
		v.Label = "日期边界未确认"
	}
	return v
}

func reviewBoundary(v Verdict, interrupted, allowAsk bool) (Verdict, *Probe) {
	return evaluateBoundary(v, interrupted, allowAsk, "")
}

func evaluateBoundary(v Verdict, interrupted, allowAsk bool, checkID string) (Verdict, *Probe) {
	r := BoundaryReview{Status: "not_needed", Reason: "no_anchor"}
	rows := append([]Row(nil), v.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].HappenedAt == rows[j].HappenedAt {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].HappenedAt < rows[j].HappenedAt
	})
	var anchor *Row
	for i := range rows {
		if rows[i].Kind == kindUnknown && rows[i].ID != checkID {
			anchor = &rows[i]
			break
		}
	}
	if anchor != nil {
		r.Reason = "outside_window"
		for _, window := range [][2]string{{"2026-04-21", "2026-04-30"}, {"2026-05-01", "2026-05-18"}, {"2026-05-19", "2026-06-01"}} {
			if anchor.HappenedAt >= window[0] && anchor.HappenedAt <= window[1] {
				r.From, r.To, r.AnchorID = window[0], window[1], anchor.ID
				r.Status, r.Reason = "unconfirmed", "no_question"
				break
			}
		}
	}
	failure := ""
	for _, row := range rows {
		if row.Kind == kindError || row.Error != "" {
			failure = "main_error"
			if row.ID == checkID {
				failure = "check_error"
			}
			break
		}
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Error, "上游返回的正文答案为空") || strings.HasPrefix(row.Error, "上游输出被长度预算截断") {
			failure = "answer_missing"
		}
	}
	for _, row := range rows {
		if row.Kind == kindInvalid {
			failure = "invalid"
		}
	}
	if v.WebSuspect != "" || v.Status == "invalid" {
		failure = "invalid"
	}
	if interrupted {
		failure = "interrupted"
	}
	if failure != "" {
		r.Status, r.Reason = "unconfirmed", failure
		return setReview(v, r), nil
	}
	if r.AnchorID == "" {
		return setReview(v, r), nil
	}
	if !explicitUnknown(*anchor) {
		r.Reason = "answer_missing"
		return setReview(v, r), nil
	}
	for _, row := range rows {
		if row.Kind == kindKnow && row.HappenedAt >= anchor.HappenedAt {
			r.Reason = "conflict"
			return setReview(v, r), nil
		}
	}
	var candidates []Probe
	seen := map[string]Row{}
	for _, row := range rows {
		seen[row.ID] = row
	}
	for _, p := range PreviousAnchors() {
		if p.ID != anchor.ID && p.HappenedAt != anchor.HappenedAt && p.HappenedAt >= r.From && p.HappenedAt <= r.To {
			candidates = append(candidates, p)
		}
	}
	at, _ := time.Parse("2006-01-02", anchor.HappenedAt)
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := candidates[i].at().Sub(at).Abs(), candidates[j].at().Sub(at).Abs()
		if di != dj {
			return di < dj
		}
		if candidates[i].HappenedAt != candidates[j].HappenedAt {
			return candidates[i].HappenedAt < candidates[j].HappenedAt
		}
		return candidates[i].ID < candidates[j].ID
	})
	for _, p := range candidates {
		if row, ok := seen[p.ID]; ok && explicitUnknown(row) {
			r.CheckID, r.Status, r.Reason = p.ID, "two_unknown", "two_unknown"
			return setReview(v, r), nil
		}
	}
	if allowAsk && v.Total < maxAsks {
		for _, p := range candidates {
			if _, ok := seen[p.ID]; !ok {
				r.CheckID = p.ID
				return setReview(v, r), &p
			}
		}
	}
	for _, p := range candidates {
		if row, ok := seen[p.ID]; ok {
			r.CheckID = p.ID
			switch row.Kind {
			case kindKnow:
				r.Reason = "check_known"
			case kindWrong:
				r.Reason = "check_wrong"
			case kindError:
				r.Reason = "check_error"
			default:
				r.Reason = "answer_missing"
			}
			return setReview(v, r), nil
		}
	}
	if len(candidates) > 0 && v.Total >= maxAsks {
		r.Reason = "limit"
	}
	return setReview(v, r), nil
}

// ReviewAfterRetry only evaluates persisted observations. It sends no requests.
func ReviewAfterRetry(v Verdict, previous *BoundaryReview) Verdict {
	if previous == nil {
		return v
	}
	next, _ := evaluateBoundary(v, false, false, previous.CheckID)
	r := next.BoundaryReview
	if previous.AnchorID != r.AnchorID || previous.From != r.From || previous.To != r.To {
		r = &BoundaryReview{From: previous.From, To: previous.To, AnchorID: previous.AnchorID, CheckID: previous.CheckID, Status: "unconfirmed", Reason: "observations_changed"}
	} else if previous.CheckID != "" && r.CheckID != previous.CheckID {
		r.CheckID = previous.CheckID
		if r.Reason == "two_unknown" || r.Reason == "no_question" || r.Reason == "limit" {
			r.Status, r.Reason = "unconfirmed", "answer_missing"
		}
	}
	return setReview(next, *r)
}

func reviewMissingError(m gateway.Metrics) error {
	if reason := ReviewResponseError(m, nil); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	return nil
}
