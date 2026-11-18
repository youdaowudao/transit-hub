package iq

import "math/rand"

// Paper preserves the original order and one shared random source.
func Paper(rng *rand.Rand) []Question { return paper(rng, true) }
func paper(rng *rand.Rand, chart bool) []Question {
	out := []Question{GenPill(), GenCarWash(), GenBatBall(), GenColorBlind(rng), GenAnchor(), GenCandyClassic(rng), GenCandyVariant(rng), GenSocks(rng), GenBridge()}
	if chart {
		out = append(out, GenChart(rng))
	}
	return out
}

// PaperQuestion avoids rendering an unused chart for text-only previews.
func PaperQuestion(rng *rand.Rand, id string) (Question, bool) {
	for _, q := range paper(rng, id == "vision") {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}
