package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

type globalizationTransport func(*http.Request) (*http.Response, error)

func (f globalizationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type globalizationNotificationRepository struct{ saved int }

func (*globalizationNotificationRepository) GetNotificationChannels(context.Context, string, string) (NotificationChannelSettings, error) {
	return DefaultNotificationChannelSettings(), nil
}
func (r *globalizationNotificationRepository) SaveNotificationChannels(context.Context, string, string, NotificationChannelSettings) error {
	r.saved++
	return nil
}

type globalizationAccountResolver struct{}

func (globalizationAccountResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "fixture-workspace", nil
}

func TestGlobalizationRetiredNotificationChannelsRejectWithoutOutboundRequests(t *testing.T) {
	for _, channel := range []string{"dingtalk", "wecom", "qq", "feishu"} {
		t.Run(channel, func(t *testing.T) {
			calls := 0
			svc := NewService(&http.Client{Transport: globalizationTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"errcode":0,"code":0}`))}, nil
			})}, nil)
			err := svc.TestNotification(context.Background(), TestNotificationRequest{Channel: NotificationChannel(channel)})
			if !errors.Is(err, ErrInvalidNotificationChannel) || calls != 0 {
				t.Fatalf("retired channel must be rejected before transport: error=%v, outbound=%d", err, calls)
			}
		})
	}
}

func TestGlobalizationLegacySettingsExposeOnlyOriginalTelegramRecipients(t *testing.T) {
	for _, raw := range []string{
		`{"dingtalk":[],"wecom":[],"qq":[],"feishu":[],"telegram":[{"id":"original-tg","name":"existing","enabled":true,"botToken":"fixture","chatId":"fixture-chat"}]}`,
		`{"dingtalk":{"enabled":true,"webhook":"https://fixture.invalid"},"feishu":{"enabled":true,"webhook":"https://fixture.invalid"},"telegram":{"id":"original-tg","enabled":true,"botToken":"fixture","chatId":"fixture-chat"}}`,
	} {
		channels := DefaultNotificationChannelSettings()
		if err := unmarshalNotificationChannelSettings([]byte(raw), &channels); err != nil {
			t.Fatal(err)
		}
		if len(channels.Telegram) != 1 || channels.Telegram[0].ID != "original-tg" {
			t.Fatal("legacy Telegram recipient must retain its ID")
		}
		payload, err := json.Marshal(channels)
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal(payload, &output); err != nil {
			t.Fatal(err)
		}
		if len(output) != 1 || output["telegram"] == nil {
			t.Fatal("notification API must expose only Telegram")
		}
	}
}

func TestGlobalizationSaveRejectsRetiredChannelFieldsBeforePersistence(t *testing.T) {
	repo := &globalizationNotificationRepository{}
	svc := NewService(nil, nil)
	svc.notificationRepo = repo
	svc.SetAdminAccountResolver(globalizationAccountResolver{})
	h := &Handler{service: svc}
	r := httptest.NewRequest(http.MethodPut, "/api/settings/notification-channels", strings.NewReader(`{"dingtalk":[],"telegram":[]}`))
	r = r.WithContext(authctx.WithUserID(r.Context(), "fixture-user"))
	w := httptest.NewRecorder()
	h.saveNotificationChannels(w, r)
	if w.Code != http.StatusBadRequest || repo.saved != 0 {
		t.Fatalf("retired channel field must fail validation, got HTTP %d", w.Code)
	}
}
