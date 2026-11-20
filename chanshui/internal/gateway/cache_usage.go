package gateway

import (
	"encoding/json"
	"math"
)

// CacheCountLimit keeps verified evidence exact in both Go and browser JSON.
const CacheCountLimit = min(math.MaxInt, 1<<53-1)

type CacheCountState uint8

const (
	CacheCountMissing CacheCountState = iota
	CacheCountInvalid
	CacheCountValid
)

type CacheCount struct {
	Value int
	State CacheCountState
}

// CacheUsage is per-attempt evidence for Hub cache checks. Input includes
// cache for compatible protocols and excludes it for Messages.
type CacheUsage struct {
	Read, Write, Input CacheCount
}

func AddCacheCounts(left, right int) (int, bool) {
	if left < 0 || right < 0 || right > CacheCountLimit || left > CacheCountLimit-right {
		return 0, false
	}
	return left + right, true
}

func cacheCount(raw json.RawMessage) CacheCount {
	if raw == nil {
		return CacheCount{}
	}
	var n *int
	if err := json.Unmarshal(raw, &n); err != nil || n == nil || *n < 0 || *n > CacheCountLimit {
		return CacheCount{State: CacheCountInvalid}
	}
	return CacheCount{Value: *n, State: CacheCountValid}
}

func firstCacheCount(fields ...json.RawMessage) CacheCount {
	for _, raw := range fields {
		if raw != nil {
			return cacheCount(raw)
		}
	}
	return CacheCount{}
}

func cacheCreationCount(aggregate CacheCount, raw json.RawMessage) CacheCount {
	if raw == nil {
		return aggregate
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return CacheCount{State: CacheCountInvalid}
	}
	five := cacheCount(fields["ephemeral_5m_input_tokens"])
	hour := cacheCount(fields["ephemeral_1h_input_tokens"])
	if aggregate.State == CacheCountInvalid || five.State == CacheCountInvalid || hour.State == CacheCountInvalid {
		return CacheCount{State: CacheCountInvalid}
	}
	if five.State == CacheCountMissing && hour.State == CacheCountMissing {
		return aggregate
	}
	sum, ok := AddCacheCounts(five.Value, hour.Value)
	if !ok {
		return CacheCount{State: CacheCountInvalid}
	}
	return CacheCount{Value: max(aggregate.Value, sum), State: CacheCountValid}
}

func compatibleCacheUsage(usage, inputDetails, promptDetails map[string]json.RawMessage) CacheUsage {
	read := firstCacheCount(inputDetails["cached_tokens"], promptDetails["cached_tokens"],
		usage["cache_read_input_tokens"], usage["cache_read_tokens"], usage["cached_tokens"], usage["prompt_cache_hit_tokens"])
	write := firstCacheCount(inputDetails["cache_write_tokens"], promptDetails["cache_write_tokens"],
		inputDetails["cache_creation_tokens"], promptDetails["cache_creation_tokens"])
	if write.State == CacheCountMissing {
		for _, key := range [...]string{"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens"} {
			next := cacheCount(usage[key])
			if next.State == CacheCountValid && next.Value > 0 {
				write = next
				break
			}
			if write.State == CacheCountMissing {
				write = next
			}
		}
		write = cacheCreationCount(write, usage["cache_creation"])
		if write.State == CacheCountMissing {
			write = cacheCount(usage["prompt_cache_miss_tokens"])
		}
	}
	return CacheUsage{Read: read, Write: write, Input: firstCacheCount(usage["prompt_tokens"], usage["input_tokens"])}
}

func messagesCacheUsage(usage map[string]json.RawMessage) CacheUsage {
	return CacheUsage{
		Read:  cacheCount(usage["cache_read_input_tokens"]),
		Write: cacheCreationCount(cacheCount(usage["cache_creation_input_tokens"]), usage["cache_creation"]),
		Input: cacheCount(usage["input_tokens"]),
	}
}

func (usage *CacheUsage) merge(next CacheUsage, messageDelta bool) {
	placeholder := messageDelta && usage.Read.State == CacheCountValid && usage.Read.Value > 0 &&
		next.Read.State == CacheCountValid && next.Read.Value == 0
	if next.Read.State != CacheCountMissing && !placeholder {
		usage.Read = next.Read
	}
	if next.Write.State != CacheCountMissing && (!placeholder || next.Write.State == CacheCountInvalid) {
		usage.Write = next.Write
	}
	if next.Input.State != CacheCountMissing {
		usage.Input = next.Input
	}
}
