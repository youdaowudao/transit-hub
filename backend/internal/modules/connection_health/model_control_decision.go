package connection_health

import (
	"math/big"
	"sort"
	"strings"
	"time"
)

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
func modelControlQuestionAccuracy(questions []ModelControlQuestionResult) *big.Rat {
	if len(questions) == 0 {
		return nil
	}
	total := new(big.Rat)
	for _, q := range questions {
		n := q.Correct + q.Incorrect
		if n <= 0 {
			return nil
		}
		total.Add(total, new(big.Rat).SetFrac64(int64(q.Correct), int64(n)))
	}
	return total.Quo(total, new(big.Rat).SetInt64(int64(len(questions))))
}
func buildModelControlQuestionQuality(questions []ModelControlQuestionResult) *float64 {
	for i := range questions {
		q := &questions[i]
		q.AccuracyPercent = nil
		if n := q.Correct + q.Incorrect; n > 0 {
			value, _ := new(big.Rat).Mul(new(big.Rat).SetFrac64(int64(q.Correct), int64(n)), big.NewRat(100, 1)).Float64()
			q.AccuracyPercent = &value
		}
	}
	score := modelControlQuestionAccuracy(questions)
	if score == nil {
		return nil
	}
	value, _ := new(big.Rat).Mul(score, big.NewRat(100, 1)).Float64()
	return &value
}
func compareModelControlQuestionAccuracy(questions []ModelControlQuestionResult, threshold int) (int, bool) {
	score := modelControlQuestionAccuracy(questions)
	if score == nil {
		return 0, false
	}
	return score.Cmp(big.NewRat(int64(threshold), 100)), true
}
func prepareModelControlRoundQuality(round *modelControlRound) {
	if round.Questions == nil {
		round.Questions = []ModelControlQuestionResult{}
	}
	round.AccuracyPercent = buildModelControlQuestionQuality(round.Questions)
	bodies := map[string][]string{}
	for _, q := range round.Questions {
		body := strings.TrimSpace(strings.ReplaceAll(q.QuestionBody, "\r\n", "\n"))
		bodies[body] = append(bodies[body], q.QuestionID)
	}
	round.DuplicateQuestionIDs = []string{}
	for _, ids := range bodies {
		if len(ids) > 1 {
			round.DuplicateQuestionIDs = append(round.DuplicateQuestionIDs, ids...)
		}
	}
	sort.Strings(round.DuplicateQuestionIDs)
}
func evaluateModelControlDecision(latest *modelControlRound, rule ModelControlRule) modelControlDecision {
	d := modelControlDecision{Decision: "no_evidence", Reason: "no_participating_round"}
	if latest == nil {
		return d
	}
	prepareModelControlRoundQuality(latest)
	d.AccuracyPercent = latest.AccuracyPercent
	sufficient := len(latest.Questions) > 0 && rule.MinJudgedAnswers <= 10
	for _, q := range latest.Questions {
		if q.Correct+q.Incorrect < rule.MinJudgedAnswers {
			sufficient = false
		}
	}
	comparison, calculable := compareModelControlQuestionAccuracy(latest.Questions, rule.MinAccuracyPercent)
	switch {
	case latest.Running:
		d.Decision, d.Reason = "testing", "running"
	case latest.Unreviewed > 0:
		d.Decision, d.Reason = "awaiting_review", "unreviewed"
	case !sufficient || !calculable:
		d.Decision, d.Reason = "insufficient", "min_judged_answers"
	case comparison < 0:
		d.Decision, d.Reason = "close_recommended", "below_accuracy"
	default:
		d.Decision, d.Reason = "usable", "accuracy_met"
	}
	return d
}

func modelControlRoundIsToday(round *modelControlRound, now time.Time) bool {
	if round == nil {
		return false
	}
	ry, rm, rd := round.CreatedAt.In(questionAnswerScheduleLocation).Date()
	ny, nm, nd := now.In(questionAnswerScheduleLocation).Date()
	return ry == ny && rm == nm && rd == nd
}
