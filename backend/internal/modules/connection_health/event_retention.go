package connection_health

import (
	"context"
	"log"
	"time"
)

const (
	eventRetentionWindow     = 24 * time.Hour
	eventRetentionInterval   = 5 * time.Minute
	eventRetentionBatchSize  = 1000
	eventRetentionMaxBatches = 20
)

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
		s.runEventRetentionSafely(ctx)
		ticker := time.NewTicker(eventRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runEventRetentionSafely(ctx)
			}
		}
	}()
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
