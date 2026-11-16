package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/cachecheck"
	"github.com/kiss-kedaya/chanshui/internal/capability"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/inject"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"github.com/kiss-kedaya/chanshui/internal/thinking"
)

type Config struct {
	Deployment Deployment
	Transport  http.RoundTripper
	Notify     func(string, map[string]any)
}
type itemResult struct {
	Recognized   bool               `json:"recognized,omitempty"`
	Answer       string             `json:"answer,omitempty"`
	ThinkingText string             `json:"thinkingText,omitempty"`
	Error        string             `json:"error,omitempty"`
	Judgment     questions.Judgment `json:"judgment"`
	Metrics      *gateway.Metrics   `json:"metrics,omitempty"`
	Evidence     any                `json:"evidence,omitempty"`
	Attempts     int                `json:"attempts"`
	Ms           int64              `json:"ms"`
}

func clean(value any, secrets ...string) any {
	raw, e := json.Marshal(value)
	if e != nil {
		return map[string]any{"error": "结果无法编码，请查看本次检测诊断"}
	}
	var v any
	if e = json.Unmarshal(raw, &v); e != nil {
		return nil
	}
	var visit func(any) any
	visit = func(v any) any {
		switch x := v.(type) {
		case string:
			for _, secret := range secrets {
				if secret != "" {
					x = strings.ReplaceAll(x, secret, "[密钥已隐藏]")
				}
			}
			return x
		case map[string]any:
			for k, v := range x {
				x[k] = visit(v)
			}
		case []any:
			for i, v := range x {
				x[i] = visit(v)
			}
		}
		return v
	}
	return visit(v)
}
func RunOne(ctx context.Context, s *store.Store, o store.Owner, id string, c Credential, cfg Config) error {
	if e := validateCredential(c); e != nil {
		return e
	}
	report, snap, e := s.LoadRun(ctx, o, id)
	if e != nil {
		return e
	}
	if report.BaseURL != "" && (c.BaseURL != report.BaseURL || c.Protocol != report.Protocol) {
		return store.Fail(409, "账号上游地址或协议与报告创建时不一致，未发送检测请求", "context")
	}
	if report.Status != "queued" {
		return store.Fail(409, "本次报告已受理或结束，不会重复调用模型", "run")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(snap.Settings.TotalTimeoutSeconds)*time.Second)
	defer cancel()
	if e = s.BeginRun(ctx, o, id); e != nil {
		return e
	}
	if cfg.Notify != nil {
		cfg.Notify("accepted", map[string]any{"reportId": id})
	}
	t, tr := NewTransport(snap.Settings.Concurrency, snap.Settings.MaxAttempts, cfg.Deployment.AllowPrivateTargets, cfg.Transport)
	defer tr.CloseIdleConnections()
	g := client(c, t, snap.Settings.Retries)
	var currentID atomic.Value
	currentID.Store("")
	t.BeforeAttempt = func(callCtx context.Context) error { return s.RecordAttempt(callCtx, o, id, currentID.Load().(string)) }
	hadError := false
	itemResults := map[string]itemResult{}
	knowledgeDefs := []questions.Generated{}
	fatalStorage := error(nil)
	execute := func(q questions.Generated) (itemResult, error) {
		currentID.Store(q.Definition.ID)
		if e := s.BeginItem(ctx, o, id, q.Definition.ID); e != nil {
			return itemResult{}, e
		}
		before := t.Attempts.Load()
		started := time.Now()
		result, err := runItem(ctx, s, o, id, report.Model, q, g, cfg)
		result.Ms = time.Since(started).Milliseconds()
		result.Attempts = int(t.Attempts.Load() - before)
		if err != nil {
			result.Error = err.Error()
			result.Judgment = questions.Judgment{Status: "unknown", Max: q.Definition.MaxScore, Reason: "本项未完成，请人工查看已保存的错误"}
			hadError = true
		}
		if result.Judgment.Status == "unknown" {
			hadError = true
		}
		result.Metrics = trimMetrics(result.Metrics)
		cleaned := clean(result, c.Key)
		raw, _ := json.Marshal(cleaned)
		if len(raw) > 256<<10 {
			result = itemResult{Error: "本题结果超过256KiB，已停止完整保存；本题未完成，不使用截断内容判分", Judgment: questions.Judgment{Status: "unknown", Max: q.Definition.MaxScore}, Attempts: result.Attempts, Ms: result.Ms}
			if len(raw) > 64<<10 {
				result.Evidence = string(raw[:64<<10])
			} else {
				result.Evidence = string(raw)
			}
			cleaned = clean(result, c.Key)
			hadError = true
		}
		status := "done"
		if result.Error != "" {
			status = "error"
		}
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
		defer saveCancel()
		if e := s.SaveItem(saveCtx, o, id, q.Definition.ID, status, cleaned, result.Attempts); e != nil {
			return result, e
		}
		if cfg.Notify != nil {
			cfg.Notify("item-saved", map[string]any{"reportId": id, "itemId": q.Definition.ID})
		}
		compact := itemResult{Judgment: result.Judgment, Error: result.Error, Recognized: result.Recognized}
		if q.Definition.Kind == "fingerprint_behavior" {
			compact.Answer = result.Answer
		}
		itemResults[q.Definition.ID] = compact
		return result, nil
	}
	for _, q := range snap.Questions {
		if q.Definition.Kind == "knowledge" {
			knowledgeDefs = append(knowledgeDefs, q)
			continue
		}
		if ctx.Err() != nil {
			hadError = true
			break
		}
		_, e := execute(q)
		if e != nil {
			fatalStorage = e
			cancel()
			break
		}

	}
	knowledgeSummary := map[string]any{"status": "not_selected"}
	if fatalStorage == nil && ctx.Err() == nil && len(knowledgeDefs) > 0 {
		knowledgeSummary, e = runKnowledge(ctx, s, o, id, knowledgeDefs, execute)
		if e != nil {
			fatalStorage = e
			cancel()
		}
	}
	summary := map[string]any{"complete": !hadError && ctx.Err() == nil, "attempts": min(t.Attempts.Load(), int64(snap.Settings.MaxAttempts)), "knowledge": knowledgeSummary, "upstreamVersion": snap.UpstreamVersion}
	for key, value := range summarize(snap.Questions, itemResults) {
		summary[key] = value
	}
	addBehaviorSummary(summary, snap.Questions, itemResults)
	if fatalStorage != nil {
		return fmt.Errorf("部分结果保存失败，已停止新模型请求：%w；已保存范围可按报告编号查询", fatalStorage)
	}
	status := "done"
	detail := map[string]any{}
	if ctx.Err() != nil {
		status = "cancelled"
		detail = map[string]any{"message": "检测已停止或达到本次总时长；已发出的请求可能已计费，未自动重试", "step": "run", "operationResult": "unknown"}
	} else if hadError {
		status = "partial"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 11*time.Second)
	defer finishCancel()
	if e = s.Finish(finishCtx, o, id, status, summary, detail); e != nil {
		return fmt.Errorf("结果结束状态保存失败：%w", e)
	}
	if cfg.Notify != nil {
		cfg.Notify("persisted", map[string]any{"reportId": id, "status": status})
	}
	return nil
}
func trimMetrics(m *gateway.Metrics) *gateway.Metrics {
	if m != nil {
		m.Answer = ""
		m.ThinkingText = ""
		m.Signature = ""
		m.SSEFrames = nil
	}
	return m
}
func runItem(ctx context.Context, s *store.Store, o store.Owner, id, model string, q questions.Generated, g gateway.Client, cfg Config) (itemResult, error) {
	out := itemResult{Judgment: questions.Judgment{Status: "unreviewed", Max: q.Definition.MaxScore}}
	def := q.Definition
	timeout := 120
	report, e := s.Report(ctx, o, id)
	if e != nil {
		return out, e
	}
	timeout = report.Settings.TimeoutSeconds
	switch def.Kind {
	case "iq", "vision", "fingerprint_behavior", "knowledge":
		req := gateway.CallRequest{Model: model, Prompt: q.Prompt, MaxTokens: report.Settings.MaxOutputTokens, Stream: true, TimeoutSeconds: timeout, SuppressDefaultEffort: true}
		if def.Kind == "vision" {
			image, e := s.Image(ctx, o, id, def.ID)
			if e != nil {
				return out, e
			}
			req.Prompt = ""
			req.Images = []gateway.Image{{MIME: "image/png", Data: image}}
		}
		if def.Kind == "fingerprint_behavior" {
			req.MaxTokens = 2048
		}
		if def.Kind == "knowledge" {
			req.MaxTokens = 512
		}
		m, e := g.CallMessages(ctx, req)
		out.Metrics = &m
		out.Answer = m.Answer
		out.ThinkingText = m.ThinkingText
		if e != nil {
			return out, e
		}
		if m.Err != "" {
			return out, fmt.Errorf("%s", m.Err)
		}
		if m.StopReason == "length" || m.StopReason == "max_tokens" {
			return out, fmt.Errorf("上游输出被长度预算截断，本题未完成，不使用截断答案判分")
		}
		if strings.TrimSpace(m.Answer) == "" {
			return out, fmt.Errorf("上游没有返回正文答案，请人工检查模型及输出预算")
		}
		if def.Kind == "fingerprint_behavior" {
			out.Judgment = questions.Judgment{Status: "reference", Max: 0}
			if !questions.Compatible(def) {
				out.Judgment.Reason = "提示模板已修改，仅保留行为样本，不作旧参考库比对"
			}
		} else if def.Kind == "knowledge" {
			kind, reason := questions.GradeKnowledge(def, m.Answer)
			out.Judgment = questions.Judgment{Status: "reference", Reason: reason}
			if kind == "invalid" {
				out.Judgment.Status = "unknown"
			}
			out.Evidence = map[string]any{"kind": kind, "reason": reason}
		} else {
			out.Judgment = questions.Grade(q, m.Answer)
			if def.Kind == "vision" {
				out.Recognized = iq.NumericAnswer(m.Answer)
			}
		}
		return out, nil
	case "tools":
		v, e := capability.ToolsWithTemplate(ctx, g, model, timeout, def.Template, questions.Int(def.Parameters, "lhs"), questions.Int(def.Parameters, "rhs"))
		out.Evidence = v
		out.Attempts = v.Attempts
		out.Judgment = boolJudgment(v.Pass, v.Reason)
		return out, e
	case "cache":
		col, e := cachecheck.CollectWithTemplates(ctx, g, model, timeout, &cachecheck.HubTemplates{Heading: questions.Text(def.Parameters, "prefixHeading"), Line: questions.Text(def.Parameters, "prefixLineTemplate"), Turn: questions.Text(def.Parameters, "turnTemplate")})
		out.Evidence = col.Result
		out.Judgment = boolJudgment(col.Result.Pass, col.Result.Reason)
		return out, e
	case "injection":
		v, e := inject.MeasureWithTemplate(ctx, g, model, def.Template, timeout)
		out.Evidence = v
		out.Judgment = questions.Judgment{Status: "unknown", Max: 10, Reason: v.Note}
		if v.Reliable && v.Score != nil {
			out.Judgment.Status = "known"
			out.Judgment.Earned = v.Score
		}
		return out, e
	case "web":
		var v capability.Result
		var e error
		if gateway.IsDeepSeek(model) {
			v, e = capability.DeepSeekSearchWithQuery(ctx, g, timeout, questions.Text(def.Parameters, "deepSeekQuery"))
		} else {
			v, e = capability.WebWithPrompt(ctx, g, model, timeout, questions.Expand(def.Template, map[string]string{"today": time.Now().UTC().Format("2006-01-02")}))
		}
		out.Evidence = v
		out.Judgment = boolJudgment(v.Pass, v.Reason)
		if v.Pass == nil && (v.Status == "unsupported" || v.Protocol == "chat") {
			out.Judgment.Status = "not_applicable"
		}
		return out, e
	case "thinking":
		if !gateway.IsClaude(model) {
			out.Judgment = questions.Judgment{Status: "not_applicable", Reason: "此项仅适用于Claude思维摘要，不发送额外请求"}
			return out, nil
		}
		v, e := thinking.CheckWithPrompt(ctx, thinkAdapter{g: g, timeout: timeout}, model, def.Template)
		out.Evidence = v
		out.Judgment = questions.Judgment{Status: "known", Max: 5, Earned: &v.Score, Reason: v.Reason}
		return out, e
	case "fingerprint_numeric":
		return numeric(ctx, g, model, timeout, q)
	}
	return out, fmt.Errorf("不支持的检测类型%s，未发送请求", def.Kind)
}

type thinkAdapter struct {
	g       gateway.Client
	timeout int
}

func (a thinkAdapter) CallMessages(ctx context.Context, q thinking.CallRequest) (thinking.Metrics, error) {
	req := gateway.CallRequest{Model: q.Model, MaxTokens: q.MaxTokens, Stream: true, TimeoutSeconds: a.timeout, SuppressDefaultEffort: true}
	if len(q.Messages) > 0 {
		req.Prompt = q.Messages[0].Content
	}
	if q.Thinking != nil {
		req.Thinking = &gateway.ThinkingConfig{Type: q.Thinking.Type, Display: q.Thinking.Display}
	}
	m, e := a.g.CallMessages(ctx, req)
	return thinking.Metrics{Status: m.Status, Ms: m.Ms, Err: m.Err, Answer: m.Answer, ThinkingText: m.ThinkingText, Signature: m.Signature, ThinkLen: len(m.ThinkingText), SigLen: len(m.Signature), ThinkingTokens: m.ThinkingTokens}, e
}
func runKnowledge(ctx context.Context, s *store.Store, o store.Owner, id string, defs []questions.Generated, execute func(questions.Generated) (itemResult, error)) (map[string]any, error) {
	today := time.Now().UTC().Format("2006-01-02")
	eligible := []questions.Generated{}
	for _, q := range defs {
		if questions.Text(q.Definition.Parameters, "date") <= today {
			eligible = append(eligible, q)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := questions.Text(eligible[i].Definition.Parameters, "date"), questions.Text(eligible[j].Definition.Parameters, "date")
		return a < b || a == b && eligible[i].Definition.ID < eligible[j].Definition.ID
	})
	seen := map[string]bool{}
	lo, hi := -1, len(eligible)
	observed := []map[string]string{}
	valid := true
	for hi-lo > 1 && len(seen) < 10 && ctx.Err() == nil {
		mid := lo + (hi-lo)/2
		q := eligible[mid]
		result, e := execute(q)
		if e != nil {
			return nil, e
		}
		seen[q.Definition.ID] = true
		if result.Error != "" {
			valid = false
			break
		}
		kind, reason := questions.GradeKnowledge(q.Definition, result.Answer)
		date := questions.Text(q.Definition.Parameters, "date")
		observed = append(observed, map[string]string{"id": q.Definition.ID, "date": date, "kind": kind, "reason": reason})
		if kind == "invalid" {
			valid = false
			break
		}
		if kind == "know" {
			lo = mid
		} else {
			hi = mid
		}
	}
	lower, upper := "", ""
	for _, v := range observed {
		if v["kind"] == "know" && v["date"] > lower {
			lower = v["date"]
		}
	}
	for _, v := range observed {
		if v["kind"] != "know" && v["kind"] != "invalid" {
			if lower != "" && v["date"] <= lower {
				valid = false
			}
			if upper == "" || v["date"] < upper {
				upper = v["date"]
			}
		}
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
	defer cleanupCancel()
	for _, q := range defs {
		if !seen[q.Definition.ID] {
			reason := "本次事件题取题未抽到此项，不作为答错"
			if questions.Text(q.Definition.Parameters, "date") > today {
				reason = "此事件日期晚于今天，本次不提问"
			}
			if e := s.SaveItem(cleanupCtx, o, id, q.Definition.ID, "done", map[string]any{"judgment": map[string]any{"status": "not_asked", "reason": reason}}, 0); e != nil {
				return nil, e
			}
		}
	}
	status := "range"
	if !valid {
		status = "unknown"
	}
	if len(eligible) == 0 {
		status = "not_applicable"
	}
	return map[string]any{"status": status, "lower": lower, "upper": upper, "observations": observed, "note": "本次事件题推测范围，不证明模型训练日期"}, nil
}
