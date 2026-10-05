package upstream

import (
	"errors"
	"time"
)

func withCostReadFailure(metrics Metrics, err error, failedAt time.Time) Metrics {
	metrics.TodayConsumeStatus = "unreadable"
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
		if previous.TodayConsumeDate == metrics.TodayConsumeDate {
			metrics.TodayConsume = previous.TodayConsume
			metrics.TodayConsumeAt = previous.TodayConsumeAt
		}
	}
	return metrics
}
