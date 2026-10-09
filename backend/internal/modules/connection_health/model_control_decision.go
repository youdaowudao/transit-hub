package connection_health

type modelControlDecision struct {
	Decision, Reason string
	AccuracyPercent  *float64
}

func selectModelControlRounds(rounds []modelControlRound, rule ModelControlRule) (latest, previous *modelControlRound, reason string) {
	for n := range rounds {
		r := &rounds[n]
		if (r.Source == "manual" && !rule.IncludeManual) || (r.Source != "manual" && !rule.IncludeScheduled) {
			continue
		}
		if latest == nil {
			latest = r
			if !r.Running {
				return latest, nil, ""
			}
		} else if !r.Running {
			return latest, r, ""
		}
	}
	if latest == nil {
		reason = "no_participating_round"
	}
	return
}
func evaluateModelControlDecision(latest *modelControlRound, rule ModelControlRule) modelControlDecision {
	d := modelControlDecision{Decision: "no_evidence", Reason: "no_participating_round"}
	if latest == nil {
		return d
	}
	judged := latest.Correct + latest.Incorrect
	if judged > 0 {
		v := float64(latest.Correct) * 100 / float64(judged)
		d.AccuracyPercent = &v
	}
	switch {
	case latest.Running:
		d.Decision, d.Reason = "testing", "running"
	case latest.Unreviewed > 0:
		d.Decision, d.Reason = "awaiting_review", "unreviewed"
	case judged < rule.MinJudgedAnswers:
		d.Decision, d.Reason = "insufficient", "min_judged_answers"
	case latest.Correct*100 < rule.MinAccuracyPercent*judged:
		d.Decision, d.Reason = "close_recommended", "below_accuracy"
	default:
		d.Decision, d.Reason = "usable", "accuracy_met"
	}
	return d
}
