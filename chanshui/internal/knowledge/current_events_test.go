package knowledge

import "testing"

func TestCurrentFallbackDoesNotInferBoundariesFromWrongAnswers(t *testing.T) {
	s := newSearch()
	s.current = true
	for i := range maxAsks {
		p, ok := s.next()
		if !ok || s.seen[p.ID] {
			t.Fatalf("ask %d stopped early or repeated %s", i+1, p.ID)
		}
		if i == 0 && p.ID != "virginia_2025_governor" {
			t.Fatalf("incorrect initial anchor: %s", p.ID)
		}
		s.apply(p, kindWrong, "错误答案")
	}
	if p, ok := s.next(); ok || !s.lo.IsZero() || !s.hi.IsZero() {
		t.Fatalf("limits or bounds changed: next=%s low=%s high=%s", p.ID, s.lo, s.hi)
	}
}

func TestCurrentInvalidObservationStopsProbe(t *testing.T) {
	s := newSearch()
	s.current = true
	p, _ := s.next()
	s.apply(p, kindInvalid, "调用了工具")
	if _, ok := s.next(); ok {
		t.Fatal("invalid measurement allowed another request")
	}
}
