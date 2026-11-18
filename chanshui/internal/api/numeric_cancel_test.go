package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/history"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
)

func TestNumericBehaviorCancellationCannotConfirmOverlap(t *testing.T) {
	s := numericCancellationServer(t)
	bank, err := fingerprint.CloneBank(s.currentBank())
	if err != nil {
		t.Fatal(err)
	}
	for i := range bank.Numeric.Models {
		model := &bank.Numeric.Models[i]
		model.Family = "gpt"
		model.ID = fmt.Sprintf("zz-test-%d", i)
		if i == 0 {
			model.ID = "gpt-6-astra"
		}
	}
	for k, v := range bank.Numeric.Calibration {
		v.Beta = 0
		bank.Numeric.Calibration[k] = v
	}
	s.bank = bank
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	behaviorCalled := false
	client := gateway.Client{Mode: "chat", BaseURL: "https://fixture.invalid", NoRetry: true,
		BeforeCall: func(c context.Context, r gateway.CallRequest) (context.Context, error) {
			if r.Prompt == fingerprint.BehaviorProbes[0].Prompt {
				behaviorCalled = true
				cancel()
				return c, context.Canceled
			}
			return c, nil
		},
		HTTP: &http.Client{Transport: syncTransport(func(r *http.Request) (*http.Response, error) {
			body := fmt.Sprintf("{\"choices\":[{\"message\":{\"content\":%q},\"finish_reason\":\"stop\"}]}", strings.Repeat("247,83,319,", 105))
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})},
	}
	result, err := s.numeric(ctx, client, Request{Model: "gpt-6-astra", Timeout: 10})
	out := result.(map[string]any)
	if !behaviorCalled || out["used_outputs"].(int) < 3 {
		t.Fatal("did not reach the intended cancellation path", out)
	}
	if err == nil || !strings.Contains(err.Error(), "数字指纹检测已中止") {
		t.Fatal("lost Chinese cancellation reason", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled behavior was returned as success: %v", err)
	}
	if strings.Contains(out["note"].(string), "这两款暂时分不开") {
		t.Fatal("cancelled probe retained identity conclusion", out)
	}
}

func TestNumericCancellationPersistsChineseThroughStandaloneWrapper(t *testing.T) {
	s := numericCancellationServer(t)
	bank, err := fingerprint.CloneBank(s.currentBank())
	if err != nil {
		t.Fatal(err)
	}
	for i := range bank.Numeric.Models {
		bank.Numeric.Models[i].Family = "gpt"
		bank.Numeric.Models[i].ID = fmt.Sprintf("zz-cancel-%d", i)
		if i == 0 {
			bank.Numeric.Models[i].ID = "gpt-6-astra"
		}
	}
	for k, v := range bank.Numeric.Calibration {
		v.Beta = 0
		bank.Numeric.Calibration[k] = v
	}
	s.bank = bank
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := false
	s.httpClient = &http.Client{Transport: syncTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), fingerprint.BehaviorProbes[0].Prompt) {
			reached = true
			cancel()
			return nil, context.Canceled
		}
		body := fmt.Sprintf("{\"choices\":[{\"message\":{\"content\":%q},\"finish_reason\":\"stop\"}]}", strings.Repeat("247,83,319,", 105))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	id := strings.Repeat("c", 32)
	created := time.Now().UTC().Format(time.RFC3339)
	if _, err = s.store.CreateJob(t.Context(), history.Record{ReportID: id, CreatedAt: created, Model: "gpt-6-astra", BaseURL: "https://fixture.invalid"}, json.RawMessage("[]"), "hash", ""); err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: id, Status: "running", Model: "gpt-6-astra", CreatedAt: created, Probes: []Probe{{ID: "fingerprint", Status: "queued"}}, cancel: cancel}
	s.run(ctx, task, Request{Model: "gpt-6-astra", BaseURL: "https://fixture.invalid", Protocol: "chat", Timeout: 10}, map[string]bool{"fingerprint": true})
	if !reached || task.Status != "cancelled" {
		t.Fatal("wrong stop path", task.Status)
	}
	saved, err := s.store.GetByReportID(id)
	if err != nil {
		t.Fatal(err)
	}
	var v Verdict
	if err = json.Unmarshal(saved.Verdict, &v); err != nil {
		t.Fatal(err)
	}
	fp := v.Fingerprint.(map[string]any)
	if !strings.Contains(fmt.Sprint(fp["error"]), "数字指纹检测已中止") {
		t.Fatal("wrapper replaced Chinese error", fp)
	}
	if strings.Contains(fmt.Sprint(fp["note"]), "这两款暂时分不开") {
		t.Fatal("cancelled report kept conclusion", fp)
	}
}

func numericCancellationServer(t *testing.T) *Server {
	t.Helper()
	db, err := history.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bank, err := fingerprint.LoadBank()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{bank: bank, store: db, httpClient: &http.Client{}}
}
