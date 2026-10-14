package notifications

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput  = errors.New("通知内容不符合安全格式，请发布方检查归属、等级、字段与长度后重试")
	ErrEventConflict = errors.New("同一通知事件已保存不同内容，本次没有覆盖原提醒，请发布方核查事件编号")
	ErrWorkspace     = errors.New("通知所属工作区不存在或不属于这个用户，本次没有保存，请核查工作区后重试")
	unsafeText       = regexp.MustCompile(`(?i)(?:<\s*/?\s*[a-z!][^>]*>|(?:password|passwd|token|key|secret|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|cookie|session|real[_ -]?connection|密码|密钥|令牌)["']?\s*[:=]|\bbearer\s+\S+|(?:postgres(?:ql)?|redis)://)`)
	stableActionKind = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
)

type Service struct{ repository *Repository }

func NewService(repository *Repository) *Service { return &Service{repository: repository} }

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("生成通知编号失败，本次没有保存，请重试")
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func invalidInput(field, reason string) error {
	return fmt.Errorf("%w：%s校验失败，%s；本次通知未保存，请发布方修正后使用原事件编号重试", ErrInvalidInput, field, reason)
}

func checkText(field, value string, limit int, required, identifier bool) error {
	if !utf8.ValidString(value) {
		return invalidInput(field, "文字编码不完整，请提供有效的UTF-8文字")
	}
	if required && strings.TrimSpace(value) == "" {
		return invalidInput(field, "缺少必要内容")
	}
	if utf8.RuneCountInString(value) > limit {
		reason := fmt.Sprintf("超过%d个字符的上限", limit)
		if identifier {
			reason += "，请核查完整业务编号，超长编号不会被截断成其他编号"
		}
		return invalidInput(field, reason)
	}
	var structured any
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &structured) == nil {
		switch structured.(type) {
		case map[string]any, []any:
			return invalidInput(field, "包含整段资料或数组，只接受纯文字，请只传必要的白话说明")
		}
	}
	if unsafe := unsafeText.FindString(value); unsafe != "" {
		if strings.HasPrefix(strings.TrimSpace(unsafe), "<") {
			return invalidInput(field, "包含网页标签，请去掉标签并提供纯文字")
		}
		return invalidInput(field, "包含登录凭据或存储连接资料，请去掉敏感资料并提供纯文字")
	}
	for _, r := range value {
		if unicode.IsControl(r) && (identifier || r != '\n' && r != '\t') {
			return invalidInput(field, "包含不能展示的控制字符，请核查原文字或完整编号")
		}
	}
	return nil
}

func sensitiveDetailLabel(label string) bool {
	label = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.TrimSpace(label)))
	switch label {
	case "password", "passwd", "token", "key", "apikey", "accesstoken", "refreshtoken", "secret", "authorization", "cookie", "session", "realconnection", "凭据", "密码", "密钥", "令牌":
		return true
	}
	return false
}

func validate(input NoticeInput) ([]byte, error) {
	for _, field := range []struct{ name, value string }{
		{"用户归属", input.UserID}, {"工作区归属", input.AdminAccountID}, {"消息类型", input.Kind}, {"事件编号", input.EventKey}, {"对象类型", input.SubjectType}, {"对象编号", input.SubjectID},
	} {
		if err := checkText(field.name, field.value, 512, true, true); err != nil {
			return nil, err
		}
	}
	if input.Level < LevelNormal || input.Level > LevelUrgent {
		return nil, invalidInput("消息等级", "只接受1到4四个等级，请选择普通、关注、重要或紧急")
	}
	if input.DiscoveredAt.IsZero() {
		return nil, invalidInput("发现时间", "缺少实际取得这条事实的时间，请提供本次发现时间")
	}
	if err := checkText("标题", input.Content.Title, 160, true, false); err != nil {
		return nil, err
	}
	if err := checkText("摘要", input.Content.Summary, 1000, true, false); err != nil {
		return nil, err
	}
	if len(input.Content.Details) > 20 {
		return nil, invalidInput("详情", "超过20项的上限，请只保留必要的白话详情")
	}
	for i, detail := range input.Content.Details {
		field := fmt.Sprintf("详情第%d项", i+1)
		if sensitiveDetailLabel(detail.Label) {
			return nil, invalidInput(field+"的标签", "不能使用登录凭据或完整登录、对接资料字段，请只提供不含敏感资料的白话标签")
		}
		if err := checkText(field+"的标签", detail.Label, 160, true, false); err != nil {
			return nil, err
		}
		if err := checkText(field+"的内容", detail.Value, 1000, false, false); err != nil {
			return nil, err
		}
	}
	if action := input.Content.Action; action != nil {
		for _, field := range []struct{ name, value string }{{"检查入口的网站编号", action.SiteID}, {"检查入口的分组编号", action.FocusGroupID}, {"检查入口的分组名称", action.FocusGroup}} {
			if err := checkText(field.name, field.value, 512, false, true); err != nil {
				return nil, err
			}
		}
		switch action.Kind {
		case "group_rates":
			if action.SiteID == "" {
				return nil, invalidInput("检查入口的网站编号", "缺少要检查的网站编号，请核查原网站")
			}
			if action.FocusGroupID != "" && action.FocusGroup != "" {
				return nil, invalidInput("检查入口的分组", "不能同时提供分组编号和名称，请有编号时保留原编号，缺编号时提供原名称")
			}
		case "connection_health":
			if action.SiteID != "" || action.FocusGroupID != "" || action.FocusGroup != "" {
				return nil, invalidInput("检查入口", "分组健康入口不能附带网站或分组编号，请去掉这些导航字段")
			}
		default:
			if !stableActionKind.MatchString(action.Kind) {
				return nil, invalidInput("检查入口类型", "动作名称格式不正确，请使用以小写字母开头、由小写字母、数字、点、下划线或短横线组成且不超过128字符的动作名称")
			}
			if action.SiteID != "" || action.FocusGroupID != "" || action.FocusGroup != "" {
				return nil, invalidInput("检查入口", "当前版本尚不能导航这个动作，不能附带网站或分组编号，请只保留动作名称和消息正文")
			}
		}
	}
	raw, err := json.Marshal(input.Content)
	if err != nil {
		return nil, invalidInput("全部内容", "文字内容无法按安全格式保存，请核查消息字段")
	}
	if len(raw) > 32*1024 {
		return nil, invalidInput("全部内容", "超过32KiB（32768字节）的上限，请缩短显示文字，保留完整业务编号")
	}
	return raw, nil
}

// AppendTx joins the caller's business transaction. It never commits or calls out.
func (s *Service) AppendTx(ctx context.Context, tx pgx.Tx, input NoticeInput) (string, bool, error) {
	return s.repository.AppendTx(ctx, tx, input)
}

func (s *Service) Publish(ctx context.Context, input NoticeInput) (id string, inserted bool, resultErr error) {
	if _, err := validate(input); err != nil {
		return "", false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.repository.db.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("开始保存这条通知失败：%s；本次没有保存，处理原因后可使用原事件编号重试", safeStorageCause(err))
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		if err := tx.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			resultErr = errors.New("通知保存或回滚结果尚未确认，请核查这条事件后使用同一事件编号重试")
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return "", false, fmt.Errorf("准备保存这条通知失败：%s；本次没有保存，处理原因后可使用原事件编号重试", safeStorageCause(err))
	}
	id, inserted, err = s.AppendTx(ctx, tx, input)
	if err != nil {
		return "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, errors.New("通知提交结果尚未确认，请核查这条事件后使用同一事件编号重试")
	}
	return id, inserted, nil
}
