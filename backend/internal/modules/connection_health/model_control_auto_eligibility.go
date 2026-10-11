package connection_health

import "time"

type modelControlAutoBasis struct {
	BusinessDay string     `json:"businessDay"`
	BatchID     string     `json:"batchId"`
	RuleVersion int64      `json:"ruleVersion"`
	Decision    string     `json:"decision"`
	EvidenceAt  *time.Time `json:"evidenceAt"`
}
type modelControlAutoAttempt struct {
	Operation string                `json:"operation"`
	Basis     modelControlAutoBasis `json:"basis"`
	AttemptAt time.Time             `json:"attemptAt"`
	ReasonKey string                `json:"reasonKey"`
}
type modelControlAutoView struct {
	State     string     `json:"state"`
	Operation string     `json:"operation"`
	NextAt    *time.Time `json:"nextAt"`
	ReasonKey string     `json:"reasonKey"`
	ChangedAt time.Time  `json:"changedAt"`
}
type modelControlAutoStatusInput struct {
	Settings    ModelControlAutoSettings
	RuleSavedAt *time.Time
	Item        ModelControlItem
	Object      modelControlTarget
	EvidenceAt  *time.Time
	Now         time.Time
	IgnoreRetry bool
}

func autoBasis(b modelControlBasis, at *time.Time) modelControlAutoBasis {
	return modelControlAutoBasis{b.BusinessDay, b.BatchID, b.RuleVersion, b.Decision, at}
}
func modelControlAutoBasisMatches(a, b modelControlAutoBasis) bool {
	return modelControlBasisMatches(modelControlBasis{BusinessDay: a.BusinessDay, BatchID: a.BatchID, RuleVersion: a.RuleVersion, Decision: a.Decision}, modelControlBasis{BusinessDay: b.BusinessDay, BatchID: b.BatchID, RuleVersion: b.RuleVersion, Decision: b.Decision}) && ((a.EvidenceAt == nil && b.EvidenceAt == nil) || (a.EvidenceAt != nil && b.EvidenceAt != nil && a.EvidenceAt.Equal(*b.EvidenceAt)))
}
func modelControlItemEvidenceAt(item ModelControlItem) *time.Time {
	if item.Round == nil {
		return nil
	}
	return item.Round.EvidenceAt
}

// The same ordered decision is used by list views, scans and both locked checks.
func modelControlAutoStatus(in modelControlAutoStatusInput) modelControlAutoView {
	out := modelControlAutoView{State: "off", ChangedAt: in.Object.UpdatedAt}
	settings, item := in.Settings, in.Item
	if !settings.AutoCloseEnabled && !settings.AutoRestoreEnabled {
		return out
	}
	out.State = "none"
	switch {
	case item.Decision == "close_recommended" && settings.AutoCloseEnabled:
		out.Operation = "close"
	case item.Decision == "usable" && len(item.Control.ClosedEntries) > 0 && settings.AutoRestoreEnabled:
		out.Operation = "restore"
	default:
		return out
	}
	if item.Control.AccountPending != nil || item.Control.Pending != nil {
		out.State = "busy"
		return out
	}
	reason := ""
	state := item.Control.Observation.State
	if out.Operation == "close" {
		switch state {
		case "last_model":
			reason = "AutoLastModel"
		case "not_isolatable":
			reason = "AutoNotIsolatable"
		case "not_provided":
			reason = "AutoNotProvided"
		case "account_missing":
			reason = "AutoAccountMissing"
		}
		if reason == "" && len(item.Control.ClosedEntries) > 0 && state != "closed" {
			reason = "AutoPartiallyClosed"
		}
		if reason == "" && in.Object.UnconfirmedClose != nil && in.Now.Before(in.Object.UnconfirmedClose.SentAt.Add(24*time.Hour)) {
			reason = "AutoUnconfirmedClose"
		}
	} else {
		if state == "account_missing" {
			reason = "AutoAccountMissing"
		} else if item.Control.ConflictReason != "" {
			reason = "AutoConflict"
		}
	}
	if reason != "" {
		out.State = "manual"
		out.ReasonKey = modelControlError(reason)
		return out
	}
	if out.Operation == "close" && len(item.Control.ClosedEntries) > 0 && state == "closed" {
		out.State = "done"
		return out
	}
	enabledAt := settings.AutoCloseEnabledAt
	if out.Operation == "restore" {
		enabledAt = settings.AutoRestoreEnabledAt
	}
	start := in.Object.UpdatedAt
	if enabledAt == nil || in.EvidenceAt == nil {
		out.State = "waiting"
		return out
	}
	if enabledAt.After(start) {
		start = *enabledAt
	}
	if in.RuleSavedAt != nil && in.RuleSavedAt.After(start) {
		start = *in.RuleSavedAt
	}
	if !in.EvidenceAt.After(start) {
		out.State = "waiting"
		return out
	}
	if last := in.Object.AutoState; !in.IgnoreRetry && last != nil && last.Operation == out.Operation && modelControlAutoBasisMatches(last.Basis, autoBasis(item.Basis, in.EvidenceAt)) {
		next := last.AttemptAt.Add(time.Duration(settings.RetryIntervalMinutes) * time.Minute)
		if next.After(in.Now) {
			out.State = "retry"
			out.NextAt = &next
			out.ReasonKey = last.ReasonKey
			return out
		}
	}
	out.State = "eligible"
	return out
}
