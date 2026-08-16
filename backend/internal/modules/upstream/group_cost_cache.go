package upstream

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	groupCostKeyPrefix         = "upstream:group-cost:"
	groupCostSampleKeyPrefix   = groupCostKeyPrefix + "samples:"
	groupCostSamplingKeyPrefix = groupCostKeyPrefix + "sampling:"
	groupCostStateKeyPrefix    = groupCostKeyPrefix + "state:"
)

func groupCostKeyPart(value string) string {
	return hex.EncodeToString([]byte(value))
}

func groupCostSampleKey(siteID, groupKey, date string) string {
	return groupCostSampleKeyPrefix + groupCostKeyPart(siteID) + ":" + groupCostKeyPart(groupKey) + ":" + date
}

func groupCostSamplingKey(siteID string) string {
	return groupCostSamplingKeyPrefix + groupCostKeyPart(siteID)
}

func groupCostStateKey(siteID string) string {
	return groupCostStateKeyPrefix + groupCostKeyPart(siteID)
}

func (c *RedisSiteCache) TryStartGroupCostSampling(ctx context.Context, siteID string, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, groupCostSamplingKey(siteID), "1", ttl).Result()
}

func (c *RedisSiteCache) GetGroupCostSamplingState(ctx context.Context, siteID string) (GroupCostSamplingState, error) {
	payload, err := c.client.Get(ctx, groupCostStateKey(siteID)).Bytes()
	if err == redis.Nil {
		return GroupCostSamplingState{}, nil
	}
	if err != nil {
		return GroupCostSamplingState{}, err
	}
	var state GroupCostSamplingState
	if err := json.Unmarshal(payload, &state); err != nil {
		return GroupCostSamplingState{}, err
	}
	return state, nil
}

func (c *RedisSiteCache) SetGroupCostSamplingState(ctx context.Context, siteID string, state GroupCostSamplingState, ttl time.Duration) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, groupCostStateKey(siteID), payload, ttl).Err()
}

func (c *RedisSiteCache) AppendGroupCostSamples(
	ctx context.Context,
	siteID string,
	groupKey string,
	date string,
	samples []GroupCostSample,
	maxSamples int,
	ttl time.Duration,
) error {
	if len(samples) == 0 || maxSamples <= 0 {
		return nil
	}
	key := groupCostSampleKey(siteID, groupKey, date)
	values := make([]any, 0, len(samples))
	for _, sample := range samples {
		payload, err := marshalGroupCostSample(sample)
		if err != nil {
			return err
		}
		values = append(values, payload)
	}
	pipe := c.client.TxPipeline()
	pipe.RPush(ctx, key, values...)
	pipe.LTrim(ctx, key, int64(-maxSamples), -1)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *RedisSiteCache) ListGroupCostSamples(ctx context.Context, siteID, groupKey, date string) ([]GroupCostSample, error) {
	values, err := c.client.LRange(ctx, groupCostSampleKey(siteID, groupKey, date), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	result := make([]GroupCostSample, 0, len(values))
	for _, value := range values {
		var sample GroupCostSample
		if err := json.Unmarshal([]byte(value), &sample); err != nil {
			return nil, err
		}
		result = append(result, sample)
	}
	return result, nil
}

func (c *RedisSiteCache) DeleteGroupCostSamples(ctx context.Context, siteID string) error {
	pattern := groupCostSampleKeyPrefix + groupCostKeyPart(siteID) + ":*"
	if err := deleteRedisKeysByPattern(ctx, c.client, pattern); err != nil {
		return err
	}
	return c.client.Del(ctx, groupCostSamplingKey(siteID), groupCostStateKey(siteID)).Err()
}

func (c *RedisSiteCache) ClearGroupCostSamples(ctx context.Context) error {
	return deleteRedisKeysByPattern(ctx, c.client, groupCostKeyPrefix+"*")
}

func deleteRedisKeysByPattern(ctx context.Context, client *redis.Client, pattern string) error {
	iter := client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		if err := client.Del(ctx, iter.Val()).Err(); err != nil {
			return err
		}
	}
	return iter.Err()
}
