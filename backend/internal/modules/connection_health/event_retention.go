package connection_health

import (
	"context"
	"log"
	"time"
)

const (
	eventRetentionWindow     = 24 * time.Hour
	eventRetentionBatchSize  = 1000
	eventRetentionMaxBatches = 20
)

var eventRetentionLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return location
}()

var eventRetentionRunHours = []int{3, 15}

type eventRetentionRepository interface {
	TryAcquireEventRetentionLease(ctx context.Context) (release func(), acquired bool, err error)
	EnsureEventRetentionIndex(ctx context.Context) error
	DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

func (s *Service) startEventRetention(ctx context.Context) {
	if s.eventRetention == nil {
		return
	}
	go func() {
		for {
			nextRun := nextEventRetentionRun(time.Now())
			timer := time.NewTimer(time.Until(nextRun))
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
				if ctx.Err() != nil {
					return
				}
				s.runEventRetentionSafely(ctx)
			}
		}
	}()
}

// nextEventRetentionRun intentionally returns a point strictly after now. A process that
// starts at a fixed time does not treat that as a missed run and waits for the following one.
func nextEventRetentionRun(now time.Time) time.Time {
	localNow := now.In(eventRetentionLocation)
	for _, hour := range eventRetentionRunHours {
		candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, 0, 0, 0, eventRetentionLocation)
		if candidate.After(localNow) {
			return candidate
		}
	}
	return time.Date(localNow.Year(), localNow.Month(), localNow.Day()+1, eventRetentionRunHours[0], 0, 0, 0, eventRetentionLocation)
}

func (s *Service) runEventRetentionSafely(ctx context.Context) {
	defer func() {
		if recover() != nil {
			log.Printf("[connection-health] event retention panic recovered")
		}
	}()
	s.runEventRetentionAt(ctx, time.Now().UTC())
}

func (s *Service) runEventRetentionAt(ctx context.Context, now time.Time) {
	if s.eventRetention == nil {
		return
	}
	release, acquired, err := s.eventRetention.TryAcquireEventRetentionLease(ctx)
	if err != nil {
		log.Printf("[connection-health] acquire event retention lease failed: %v", err)
		return
	}
	if !acquired {
		return
	}
	defer release()

	if err := s.eventRetention.EnsureEventRetentionIndex(ctx); err != nil {
		log.Printf("[connection-health] ensure event retention index failed: %v", err)
		return
	}

	cutoff := now.UTC().Add(-eventRetentionWindow)
	var totalDeleted int64
	for batch := 0; batch < eventRetentionMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return
		}
		deleted, err := s.eventRetention.DeleteEventsBefore(ctx, cutoff, eventRetentionBatchSize)
		if err != nil {
			log.Printf("[connection-health] delete expired events failed cutoff=%s deleted=%d err=%v", cutoff.Format(time.RFC3339), totalDeleted, err)
			return
		}
		totalDeleted += deleted
		if deleted < eventRetentionBatchSize {
			break
		}
	}
	if totalDeleted > 0 {
		log.Printf("[connection-health] expired events deleted cutoff=%s count=%d", cutoff.Format(time.RFC3339), totalDeleted)
	}
}
