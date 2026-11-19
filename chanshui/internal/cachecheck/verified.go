package cachecheck

import (
	"context"
	"fmt"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
)

// MeasureVerified uses the same requests as Measure with the usage evidence
// required by the scoring rule for new Hub reports.
func MeasureVerified(ctx context.Context, c gateway.Client, model string, timeout int, templates *HubTemplates) (Result, error) {
	collection, err := collect(ctx, c, model, timeout, templates, true)
	return collection.Result, err
}

// ReportEvidence omits the cumulative read count when it cannot be saved
// exactly. The per-turn evidence remains available.
func (r Result) ReportEvidence() any {
	if !r.omitTotalRead {
		return r
	}
	return struct {
		Result
		TotalRead *int `json:"total_read,omitempty"`
	}{Result: r}
}

func fromVerifiedMetrics(metrics []gateway.Metrics, prefixHash, protocol string, retriesObserved bool) Result {
	r := Result{Status: "unknown", Protocol: protocol, PrefixHash: prefixHash, RetriesObserved: retriesObserved,
		Samples: make([]Sample, len(metrics)), Transitions: []Transition{}}
	complete := len(metrics) == cacheTurns
	for i, m := range metrics {
		s := verifiedSample(m, i+1)
		r.Samples[i] = s
		complete = complete && s.Error == ""
		if s.ReadReported && !r.omitTotalRead {
			var ok bool
			r.TotalRead, ok = gateway.AddCacheCounts(r.TotalRead, s.Read)
			r.omitTotalRead = !ok
		}
		r.TotalOutput += s.Output
	}
	readOnly := complete && prepareVerifiedReadOnly(metrics, r.Samples)
	for i := range r.Samples {
		s := &r.Samples[i]
		if s.ReadReported && s.InputReported {
			s.HitRate = chainRate(s.Read, s.Total)
		}
		if i > 0 {
			r.Transitions = append(r.Transitions, verifiedTransition(r.Samples[i-1], *s, readOnly))
		}
	}
	if !complete {
		r.Reason = "尚未完成六轮缓存请求。请查看本项的已完成轮次和错误提示，核对后补测。"
		return r
	}
	last := r.Samples[cacheTurns-1]
	link := r.Transitions[cacheTurns-2]
	r.Expected, r.Actual, r.Delta = link.Expected, link.Actual, link.Delta
	r.ThirdReadBaseline = link.Expected
	if readOnly {
		r.ThirdReadRatio = last.HitRate
		if last.Read > 0 {
			r.setVerifiedOutcome(true, "上游没有回传缓存写入。后续轮次有明确的缓存读取，末轮命中率按本轮读取除以本轮总输入计算。")
		} else {
			r.setVerifiedOutcome(false, "上游没有回传缓存写入，末轮明确回传缓存读取为 0。本次缓存未通过。")
		}
		return r
	}
	if link.Pass != nil {
		r.ThirdReadRatio = chainRate(link.Actual, link.Expected)
	}
	r.judgeVerifiedChain(last, link)
	return r
}

func verifiedSample(m gateway.Metrics, index int) Sample {
	s := Sample{Index: index, Output: m.OutputTokens, OutputReported: m.OutputReported, Attempts: m.Attempts, Error: m.Err}
	if m.Err != "" || m.CacheUsage == nil {
		return s
	}
	u := *m.CacheUsage
	s.ReadReported = u.Read.State == gateway.CacheCountValid
	s.WriteReported = u.Write.State == gateway.CacheCountValid
	if m.Protocol == "messages" {
		s.Input = u.Input.Value
		s.Total, s.InputReported = messagesTotal(u, false)
	} else {
		s.Total, s.InputReported = u.Input.Value, u.Input.State == gateway.CacheCountValid
		if m.InputTokens <= gateway.CacheCountLimit {
			s.Input = m.InputTokens
		}
		if s.InputReported {
			if s.ReadReported && u.Read.Value > s.Total {
				s.ReadReported = false
			}
			available := s.Total
			if s.ReadReported {
				available -= u.Read.Value
			}
			if s.WriteReported && u.Write.Value > available {
				s.WriteReported = false
			}
		}
	}
	if s.ReadReported {
		s.Read = u.Read.Value
	}
	if s.WriteReported {
		s.Write = u.Write.Value
	}
	return s
}

func messagesTotal(u gateway.CacheUsage, readOnly bool) (int, bool) {
	if u.Input.State != gateway.CacheCountValid || u.Read.State != gateway.CacheCountValid ||
		(u.Write.State != gateway.CacheCountValid && !(readOnly && u.Write.State == gateway.CacheCountMissing)) {
		return 0, false
	}
	total, ok := gateway.AddCacheCounts(u.Input.Value, u.Read.Value)
	if !ok {
		return 0, false
	}
	return gateway.AddCacheCounts(total, u.Write.Value)
}

// prepareVerifiedReadOnly checks whether a missing write count is expected
// for this six-turn result. Only this branch may exclude it from a Messages total.
func prepareVerifiedReadOnly(metrics []gateway.Metrics, samples []Sample) bool {
	reads := 0
	for i, m := range metrics {
		u := m.CacheUsage
		if u == nil || u.Write.State != gateway.CacheCountMissing {
			return false
		}
		if samples[i].ReadReported {
			reads++
		} else if u.Read.State != gateway.CacheCountMissing || reads > 0 {
			return false
		}
	}
	if reads < 2 {
		return false
	}
	last := len(samples) - 1
	total, ok := samples[last].Total, samples[last].InputReported
	if metrics[last].Protocol == "messages" {
		total, ok = messagesTotal(*metrics[last].CacheUsage, true)
	}
	if !ok || total <= 0 {
		return false
	}
	for i, m := range metrics {
		if m.Protocol == "messages" {
			samples[i].Total, samples[i].InputReported = messagesTotal(*m.CacheUsage, true)
		}
	}
	return true
}

func verifiedBaseline(s Sample) (int, bool) {
	if !s.ReadReported || !s.WriteReported {
		return 0, false
	}
	return gateway.AddCacheCounts(s.Read, s.Write)
}

func verifiedTransition(from, to Sample, readOnly bool) Transition {
	link := Transition{From: from.Index, To: to.Index, Actual: to.Read}
	baseline, ok := verifiedBaseline(from)
	if readOnly {
		baseline, ok = from.Read, from.ReadReported
	}
	link.Expected = baseline
	if !ok || !to.ReadReported {
		return link
	}
	link.Delta = to.Read - baseline
	var pass bool
	if readOnly {
		pass = to.Read > 0 && (baseline == 0 || absInt(link.Delta) <= cacheSlack)
	} else if baseline > 0 {
		pass = absInt(link.Delta) <= cacheSlack
	} else {
		return link
	}
	link.Pass = &pass
	return link
}

func (r *Result) judgeVerifiedChain(last Sample, link Transition) {
	allZero := true
	for _, s := range r.Samples {
		allZero = allZero && s.ReadReported && s.WriteReported && s.Read == 0 && s.Write == 0 && s.InputReported && s.Total > 0
	}
	if allZero {
		r.setVerifiedOutcome(false, "六轮请求均已完成，接口明确回传的缓存读取和写入都为 0。本次缓存未通过。")
		return
	}
	middle := r.Samples[1 : cacheTurns-1]
	for _, s := range middle {
		if s.ReadReported && s.WriteReported && s.Read == 0 && s.Write > cacheSlack {
			r.setVerifiedOutcome(false, fmt.Sprintf("第%d轮缓存读取为 0，接口重新写入了缓存前缀。本次缓存未通过。", s.Index))
			return
		}
	}
	if link.Pass != nil && !*link.Pass {
		r.setVerifiedOutcome(false, "上一轮读加写与末轮读相差超过 64 token。本次缓存未通过。")
		return
	}
	first := r.Samples[0]
	for _, s := range []Sample{first, last} {
		if !s.ReadReported {
			r.Reason = fmt.Sprintf("缓存尚未确认：第%d轮缺少有效的缓存读取。请核对上游回包后补测。", s.Index)
			return
		}
	}
	grew := false
	for _, s := range middle {
		baseline, ok := verifiedBaseline(s)
		if !ok {
			r.Reason = fmt.Sprintf("缓存尚未确认：第%d轮没有可用于衔接的读写计数。请核对上游回包后补测。", s.Index)
			return
		}
		grew = grew || (baseline > first.Read && baseline-first.Read > cacheSlack)
	}
	if link.Pass == nil {
		r.Reason = "缓存尚未确认：第5轮读写合计为 0，无法核对末轮衔接。请核对上游回包后补测。"
		return
	}
	if !grew {
		r.setVerifiedOutcome(false, "缓存链没有建立：后续轮次没有比首轮多出一段可复用缓存。本次缓存未通过。")
		return
	}
	r.setVerifiedOutcome(true, "上一轮读加写与末轮读相差不超过 64 token。本次缓存通过。")
}

func (r *Result) setVerifiedOutcome(pass bool, reason string) {
	r.Pass = &pass
	r.Reason = reason
	r.Status = "mismatch"
	if pass {
		r.Status = "pass"
	}
	if r.RetriesObserved {
		r.Reason += " 检测发生过重试，首轮成功回包可能已经命中缓存。"
	}
}
