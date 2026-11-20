package system

type CredentialsStatus struct {
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
	Message    string `json:"message"`
}

// SetCredentialsVerified is called only after startup verification/conversion succeeds.
// The API reports this immutable result rather than inspecting environment variables.
func (s *Service) SetCredentialsVerified(configured bool) {
	s.credentials = CredentialsStatus{Configured: configured, Mode: "legacy", Message: "管理凭据仍按原方式保存，本次未启用加密。须先完成回滚兼容验证，再配置并备份密钥；已有登录和对接继续使用。"}
	if configured {
		s.credentials.Mode = "encrypted"
		s.credentials.Message = "管理凭据按当前密钥加密保存。恢复备份时必须同时保留这把密钥。"
	}
}

func (s *Service) CredentialsStatus() CredentialsStatus {
	if s.credentials.Mode == "" {
		return CredentialsStatus{Mode: "unverified", Message: "管理凭据保存方式尚未完成核验；请检查服务启动日志后重新打开系统设置。"}
	}
	return s.credentials
}
