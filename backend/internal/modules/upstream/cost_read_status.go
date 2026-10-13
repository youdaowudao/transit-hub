package upstream

import (
	"errors"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

func withCostReadFailure(metrics Metrics, err error, failedAt time.Time) Metrics {
	metrics.TodayConsumeStatus = "unreadable"
	metrics.TodayConsumeErrorDetail = formatCostFailure("本站指定新加坡日总额读取", err)
	metrics.TodayConsumeErrorKey = siteErrorKey(err)
	metrics.TodayConsumeFailedAt = &failedAt
	var detail *RequestError
	if errors.As(err, &detail) {
		metrics.TodayConsumeUpstreamCode = detail.UpstreamCode
		metrics.TodayConsumeHTTPStatus = detail.StatusCode
	}
	return metrics
}

func mergeCostReadStatus(metrics, previous Metrics) Metrics {
	if metrics.TodayConsumeStatus == "unreadable" {
		metrics.TodayConsume = metric(nil)
		metrics.TodayConsumeAt = nil
		if UsableSingaporeTodayCost(previous, metrics.TodayConsumeDate) {
			metrics.TodayConsumeTimezone = previous.TodayConsumeTimezone
			metrics.TodayConsume = previous.TodayConsume
			metrics.TodayConsumeAt = previous.TodayConsumeAt
		}
	}
	return metrics
}

func maskUnverifiedTodayCost(metrics Metrics, date string) Metrics {
	if !UsableSingaporeTodayCost(metrics, date) {
		metrics.TodayConsume = metric(nil)
		if metrics.TodayConsumeTimezone != businesstime.Timezone {
			if metrics.TodayConsumeErrorDetail != "" {
				metrics.TodayConsumeErrorDetail = oldSingaporeCostMessage + " " + metrics.TodayConsumeErrorDetail
			} else {
				metrics.TodayConsumeErrorDetail = oldSingaporeCostMessage
			}
		}
		metrics.TodayConsumeStatus = "unreadable"
	}
	return metrics
}
