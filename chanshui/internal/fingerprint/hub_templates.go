package fingerprint

// HubChallengeTemplateSet exports copies, allowing an isolated caller to save
// its own editable prompt definitions without changing upstream defaults.
type HubChallengeTemplateSet struct{ Openings, Actions, Endings, SeparatorHints []string }

func HubChallengeTemplates() HubChallengeTemplateSet {
	return HubChallengeTemplateSet{append([]string(nil), chOpenings...), append([]string(nil), chActions...), append([]string(nil), chEndings...), append([]string(nil), chSeparatorHints...)}
}
