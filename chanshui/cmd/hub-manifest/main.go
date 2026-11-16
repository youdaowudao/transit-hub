// hub-manifest runs at build time only. Opening the Hub page never loads the bank.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func main() {
	bank, err := fingerprint.LoadBank()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	seen, familySet := map[string]bool{}, map[string]bool{}
	for _, m := range bank.Numeric.Models {
		seen[m.ID] = true
		for _, alias := range m.Aliases {
			seen[alias] = true
		}
		familySet[m.FamilyName] = true
	}
	for _, m := range bank.Behavioral.Models {
		seen[cleanModel(m.ModelID)] = true
	}
	ids, families := []string{}, []string{}
	for id := range seen {
		ids = append(ids, id)
	}
	for name := range familySet {
		families = append(families, name)
	}
	sort.Strings(ids)
	sort.Strings(families)
	presetIDs := []string{}
	presetSummaries := []map[string]string{}
	presets := questions.Presets()
	for _, q := range presets {
		presetIDs = append(presetIDs, q.ID)
		presetSummaries = append(presetSummaries, map[string]string{"id": q.ID, "title": q.Title, "kind": q.Kind})
	}
	manifest := map[string]any{
		"supported_models": ids, "fingerprint_count": len(ids),
		"numeric_count": len(bank.Numeric.Models), "behavioral_count": len(bank.Behavioral.Models),
		"families":         []string{"GPT", "Claude", "Gemini", "DeepSeek", "Grok", "GLM"},
		"numeric_families": families, "source": bank.Source,
		"upstream_sha": "308315dc9c0dd83504f042bde2a96e82e243fe73",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"preset_count": len(presetIDs), "preset_ids": presetIDs,
		"preset_summaries": presetSummaries, "preset_definitions": presets,
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > 128*1024 {
		fmt.Fprintln(os.Stderr, "参考库元信息无法生成或超过128KiB")
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(append(raw, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cleanModel(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if strings.HasPrefix(id, "claude-") {
		id = strings.ReplaceAll(id, ".", "-")
		if id == "claude-haiku-4-5" {
			return "claude-haiku-4-5-20251001"
		}
	}
	return id
}
