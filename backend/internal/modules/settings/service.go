package settings

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const testMessage = "Transit Hub notification channel test succeeded."

const minRefreshIntervalSeconds = 60

var (
	ErrInvalidNotificationChannel = errors.New("admin.settings.errors.invalidChannel")
	ErrMissingTelegramConfig      = errors.New("admin.settings.errors.missingTelegramConfig")
	ErrSendNotificationFailed     = errors.New("admin.settings.errors.sendFailed")
)

// SMTP 错误 sentinel：handler 用 errors.Is 逐一映射到固定状态码，详见 handler.go writeSmtpError。
var (
	ErrSMTPValidation               = errors.New("admin.settings.smtp.errors.validation")
	ErrSMTPMissingConfig            = errors.New("admin.settings.smtp.errors.missingConfig")
	ErrSMTPInvalidTLSMode           = errors.New("admin.settings.smtp.errors.invalidTlsMode")
	ErrSMTPInvalidEmail             = errors.New("admin.settings.smtp.errors.invalidEmail")
	ErrSMTPEncryptionKeyUnavailable = errors.New("admin.settings.smtp.errors.encryptionKeyUnavailable")
	ErrSMTPDecryptFailed            = errors.New("admin.settings.smtp.errors.decryptFailed")
	ErrSMTPSendFailed               = errors.New("admin.settings.smtp.errors.sendFailed")
	ErrSMTPPersistence              = errors.New("admin.settings.smtp.errors.persistence")
)

type Service struct {
	client            *http.Client
	repository        *Repository
	notificationRepo  notificationRepository
	strategyRepo      strategyRepository
	accounts          AdminAccountResolver
	OnStrategyChanged func(userID, adminAccountID string, settings StrategySettings)

	// smtpRepo 是 SMTP 存储层的窄接口，由 *Repository 结构性满足；测试可注入内存 fake。
	smtpRepo smtpRepository
	// smtpKeyGCM 为 nil 表示 SMTP_ENCRYPTION_KEY 未配置，此时禁止保存非空密码或解密已保存密码。
	smtpKeyGCM cipher.AEAD
	// smtpSender 默认使用生产实现；测试可注入 fake sender 以避免依赖真实外部 SMTP 服务。
	smtpSender smtpSender
	// emailTemplateRepo 是邮件模板存储层窄接口，测试通过内存 fake 覆盖 workspace 隔离和限制规则。
	emailTemplateRepo emailTemplateRepository
}

type AdminAccountResolver interface {
	RequireCurrentID(ctx context.Context, userID string) (string, error)
}

type notificationRepository interface {
	GetNotificationChannels(ctx context.Context, userID string, adminAccountID string) (NotificationChannelSettings, error)
	SaveNotificationChannels(ctx context.Context, userID string, adminAccountID string, settings NotificationChannelSettings) error
}

type strategyRepository interface {
	GetStrategy(ctx context.Context, userID string, adminAccountID string) (StrategySettings, error)
	ListStrategies(ctx context.Context) ([]WorkspaceStrategy, error)
	SaveStrategy(ctx context.Context, userID string, adminAccountID string, settings StrategySettings) error
}

func NewService(client *http.Client, repository *Repository) *Service {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if clone.Timeout <= 0 {
		clone.Timeout = notificationRequestTimeout
	}
	if clone.Transport == nil || clone.Transport == http.DefaultTransport {
		clone.Transport = newSafeSettingsTransport(net.DefaultResolver)
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("notification redirects are not allowed")
	}
	return &Service{
		client:            &clone,
		repository:        repository,
		notificationRepo:  repository,
		strategyRepo:      repository,
		smtpRepo:          repository,
		emailTemplateRepo: repository,
	}
}

func (s *Service) SetAdminAccountResolver(accounts AdminAccountResolver) {
	s.accounts = accounts
}

func DefaultNotificationChannelSettings() NotificationChannelSettings {
	return NotificationChannelSettings{Telegram: []TelegramChannelSettings{}}
}

func DefaultStrategySettings() StrategySettings {
	return StrategySettings{
		RefreshInterval:          minRefreshIntervalSeconds,
		BalanceTemplateFormat:    NotificationTemplateFormatText,
		MultiplierTemplateFormat: NotificationTemplateFormatText,
	}
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	return s.repository.EnsureSchema(ctx)
}

func (s *Service) GetNotificationChannels(ctx context.Context, userID string) (NotificationChannelSettings, error) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return NotificationChannelSettings{}, err
	}
	return s.notificationChannelsForWorkspace(ctx, userID, adminAccountID)
}

// ListStrategies restores business refresh settings independently of notification availability.
func (s *Service) ListStrategies(ctx context.Context) ([]WorkspaceStrategy, error) {
	return s.strategyRepo.ListStrategies(ctx)
}

func (s *Service) GetStrategy(ctx context.Context, userID string) (StrategySettings, error) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return StrategySettings{}, err
	}
	strategy, err := s.GetStrategyForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return StrategySettings{}, err
	}
	return s.projectStrategy(ctx, userID, adminAccountID, strategy)
}

func (s *Service) GetStrategyForWorkspace(ctx context.Context, userID, adminAccountID string) (StrategySettings, error) {
	strategy, err := s.strategyRepo.GetStrategy(ctx, userID, adminAccountID)
	if err != nil {
		return strategy, err
	}
	// Runtime alerts use the original IDs; the sender independently enforces Telegram.
	// A notification lookup failure must not interrupt refresh or pricing business work.
	strategy.BalanceTemplate = projectDefaultBalanceTemplate(strategy.BalanceTemplate)
	return strategy, nil
}

func (s *Service) SaveStrategy(ctx context.Context, userID string, settings StrategySettings) (StrategySettings, error) {
	settings = normalizeStrategySettings(settings)
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return StrategySettings{}, err
	}
	// Warning fields describe the persisted recipient projection, not client claims.
	settings.BalanceNotifyRecipientsInvalid = false
	settings.MultiplierNotifyRecipientsInvalid = false
	settings, err = s.projectStrategy(ctx, userID, adminAccountID, settings)
	if err != nil {
		return StrategySettings{}, err
	}
	if err := s.strategyRepo.SaveStrategy(ctx, userID, adminAccountID, settings); err != nil {
		return StrategySettings{}, err
	}
	if s.OnStrategyChanged != nil {
		s.OnStrategyChanged(userID, adminAccountID, settings)
	}
	return settings, nil
}

func (s *Service) SaveNotificationChannels(ctx context.Context, userID string, settings NotificationChannelSettings) (NotificationChannelSettings, error) {
	settings = normalizeNotificationChannelSettings(settings)
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return NotificationChannelSettings{}, err
	}
	if err := s.notificationRepo.SaveNotificationChannels(ctx, userID, adminAccountID, settings); err != nil {
		return NotificationChannelSettings{}, err
	}
	return settings, nil
}

func (s *Service) currentAdminAccountID(ctx context.Context, userID string) (string, error) {
	if s.accounts == nil {
		return "", errors.New("admin.adminAccounts.errors.noCurrentAccount")
	}
	return s.accounts.RequireCurrentID(ctx, userID)
}

// generateBotID 生成 16 字节的随机十六进制 ID，确保每个机器人拥有全局唯一标识。
func generateBotID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func normalizeNotificationChannelSettings(settings NotificationChannelSettings) NotificationChannelSettings {
	settings.Telegram = append(make([]TelegramChannelSettings, 0, len(settings.Telegram)), settings.Telegram...)
	if settings.Telegram == nil {
		settings.Telegram = []TelegramChannelSettings{}
	}
	seen := make(map[string]struct{})
	for index := range settings.Telegram {
		settings.Telegram[index].ID = strings.TrimSpace(settings.Telegram[index].ID)
		if settings.Telegram[index].ID == "" {
			settings.Telegram[index].ID = generateBotID()
		}
		if _, dup := seen[settings.Telegram[index].ID]; dup {
			settings.Telegram[index].ID = generateBotID()
		}
		seen[settings.Telegram[index].ID] = struct{}{}
		settings.Telegram[index].Name = strings.TrimSpace(settings.Telegram[index].Name)
		settings.Telegram[index].BotToken = strings.TrimSpace(settings.Telegram[index].BotToken)
		settings.Telegram[index].ChatID = strings.TrimSpace(settings.Telegram[index].ChatID)
		settings.Telegram[index].ProxyURL = strings.TrimSpace(settings.Telegram[index].ProxyURL)
	}
	return settings
}

func normalizeStrategySettings(settings StrategySettings) StrategySettings {
	if settings.RefreshInterval < minRefreshIntervalSeconds {
		settings.RefreshInterval = minRefreshIntervalSeconds
	}
	settings.BalanceTemplateFormat = normalizeNotificationTemplateFormat(settings.BalanceTemplateFormat)
	settings.MultiplierTemplateFormat = normalizeNotificationTemplateFormat(settings.MultiplierTemplateFormat)
	return settings
}

func (s *Service) TestNotification(ctx context.Context, dto TestNotificationRequest) error {
	switch dto.Channel {
	case NotificationChannelTelegram:
		return s.sendTelegram(ctx, strings.TrimSpace(dto.TelegramBotToken), strings.TrimSpace(dto.TelegramChatID), strings.TrimSpace(dto.TelegramProxyURL), testMessage)
	default:
		return ErrInvalidNotificationChannel
	}
}

// SendToBots 保留历史纯文本入口，活动通知、自动调价等现有调用方无需感知新增格式字段。
func (s *Service) SendToBots(ctx context.Context, userID string, botIDs []string, message string) {
	s.sendNotificationToBots(ctx, userID, botIDs, notificationMessage{
		Content: message,
		Format:  NotificationTemplateFormatText,
	})
}

// SendToWorkspaceBots keeps stored activity recipients bound to their original workspace.
func (s *Service) SendToWorkspaceBots(ctx context.Context, userID, adminAccountID string, botIDs []string, message string) {
	s.sendNotificationToWorkspaceBots(ctx, userID, adminAccountID, botIDs, notificationMessage{Content: message, Format: NotificationTemplateFormatText})
}

// SendFormattedToBots 发送显式选择格式的 Telegram 通知，保留纯文本入口。
func (s *Service) SendFormattedToBots(ctx context.Context, userID string, botIDs []string, message string, format NotificationTemplateFormat) {
	s.sendNotificationToBots(ctx, userID, botIDs, notificationMessage{
		Content: message,
		Format:  normalizeNotificationTemplateFormat(format),
	})
}

func (s *Service) SendFormattedToWorkspaceBots(ctx context.Context, userID, adminAccountID string, botIDs []string, message string, format NotificationTemplateFormat) {
	s.sendNotificationToWorkspaceBots(ctx, userID, adminAccountID, botIDs, notificationMessage{
		Content: message,
		Format:  normalizeNotificationTemplateFormat(format),
	})
}

// sendNotificationToBots 从数据库加载用户的渠道配置，匹配 ID 后逐个发送。
// 单个渠道失败只记录日志，不中断其他机器人发送（fire-and-forget）。
func (s *Service) sendNotificationToBots(ctx context.Context, userID string, botIDs []string, message notificationMessage) {
	if len(botIDs) == 0 || message.Content == "" {
		return
	}
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		log.Printf("[settings] 当前 admin workspace 缺失 user_id=%s err=%v", userID, err)
		return
	}
	s.sendNotificationToWorkspaceBots(ctx, userID, adminAccountID, botIDs, message)
}

func (s *Service) sendNotificationToWorkspaceBots(ctx context.Context, userID, adminAccountID string, botIDs []string, message notificationMessage) {
	if len(botIDs) == 0 || message.Content == "" {
		return
	}
	channels, err := s.notificationChannelsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		log.Printf("[settings] 加载通知渠道配置失败 user_id=%s err=%v", userID, err)
		return
	}

	idSet := make(map[string]struct{}, len(botIDs))
	for _, id := range botIDs {
		idSet[id] = struct{}{}
	}

	for _, bot := range channels.Telegram {
		if _, ok := idSet[bot.ID]; ok && bot.Enabled {
			if err := s.sendTelegramMessage(ctx, bot.BotToken, bot.ChatID, bot.ProxyURL, message); err != nil {
				log.Printf("[settings] Telegram 通知发送失败 bot=%s err=%v", bot.Name, err)
			}
		}
	}
}

func (s *Service) sendTelegram(ctx context.Context, botToken string, chatID string, proxyURL string, message string) error {
	return s.sendTelegramMessage(ctx, botToken, chatID, proxyURL, notificationMessage{Content: message, Format: NotificationTemplateFormatText})
}

func (s *Service) sendTelegramMessage(ctx context.Context, botToken string, chatID string, proxyURL string, message notificationMessage) error {
	if botToken == "" || chatID == "" {
		return ErrMissingTelegramConfig
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(botToken))
	body := map[string]string{
		"chat_id": chatID,
		"text":    message.Content,
	}
	switch normalizeNotificationTemplateFormat(message.Format) {
	case NotificationTemplateFormatMarkdown:
		// Telegram 的 legacy Markdown 与普通 Markdown 模板最接近，且不会要求用户手动
		// 转义 MarkdownV2 中大量普通标点。
		body["parse_mode"] = "Markdown"
	case NotificationTemplateFormatHTML:
		body["text"] = telegramHTMLForChannel(message.Content)
		body["parse_mode"] = "HTML"
	}
	return s.postJSONWithClient(ctx, s.telegramClient(proxyURL), endpoint, body)
}

func (s *Service) postJSONWithClient(ctx context.Context, client *http.Client, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return ErrSendNotificationFailed
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 512))
		return fmt.Errorf("%w: status=%d", ErrSendNotificationFailed, response.StatusCode)
	}
	return nil
}

func (s *Service) telegramClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return s.client
	}
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return s.client
	}
	transport := newSafeSettingsTransport(net.DefaultResolver)
	if current, ok := s.client.Transport.(*http.Transport); ok {
		transport = current.Clone()
	}
	transport.Proxy = http.ProxyURL(parsedProxy)
	return &http.Client{Transport: transport, Timeout: s.client.Timeout, CheckRedirect: s.client.CheckRedirect}
}
