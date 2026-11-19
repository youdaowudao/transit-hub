package api

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/history"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"github.com/kiss-kedaya/chanshui/internal/thinking"
)

const (
	DefaultScoringVersion = "2026-10-02-v6-total"
	HubScoringVersion     = "2026-11-19-v7-hub"
	HubCachePendingReason = "缓存尚未确认。可以展开本项查看缺少的回包字段或未完成的轮次，本次报告的总分也暂未确认。"
)

// ProbeRunner exposes the original probes without starting Server's HTTP API,
// worker queue or history cleanup. The caller owns the existing database.
type ProbeRunner struct{ server *Server }

func NewProbeRunner(ctx context.Context, db *sql.DB, autoSync bool) (*ProbeRunner, error) {
	bank, err := fingerprint.LoadBank()
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(ctx)
	s := &Server{ctx: ctx, stop: stop, bank: bank, store: history.RuntimeStore(db)}
	s.loadCachedBank()
	if autoSync {
		s.startBankSync()
	}
	return &ProbeRunner{server: s}, nil
}
func (r *ProbeRunner) Close() { r.server.stop(); r.server.wg.Wait() }
func (r *ProbeRunner) Question(ctx context.Context, g gateway.Client, q Request, item iq.Question, scoringVersion string) (QuestionResult, error) {
	return r.server.askQuestion(ctx, g, q, item, scoringVersion == HubScoringVersion)
}
func (r *ProbeRunner) Numeric(ctx context.Context, g gateway.Client, q Request) (any, error) {
	return r.server.numeric(ctx, g, q)
}
func (r *ProbeRunner) Thinking(ctx context.Context, g gateway.Client, q Request) (any, error) {
	v, e := thinking.Check(ctx, thinkAdapter{g, q.Timeout}, q.Model)
	if e != nil {
		return map[string]string{"error": e.Error(), "reason": "请求失败，不能据此判定思考被清空"}, e
	}
	return v, nil
}
func VisionResult(item iq.Question, v QuestionResult, err error) map[string]any {
	return visionResult(item, v, err)
}
func AssembleVerdict(v Verdict, paper []iq.Question, answers map[string]QuestionResult, model string, selected map[string]bool) Verdict {
	if v.ScoringVersion == "" {
		v.ScoringVersion = DefaultScoringVersion
	}
	var lines []QuestionResult
	v.IQ, lines = aggregateIQ(paper, answers, v.ScoringVersion == HubScoringVersion)
	v.Integrity = buildIntegrity(v, lines, model, selected)
	v.Total = buildTotalFor(v, model, selected)
	return v
}
func aggregateIQ(paper []iq.Question, answers map[string]QuestionResult, countHalf bool) (any, []QuestionResult) {
	items := []iq.QItem{}
	lines := []QuestionResult{}
	complete := true
	answered := 0
	for _, item := range paper {
		if item.ID == "vision" {
			continue
		}
		v := answers[item.ID]
		if v.ID == "" {
			v = QuestionResult{ID: item.ID, Title: item.Title, Max: item.MaxScore, Error: "请求未完成", Tags: []string{}}
		}
		if v.Error != "" && !v.Skipped {
			complete = false
		}
		if v.Error == "" && !v.Skipped {
			answered++
		}
		lines = append(lines, v)
		if v.Skipped {
			continue
		}
		items = append(items, iq.QItem{ID: v.ID, Title: v.Title, Max: v.Max, Earned: v.Earned, Half: v.Half, Tags: v.Tags})
	}
	if len(lines) > 0 {
		var score iq.IQResult
		var note string
		if countHalf {
			score = iq.ScoreEarned(items)
			note = "百分制按各题实际得到的分数计算。逐题可以查看得分、半分和保留的模型回答。"
		} else {
			score = iq.Score(items)
			note = fmt.Sprintf("当前卷面 %d；半分仅在卷面展示，不进入百分制通过分。失败请求标记未完成，但已答题目照常计分，不伪装为模型答错。", score.Max)
		}
		var validScore any
		if answered > 0 {
			validScore = score.Score
		}
		return map[string]any{"score": validScore, "max": 100, "paper_max": score.Max, "complete": complete, "per_question": lines, "note": note}, lines
	}
	return nil, lines
}

func (r *ProbeRunner) BankStats() map[string]any { return r.server.bankStats() }
