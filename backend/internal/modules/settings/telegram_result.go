package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"
	DeliveryUnknown = "unknown"
)

var ErrTelegramProxyInvalid = errors.New("所选机器人的代理地址不正确，本次没有发送；请到通知设置核查代理地址后保存。")
var ErrTelegramChannelFormat = errors.New("读取通知机器人失败：已保存的通知配置格式不正确；请到通知设置核查后重试。")

var ErrTelegramTargetUnavailable = errors.New("所选机器人已删除或停用，本次没有发送；请到通知设置选择可用机器人并保存。")

// TelegramTargetError keeps the unavailable category while reporting the known cause.
type TelegramTargetError struct{ Reason string }

func (e *TelegramTargetError) Error() string        { return e.Reason }
func (e *TelegramTargetError) Is(target error) bool { return target == ErrTelegramTargetUnavailable }
func unavailableTarget(reason string) error         { return &TelegramTargetError{Reason: reason} }

// TelegramTarget freezes one recipient in memory. Credential fields cannot be serialized.
type TelegramTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	botToken string
	chatID   string
	proxyURL string
}

type DeliveryResult struct {
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	ErrorCode int    `json:"errorCode,omitempty"`
	Attempted bool   `json:"attempted"`
}

type telegramTargetReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// TelegramTargets reads the original encrypted JSON without ID generation or other writes.
// The locked form is used inside the caller's registration transaction.
func (s *Service) TelegramTargets(ctx context.Context, reader telegramTargetReader, userID, workspaceID string, lock bool) ([]TelegramTarget, error) {
	statement := `SELECT settings FROM notification_channel_settings WHERE user_id=$1 AND admin_account_id=$2`
	if lock {
		statement += " FOR SHARE"
	}
	var raw []byte
	if err := reader.QueryRow(ctx, statement, userID, workspaceID).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return []TelegramTarget{}, nil
		}
		return nil, err
	}
	plain, err := OpenNotificationChannelsJSON(s.repository.box, userID, workspaceID, raw)
	if err != nil {
		return nil, err
	}
	var channels NotificationChannelSettings
	if err := unmarshalNotificationChannelSettings(plain, &channels); err != nil {
		return nil, ErrTelegramChannelFormat
	}
	targets := make([]TelegramTarget, 0, len(channels.Telegram))
	for _, bot := range channels.Telegram {
		targets = append(targets, TelegramTarget{
			ID: bot.ID, Name: bot.Name, Enabled: bot.Enabled,
			botToken: bot.BotToken, chatID: bot.ChatID, proxyURL: bot.ProxyURL,
		})
	}
	return targets, nil
}

func SelectTelegramTarget(targets []TelegramTarget, botIDs []string) (TelegramTarget, error) {
	if len(botIDs) == 0 {
		return TelegramTarget{}, unavailableTarget("尚未选择提醒机器人，本次没有发送；请选择一个已启用的Telegram机器人并保存。")
	}
	if len(botIDs) > 1 {
		return TelegramTarget{}, unavailableTarget("本轮提醒只允许一个Telegram机器人，本次没有发送；请只保留一个目标并保存。")
	}
	if botIDs[0] == "" {
		return TelegramTarget{}, unavailableTarget("提醒机器人编号为空，本次没有发送；请重新选择一个已启用的机器人并保存。")
	}
	var selected TelegramTarget
	count := 0
	for _, target := range targets {
		if target.ID == botIDs[0] {
			selected = target
			count++
		}
	}
	if count == 0 {
		return TelegramTarget{}, unavailableTarget("当前通知设置中未找到所选机器人编号。" + ErrTelegramTargetUnavailable.Error())
	}
	if count > 1 {
		return TelegramTarget{}, unavailableTarget("当前通知设置中有重复的机器人编号，无法确定唯一目标，本次没有发送；请到通知设置修正重复编号并重新选择。")
	}
	if !selected.Enabled {
		return TelegramTarget{}, unavailableTarget("这个机器人在通知设置中已停用。" + ErrTelegramTargetUnavailable.Error())
	}
	if strings.TrimSpace(selected.botToken) == "" {
		return TelegramTarget{}, unavailableTarget("所选机器人尚未填写机器人令牌（Bot Token），本次没有发送；请到通知设置补齐后保存。")
	}
	if strings.TrimSpace(selected.chatID) == "" {
		return TelegramTarget{}, unavailableTarget("所选机器人尚未填写接收对话编号（Chat ID），本次没有发送；请到通知设置补齐接收位置后保存。")
	}
	if selected.proxyURL != "" {
		proxy, err := url.Parse(selected.proxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "" && proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
			return TelegramTarget{}, ErrTelegramProxyInvalid
		}
	}
	return selected, nil
}

// SendFrozenTelegramWithResult preserves the old sender and its return semantics.
// Only this new path requires an explicit Telegram delivery confirmation.
func (s *Service) SendFrozenTelegramWithResult(ctx context.Context, target TelegramTarget, text string, format NotificationTemplateFormat) DeliveryResult {
	if _, err := SelectTelegramTarget([]TelegramTarget{target}, []string{target.ID}); err != nil {
		return DeliveryResult{Status: DeliveryFailed, Reason: err.Error()}
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 4096 {
		return DeliveryResult{Status: DeliveryFailed, Reason: "消息为空或超过 Telegram 的4096字限制，本次没有发送；请缩短消息后重试。"}
	}
	if ctx.Err() != nil {
		return DeliveryResult{Status: DeliveryFailed, Reason: "发送开始前操作已取消，本次没有发送；请在服务恢复后重试。"}
	}
	body := map[string]string{"chat_id": target.chatID, "text": text}
	switch normalizeNotificationTemplateFormat(format) {
	case NotificationTemplateFormatMarkdown:
		body["parse_mode"] = "Markdown"
	case NotificationTemplateFormatHTML:
		body["text"] = telegramHTMLForChannel(text)
		body["parse_mode"] = "HTML"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Reason: "构造 Telegram 消息失败，本次没有发送；请核查消息内容后重试。"}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var connected, wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		GotConn:      func(httptrace.GotConnInfo) { connected.Store(true) },
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}
	requestCtx = httptrace.WithClientTrace(requestCtx, trace)
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(target.botToken) + "/sendMessage"
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Reason: "构造 Telegram 请求失败，本次没有发送；请核查通知设置后重试。"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.telegramClient(target.proxyURL).Do(request)
	if err != nil {
		var dial *net.OpError
		var dns *net.DNSError
		definitelyNotConnected := !connected.Load() && !wrote.Load() &&
			((errors.As(err, &dial) && dial.Op == "dial") || errors.As(err, &dns))
		if definitelyNotConnected {
			return DeliveryResult{Status: DeliveryFailed, Attempted: true, Reason: "未能建立到 Telegram 或所配代理的连接，本次没有发出消息；请检查 DNS、网络和代理设置后重试。"}
		}
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: "向 Telegram 请求后未确认结果，可能已经收到；为避免重复，本次不自动重发，请先检查 Telegram 和本站服务日志。"}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: "Telegram 响应读取中断或超过64KiB，未确认是否送达；请先检查 Telegram，避免重复发送。"}
	}
	if response.StatusCode >= 500 {
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: fmt.Sprintf("Telegram 返回服务端错误（HTTP %d），未确认消息是否已经收到；请先检查 Telegram，本次不自动重发。", response.StatusCode)}
	}
	var result struct {
		OK          *bool       `json:"ok"`
		Description string      `json:"description"`
		ErrorCode   json.Number `json:"error_code"`
		Result      struct {
			MessageID json.Number `json:"message_id"`
		} `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: "Telegram 未返回可确认的完整JSON，可能已经收到；请先检查 Telegram，本次不自动重发。"}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || result.OK == nil {
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: "Telegram 响应不完整或包含额外内容，未确认是否送达；请先检查 Telegram。"}
	}
	if !*result.OK {
		code, _ := strconv.Atoi(string(result.ErrorCode))
		description := redactTelegramDescription(result.Description, target)
		if description == "" {
			description = "Telegram 明确拒绝了本次请求，但未提供具体原因"
		}
		return DeliveryResult{Status: DeliveryFailed, Attempted: true, ErrorCode: code,
			Reason: telegramRejection(description, code, response.StatusCode)}
	}
	messageID, err := strconv.ParseInt(string(result.Result.MessageID), 10, 64)
	if response.StatusCode < 200 || response.StatusCode >= 300 || err != nil || messageID <= 0 {
		return DeliveryResult{Status: DeliveryUnknown, Attempted: true, Reason: "Telegram 没有返回可确认的消息编号，可能已经收到；请先检查 Telegram，本次不自动重发。"}
	}
	return DeliveryResult{Status: DeliverySent, Attempted: true, Reason: "Telegram 已明确确认收到这条消息。"}
}

var telegramPercentEscapes = regexp.MustCompile(`(?:%[0-9A-Fa-f]{2})+`)

var telegramDescriptionSecrets = regexp.MustCompile(`(?i)(https?://\S+|(?:bearer|basic)\s+\S+|(?:token|api_key|password|chat_id)\s*[:=]\s*\S+)`)

func redactTelegramDescription(description string, target TelegramTarget) string {
	secrets := []string{target.botToken, target.chatID, target.proxyURL}
	if proxy, err := url.Parse(target.proxyURL); err == nil && proxy.User != nil {
		password, _ := proxy.User.Password()
		secrets = append(secrets, proxy.User.Username(), password)
	}
	// Normalize URL escaping before redacting so percent-encoded credentials cannot bypass masking.
	description = decodeTelegramEscapes(description)
	for _, secret := range secrets {
		secret = decodeTelegramEscapes(secret)
		if secret != "" {
			for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				description = strings.ReplaceAll(description, encoded, "[已隐藏]")
				lowerEscapes := telegramPercentEscapes.ReplaceAllStringFunc(encoded, strings.ToLower)
				description = strings.ReplaceAll(description, lowerEscapes, "[已隐藏]")
			}
		}
	}
	description = telegramDescriptionSecrets.ReplaceAllString(description, "[已隐藏]")
	return strings.Join(strings.Fields(description), " ")
}

func decodeTelegramEscapes(value string) string {
	for range 2 {
		value = telegramPercentEscapes.ReplaceAllStringFunc(value, func(encoded string) string {
			decoded, err := url.PathUnescape(encoded)
			if err != nil {
				return encoded
			}
			return decoded
		})
	}
	return value
}

func telegramRejection(description string, code, status int) string {
	detail := fmt.Sprintf("HTTP %d", status)
	if code > 0 {
		detail = fmt.Sprintf("错误编号%d，%s", code, detail)
	}
	return fmt.Sprintf("Telegram 明确拒绝发送：%s（%s）；请核查通知设置和机器人权限后重试。", description, detail)
}
