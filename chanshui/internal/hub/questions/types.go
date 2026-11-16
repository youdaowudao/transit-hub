// Package questions owns the editable Hub question catalogue. It never calls an
// upstream: definitions, generation and grading are local and deterministic.
package questions

import "github.com/kiss-kedaya/chanshui/internal/iq"

type Judge struct {
	Type            string   `json:"type"`
	ExpectedText    string   `json:"expectedText,omitempty"`
	ExpectedNumber  float64  `json:"expectedNumber,omitempty"`
	ExpectedFrom    string   `json:"expectedFrom,omitempty"`
	Match           string   `json:"match,omitempty"`
	Required        []string `json:"required,omitempty"`
	Forbidden       []string `json:"forbidden,omitempty"`
	ReferenceAnswer string   `json:"referenceAnswer,omitempty"`
}
type Definition struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Kind       string         `json:"kind"`
	Enabled    bool           `json:"enabled"`
	Order      int            `json:"order"`
	MaxScore   int            `json:"maxScore"`
	Template   string         `json:"template"`
	Parameters map[string]any `json:"parameters"`
	Judge      Judge          `json:"judge"`
	Revision   int            `json:"revision,omitempty"`
}
type Generated struct {
	Definition Definition `json:"definition"`
	Seed       int64      `json:"seed"`
	Prompt     string     `json:"prompt"`
	Expected   string     `json:"expected"`
	Tags       []string   `json:"tags,omitempty"`
	Image      []byte     `json:"-"`
}

func (g Generated) IQ() iq.Question {
	return iq.Question{ID: g.Definition.ID, Title: g.Definition.Title, Prompt: g.Prompt, Answer: g.Expected, MaxScore: g.Definition.MaxScore, Tags: g.Tags, ImagePNG: g.Image}
}

type Judgment struct {
	Status string   `json:"status"`
	Earned *int     `json:"earned"`
	Max    int      `json:"max"`
	Half   bool     `json:"half"`
	Tags   []string `json:"tags,omitempty"`
	Reason string   `json:"reason,omitempty"`
}
