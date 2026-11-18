package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func TestNumericBehaviorCancellationPersistsChineseInHub(t *testing.T) {
	q, _ := questions.Preset("fingerprint:numeric")
	s, o, id, cred := configuredReport(t, []questions.Definition{q}, "gpt-6-astra")
	bank, err := fingerprint.LoadBank()
	if err != nil {
		t.Fatal(err)
	}
	for i := range bank.Numeric.Models {
		m := &bank.Numeric.Models[i]
		m.Family = "gpt"
		m.Aliases = nil
		m.ID = fmt.Sprintf("zz-cancel-%d", i)
		if i == 0 {
			m.ID = "gpt-6-astra"
		}
		bank.Numeric.Robust.ModelOrder[i] = m.ID
	}
	for k, v := range bank.Numeric.Calibration {
		v.Beta = 1e-12
		bank.Numeric.Calibration[k] = v
	}
	raw, _ := json.Marshal(bank)
	if _, err = s.RuntimeDB().Exec("INSERT OR REPLACE INTO runtime_state(name,value) VALUES(?,?)", "modeltrace-bank-v1", raw); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := false
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		if prompt == fingerprint.BehaviorProbes[0].Prompt {
			reached = true
			cancel()
			return nil, context.Canceled
		}
		return fixtureResponse(r, strings.Repeat("247,83,319,", 105)), nil
	})
	if err = RunOne(ctx, s, o, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reached || report.Status != "cancelled" {
		t.Fatal("did not cancel the behavior request", report.Status)
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatal(page)
	}
	var result itemResult
	if err = json.Unmarshal(page.Items[0].Result, &result); err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Status != "error" || !strings.Contains(result.Error, "数字指纹检测已中止") {
		t.Fatal("lost Chinese error", result)
	}
	ev := asMap(result.Evidence)
	if !strings.Contains(fmt.Sprint(ev["error"]), "数字指纹检测已中止") || strings.Contains(fmt.Sprint(ev["note"]), "这两款暂时分不开") {
		t.Fatal(ev)
	}
}
