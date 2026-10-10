package connection_health

import "sort"

func modelControlSummaryStatus(t modelControlTarget) string {
	if len(t.ClosedEntries) > 0 {
		return "closed"
	}
	switch t.Observation.State {
	case "serving", "last_model":
		return "open"
	case "not_isolatable":
		switch t.Observation.ReasonKey {
		case modelControlError("OpenAIPassthrough"), modelControlError("EmptyMapping"), modelControlError("WildcardMapping"), modelControlError("WildcardFallback"):
			return "open"
		}
	case "not_provided":
		return "not_provided"
	case "account_missing":
		return "account_missing"
	}
	return "unknown"
}
func appendModelControlSummary(summary *ModelControlAccountSummary, t modelControlTarget, item ModelControlItem) {
	status := modelControlSummaryStatus(t)
	if status == "open" {
		summary.Open++
	}
	if status == "closed" {
		summary.Closed++
	}
	if item.Attention {
		summary.Attention++
	}
	summary.Models = append(summary.Models, ModelControlSummaryModel{ModelName: t.ModelName, Status: status, Decision: item.Decision, Attention: item.Attention, CheckedAt: t.Observation.CheckedAt})
}
func sortModelControlSummary(summary *ModelControlAccountSummary) {
	sort.Slice(summary.Models, func(i, j int) bool { return summary.Models[i].ModelName < summary.Models[j].ModelName })
}
func modelControlCounts(items []ModelControlItem) ModelControlCounts {
	counts := ModelControlCounts{Total: len(items)}
	for _, item := range items {
		status := modelControlSummaryStatus(modelControlTarget{ClosedEntries: item.Control.ClosedEntries, Observation: item.Control.Observation})
		if status == "open" {
			counts.Open++
		}
		if status == "closed" {
			counts.Closed++
		}
		if item.Attention {
			counts.Attention++
		}
		if item.Decision == "no_evidence" {
			counts.Untested++
		}
	}
	return counts
}
