package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func TestIncompleteBankPreservesEditedModelQuestions(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	for _, id := range []string{"knowledge:superbowl_2026_score", "knowledge:milano_2026_men_skate", "knowledge:bp_2026_chair_ouster"} {
		q, err := s.Question(ctx, testOwner, id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(id, "superbowl") {
			q.Enabled = false
		} else {
			q.Title += "（用户修改）"
		}
		if _, err = s.SaveQuestion(ctx, testOwner, q, q.Revision, false); err != nil {
			t.Fatal(err)
		}
	}
	id := strings.Repeat("8", 32)
	_, err = s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedSectionIDs: []string{"knowledge"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(ctx, testOwner, id)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, q := range snap.Questions {
		seen[q.Definition.ID] = true
	}
	if len(seen) != len(questions.CurrentKnowledge())-1+2 || !seen["knowledge:milano_2026_men_skate"] || !seen["knowledge:bp_2026_chair_ouster"] || seen["knowledge:anthropic_2026_mythos_access"] {
		t.Fatalf("修改过的题或未经修改的新题范围错误：%v", seen)
	}
}

func TestIncompleteBankFiltersBeforeReportLimit(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	q, err := s.Question(ctx, testOwner, "knowledge:superbowl_2026_score")
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = false
	if _, err = s.SaveQuestion(ctx, testOwner, q, q.Revision, false); err != nil {
		t.Fatal(err)
	}
	custom, _ := questions.Preset("tools")
	for i := 0; i < 101; i++ {
		custom.ID = fmt.Sprintf("custom:%032x", i+1)
		raw, _ := json.Marshal(custom)
		if _, err = s.db.Exec(`INSERT INTO hub_questions VALUES(?,?,?,?,1)`, testOwner.UserID, testOwner.WorkspaceID, custom.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	request := CreateRequest{ReportID: strings.Repeat("8", 32), TargetID: "fixture", Model: "fixture", SelectedSectionIDs: []string{"knowledge", "tools"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}
	if _, err = s.CreateReport(ctx, testOwner, request); err != nil {
		t.Fatal("过滤后恰好 128 项却无法创建", err)
	}
	_, snap, err := s.LoadRun(ctx, testOwner, request.ReportID)
	if err != nil || len(snap.Questions) != 128 {
		t.Fatalf("过滤后的快照应有 128 项：%d %v", len(snap.Questions), err)
	}
	request.ReportID = strings.Repeat("9", 32)
	request.SingleAsk = &api.SingleAskInput{Prompt: "单问内容"}
	if _, err = s.CreateReport(ctx, testOwner, request); err != nil {
		t.Fatal("新增默认题不应使单问被提前拒绝", err)
	}
	_, snap, err = s.LoadRun(ctx, testOwner, request.ReportID)
	if err != nil || len(snap.Questions) != 1 || snap.Questions[0].Prompt != "单问内容" {
		t.Fatalf("单问快照改变：%+v %v", snap, err)
	}
}

func TestCompleteBankKeepsOldLimitAndCancelledRetry(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "fixture"} {
		t.Run(model, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := t.Context()
			if err = s.EnsureScope(ctx, testOwner); err != nil {
				t.Fatal(err)
			}
			custom, _ := questions.Preset("tools")
			for i := 0; i < 100; i++ {
				custom.ID = fmt.Sprintf("custom:%032x", i+1)
				raw, _ := json.Marshal(custom)
				if _, err = s.db.Exec(`INSERT INTO hub_questions VALUES(?,?,?,?,1)`, testOwner.UserID, testOwner.WorkspaceID, custom.ID, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			id := strings.Repeat("6", 32)
			_, err = s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: model, SelectedSectionIDs: []string{"knowledge", "tools"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}})
			if err != nil {
				t.Fatal("旧版 128 项范围不应被新增默认题拒绝", err)
			}
			_, snap, err := s.LoadRun(ctx, testOwner, id)
			if err != nil || len(snap.Questions) != 128+len(questions.ModelAdditionIDs()) {
				t.Fatalf("完整快照应保留所有题：%d %v", len(snap.Questions), err)
			}
			var retryIDs []string
			cursor := ""
			for {
				page, err := s.ListItems(ctx, testOwner, id, Filters{Limit: 5, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					if item.Status == "queued" {
						retryIDs = append(retryIDs, item.ItemID)
					}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			want := 128
			if model == "gpt-6.1-sol" {
				want = 107
			}
			if len(retryIDs) != want {
				t.Fatalf("排队待测 %d 项，期望 %d 项", len(retryIDs), want)
			}
			if err = s.CancelQueued(ctx, testOwner, id); err != nil {
				t.Fatal(err)
			}
			if _, err = s.RetryReport(ctx, testOwner, RetryRequest{ReportID: id, RetryID: strings.Repeat("e", 32), ItemIDs: retryIDs}); err != nil {
				t.Fatal("取消后无法按原上限补测全部未完成项", err)
			}
		})
	}
}
