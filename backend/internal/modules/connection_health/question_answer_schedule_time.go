package connection_health

import (
	"sort"
	"time"
)

var questionAnswerScheduleLocation = time.FixedZone("Asia/Singapore", 8*60*60)

func questionAnswerScheduleMinute(value string) (int, error) {
	t, err := time.Parse("15:04", value)
	if err != nil || t.Format("15:04") != value {
		return 0, requestError(ErrorRequest)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func validateQuestionAnswerScheduleTime(config QuestionAnswerScheduleConfig) error {
	start, err := questionAnswerScheduleMinute(config.PeakStart)
	if err != nil {
		return err
	}
	end, err := questionAnswerScheduleMinute(config.PeakEnd)
	if err != nil {
		return err
	}
	if start == end {
		return requestError(ErrorRequest)
	}
	for _, interval := range []int{config.PeakIntervalMinutes, config.OffPeakIntervalMinutes} {
		if interval < 30 || interval > 1440 || interval%30 != 0 {
			return requestError(ErrorRequest)
		}
	}
	return nil
}

// Every day begins a new peak grid at peakStart and a new off-peak grid at
// peakEnd. The endpoint belongs to the next grid even when its interval differs.
func questionAnswerScheduleDaySlots(config QuestionAnswerScheduleConfig, day time.Time) []time.Time {
	start, _ := questionAnswerScheduleMinute(config.PeakStart)
	end, _ := questionAnswerScheduleMinute(config.PeakEnd)
	day = day.In(questionAnswerScheduleLocation)
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, questionAnswerScheduleLocation)
	peak := midnight.Add(time.Duration(start) * time.Minute)
	peakEnd := midnight.Add(time.Duration(end) * time.Minute)
	if !peakEnd.After(peak) {
		peakEnd = peakEnd.AddDate(0, 0, 1)
	}
	nextPeak := peak.AddDate(0, 0, 1)
	slots := make([]time.Time, 0, 50)
	for at := peak; at.Before(peakEnd); at = at.Add(time.Duration(config.PeakIntervalMinutes) * time.Minute) {
		slots = append(slots, at.UTC())
	}
	for at := peakEnd; at.Before(nextPeak); at = at.Add(time.Duration(config.OffPeakIntervalMinutes) * time.Minute) {
		slots = append(slots, at.UTC())
	}
	return slots
}

func nextQuestionAnswerScheduleSlot(config QuestionAnswerScheduleConfig, after time.Time, strict bool) (time.Time, error) {
	if err := validateQuestionAnswerScheduleTime(config); err != nil {
		return time.Time{}, err
	}
	candidates := []time.Time{}
	for day := -1; day <= 2; day++ {
		candidates = append(candidates, questionAnswerScheduleDaySlots(config, after.In(questionAnswerScheduleLocation).AddDate(0, 0, day))...)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	for _, at := range candidates {
		if at.After(after) || (!strict && at.Equal(after)) {
			return at, nil
		}
	}
	return time.Time{}, requestError(ErrorRequest)
}

func questionAnswerScheduleSlotsBetween(config QuestionAnswerScheduleConfig, from, through time.Time) ([]time.Time, error) {
	if err := validateQuestionAnswerScheduleTime(config); err != nil {
		return nil, err
	}
	slots := []time.Time{}
	if through.Before(from) {
		return slots, nil
	}
	at, err := nextQuestionAnswerScheduleSlot(config, from, false)
	if err != nil {
		return nil, err
	}
	for !at.After(through) {
		slots = append(slots, at)
		at, err = nextQuestionAnswerScheduleSlot(config, at, true)
		if err != nil {
			return nil, err
		}
	}
	return slots, nil
}

// Pending records and unbuilt targets are still work. Only succeeded records
// count as usable answers; incorrect and unreviewed answers remain usable.
func summarizeQuestionAnswerScheduleExecution(execution QuestionAnswerScheduleExecution, targets []QuestionAnswerScheduleExecutionTarget) (string, string) {
	usable, batches, bad, active := 0, 0, false, false
	for _, target := range targets {
		if target.BatchAvailable {
			batches++
			usable += target.Stats.Requests.Succeeded
			if target.Stats.Requests.InProgress > 0 {
				active = true
			}
			if target.Stats.Requests.Failed > 0 || target.Stats.Requests.Cancelled > 0 {
				bad = true
			}
		} else if target.Status == "pending" || target.Status == "starting" || target.Status == "batch_created" {
			active = true
		}
		if target.Status == "failed" || target.Status == "skipped" || target.Status == "cancelled" || len(target.UnavailableModels) > 0 {
			bad = true
		}
	}
	if active {
		return "active", ""
	}
	if execution.TerminationCause == "user_cancel" {
		return "cancelled", "user_cancel"
	}
	if execution.TerminationCause == "execution_timeout" {
		if batches == 0 {
			return "skipped", "execution_timeout_before_start"
		}
		if usable > 0 {
			return "partial", "execution_timeout"
		}
		return "failed", "execution_timeout"
	}
	if execution.Status == "skipped" && execution.StatusReason == "all_accounts_missing" {
		return "skipped", "all_accounts_missing"
	}
	if execution.ConfigSnapshot.Resolved == nil {
		return "active", ""
	}
	if len(execution.ConfigSnapshot.Resolved.MissingTargetIDs) > 0 && len(execution.ConfigSnapshot.Resolved.MissingTargetIDs) == len(targets) && batches == 0 {
		return "skipped", "all_accounts_missing"
	}
	if len(targets) == 0 {
		return "skipped", "empty_scope"
	}
	if usable == 0 {
		return "failed", "no_usable_results"
	}
	if bad {
		return "partial", "partial_results"
	}
	return "completed", ""
}

func questionAnswerScheduleHardLimitReason(config QuestionAnswerScheduleConfig, limits QuestionAnswerScheduleLimits) string {
	requests := len(config.Models) * len(config.QuestionIDs) * config.RepeatCount
	if requests > QuestionAnswerBatchRecordLimit {
		return "batch_request_limit"
	}
	if config.TargetMode == "accounts" {
		if len(config.SelectedAccountTargetIDs) > limits.MaxScheduleTargets {
			return "target_limit"
		}
		if len(config.SelectedAccountTargetIDs)*requests > limits.MaxScheduleRequestsPerExecution {
			return "execution_request_limit"
		}
	}
	return ""
}

func questionAnswerScheduleLocalLimitReason(reason string) bool {
	return reason == "batch_request_limit" || reason == "target_limit" || reason == "execution_request_limit"
}
