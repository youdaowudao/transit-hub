package system

// VersionResponse GET /api/system/version 响应。
// 开源版只展示版本号，不再包含授权状态或更新开关字段。
type VersionResponse struct {
	Version string `json:"version"`
}

type UpgradeState string

const (
	UpgradeStateIdle      UpgradeState = "idle"
	UpgradeStateStarting  UpgradeState = "starting"
	UpgradeStateRunning   UpgradeState = "running"
	UpgradeStateSucceeded UpgradeState = "succeeded"
	UpgradeStateFailed    UpgradeState = "failed"
)

// UpgradeStartResponse POST /api/system/upgrade 响应。
type UpgradeStartResponse struct {
	State       UpgradeState `json:"state"`
	RequestedAt string       `json:"requestedAt"`
}

// UpgradeStatusResponse GET /api/system/upgrade 响应。
type UpgradeStatusResponse struct {
	State      UpgradeState `json:"state"`
	StartedAt  string       `json:"startedAt,omitempty"`
	FinishedAt string       `json:"finishedAt,omitempty"`
	ExitCode   *int         `json:"exitCode,omitempty"`
	Output     string       `json:"output,omitempty"`
}

type RestartState string

const (
	RestartStateIdle      RestartState = "idle"
	RestartStateStarting  RestartState = "starting"
	RestartStateRunning   RestartState = "running"
	RestartStateSucceeded RestartState = "succeeded"
	RestartStateFailed    RestartState = "failed"
)

// RestartStartResponse POST /api/system/restart 响应。
type RestartStartResponse struct {
	State       RestartState `json:"state"`
	RequestedAt string       `json:"requestedAt"`
}

// RestartStatusResponse GET /api/system/restart 响应。
type RestartStatusResponse struct {
	State      RestartState `json:"state"`
	StartedAt  string       `json:"startedAt,omitempty"`
	FinishedAt string       `json:"finishedAt,omitempty"`
	ExitCode   *int         `json:"exitCode,omitempty"`
	Output     string       `json:"output,omitempty"`
}

type RollbackState string

const (
	RollbackStateIdle      RollbackState = "idle"
	RollbackStateStarting  RollbackState = "starting"
	RollbackStateRunning   RollbackState = "running"
	RollbackStateSucceeded RollbackState = "succeeded"
	RollbackStateFailed    RollbackState = "failed"
)

// RollbackPoint 升级脚本在切换代码前写入的还原点，缺失表示尚未升级过。
// SchemaVersion 是迁移文件名的数字前缀，由 deploy/update-source.sh 以 JSON 数字写入。
type RollbackPoint struct {
	Commit        string `json:"commit"`
	Version       string `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	DumpPath      string `json:"dumpPath,omitempty"`
	CapturedAt    string `json:"capturedAt"`
}

// RollbackStartResponse POST /api/system/rollback 响应。
type RollbackStartResponse struct {
	State       RollbackState `json:"state"`
	RequestedAt string        `json:"requestedAt"`
}

// RollbackStatusResponse GET /api/system/rollback 响应。
// Point 为空表示当前没有可回滚的还原点，前端应禁用按钮。
type RollbackStatusResponse struct {
	State      RollbackState  `json:"state"`
	StartedAt  string         `json:"startedAt,omitempty"`
	FinishedAt string         `json:"finishedAt,omitempty"`
	ExitCode   *int           `json:"exitCode,omitempty"`
	Output     string         `json:"output,omitempty"`
	Point      *RollbackPoint `json:"point,omitempty"`
}
