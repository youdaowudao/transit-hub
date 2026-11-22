package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/cachecheck"
	"github.com/kiss-kedaya/chanshui/internal/capability"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/inject"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
	"github.com/kiss-kedaya/chanshui/internal/thinking"
)

type Config struct {
	DisableBankSync bool
	runner          *api.ProbeRunner
	singleAsk       *api.SingleAskInput
	Deployment      Deployment
	Transport       http.RoundTripper
	Notify          func(string, map[string]any)
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

func cleanRunItem(result itemResult, key, scoringVersion string) any {
	cleaned := clean(result, key)
	if scoringVersion == api.HubScoringVersion {
		switch result.Judgment.Status {
		case "known", "unknown", "not_applicable", "reference", "unreviewed":
			root, _ := cleaned.(map[string]any)
			if judgment, ok := root["judgment"].(map[string]any); ok {
				judgment["status"] = result.Judgment.Status
			}
		}
	}
	return cleaned
}

type itemAttemptContextKey struct{}
type itemAttempts struct {
	ID    string
	Count atomic.Int64
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
	cfg.singleAsk = snap.SingleAsk
	if cfg.singleAsk != nil && (len(snap.Questions) != 1 || snap.Questions[0].Definition.ID != "custom:"+id) {
		return fmt.Errorf("单问单答快照不完整，未发送请求，请核查此记录")
	}
	if err := snap.Settings.Validate(); err != nil {
		return fmt.Errorf("本次报告的运行设置无效，未发送检测请求：%w", err)
	}
	runStarted := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(snap.Settings.TotalTimeoutSeconds)*time.Second)
	defer cancel()
	if e = s.BeginRun(ctx, o, id); e != nil {
		return e
	}
	var publishProgress func() error
	var progressFailure func(error)
	var notifyMu sync.Mutex
	notify := func(event string, data map[string]any) {
		if event == "item-saved" && publishProgress != nil {
			if err := publishProgress(); err != nil {
				progressFailure(err)
				return
			}
		}
		if cfg.Notify != nil {
			notifyMu.Lock()
			defer notifyMu.Unlock()
			cfg.Notify(event, data)
		}
	}
	notify("accepted", map[string]any{"reportId": id})
	t, tr := NewTransport(snap.Settings.Concurrency, snap.Settings.MaxAttempts, cfg.Deployment.AllowPrivateTargets, cfg.Transport)
	defer tr.CloseIdleConnections()
	g := client(c, t, snap.Settings.Retries)
	if snap.Settings.Protocol != "" {
		g.Mode = snap.Settings.Protocol
	}
	cfg.runner, e = api.NewProbeRunner(ctx, s.RuntimeDB(), !cfg.DisableBankSync && !cfg.Deployment.DisableBankSync)
	if e != nil {
		return e
	}
	defer cfg.runner.Close()
	var resultMu sync.Mutex
	var saveMu sync.Mutex
	itemResults := map[string]itemResult{}
	allowed := map[string]bool{}
	previousSummary := map[string]any{}
	_ = json.Unmarshal(report.Summary.Summary, &previousSummary)
	previousVerdict := asMap(previousSummary["verdict"])
	scoringVersion, _ := previousVerdict["scoring_version"].(string)
	if scoringVersion == "" {
		scoringVersion = api.DefaultScoringVersion
	}
	cleanSummary := func(summary map[string]any) any {
		cleaned := clean(summary, c.Key)
		if cfg.singleAsk == nil && (scoringVersion == api.HubScoringVersion || scoringVersion == api.DefaultScoringVersion) {
			root, _ := cleaned.(map[string]any)
			if verdict, ok := root["verdict"].(map[string]any); ok {
				// Public rule identifiers are generated by this service, and must
				// survive even if a short credential happens to match part of one.
				verdict["scoring_version"] = scoringVersion
			}
			if scoringVersion == api.HubScoringVersion {
				restoreScorePartKeys(summary, root)
			}
		}
		return cleaned
	}
	if _, ok := previousSummary["knowledge"].(map[string]any); !ok {
		previousSummary["knowledge"] = previousVerdict["knowledge"]
	}
	previousKnowledge := asMap(previousSummary["knowledge"])
	crossYear := hasCrossYearMethod(previousKnowledge)
	previousCrossYear := false
	if crossYear && !questions.MatchesCurrentKnowledge(snap.Questions) {
		previousCrossYear = questions.MatchesPreviousKnowledge(snap.Questions)
		crossYear = previousCrossYear
	}
	crossYearFirst := crossYear && snap.Retry == nil
	previousTiming := asMap(previousVerdict["timing"])
	number := func(v any) int64 {
		if f, ok := v.(float64); ok {
			return int64(f)
		}
		return 0
	}
	timing := func(done bool) *api.Timing {
		v := &api.Timing{StartedAt: runStarted.UTC().Format(time.RFC3339Nano), TotalMs: number(previousTiming["total_ms"]), Runs: int(number(previousTiming["runs"])) + 1}
		if done {
			v.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			v.LastRunMs = time.Since(runStarted).Milliseconds()
			v.TotalMs += v.LastRunMs
		}
		return v
	}
	if snap.Retry != nil {
		for _, itemID := range snap.Retry.ItemIDs {
			allowed[itemID] = true
		}
		cursor := ""
		for {
			page, err := s.ListItems(ctx, o, id, store.Filters{Cursor: cursor, Limit: 5})
			if err != nil {
				return err
			}
			for _, item := range page.Items {
				result, err := savedResult(item.Result)
				if err != nil {
					return fmt.Errorf("原报告题目%s的结果无法读取，未开始补测：%w", item.ItemID, err)
				}
				if item.Status != "done" {
					result.Judgment.Status = "unknown"
				}
				if crossYear && item.Definition.Kind == "knowledge" && (item.Status == "queued" || item.Status == "running") {
					result.Answer, result.Error, result.Evidence = "", "", nil
				}
				result.Attempts = item.Attempts
				itemResults[item.ItemID] = compactResult(item.Definition.Kind, result)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	knowledgeSummary := map[string]any{"status": "not_selected"}
	if crossYearFirst || (snap.Retry != nil && previousKnowledge != nil) {
		knowledgeSummary = previousKnowledge
	}
	pendingKnowledge := knowledgeSummary
	pendingBoundary := asMap(pendingKnowledge["boundary_review"])["reason"] == "retry_pending"
	fatalStorage := error(nil)
	failStorage := func(err error) {
		resultMu.Lock()
		if fatalStorage == nil {
			fatalStorage = err
		}
		resultMu.Unlock()
		cancel()
	}
	progressFailure = failStorage
	var progressMu sync.Mutex
	publishProgress = func() error {
		progressMu.Lock()
		defer progressMu.Unlock()
		resultMu.Lock()
		summary := summarizeForVersion(snap.Questions, itemResults, report.Model, scoringVersion)
		if cfg.singleAsk != nil {
			summary["singleAsk"] = true
			summary["verdict"] = map[string]any{}
			summary["totalScore"] = nil
			summary["iqScore"] = nil
			summary["total"] = nil
		}
		summary["upstreamVersion"] = snap.UpstreamVersion
		summary["attempts"] = t.Attempts.Load()
		if snap.Retry != nil {
			summary["attempts"] = t.Attempts.Load() + int64(snap.Retry.AttemptsBefore)
		}
		publishedKnowledge := knowledgeSummary
		if pendingBoundary || crossYearFirst {
			publishedKnowledge = pendingKnowledge
		}
		if cfg.singleAsk == nil && publishedKnowledge["boundary_review"] != nil {
			summary["knowledge"] = publishedKnowledge
		}
		if v, ok := summary["verdict"].(map[string]any); ok {
			v["protocol"] = g.Mode
			v["timing"] = timing(false)
			if publishedKnowledge["status"] != "not_selected" {
				v["knowledge"] = publishedKnowledge
			}
		}
		resultMu.Unlock()
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
		defer saveCancel()
		return s.SaveProgress(saveCtx, o, id, cleanSummary(summary))
	}
	t.BeforeAttempt = func(callCtx context.Context) error {
		item, ok := callCtx.Value(itemAttemptContextKey{}).(*itemAttempts)
		if !ok {
			return fmt.Errorf("检测请求没有对应题目，未发送上游请求")
		}
		if err := s.RecordAttempt(callCtx, o, id, item.ID); err != nil {
			if ctx.Err() == nil {
				failStorage(err)
			}
			return err
		}
		item.Count.Add(1)
		return nil
	}
	execute := func(q questions.Generated) (itemResult, error) {
		if err := ctx.Err(); err != nil {
			return itemResult{}, err
		}
		item := &itemAttempts{ID: q.Definition.ID}
		itemCtx := context.WithValue(ctx, itemAttemptContextKey{}, item)
		if err := s.BeginItem(itemCtx, o, id, q.Definition.ID); err != nil {
			if ctx.Err() == nil {
				failStorage(err)
			}
			return itemResult{}, err
		}
		started := time.Now()
		result, err := runItem(itemCtx, s, o, id, report.Model, q, g, cfg, scoringVersion)
		saveMu.Lock()
		defer saveMu.Unlock()
		result.Ms = time.Since(started).Milliseconds()
		result.Attempts = int(item.Count.Load())
		resultMu.Lock()
		result.Attempts += itemResults[q.Definition.ID].Attempts
		resultMu.Unlock()
		if err != nil {
			result.Error = err.Error()
			ev := asMap(result.Evidence)
			if ev == nil {
				ev = map[string]any{}
			}
			ev["error"] = result.Error
			result.Evidence = ev
			result.Judgment = questions.Judgment{Status: "unknown", Max: q.Definition.MaxScore, Reason: "本项未完成，请人工查看已保存的错误"}
		}
		if questions.IsOriginalQuestion(q.Definition) {
			// The complete original QuestionResult already owns these bodies.
			// Do not duplicate multi-megabyte answers in the Hub projection.
			result.Answer = ""
			result.ThinkingText = ""
			result.Metrics = nil
		}
		result.Metrics = trimMetrics(result.Metrics)
		cleaned := cleanRunItem(result, c.Key, scoringVersion)
		raw, _ := json.Marshal(cleaned)
		if len(raw) > store.MaxItemBytes {
			result = itemResult{Error: "本题完整结果超过12MiB保存上限，本题未完成；已保存的其他回答保留，请降低输出预算后补测此项", Judgment: questions.Judgment{Status: "unknown", Max: q.Definition.MaxScore}, Attempts: result.Attempts, Ms: result.Ms}
			cleaned = cleanRunItem(result, c.Key, scoringVersion)
		}
		status := "done"
		if result.Error != "" {
			status = "error"
		}
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(itemCtx), 11*time.Second)
		defer saveCancel()
		if err := s.SaveItem(saveCtx, o, id, q.Definition.ID, status, cleaned, result.Attempts); err != nil {
			failStorage(err)
			return result, err
		}
		compact := compactResult(q.Definition.Kind, result)
		resultMu.Lock()
		itemResults[q.Definition.ID] = compact
		resultMu.Unlock()
		notify("item-saved", map[string]any{"reportId": id, "itemId": q.Definition.ID})
		return result, nil
	}
	if snap.Retry == nil {
		initial := map[string]any{"verdict": api.Verdict{Timing: timing(false), Protocol: g.Mode, ScoringVersion: scoringVersion}, "upstreamVersion": snap.UpstreamVersion}
		if crossYearFirst {
			v := initial["verdict"].(api.Verdict)
			v.Knowledge = previousKnowledge
			initial["verdict"], initial["knowledge"] = v, previousKnowledge
		}
		if cfg.singleAsk != nil {
			initial["singleAsk"] = true
			initial["verdict"] = map[string]any{"timing": timing(false), "protocol": g.Mode}
		}
		if err := s.SaveProgress(ctx, o, id, cleanSummary(initial)); err != nil {
			return err
		}
	}
	knowledgeDefs := []questions.Generated{}
	work := []func() error{}
	for _, q := range snap.Questions {
		if snap.Retry != nil && !allowed[q.Definition.ID] {
			continue
		}
		if q.Definition.Kind == "knowledge" {
			knowledgeDefs = append(knowledgeDefs, q)
			continue
		}
		if snap.Retry == nil && q.Definition.Kind == "fingerprint_behavior" && questions.Compatible(q.Definition) {
			hasNumeric := false
			for _, other := range snap.Questions {
				if other.Definition.Kind == "fingerprint_numeric" && questions.Compatible(other.Definition) {
					hasNumeric = true
					break
				}
			}
			if hasNumeric {
				if e = s.SaveItem(ctx, o, id, q.Definition.ID, "done", map[string]any{"judgment": map[string]any{"status": "not_asked", "reason": "原版行为探针由模型指纹按需要调用，不额外重复提问"}}, 0); e != nil {
					return e
				}
				continue
			}
		}
		work = append(work, func() error { _, err := execute(q); return err })
	}

	if len(knowledgeDefs) > 0 {
		work = append(work, func() error {
			value, err := originalKnowledge(ctx, s, o, id, knowledgeDefs, snap.Questions, previousKnowledge, crossYear, previousCrossYear, g, snap.Settings.TimeoutSeconds, report.Model, snap.Retry, execute, itemResults, &resultMu, failStorage, notify)
			if value != nil {
				resultMu.Lock()
				knowledgeSummary = value
				resultMu.Unlock()
				if progressErr := publishProgress(); progressErr != nil {
					return progressErr
				}
			}
			return err
		})
	}
	jobs := make(chan func() error)
	var workers sync.WaitGroup
	for i := 0; i < min(len(work), snap.Settings.Concurrency); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := job(); err != nil {
					if ctx.Err() == nil {
						failStorage(err)
					}
					return
				}
			}
		}()
	}
dispatch:
	for _, job := range work {
		select {
		case jobs <- job:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	var pa prefixcheck.Analysis
	if cfg.singleAsk == nil {
		pa = prefixSummary(ctx, s, o, id)
	}
	attempts := t.Attempts.Load()
	if snap.Retry != nil {
		attempts += int64(snap.Retry.AttemptsBefore)
	}
	if pendingBoundary && !crossYear && ctx.Err() != nil {
		knowledgeSummary = pendingKnowledge
	}
	summary := finishedScores(snap.Questions, itemResults, report.Model, scoringVersion)
	summary["attempts"], summary["knowledge"], summary["upstreamVersion"] = attempts, knowledgeSummary, snap.UpstreamVersion
	if ctx.Err() != nil {
		summary["complete"] = false
	}
	if cfg.singleAsk != nil {
		summary["singleAsk"] = true
		summary["verdict"] = map[string]any{}
		delete(summary, "knowledge")
		delete(summary, "parts")
		delete(summary, "iqMax")
		summary["totalScore"] = nil
		summary["iqScore"] = nil
		summary["total"] = nil
	}
	if v, ok := summary["verdict"].(map[string]any); ok {
		v["protocol"] = g.Mode
		v["timing"] = timing(true)
		if cfg.singleAsk == nil {
			v["knowledge"] = knowledgeSummary
		}
	}
	if snap.Retry != nil && cfg.singleAsk == nil {
		addBehaviorSummary(summary, snap.Questions, itemResults)
	}
	if fatalStorage != nil {
		return fmt.Errorf("部分结果保存失败，已停止新模型请求：%w；已保存范围可按报告编号查询", fatalStorage)
	}
	status := "done"
	detail := map[string]any{}
	if ctx.Err() != nil {
		status = "cancelled"
		detail = map[string]any{"message": "检测已停止或达到本次总时长；已发出的请求可能已计费，未自动重试", "step": "run", "operationResult": "unknown"}
		if crossYear {
			summary["complete"] = false
			if pendingBoundary {
				knowledgeSummary = pendingKnowledge
			} else if crossYearFirst {
				var v knowledge.Verdict
				raw, _ := json.Marshal(knowledgeSummary)
				_ = json.Unmarshal(raw, &v)
				knowledgeSummary = asMap(knowledge.InterruptCrossYear(v))
			}
			summary["knowledge"] = knowledgeSummary
			if v, ok := summary["verdict"].(map[string]any); ok {
				v["knowledge"] = knowledgeSummary
			}
		}
	} else if summary["complete"] == false {
		status = "partial"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 11*time.Second)
	defer finishCancel()
	finalSummary := cleanSummary(summary)
	if cfg.singleAsk == nil {
		if root, ok := finalSummary.(map[string]any); ok {
			if reason, ok := clean(pa.Reason, c.Key).(string); ok {
				pa.Reason = reason
			}
			pa.Reason = prefixcheck.LimitReason(pa.Reason)
			root["prefix"] = pa
		}
	}
	if e = s.Finish(finishCtx, o, id, status, finalSummary, clean(detail, c.Key)); e != nil {
		return fmt.Errorf("结果结束状态保存失败：%w", e)
	}
	notify("persisted", map[string]any{"reportId": id, "status": status})
	return nil
}
func trimMetrics(m *gateway.Metrics) *gateway.Metrics {
	if m != nil {
		m.Answer = ""
		m.ThinkingText = ""
		m.SSEFrames = nil
	}
	return m
}
func runItem(ctx context.Context, s *store.Store, o store.Owner, id, model string, q questions.Generated, g gateway.Client, cfg Config, scoringVersion string) (itemResult, error) {
	out := itemResult{Judgment: questions.Judgment{Status: "unreviewed", Max: q.Definition.MaxScore}}
	def := q.Definition
	timeout := 120
	report, e := s.Report(ctx, o, id)
	if e != nil {
		return out, e
	}
	timeout = report.Settings.TimeoutSeconds
	reqOriginal := api.Request{Model: model, Key: g.Key, BaseURL: g.BaseURL, Protocol: g.Mode, Timeout: timeout, OutputTokens: report.Settings.MaxOutputTokens}
	if cfg.singleAsk != nil {
		m, err := api.SingleAsk(ctx, g, reqOriginal, *cfg.singleAsk)
		out.Evidence = map[string]any{"single_ask": m}
		out.Judgment = questions.Judgment{Status: "unreviewed"}
		if err == nil && m.Err != "" {
			err = fmt.Errorf("%s", m.Err)
		}
		return out, err
	}

	if def.Judge.Type == questions.ModelIdentityJudge && !questions.ModelIdentityApplies(model) {
		out.Judgment = questions.Judgment{Status: "not_applicable", Reason: "本题只检查选测GPT-6系列时的自报矛盾；当前模型不适用，未发送请求，不计入卷面。"}
		return out, nil
	}
	if questions.IsOriginalQuestion(def) {
		item := q.IQ()
		if def.Kind == "vision" {
			var err error
			item.ImagePNG, err = s.Image(ctx, o, id, def.ID)
			if err != nil {
				return out, err
			}
		}
		v, err := cfg.runner.Question(ctx, g, reqOriginal, item, scoringVersion)
		out.Answer, out.ThinkingText, out.Metrics = v.Answer, v.ThinkingText, &v.Metrics
		out.Evidence = v
		out.Judgment = questions.Judgment{Status: "known", Max: v.Max, Earned: &v.Earned, Half: v.Half, Tags: v.Tags}
		if def.Kind == "vision" {
			out.Evidence = api.VisionResult(item, v, err)
			out.Recognized = iq.NumericAnswer(v.Answer) && err == nil && !v.Skipped
		}
		if v.Skipped {
			out.Judgment.Status = "not_applicable"
			return out, nil
		}
		return out, err
	}
	if def.Kind == "fingerprint_numeric" && questions.Compatible(def) {
		v, err := cfg.runner.Numeric(ctx, g, reqOriginal)
		out.Evidence = v
		out.Judgment.Status = "reference"
		return out, err
	}
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
			req.Stream = false
			req.SuppressDefaultEffort = false
			if p, ok := questions.OriginalKnowledge(def); ok {
				req.Prompt = p.Prompt()
			}
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
			stop := m.StopReason
			if stop == "" {
				stop = "未提供"
			}
			usage := "未报告输出用量"
			if m.OutputReported {
				usage = fmt.Sprintf("输出用量%d token", m.OutputTokens)
			}
			return out, fmt.Errorf("上游返回HTTP %d但正文答案为空（结束标记：%s；%s）；本题未判分，请展开响应记录核对上游返回后补测此项", m.Status, stop, usage)
		}
		if def.Kind == "fingerprint_behavior" {
			out.Judgment = questions.Judgment{Status: "reference", Max: 0}
			if !questions.Compatible(def) {
				out.Judgment.Reason = "提示模板已修改，仅保留行为样本，不作旧参考库比对"
			}
		} else if def.Kind == "knowledge" {
			kind, reason := questions.GradeKnowledge(def, m.Answer)
			if p, ok := questions.OriginalKnowledge(def); ok {
				row := knowledge.Observe(p, m.Answer, "")
				kind, reason = row.Kind, row.Answer
				out.Evidence = map[string]any{"row": row, "prompt": p.Prompt()}
			}
			out.Judgment = questions.Judgment{Status: "reference", Reason: reason}
			if kind == "invalid" {
				out.Judgment.Status = "unknown"
			}
			if out.Evidence == nil {
				out.Evidence = map[string]any{"kind": kind, "reason": reason}
			}
		} else {
			out.Judgment = questions.Grade(q, m.Answer, scoringVersion == api.HubScoringVersion && def.ID == "Q4")
			if def.Kind == "vision" {
				out.Recognized = iq.NumericAnswer(m.Answer)
			}
		}
		return out, nil
	case "tools":
		if questions.Compatible(def) {
			v, e := capability.Tools(ctx, g, model, timeout)
			out.Evidence = v
			out.Judgment = boolJudgment(v.Pass, v.Reason)
			return out, e
		}
		v, e := capability.ToolsWithTemplate(ctx, g, model, timeout, def.Template, questions.Int(def.Parameters, "lhs"), questions.Int(def.Parameters, "rhs"))
		out.Evidence = v
		out.Attempts = v.Attempts
		out.Judgment = boolJudgment(v.Pass, v.Reason)
		return out, e
	case "cache":
		var templates *cachecheck.HubTemplates
		if !questions.Compatible(def) {
			templates = &cachecheck.HubTemplates{Heading: questions.Text(def.Parameters, "prefixHeading"), Line: questions.Text(def.Parameters, "prefixLineTemplate"), Turn: questions.Text(def.Parameters, "turnTemplate")}
		}
		if scoringVersion == api.HubScoringVersion {
			v, e := cachecheck.MeasureVerified(ctx, g, model, timeout, templates)
			out.Evidence = v.ReportEvidence()
			out.Judgment = boolJudgment(v.Pass, v.Reason)
			return out, e
		}
		if templates == nil {
			v, e := cachecheck.Measure(ctx, g, model, timeout)
			out.Evidence = v
			out.Judgment = boolJudgment(v.Pass, v.Reason)
			return out, e
		}
		col, e := cachecheck.CollectWithTemplates(ctx, g, model, timeout, templates)
		out.Evidence = col.Result
		out.Judgment = boolJudgment(col.Result.Pass, col.Result.Reason)
		return out, e
	case "injection":
		if questions.Compatible(def) {
			v, e := inject.Measure(ctx, g, model, timeout)
			out.Evidence = v
			out.Judgment.Status = "reference"
			return out, e
		}
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
		if questions.Compatible(def) {
			if gateway.IsDeepSeek(model) {
				v, e = capability.DeepSeekSearch(ctx, g, timeout)
			} else {
				v, e = capability.Web(ctx, g, model, timeout)
			}
		} else if gateway.IsDeepSeek(model) {
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
		if questions.Compatible(def) {
			v, e := cfg.runner.Thinking(ctx, g, reqOriginal)
			out.Evidence = v
			out.Judgment.Status = "reference"
			return out, e
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

// Compact aggregation retains original scoring fields, never duplicated bodies.
func compactResult(kind string, r itemResult) itemResult {
	out := r
	out.Metrics = nil
	if kind != "knowledge" && kind != "fingerprint_behavior" {
		out.Answer = ""
		out.ThinkingText = ""
	}
	if r.Evidence != nil {
		raw, _ := json.Marshal(r.Evidence)
		var ev map[string]any
		_ = json.Unmarshal(raw, &ev)
		if kind == "iq" {
			for _, k := range []string{"prompt", "expected", "answer", "thinking_text", "signature", "image"} {
				delete(ev, k)
			}
		}
		if kind == "vision" {
			delete(ev, "question")
		}
		if kind == "fingerprint_numeric" {
			delete(ev, "evidence")
			delete(ev, "behavioral_top3")
		}
		if kind == "cache" {
			delete(ev, "samples")
		}
		if kind == "injection" {
			delete(ev, "points")
		}
		out.Evidence = ev
	}
	return out
}

// Read old string judgments and current structured judgments alike. Ignore
// large evidence bodies here; aggregation only needs these persisted fields.
func savedResult(raw json.RawMessage) (itemResult, error) {
	var saved struct {
		Answer     string          `json:"answer"`
		Error      string          `json:"error"`
		Recognized bool            `json:"recognized"`
		Evidence   json.RawMessage `json:"evidence"`
		Judgment   json.RawMessage `json:"judgment"`
	}
	if e := json.Unmarshal(raw, &saved); e != nil {
		return itemResult{}, e
	}
	out := itemResult{Answer: saved.Answer, Error: saved.Error, Recognized: saved.Recognized}
	if len(saved.Evidence) > 0 {
		_ = json.Unmarshal(saved.Evidence, &out.Evidence)
	}
	if len(saved.Judgment) > 0 {
		var status string
		if json.Unmarshal(saved.Judgment, &status) == nil {
			out.Judgment.Status = status
		} else if e := json.Unmarshal(saved.Judgment, &out.Judgment); e != nil {
			return itemResult{}, e
		}
	}
	return out, nil
}
