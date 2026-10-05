package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Every key/SCAN pattern receives one explicit acceptance namespace. The hook
// only isolates test keys; WATCH/MULTI/EXEC run against the real local Redis.
type keyUsageRedisNamespace struct {
	prefix       string
	mu           sync.Mutex
	touched      map[string]bool
	blockPayload string
	entered      chan struct{}
	release      chan struct{}
	blocked      bool
	releaseOnce  sync.Once
}

func (h *keyUsageRedisNamespace) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *keyUsageRedisNamespace) rewrite(cmd redis.Cmder) {
	args := cmd.Args()
	indexes := []int{}
	switch cmd.Name() {
	case "get", "set", "ttl", "pttl", "sadd", "srem", "smembers", "exists":
		indexes = []int{1}
	case "watch", "del":
		for i := 1; i < len(args); i++ {
			indexes = append(indexes, i)
		}
	case "scan":
		for i := 2; i+1 < len(args); i++ {
			if fmt.Sprint(args[i]) == "match" {
				indexes = append(indexes, i+1)
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, index := range indexes {
		key := fmt.Sprint(args[index])
		if !strings.HasPrefix(key, h.prefix) {
			key = h.prefix + key
		}
		args[index] = key
		if cmd.Name() != "scan" {
			h.touched[key] = true
		}
	}
}
func (h *keyUsageRedisNamespace) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { h.rewrite(cmd); return next(ctx, cmd) }
}
func (h *keyUsageRedisNamespace) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		pause := false
		for _, cmd := range cmds {
			h.rewrite(cmd)
			if cmd.Name() == "set" {
				h.mu.Lock()
				if !h.blocked && h.blockPayload != "" && keyUsagePayloadString(cmd.Args()[2]) == h.blockPayload {
					h.blocked = true
					pause = true
				}
				h.mu.Unlock()
			}
		}
		if pause {
			close(h.entered)
			select {
			case <-h.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, cmds)
	}
}

func homeCostLocalRedis(t *testing.T) (*RedisSiteCache, *redis.Client, *keyUsageRedisNamespace) {
	t.Helper()
	if os.Getenv("TRANSITHUB_KEY_USAGE_REDIS_TEST") != "1" {
		t.Skip("local Redis acceptance requires explicit opt-in")
	}
	hook := &keyUsageRedisNamespace{prefix: fmt.Sprintf("验收-home-cost-%d:", time.Now().UnixNano()), touched: map[string]bool{}, entered: make(chan struct{}), release: make(chan struct{})}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379", DB: 0, PoolSize: 4})
	client.AddHook(hook)
	t.Cleanup(hook.unblock)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("approved local Redis unavailable: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		hook.mu.Lock()
		keys := make([]string, 0, len(hook.touched))
		for key := range hook.touched {
			keys = append(keys, key)
		}
		hook.mu.Unlock()
		if len(keys) > 0 {
			if err := client.Del(cleanupCtx, keys...).Err(); err != nil {
				t.Errorf("exact-ID Redis cleanup failed: %v", err)
			}
			for _, key := range keys {
				if n, err := client.Exists(cleanupCtx, key).Result(); err != nil || n != 0 {
					t.Errorf("acceptance key remains: count=%d error=%v", n, err)
				}
			}
		}
		if err := client.Close(); err != nil {
			t.Errorf("Redis client cleanup: %v", err)
		}
	})
	return NewRedisSiteCache(client), client, hook
}

func TestHomeCostStageARedisTTLFlushIsolationAndFailureRetention(t *testing.T) {
	cache, client, _ := homeCostLocalRedis(t)
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	snapshot := fixtureKeySnapshot(now, 0)
	snapshot.Items = []KeyUsageTodayStat{}
	if err := saveHomeCostRedisSnapshot(cache, t.Context(), "验收-site", snapshot); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.TTL(t.Context(), keyUsageKeyPrefix+"验收-site").Result()
	if err != nil || ttl < 48*time.Hour-time.Second || ttl > 48*time.Hour {
		t.Fatalf("snapshot TTL=%s err=%v", ttl, err)
	}
	site := newTestSite("验收-site", "验收-user", "workspace", 1, nil)
	if err := cache.Set(t.Context(), site); err != nil {
		t.Fatal(err)
	}
	if err := cache.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	site, err = cache.Get(t.Context(), "验收-site")
	if err != nil || site != nil {
		t.Fatalf("site startup cache not cleared %v %v", site, err)
	}
	kept, err := readHomeCostRedisSnapshot(cache, t.Context(), "验收-site")
	if err != nil || kept == nil || !kept.Complete {
		t.Fatalf("startup Flush deleted durable key snapshot %v %v", kept, err)
	}
	failure := fixtureKeySnapshot(now.Add(time.Minute), 0)
	failure.Complete = false
	failure.FailureReason = ErrorRateLimited
	failedAt := now.Add(2 * time.Minute)
	failure.FailureAt = &failedAt
	if err := saveHomeCostRedisSnapshot(cache, t.Context(), "验收-site", failure); err != nil {
		t.Fatal(err)
	}
	kept, err = readHomeCostRedisSnapshot(cache, t.Context(), "验收-site")
	if err != nil || kept == nil || !kept.Complete || len(kept.Items) != 0 || kept.FailureAt == nil || kept.StartedAt != snapshot.StartedAt {
		t.Fatalf("failed zero-Key snapshot lost original JSON/value %v %v", kept, err)
	}
	if err := deleteHomeCostRedisSnapshot(cache, t.Context(), "验收-site"); err != nil {
		t.Fatal(err)
	}
	kept, err = readHomeCostRedisSnapshot(cache, t.Context(), "验收-site")
	if err != nil || kept != nil {
		t.Fatalf("snapshot deletion incomplete %v %v", kept, err)
	}
}

func TestHomeCostStageARedisConcurrentOlderAttemptCannotOverwriteNewer(t *testing.T) {
	cache, _, hook := homeCostLocalRedis(t)
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := saveHomeCostRedisSnapshot(cache, t.Context(), "验收-cas", fixtureKeySnapshot(now.Add(-time.Minute), 1)); err != nil {
		t.Fatal(err)
	}
	older := fixtureKeySnapshot(now, 2)
	payload, _ := json.Marshal(older)
	hook.mu.Lock()
	hook.blockPayload = string(payload)
	hook.mu.Unlock()
	completed := make(chan error, 1)
	go func() { completed <- saveHomeCostRedisSnapshot(cache, t.Context(), "验收-cas", older) }()
	select {
	case err := <-completed:
		kept, readErr := readHomeCostRedisSnapshot(cache, t.Context(), "验收-cas")
		if err != nil || readErr != nil || kept == nil || kept.Items[0].TodayAmount != 2 {
			t.Fatalf("started collection was not persisted: snapshot=%v errors=%v/%v", kept, err, readErr)
		}
		t.Fatal("transaction never exercised WATCH barrier")
	case <-hook.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("older WATCH transaction did not reach SET barrier")
	}
	newer := fixtureKeySnapshot(now.Add(time.Second), 9)
	if err := saveHomeCostRedisSnapshot(cache, t.Context(), "验收-cas", newer); err != nil {
		hook.unblock()
		t.Fatal(err)
	}
	hook.unblock()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("older transaction not reclaimed")
	}
	kept, err := readHomeCostRedisSnapshot(cache, t.Context(), "验收-cas")
	if err != nil || kept == nil || kept.Items[0].TodayAmount != 9 || kept.StartedAt != newer.StartedAt {
		t.Fatalf("older transaction overwrote later snapshot %v %v", kept, err)
	}
}

func keyUsagePayloadString(value any) string {
	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return fmt.Sprint(value)
}
func (h *keyUsageRedisNamespace) unblock() { h.releaseOnce.Do(func() { close(h.release) }) }

// A baseline RedisSiteCache has no snapshot writer. Compatibility wrappers keep
// baseline tests executable and let the TTL/current-cost business assertions fail.
func saveHomeCostRedisSnapshot(cache *RedisSiteCache, ctx context.Context, id string, value KeyUsageSnapshot) error {
	if store, ok := any(cache).(KeyUsageSnapshotStore); ok {
		return store.SaveKeyUsageSnapshot(ctx, id, value)
	}
	return nil
}
func readHomeCostRedisSnapshot(cache *RedisSiteCache, ctx context.Context, id string) (*KeyUsageSnapshot, error) {
	if store, ok := any(cache).(KeyUsageSnapshotStore); ok {
		return store.GetKeyUsageSnapshot(ctx, id)
	}
	return nil, nil
}
func deleteHomeCostRedisSnapshot(cache *RedisSiteCache, ctx context.Context, id string) error {
	if store, ok := any(cache).(KeyUsageSnapshotStore); ok {
		return store.DeleteKeyUsageSnapshot(ctx, id)
	}
	return nil
}
