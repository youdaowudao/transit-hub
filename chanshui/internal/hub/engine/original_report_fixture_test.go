package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

// The browser consumes real original-probe DTOs produced against this local
// transport. No model, listener, main database or credentials are involved.
func TestOriginalReportKeepsEveryProbeDTOAndCanExportBrowserFixture(t *testing.T) {
	s, o, _, c := setup(t, []string{"Q2"}, "chat")
	id := strings.Repeat("b", 32)
	if _, err := s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.1-sol", SelectedSectionIDs: []string{"iq", "vision", "fingerprint", "injection", "tools", "web", "knowledge", "cache", "thinking"}, Target: store.Target{AccountName: "原版完整报告隔离示例", GroupName: "原版迁移验收", BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{}
	vision := ""
	for _, q := range snap.Questions {
		if q.Definition.Kind == "iq" {
			answers[q.Prompt] = q.Expected
		}
		if q.Definition.Kind == "vision" {
			vision = q.Expected
		}
	}
	grade := regexp.MustCompile(`本题满分 (\d+)`)
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, body := requestPrompt(t, r)
		if _, ok := body["tools"]; ok {
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"本地原版报告示例：未模拟工具往返，保留原错误诊断"}}`)), Request: r}, nil
		}
		if m := grade.FindStringSubmatch(prompt); len(m) > 1 {
			return fixtureResponse(r, m[1]), nil
		}
		if answer, ok := answers[prompt]; ok {
			return fixtureResponse(r, "最终答案: "+answer), nil
		}
		if prompt == "" {
			return fixtureResponse(r, "最终答案: "+vision), nil
		}
		for _, p := range knowledge.Anchors {
			if strings.Contains(prompt, p.Question) {
				for _, choice := range p.Choices {
					if choice.Kind == "know" {
						return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, choice.Letter)), nil
					}
				}
			}
		}
		if strings.Contains(prompt, "整数") {
			return fixtureResponse(r, strings.Repeat("1 2 3 4 5 6 7 8 9 10 ", 34)), nil
		}
		return fixtureResponse(r, "noted"), nil
	})
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err = json.Unmarshal(report.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	v := asMap(summary["verdict"])
	for _, key := range []string{"iq", "knowledge", "integrity", "total", "timing"} {
		if v[key] == nil {
			t.Fatal("original verdict field omitted", key)
		}
	}
	items := []store.Item{}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: cursor, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			items = append(items, item)
			if item.Definition.Kind == "fingerprint_behavior" || item.Definition.Kind == "knowledge" || item.Definition.Kind == "thinking" {
				continue
			}
			var result itemResult
			if err = json.Unmarshal(item.Result, &result); err != nil {
				t.Fatal(err)
			}
			if len(asMap(result.Evidence)) == 0 {
				t.Fatal("original probe DTO omitted", item.ItemID)
			}
			seen[item.Definition.Kind] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	for _, kind := range []string{"iq", "vision", "cache", "tools", "web", "injection", "fingerprint_numeric"} {
		if !seen[kind] {
			t.Fatal("original probe missing", kind)
		}
	}
	if target := os.Getenv("TRANSITHUB_CHANSHUI_BROWSER_FIXTURE"); target != "" {
		root, err := filepath.Abs("../../../..")
		if err != nil {
			t.Fatal(err)
		}
		target = filepath.Clean(target)
		if !filepath.IsAbs(target) || !strings.HasPrefix(target, filepath.Join(root, ".cache")+string(os.PathSeparator)) {
			t.Fatal("fixture export must be inside project .cache")
		}
		raw, _ := json.Marshal(report)
		var browserReport map[string]any
		_ = json.Unmarshal(raw, &browserReport)
		browserReport["processExited"] = true
		image, err := s.Image(t.Context(), o, id, "vision")
		if err != nil {
			t.Fatal(err)
		}
		fixture := map[string]any{"report": browserReport, "items": items, "images": map[string]any{"vision": map[string]any{"dataURL": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)}}}
		raw, err = json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
