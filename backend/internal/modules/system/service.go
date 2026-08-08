package system

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"transithub/backend/internal/config"
)

var (
	ErrUpgradeInProgress  = errors.New("系统升级正在执行")
	ErrRestartInProgress  = errors.New("后台服务重启正在执行")
	ErrRollbackInProgress = errors.New("版本回滚正在执行")
)

type UpgradeExecutor interface {
	Start(ctx context.Context) error
	Status(ctx context.Context) (UpgradeStatusResponse, error)
}

type RestartExecutor interface {
	Start(ctx context.Context) error
	Status(ctx context.Context) (RestartStatusResponse, error)
}

type RollbackExecutor interface {
	Start(ctx context.Context) error
	Status(ctx context.Context) (RollbackStatusResponse, error)
}

// Service 提供系统版本信息查询能力。
// 开源版不再包含商业授权校验和自动更新逻辑。
type Service struct {
	cfg              config.Config
	upgradeExecutor  UpgradeExecutor
	restartExecutor  RestartExecutor
	rollbackExecutor RollbackExecutor
	maintenanceMu    sync.Mutex

	lastUpgradeRequestedAt  time.Time
	lastRestartRequestedAt  time.Time
	lastRollbackRequestedAt time.Time
}

// NewService 创建系统服务
func NewService(cfg config.Config) *Service {
	return newServiceWithExecutors(cfg, newSystemdUpgradeExecutor(), newSystemdRestartExecutor(), newSystemdRollbackExecutor())
}

func newServiceWithExecutors(
	cfg config.Config,
	upgradeExecutor UpgradeExecutor,
	restartExecutor RestartExecutor,
	rollbackExecutor RollbackExecutor,
) *Service {
	return &Service{
		cfg:              cfg,
		upgradeExecutor:  upgradeExecutor,
		restartExecutor:  restartExecutor,
		rollbackExecutor: rollbackExecutor,
	}
}

// ensureNoMaintenanceRunning 三个维护动作共享同一把系统级维护锁，
// 因此任一动作在执行中时其余动作都必须拒绝，避免并发改动部署目录。
// 调用方必须已持有 maintenanceMu。
func (s *Service) ensureNoMaintenanceRunning(ctx context.Context, now time.Time) error {
	upgradeStatus, err := s.upgradeExecutor.Status(ctx)
	if err != nil {
		return fmt.Errorf("读取升级状态失败：%w", err)
	}
	if upgradeStatus.State == UpgradeStateStarting || upgradeStatus.State == UpgradeStateRunning ||
		startRequestPending(&s.lastUpgradeRequestedAt, upgradeStatus.StartedAt, now) {
		return ErrUpgradeInProgress
	}

	restartStatus, err := s.restartExecutor.Status(ctx)
	if err != nil {
		return fmt.Errorf("读取重启状态失败：%w", err)
	}
	if restartStatus.State == RestartStateStarting || restartStatus.State == RestartStateRunning ||
		startRequestPending(&s.lastRestartRequestedAt, restartStatus.StartedAt, now) {
		return ErrRestartInProgress
	}

	rollbackStatus, err := s.rollbackExecutor.Status(ctx)
	if err != nil {
		return fmt.Errorf("读取回滚状态失败：%w", err)
	}
	if rollbackStatus.State == RollbackStateStarting || rollbackStatus.State == RollbackStateRunning ||
		startRequestPending(&s.lastRollbackRequestedAt, rollbackStatus.StartedAt, now) {
		return ErrRollbackInProgress
	}
	return nil
}

// StartUpgrade 启动固定的 systemd 升级单元。
func (s *Service) StartUpgrade(ctx context.Context) (UpgradeStartResponse, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	now := time.Now().UTC()
	if err := s.ensureNoMaintenanceRunning(ctx, now); err != nil {
		return UpgradeStartResponse{}, err
	}
	if err := s.upgradeExecutor.Start(ctx); err != nil {
		return UpgradeStartResponse{}, fmt.Errorf("启动系统升级失败：%w", err)
	}

	s.lastUpgradeRequestedAt = now
	return UpgradeStartResponse{
		State:       UpgradeStateStarting,
		RequestedAt: now.Format(time.RFC3339Nano),
	}, nil
}

func startRequestPending(lastRequestedAt *time.Time, startedAtValue string, now time.Time) bool {
	if lastRequestedAt.IsZero() {
		return false
	}
	if startedAt, err := time.Parse(time.RFC3339Nano, startedAtValue); err == nil && !startedAt.Before(*lastRequestedAt) {
		*lastRequestedAt = time.Time{}
		return false
	}
	if now.Sub(*lastRequestedAt) < 30*time.Second {
		return true
	}
	*lastRequestedAt = time.Time{}
	return false
}

// StartRestart 启动固定的 systemd 后台服务重启单元。
func (s *Service) StartRestart(ctx context.Context) (RestartStartResponse, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	now := time.Now().UTC()
	if err := s.ensureNoMaintenanceRunning(ctx, now); err != nil {
		return RestartStartResponse{}, err
	}
	if err := s.restartExecutor.Start(ctx); err != nil {
		return RestartStartResponse{}, fmt.Errorf("启动后台服务重启失败：%w", err)
	}

	s.lastRestartRequestedAt = now
	return RestartStartResponse{
		State:       RestartStateStarting,
		RequestedAt: now.Format(time.RFC3339Nano),
	}, nil
}

// StartRollback 启动固定的 systemd 版本回滚单元。
// 还原点缺失时由执行器返回 ErrRollbackPointMissing，不会启动单元。
func (s *Service) StartRollback(ctx context.Context) (RollbackStartResponse, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	now := time.Now().UTC()
	if err := s.ensureNoMaintenanceRunning(ctx, now); err != nil {
		return RollbackStartResponse{}, err
	}
	if err := s.rollbackExecutor.Start(ctx); err != nil {
		if errors.Is(err, ErrRollbackPointMissing) {
			return RollbackStartResponse{}, err
		}
		return RollbackStartResponse{}, fmt.Errorf("启动版本回滚失败：%w", err)
	}

	s.lastRollbackRequestedAt = now
	return RollbackStartResponse{
		State:       RollbackStateStarting,
		RequestedAt: now.Format(time.RFC3339Nano),
	}, nil
}

// RollbackStatus 读取独立执行器写入的固定状态，并附带当前还原点。
func (s *Service) RollbackStatus(ctx context.Context) (RollbackStatusResponse, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	status, err := s.rollbackExecutor.Status(ctx)
	if err != nil {
		return RollbackStatusResponse{}, fmt.Errorf("读取回滚状态失败：%w", err)
	}
	return status, nil
}

// UpgradeStatus 读取独立执行器写入的固定状态。
func (s *Service) UpgradeStatus(ctx context.Context) (UpgradeStatusResponse, error) {
	status, err := s.upgradeExecutor.Status(ctx)
	if err != nil {
		return UpgradeStatusResponse{}, fmt.Errorf("读取升级状态失败：%w", err)
	}
	return status, nil
}

// RestartStatus 读取独立执行器写入的固定状态。
func (s *Service) RestartStatus(ctx context.Context) (RestartStatusResponse, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	status, err := s.restartExecutor.Status(ctx)
	if err != nil {
		return RestartStatusResponse{}, fmt.Errorf("读取重启状态失败：%w", err)
	}
	return status, nil
}

// Version 返回当前系统版本信息
func (s *Service) Version() VersionResponse {
	return VersionResponse{
		Version: s.cfg.AppVersion,
	}
}
