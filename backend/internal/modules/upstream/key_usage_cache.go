package upstream

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyUsageKeyPrefix = "upstream:key-usage:"
const keyUsageSnapshotTTL = 48 * time.Hour

// KeyUsageSnapshot stores raw amounts. Rates are applied by each read using current settings.
type KeyUsageSnapshot struct {
	BusinessDate     string
	StartedAt        time.Time
	CollectedAt      time.Time
	AttemptStartedAt time.Time
	SyncedRawCost    *float64
	CollectedRawCost *float64
	ConsumeDate      string
	Complete         bool
	FailureReason    string
	FailureAt        *time.Time
	Items            []KeyUsageTodayStat
}

type KeyUsageSnapshotStore interface {
	GetKeyUsageSnapshot(context.Context, string) (*KeyUsageSnapshot, error)
	SaveKeyUsageSnapshot(context.Context, string, KeyUsageSnapshot) error
	DeleteKeyUsageSnapshot(context.Context, string) error
}

func (c *RedisSiteCache) GetKeyUsageSnapshot(ctx context.Context, id string) (*KeyUsageSnapshot, error) {
	raw, err := c.client.Get(ctx, keyUsageKeyPrefix+id).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value KeyUsageSnapshot
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

// Each write compares its attempt start in the same Redis transaction as SET.
// Keep merging in Go so empty Key lists remain JSON arrays when preserving a value.
func newerKeyUsageSnapshot(previous *KeyUsageSnapshot, value KeyUsageSnapshot) (KeyUsageSnapshot, bool) {
	if value.AttemptStartedAt.IsZero() {
		value.AttemptStartedAt = value.StartedAt
	}
	if previous != nil {
		previousStarted := previous.AttemptStartedAt
		if previousStarted.IsZero() {
			previousStarted = previous.StartedAt
		}
		if !value.AttemptStartedAt.After(previousStarted) {
			return KeyUsageSnapshot{}, false
		}
		if !value.Complete && previous.Complete && previous.BusinessDate == value.BusinessDate {
			retained := *previous
			retained.FailureReason = value.FailureReason
			retained.FailureAt = value.FailureAt
			retained.AttemptStartedAt = value.AttemptStartedAt
			return retained, true
		}
	}
	return value, true
}

func (c *RedisSiteCache) SaveKeyUsageSnapshot(ctx context.Context, id string, value KeyUsageSnapshot) error {
	key := keyUsageKeyPrefix + id
	var finalErr error
	for attempt := 0; attempt < 5; attempt++ {
		finalErr = c.client.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Bytes()
			var previous *KeyUsageSnapshot
			if err != nil && err != redis.Nil {
				return err
			}
			if err == nil {
				previous = &KeyUsageSnapshot{}
				if err = json.Unmarshal(raw, previous); err != nil {
					return err
				}
			}
			next, write := newerKeyUsageSnapshot(previous, value)
			if !write {
				return nil
			}
			payload, err := json.Marshal(next)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error { pipe.Set(ctx, key, payload, keyUsageSnapshotTTL); return nil })
			return err
		}, key)
		if finalErr != redis.TxFailedErr {
			return finalErr
		}
	}
	return finalErr
}

func (c *RedisSiteCache) DeleteKeyUsageSnapshot(ctx context.Context, id string) error {
	return c.client.Del(ctx, keyUsageKeyPrefix+id).Err()
}
