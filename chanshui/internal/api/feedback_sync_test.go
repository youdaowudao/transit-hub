package api

import (
	"context"
	"encoding/json"
	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/history"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type syncTransport func(*http.Request) (*http.Response, error)

func TestUpstreamOnlyCachedBankKeepsLocalReference(t *testing.T) {
	db, err := history.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := os.ReadFile("../fingerprint/bank.generated.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveRuntimeState(context.Background(), bankStateKey, raw); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.supported("gpt-6-astra") || !s.supported("claude-opus-5") {
		t.Fatal("cached bank lost its enrolled models")
	}
	var upstream fingerprint.Bank
	if err = json.Unmarshal(raw, &upstream); err != nil {
		t.Fatal(err)
	}
	count := len(upstream.Numeric.Models)
	if err = s.publishBank(context.Background(), &upstream); err != nil {
		t.Fatal(err)
	}
	if len(upstream.Numeric.Models) != count {
		t.Fatal("publish mutated caller snapshot")
	}
	for _, model := range []string{"gpt-6-astra", "claude-opus-5", "gpt-6-sol"} {
		if !s.supported(model) || s.currentBank().NumericModel(model) == nil {
			t.Fatalf("published bank erased %s", model)
		}
	}
}

func (f syncTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReviewedBankIgnoresModelTraceSyncAndCache(t *testing.T) {
	db, e := history.Open(filepath.Join(t.TempDir(), "sync.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e := New(db, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old := s.currentBank()
	if old.Source["repository"] != "Ikaleio/lm-detector" {
		t.Fatalf("embedded bank source %v", old.Source)
	}
	count := 0
	client := &http.Client{Transport: syncTransport(func(r *http.Request) (*http.Response, error) {
		count++
		return nil, io.ErrUnexpectedEOF
	})}
	if e = s.syncBank(context.Background(), client); e != nil || count != 0 || s.currentBank() != old {
		t.Fatal("reviewed bank was synced from ModelTrace", e, count)
	}
	// A cache left by an earlier ModelTrace sync must not come back after restart.
	stale, e := fingerprint.CloneBank(old)
	if e != nil {
		t.Fatal(e)
	}
	stale.Source = map[string]string{"repository": modelTraceRepository, "commit": strings.Repeat("a", 40)}
	raw, _ := json.Marshal(stale)
	if e = db.SaveRuntimeState(context.Background(), bankStateKey, raw); e != nil {
		t.Fatal(e)
	}
	next, e := New(db, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if next.currentBank().Source["repository"] != old.Source["repository"] || next.currentBank().Source["commit"] != old.Source["commit"] {
		t.Fatal("stale ModelTrace cache replaced the embedded bank")
	}
	raw, _ = json.Marshal(s.currentBank())
	var invalid fingerprint.Bank
	json.Unmarshal(raw, &invalid)
	invalid.Numeric.Robust.OrderedBlocks.Centroids[0] = []float64{1}
	if e = s.publishBank(context.Background(), &invalid); e == nil {
		t.Fatal("invalid matrix accepted")
	}
	if s.currentBank() != old {
		t.Fatal("bad update replaced verified bank")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = s.currentBank()
				_ = s.supported("claude-sonnet-5-5")
			}
		}()
	}
	if e = s.publishBank(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
}
func TestFeedbackStoredPrivatelyAndRateLimited(t *testing.T) {
	s, _, _, _ := testSetup(t)
	payload := map[string]string{"kind": "method", "contact": "fixture-contact@example.invalid", "message": "建议补充模型测试对照样本。"}
	w := post(t, s, "/api/feedback", payload)
	if w.Code != 201 || strings.Contains(w.Body.String(), payload["contact"]) {
		t.Fatal(w.Code, w.Body.String())
	}
	rows, e := s.store.ListFeedback(context.Background())
	if e != nil || len(rows) != 1 || rows[0].Contact != payload["contact"] {
		t.Fatal(rows, e)
	}
	if r := getJSON(t, s.Handler(), "/api/feedback"); r.Code != 405 {
		t.Fatal("private feedback exposed", r.Code)
	}
	for i := 0; i < 4; i++ {
		if r := post(t, s, "/api/feedback", payload); r.Code != 201 {
			t.Fatal(r.Code)
		}
	}
	if r := post(t, s, "/api/feedback", payload); r.Code != 429 {
		t.Fatal("rate limit absent", r.Code)
	}
	payload["message"] = "密钥 sk-should-not-be-persisted"
	if r := post(t, s, "/api/feedback", payload); r.Code != 400 {
		t.Fatal("secret accepted")
	}
}
