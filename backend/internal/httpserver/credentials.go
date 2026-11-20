package httpserver

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/credentialstore"
	"transithub/backend/internal/shared/secretbox"
)

type startupCredentialStores struct {
	db        *pgxpool.Pool
	box       *secretbox.Box
	dashboard *dashboard.Repository
	sites     *upstream.RedisSiteCache
}

func (s startupCredentialStores) verify(ctx context.Context, db credentialstore.Postgres) error {
	var users, sites []string
	if db != nil {
		if err := credentialstore.VerifyPostgres(ctx, db, s.box); err != nil {
			return err
		}
		var err error
		users, sites, err = credentialstore.KnownSiteReferences(ctx, db)
		if err != nil {
			return err
		}
	}
	if err := s.dashboard.VerifyCredentials(ctx); err != nil {
		return err
	}
	return s.sites.VerifyCredentials(ctx, users, sites)
}

func (s startupCredentialStores) Verify() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// A route-only APIOnly assembly may have no clients; there is then no stored data to verify.
	if s.db == nil {
		return s.verify(ctx, nil)
	}
	return s.verify(ctx, s.db)
}

// Encrypt runs only during normal startup, after legacy ownership has been assigned.
// The dedicated session lock is retained through verification and all PG/Redis CAS writes.
func (s startupCredentialStores) Encrypt(ctx context.Context) error {
	if s.box == nil || s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	lockCtx, cancelLock := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLock()
	connection, err := s.db.Acquire(lockCtx)
	if err != nil {
		return errors.New("管理凭据转换未开始：10 秒内未取得数据库连接；请检查数据库连接和正在运行的实例后重试，未改动存储")
	}
	const lockID int64 = 0x54524843524544
	locked := false
	defer func() {
		if !locked {
			// A canceled lock query may have acquired the server-side lock without
			// returning its result; discard that session instead of pooling it.
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelClose()
			_ = connection.Hijack().Close(closeCtx)
			return
		}
		unlockCtx, cancelUnlock := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelUnlock()
		var released bool
		if err := connection.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&released); err != nil || !released {
			// Never return a session that might still hold the conversion lock to the pool.
			_ = connection.Hijack().Close(unlockCtx)
			return
		}
		connection.Release()
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !locked {
		if err := connection.QueryRow(lockCtx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); err != nil {
			return errors.New("管理凭据转换未开始：获取数据库专用锁失败；请检查数据库连接及其他转换实例后重试，未改动存储")
		}
		if locked {
			break
		}
		select {
		case <-lockCtx.Done():
			return errors.New("管理凭据转换未开始：等待另一个转换实例超过 10 秒；请确认其已完成或停止后重试，未改动存储")
		case <-ticker.C:
		}
	}
	if err := s.verify(ctx, connection); err != nil {
		return err
	}
	if err := credentialstore.EncryptPostgres(ctx, connection, s.box); err != nil {
		return err
	}
	return s.dashboard.EncryptCredentials(ctx)
}
