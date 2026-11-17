package knowledge

// Prompt and Classify expose the original implementation for persistence and
// explicit per-question retries. Check's search and parser remain unchanged.
func (p Probe) Prompt() string                          { return p.prompt() }
func (p Probe) Classify(answer string) (string, string) { return classify(p, answer) }
func Anchor(id string) (Probe, bool) {
	for _, p := range Anchors {
		if p.ID == id {
			return p, true
		}
	}
	return Probe{}, false
}
func Observe(p Probe, answer, failure string) Row {
	s := newSearch()
	kind, focus := classify(p, answer)
	if failure != "" {
		kind, focus = kindError, failure
	}
	s.apply(p, kind, focus)
	return s.rows[0]
}

// FromRows reuses the same boundary semantics after an explicitly requested
// retry. Callers provide only saved actual observations, never guessed rows.
func FromRows(rows []Row, protocol string, attempts int) Verdict {
	s := newSearch()
	for _, row := range rows {
		if p, ok := Anchor(row.ID); ok {
			text := row.Answer
			if row.Error != "" {
				text = row.Error
			}
			s.apply(p, row.Kind, text)
		}
	}
	v := s.verdict(nowFn())
	v.Protocol = protocol
	v.Attempts = attempts
	return v
}
