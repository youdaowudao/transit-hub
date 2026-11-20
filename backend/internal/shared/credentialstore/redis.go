package credentialstore

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// ReplaceRedisIfUnchanged preserves the original expiration and never recreates a removed key.
func ReplaceRedisIfUnchanged(ctx context.Context, client *redis.Client, key, original, replacement string) error {
	result, err := client.Eval(ctx, `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local ttl = redis.call('PTTL', KEYS[1])
redis.call('SET', KEYS[1], ARGV[2])
if ttl >= 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return 1`, []string{key}, original, replacement).Int()
	if err != nil {
		return errors.New("转换 Redis 管理凭据失败：无法确认条件保存结果；请检查 Redis 连接并停止其他旧版本写入实例后重试，已有内容不会被无条件覆盖")
	}
	if result != 1 {
		return errors.New("转换 Redis 管理凭据时内容已被其他请求改动或删除，本次未覆盖；请停止其他写入实例后重试")
	}
	return nil
}
