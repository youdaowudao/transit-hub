package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"transithub/backend/internal/shared/credentialstore"
	"transithub/backend/internal/shared/secretbox"

	"github.com/redis/go-redis/v9"
)

// Redis key 约定：
//   - sessionKeyPrefix + userID + ":" + adminAccountID 存储单个 workspace 的 admin 会话 JSON。
//   - activeSetKey 维护当前持有会话的 userID:adminAccountID 集合，供后台刷新协程高效遍历，
//     避免对整个 keyspace 做 SCAN。
const (
	sessionKeyPrefix = "dashboard:admin:session:"
	activeSetKey     = "dashboard:admin:active"
)

// Repository 封装仪表盘 admin 会话在 Redis 中的读写。
type Repository struct {
	client *redis.Client
	box    *secretbox.Box
}

type ActiveSessionRef struct {
	UserID         string
	AdminAccountID string
}

func NewRepository(client *redis.Client, boxes ...*secretbox.Box) *Repository {
	return &Repository{client: client, box: secretbox.Optional(boxes)}
}

func sessionKey(userID string, adminAccountID string) string {
	return sessionKeyPrefix + userID + ":" + adminAccountID
}

func activeMember(userID string, adminAccountID string) string {
	return userID + ":" + adminAccountID
}

// Get 读取用户的 admin 会话，不存在时返回 (nil, nil)。
func (r *Repository) Get(ctx context.Context, userID string, adminAccountID string) (*AdminSession, error) {
	raw, err := r.client.Get(ctx, sessionKey(userID, adminAccountID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := r.box.Open(raw, secretbox.AAD(sessionKey(userID, adminAccountID), userID, adminAccountID))
	if err != nil {
		return nil, secretbox.At("Redis 主站会话", userID, adminAccountID, "", err)
	}
	var session AdminSession
	if err := json.Unmarshal(plain, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Save 写入会话并把 userID 登记到活跃集合。
// 会话长期有效（依赖后台刷新维持令牌），因此不设置 TTL。
func (r *Repository) Save(ctx context.Context, userID string, adminAccountID string, session AdminSession) error {
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	stored, err := r.box.Seal(payload, secretbox.AAD(sessionKey(userID, adminAccountID), userID, adminAccountID))
	if err != nil {
		return secretbox.At("Redis 主站会话", userID, adminAccountID, "", err)
	}
	pipe := r.client.TxPipeline()
	pipe.Set(ctx, sessionKey(userID, adminAccountID), stored, 0)
	pipe.SAdd(ctx, activeSetKey, activeMember(userID, adminAccountID))
	_, err = pipe.Exec(ctx)
	return err
}

// Delete 删除会话并从活跃集合移除，用于退出当前 admin 账户。
func (r *Repository) Delete(ctx context.Context, userID string, adminAccountID string) error {
	pipe := r.client.TxPipeline()
	pipe.Del(ctx, sessionKey(userID, adminAccountID))
	pipe.SRem(ctx, activeSetKey, activeMember(userID, adminAccountID))
	_, err := pipe.Exec(ctx)
	return err
}

// ActiveSessions 返回当前持有会话的所有 userID/adminAccountID，供后台刷新协程遍历。
func (r *Repository) ActiveSessions(ctx context.Context) ([]ActiveSessionRef, error) {
	members, err := r.client.SMembers(ctx, activeSetKey).Result()
	if err != nil {
		return nil, err
	}
	refs := make([]ActiveSessionRef, 0, len(members))
	for _, member := range members {
		parts := strings.SplitN(member, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		refs = append(refs, ActiveSessionRef{UserID: parts[0], AdminAccountID: parts[1]})
	}
	return refs, nil
}

// VerifyCredentials uses only the existing active-session index, including legacy members.
// It does not change membership, values or expiration.
func (r *Repository) VerifyCredentials(ctx context.Context) error {
	return r.visitCredentials(ctx, false)
}
func (r *Repository) EncryptCredentials(ctx context.Context) error {
	return r.visitCredentials(ctx, true)
}
func (r *Repository) visitCredentials(ctx context.Context, convert bool) error {
	if r == nil || r.client == nil {
		return nil
	}
	var cursor uint64
	for {
		members, next, err := r.client.SScan(ctx, activeSetKey, cursor, "*", 100).Result()
		if err != nil {
			return errors.New("核验 Redis 主站会话失败：无法读取原会话索引；请检查 Redis 连接后重试，尚未恢复业务服务")
		}
		for _, member := range members {
			parts := strings.SplitN(member, ":", 2)
			user, workspace := parts[0], ""
			if len(parts) == 2 {
				workspace = parts[1]
			}
			key := sessionKeyPrefix + member
			raw, err := r.client.Get(ctx, key).Result()
			if errors.Is(err, redis.Nil) {
				continue
			}
			if err != nil {
				return secretbox.At("Redis 主站会话", user, workspace, "", errors.New("无法读取已登记会话；请检查 Redis 连接后重试"))
			}
			if secretbox.IsSealed(raw) && (user == "" || workspace == "") {
				return secretbox.At("Redis 主站会话", user, workspace, "", errors.New("已加密会话缺少归属，不能自动重新分配；请恢复原归属和对应密钥后重试"))
			}
			plain, err := r.box.Open(raw, secretbox.AAD(key, user, workspace))
			if err != nil {
				return secretbox.At("Redis 主站会话", user, workspace, "", err)
			}
			var session AdminSession
			if json.Unmarshal(plain, &session) != nil {
				return secretbox.At("Redis 主站会话", user, workspace, "", errors.New("会话内容格式损坏；请核对备份后重试，未改动该会话"))
			}
			if convert && r.box != nil && !secretbox.IsSealed(raw) {
				if user == "" || workspace == "" {
					return secretbox.At("Redis 主站会话", user, workspace, "", errors.New("旧会话仍缺少归属，不能确定加密位置；请核查原会话索引后重试，本次未改写这条会话"))
				}
				sealed, err := r.box.Seal(plain, secretbox.AAD(key, user, workspace))
				if err == nil {
					err = credentialstore.ReplaceRedisIfUnchanged(ctx, r.client, key, raw, sealed)
				}
				if err != nil {
					return secretbox.At("Redis 主站会话", user, workspace, "", err)
				}
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
