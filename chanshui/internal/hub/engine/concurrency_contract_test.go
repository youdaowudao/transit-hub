package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

// The slow first answer must not hold a freed slot after the fast second answer.
func TestConcurrencyRefillsSlotBeforeSlowAnswerCompletes(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o := store.Owner{UserID: "concurrency-contract", WorkspaceID: "isolated"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	settings, err := s.Settings(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Concurrency = 2
	if _, err = s.SaveSettings(ctx, o, settings); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, prompt := range []string{"slow-first", "fast-second", "replacement-third"} {
		q := questions.Definition{ID: fmt.Sprintf("custom:%032x", i+1), Title: prompt, Kind: "iq", Enabled: true, Order: i, Template: prompt, Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual"}}
		if _, err = s.SaveQuestion(ctx, o, q, 0, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.ID)
	}
	id := strings.Repeat("c", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "isolated-only", Protocol: "chat"}
	if _, err = s.CreateReport(ctx, o, store.CreateRequest{ReportID: id, TargetID: "isolated", Model: "fixture", SelectedQuestionIDs: ids, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	slowStarted, thirdStarted, releaseSlow := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseSlow) }) })
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(raw), "slow-first"):
			close(slowStarted)
			select {
			case <-releaseSlow:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		case strings.Contains(string(raw), "fast-second"):
			select {
			case <-slowStarted:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		case strings.Contains(string(raw), "replacement-third"):
			close(thirdStarted)
		}
		return fixtureResponse(r, "已保存的隔离答案"), nil
	})
	done := make(chan error, 1)
	go func() { done <- RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake}) }()
	select {
	case <-thirdStarted:
	case <-time.After(time.Second):
		t.Error("the freed slot did not start the third question while the first answer was pending")
	}
	release.Do(func() { close(releaseSlow) })
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	page, err := s.ListItems(ctx, o, id, store.Filters{})
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
	for _, item := range page.Items {
		var result itemResult
		if err = json.Unmarshal(item.Result, &result); err != nil {
			t.Fatal(err)
		}
		if item.Attempts != 1 || result.Attempts != 1 {
			t.Errorf("question %s has mixed request counts: stored=%d result=%d", item.ItemID, item.Attempts, result.Attempts)
		}
	}
}
