package fingerprint

import (
	"encoding/json"
	"testing"
)

func TestLocalReferenceValidationAndIdempotence(t *testing.T) {
	var b Bank
	if err := json.Unmarshal(bankGenerated, &b); err != nil {
		t.Fatal(err)
	}
	n := len(b.Numeric.Models)
	ref := LocalReference{ID: "fixture-local", Aliases: []string{"fixture-alias"}, Family: "deepseek", FamilyName: "DeepSeek", Source: "https://fixture.invalid"}
	nums := make([]int, 300)
	for i := range nums {
		nums[i] = 1 + (i*71)%355
	}
	for i := 0; i < 36; i++ {
		ref.Samples = append(ref.Samples, nums)
	}
	if err := AddLocalReference(&b, ref); err != nil {
		t.Fatal(err)
	}
	if err := AddLocalReference(&b, ref); err != nil {
		t.Fatal(err)
	}
	if len(b.Numeric.Models) != n+1 || b.NumericModel("fixture-alias").ID != "fixture-local" {
		t.Fatal("alias duplicate or missing")
	}
	ref.Samples[0] = []int{999}
	if err := AddLocalReference(&b, ref); err == nil {
		t.Fatal("invalid sample accepted")
	}
}

func TestReviewedBankCarriesOfficialReferences(t *testing.T) {
	bank, err := LoadBank()
	if err != nil {
		t.Fatal(err)
	}
	var refs []LocalReference
	if err = json.Unmarshal(localReferences, &refs); err != nil || len(refs) != 0 {
		t.Fatalf("embedded single-channel references should be retired: %d %v", len(refs), err)
	}
	if bank.Source["repository"] != "Ikaleio/lm-detector" {
		t.Fatalf("bank source %v", bank.Source)
	}
	for name, id := range map[string]string{
		"gpt-6.2-sol": "gpt-6.2-sol", "gpt-6.2-astra": "gpt-6.2-astra", "gpt-6.1-terra": "gpt-6.1-terra",
		"gpt-6.1-sol": "gpt-6-astra", "claude-opus-4-7": "claude-opus-4-7", "claude-opus-4.7": "claude-opus-4-7",
		"claude-fable-5-2": "claude-fable-5-2", "claude-haiku-4-5-20251001": "claude-haiku-4-5-20251001",
		"deepseek-v4.1-flash-1017": "deepseek-v4.1-flash-1017", "qwen3.8-max-0902": "qwen3.8-max-0902",
	} {
		m := bank.NumericModel(name)
		if m == nil || m.ID != id || m.ReferenceSamples != 0 {
			t.Fatalf("%s should resolve to bank model %s", name, id)
		}
	}
	for _, retired := range []string{"claude-opus-4-5", "deepseek-v4.1-flash-0910"} {
		if bank.NumericModel(retired) != nil {
			t.Fatalf("retired reference %s still loaded", retired)
		}
	}
}
