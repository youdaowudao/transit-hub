package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestModelKnowledgeSerialAndRetry(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "gpt-6.1-sol")
	if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_reports SET snapshot_json=json_set(snapshot_json,'$.settings.retries',3) WHERE report_id=?`, id); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := map[string]questions.Generated{}
	for _, q := range snapshot.Questions {
		byPrompt[q.Prompt] = q
	}
	group, _ := knowledge.GroupForModel("gpt-6.1-sol")
	newer := map[string]bool{}
	for _, id := range group.Newer {
		newer["knowledge:"+id] = true
	}
	failedID := "knowledge:" + group.Know[0]
	retry := false
	var calls []string
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q, exists := byPrompt[prompt]
		if !exists {
			t.Fatal("请求不属于报告快照")
		}
		calls = append(calls, q.Definition.ID)
		if q.Definition.ID == failedID && !retry {
			response := fixtureResponse(r, "上游服务暂不可用")
			response.StatusCode = http.StatusServiceUnavailable
			return response, nil
		}
		answer := q.Expected
		if newer[q.Definition.ID] {
			answer = "D"
		}
		return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
	})
	cfg := Config{DisableBankSync: true, Transport: fake}
	if err = RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	first := loadKnowledgeProjection(t, report)
	if len(calls) != 6 || len(first.Rows) != 6 || first.BoundaryReview.Method != knowledge.ModelGroupMethod || first.Status != "unknown" || report.Status != "partial" {
		t.Fatalf("首次核对错误：请求 %v，状态 %s，结果 %+v", calls, report.Status, first)
	}
	if !reflect.DeepEqual(first.BoundaryReview.Know, group.Know) || !reflect.DeepEqual(first.BoundaryReview.Newer, group.Newer) || first.BoundaryReview.Group != group.Name {
		t.Fatalf("报告未保存创建时的题组：%+v", first.BoundaryReview)
	}
	if _, err = s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("e", 32), ItemIDs: []string{failedID}}); err != nil {
		t.Fatal(err)
	}
	retry, calls = true, nil
	if err = RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, err = s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if len(calls) != 1 || calls[0] != failedID || len(got.Rows) != 6 || got.Status != "matched" || report.Status != "done" {
		t.Fatalf("单题补测错误：请求 %v，状态 %s，结果 %+v", calls, report.Status, got)
	}
}

func TestModelUnaskedItemRetryAfterCancellationIsClosed(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "gpt-6.1-sol")
	const outsideID = "knowledge:virginia_2025_governor"
	if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_items SET status='queued',result_json='{}' WHERE report_id=? AND item_id=?`, id, outsideID); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelQueued(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("a", 32), ItemIDs: []string{outsideID}}); err != nil {
		t.Fatal(err)
	}
	var calls int
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err := RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for {
		page, err := s.ListItems(t.Context(), owner, id, store.Filters{Limit: 5, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ItemID != outsideID {
				continue
			}
			var result itemResult
			if err := json.Unmarshal(item.Result, &result); err != nil {
				t.Fatal(err)
			}
			if calls != 0 || item.Status != "done" || result.Judgment.Status != "not_asked" {
				t.Fatalf("题组外项目未正确结束：请求 %d，项目 %+v", calls, item)
			}
			return
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	t.Fatal("未找到题组外项目")
}

func TestModelRetryAllAfterCancellationFinishes(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "gpt-6.1-sol")
	if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_items SET status='queued',result_json='{}' WHERE report_id=?`, id); err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byPrompt := map[string]questions.Generated{}
	for _, q := range snap.Questions {
		ids = append(ids, q.Definition.ID)
		byPrompt[q.Prompt] = q
	}
	if err = s.CancelQueued(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("c", 32), ItemIDs: ids}); err != nil {
		t.Fatal(err)
	}
	group, _ := knowledge.GroupForModel("gpt-6.1-sol")
	newer := map[string]bool{}
	for _, event := range group.Newer {
		newer["knowledge:"+event] = true
	}
	var calls int
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		prompt, _ := requestPrompt(t, r)
		q := byPrompt[prompt]
		answer := q.Expected
		if newer[q.Definition.ID] {
			answer = "D"
		}
		return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
	})
	if err = RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if calls != 6 || report.Status != "done" || got.Status != "matched" {
		t.Fatalf("取消后全部补测未完成：请求 %d，状态 %s，结论 %+v", calls, report.Status, got)
	}
}

func TestModelDetectsResponsesWebSearchEvidence(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "gpt-6.1-sol")
	credential.Protocol = "openai"
	if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_reports SET protocol='openai',snapshot_json=json_set(snapshot_json,'$.settings.protocol','openai') WHERE report_id=?`, id); err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := map[string]questions.Generated{}
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	group, _ := knowledge.GroupForModel("gpt-6.1-sol")
	webID := "knowledge:" + group.Know[0]
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		_, body := requestPrompt(t, r)
		input, _ := body["input"].([]any)
		if len(input) == 0 {
			t.Fatal("Responses 请求缺少题面")
		}
		message, _ := input[len(input)-1].(map[string]any)
		prompt, _ := message["content"].(string)
		q := byPrompt[prompt]
		if q.Definition.ID != webID {
			return fixtureResponse(r, `{"choice":"D"}`), nil
		}
		answer, _ := json.Marshal(fmt.Sprintf(`{"choice":%q}`, q.Expected))
		response := fmt.Sprintf(`{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":%s}]}],"usage":{"input_tokens":10,"output_tokens":5}}`, answer)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	if err = RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if got.Status != "unknown" || report.Status != "partial" {
		t.Fatalf("联网响应被当成有效回答：%+v", got)
	}
	if _, err = s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{webID}}); err != nil {
		t.Fatalf("联网题没有补测入口：%v", err)
	}
}

func TestModelSummaryRetainsPublicMarkersAfterRedaction(t *testing.T) {
	for _, key := range []string{"group", "2026", "know", "matched"} {
		t.Run(key, func(t *testing.T) {
			group, _ := knowledge.GroupForModel("gpt-6-luna")
			rows := []knowledge.Row{{ID: group.Know[0], Kind: "know", Answer: "回答包含 " + key}}
			v := asMap(knowledge.ModelGroupVerdict(group, rows, "chat", 1))
			v["status"] = "matched"
			source := map[string]any{"knowledge": v, "verdict": map[string]any{"knowledge": v}}
			cleaned := clean(source, key).(map[string]any)
			restoreModelSummary(source, cleaned)
			for _, k := range []map[string]any{asMap(cleaned["knowledge"]), asMap(asMap(cleaned["verdict"])["knowledge"])} {
				row := asMap(k["rows"].([]any)[0])
				if asMap(k["boundary_review"])["method"] != knowledge.ModelGroupMethod || k["status"] != "matched" || row["id"] != group.Know[0] || row["kind"] != "know" || !strings.Contains(row["answer"].(string), "[密钥已隐藏]") {
					t.Fatalf("脱敏破坏判定字段或泄露回答：%+v", k)
				}
			}
		})
	}
}

func TestModelKnowledgePartialSelectionKeepsExistingPath(t *testing.T) {
	defs := questions.ModelKnowledge()[:2]
	s, owner, id, credential := configuredReport(t, defs, "gpt-6.1-sol")
	var calls int
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err := RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if calls != len(defs) || got.BoundaryReview != nil && got.BoundaryReview.Method == knowledge.ModelGroupMethod {
		t.Fatalf("部分选择被改为型号题组：请求 %d，结果 %+v", calls, got)
	}
}

func TestModelBankNonGroupUsesCrossYear(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "fixture")
	var calls int
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err := RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if got.BoundaryReview == nil || got.BoundaryReview.Method != knowledge.CrossYearMethod || calls == 0 || calls > 16 {
		t.Fatalf("非题组型号未使用旧跨年流程：请求 %d，结果 %+v", calls, got)
	}
}

func TestModelInvalidAnswerCanRetryWithShortCredential(t *testing.T) {
	s, owner, id, credential := configuredReport(t, questions.ModelKnowledge(), "gpt-6.1-sol")
	credential.Key = "2026"
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := map[string]questions.Generated{}
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	group, _ := knowledge.GroupForModel("gpt-6.1-sol")
	failedID := "knowledge:" + group.Know[0]
	newer := map[string]bool{}
	for _, eventID := range group.Newer {
		newer["knowledge:"+eventID] = true
	}
	retry := false
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q := byPrompt[prompt]
		if q.Definition.ID == failedID && !retry {
			return fixtureResponse(r, `{"choice":"E"}`), nil
		}
		answer := q.Expected
		if newer[q.Definition.ID] {
			answer = "D"
		}
		return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
	})
	cfg := Config{DisableBankSync: true, Transport: fake}
	if err = RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	first := loadKnowledgeProjection(t, report)
	if first.Status != "unknown" || first.BoundaryReview.Method != knowledge.ModelGroupMethod {
		t.Fatalf("非法选项或短密钥导致错误结论：%+v", first)
	}
	if _, err = s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("f", 32), ItemIDs: []string{failedID}}); err != nil {
		t.Fatal(err)
	}
	retry = true
	if err = RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, err = s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if got.Status != "matched" || got.BoundaryReview.Method != knowledge.ModelGroupMethod || len(got.Rows) != 6 {
		t.Fatalf("短密钥脱敏后无法按原题组补测：%+v", got)
	}
}

func TestCustomizedSectionDoesNotAskInsertedDefaults(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := store.Owner{UserID: "custom", WorkspaceID: "scope"}
	q, err := s.Question(t.Context(), owner, "knowledge:superbowl_2026_score")
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = false
	if _, err = s.SaveQuestion(t.Context(), owner, q, q.Revision, false); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("9", 32)
	credential := Credential{BaseURL: "https://fixture.invalid", Key: "fixture-secret", Protocol: "chat"}
	_, err = s.CreateReport(t.Context(), owner, store.CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedSectionIDs: []string{"knowledge"}, Target: store.Target{BaseURL: credential.BaseURL, Protocol: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Questions) != len(questions.CurrentKnowledge())-1 {
		t.Fatalf("自定义题库范围改变：共 %d 道", len(snap.Questions))
	}
	for _, q := range snap.Questions {
		if questions.IsModelAddition(q.Definition.ID) {
			t.Fatal("新默认题不应自动进入旧自定义题库", q.Definition.ID)
		}
	}
	var calls int
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err = RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	if calls != len(snap.Questions) {
		t.Fatalf("自定义题库发送 %d 次，期望 %d 次", calls, len(snap.Questions))
	}
}
