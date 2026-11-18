package fingerprint

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func TestCollectedGPTReferencesLoadFrozenTrainingOnly(t *testing.T) {
	expected := map[string]string{
		"gpt-6.2-sol":   "155084e2d9b6068fb46dea0b4ec0d925edf230d7a6604c45a8313278ef0a589f",
		"gpt-6.2-astra": "43cf9368761db595b7857859333af745992bc251234bd9d989ccf1e15ca64e97",
		"gpt-6.1-terra": "5b27557bd4e8bd4a3405d5abe9a0fd7f360f66a295ed1176f4cc36fa2e9d9b72",
	}
	bank, err := LoadBank()
	if err != nil {
		t.Fatal(err)
	}
	var refs []LocalReference
	if err = json.Unmarshal(localReferences, &refs); err != nil {
		t.Fatal(err)
	}
	for id, hash := range expected {
		m := bank.NumericModel(id)
		if m == nil || m.ID != id || m.ReferenceSamples != 36 || m.Family != "gpt" {
			t.Fatalf("reference %s was not loaded with its 36 training samples", id)
		}
		found := false
		for _, r := range refs {
			if r.ID != id {
				continue
			}
			found = true
			raw, _ := json.Marshal(r.Samples)
			if len(r.Samples) != 36 || fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
				t.Fatalf("frozen training changed for %s", id)
			}
		}
		if !found {
			t.Fatal("missing embedded reference", id)
		}
	}
}
