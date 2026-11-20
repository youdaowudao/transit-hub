package reports

import (
	"encoding/json"
	"time"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/workspacescope"
)

type Scope = workspacescope.Scope

type Coverage struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Complete bool   `json:"complete"`
}

type Section struct {
	Title      string     `json:"title"`
	Date       string     `json:"date"`
	ObservedAt *time.Time `json:"observedAt"`
	Coverage   Coverage   `json:"coverage"`
	Text       string     `json:"text"`
}

type Document struct {
	BusinessDate string    `json:"businessDate"`
	Sections     []Section `json:"sections"`
	Text         string    `json:"text"`
	ObservedAt   time.Time `json:"observedAt"`
}

type ReportConfiguration struct {
	Enabled json.RawMessage `json:"dailyReportEnabled"`
	Time    json.RawMessage `json:"dailyReportTime"`
	BotIDs  json.RawMessage `json:"dailyReportBotIds"`
}

type BotOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type Preview struct {
	Document
	Configuration    ReportConfiguration `json:"configuration"`
	Bots             []BotOption         `json:"bots"`
	PauseReason      string              `json:"pauseReason"`
	Scheduled        *ScheduledResult    `json:"scheduled"`
	ScheduledWarning string              `json:"scheduledWarning"`
}

type BotResult struct {
	BotID  string `json:"botId"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type TestResult struct {
	Attempted int         `json:"attempted"`
	Sent      int         `json:"sent"`
	Failed    int         `json:"failed"`
	Unknown   int         `json:"unknown"`
	Results   []BotResult `json:"results"`
}

type ScheduledResult struct {
	AttemptID    string    `json:"attemptId"`
	SendingDate  string    `json:"sendingDate"`
	BusinessDate string    `json:"businessDate"`
	At           time.Time `json:"at"`
	BotID        string    `json:"botId"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason"`
	Attempted    bool      `json:"attempted"`
}

type preparedSend struct {
	Target   settings.TelegramTarget
	Key      string
	Original string
	Marker   ScheduledResult
}
