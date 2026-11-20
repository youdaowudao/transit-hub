package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func absorbTelegramTarget() TelegramTarget {
	return TelegramTarget{ID: "fixture-bot", Name: "Fixture bot", Enabled: true, botToken: "fixture-token:secret/part", chatID: "fixture-chat-secret"}
}

func TestAbsorbTelegramConfirmationResults(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body, want string
	}{
		{"confirmed", 200, `{"ok":true,"result":{"message_id":7}}`, DeliverySent},
		{"confirmed-created", 201, `{"ok":true,"result":{"message_id":8}}`, DeliverySent},
		{"confirmed-other-2xx", 299, `{"ok":true,"result":{"message_id":9}}`, DeliverySent},
		{"old-empty-success", 204, ``, DeliveryUnknown},
		{"missing-id", 200, `{"ok":true,"result":{}}`, DeliveryUnknown},
		{"zero-id", 200, `{"ok":true,"result":{"message_id":0}}`, DeliveryUnknown},
		{"negative-id", 200, `{"ok":true,"result":{"message_id":-1}}`, DeliveryUnknown},
		{"quoted-id", 200, `{"ok":true,"result":{"message_id":"7"}}`, DeliveryUnknown},
		{"fraction-id", 200, `{"ok":true,"result":{"message_id":1.5}}`, DeliveryUnknown},
		{"overflow-id", 200, `{"ok":true,"result":{"message_id":99999999999999999999999}}`, DeliveryUnknown},
		{"null-ok", 200, `{"ok":null,"result":{"message_id":7}}`, DeliveryUnknown},
		{"nonboolean-ok", 200, `{"ok":"true","result":{"message_id":7}}`, DeliveryUnknown},
		{"array", 200, `[]`, DeliveryUnknown},
		{"truncated", 200, `{"ok":true`, DeliveryUnknown},
		{"extra-json", 200, `{"ok":true,"result":{"message_id":7}} {}`, DeliveryUnknown},
		{"explicit-rejection", 429, `{"ok":false,"description":"Too many requests","error_code":429}`, DeliveryFailed},
		{"2xx-explicit-rejection", 200, `{"ok":false,"description":"Forbidden","error_code":403}`, DeliveryFailed},
		{"unconfirmed-client-error", 400, `{"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
		{"server-error-even-rejection", 503, `{"ok":false,"description":"Unavailable"}`, DeliveryUnknown},
		{"oversize", 200, strings.Repeat(" ", 64<<10) + `{"ok":true,"result":{"message_id":7}}`, DeliveryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("Telegram request contract changed")
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("strict sender lost its 15-second maximum")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			service := NewService(client, nil)
			result := service.SendFrozenTelegramWithResult(t.Context(), absorbTelegramTarget(), "local fixture message", NotificationTemplateFormatText)
			if result.Status != tc.want || !result.Attempted || calls != 1 {
				t.Fatalf("result=%+v calls=%d, want %s exactly once", result, calls, tc.want)
			}
			if tc.want == DeliveryUnknown && !strings.Contains(result.Reason, "Telegram") {
				t.Fatal("unknown delivery lacks a concrete follow-up")
			}
			if tc.name == "explicit-rejection" && (result.ErrorCode != 429 || !strings.Contains(result.Reason, "Too many requests")) {
				t.Fatal("safe Telegram description/error code was discarded")
			}
		})
	}
}

func TestAbsorbTelegramCanceledAndNetworkResults(t *testing.T) {
	t.Run("before-request", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: globalizationTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result := NewService(client, nil).SendFrozenTelegramWithResult(ctx, absorbTelegramTarget(), "message", NotificationTemplateFormatText)
		if result.Status != DeliveryFailed || result.Attempted || calls != 0 {
			t.Fatalf("pre-request cancel=%+v calls=%d", result, calls)
		}
	})
	for _, tc := range []struct {
		name      string
		err       error
		connected bool
		want      string
	}{
		{"dial", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fixture refused")}, false, DeliveryFailed},
		{"dns", &net.DNSError{Err: "fixture not found", Name: "fixture.invalid", IsNotFound: true}, false, DeliveryFailed},
		{"after-write", io.ErrUnexpectedEOF, true, DeliveryUnknown},
		{"unproven", context.DeadlineExceeded, false, DeliveryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				if tc.connected {
					trace := httptrace.ContextClientTrace(r.Context())
					trace.GotConn(httptrace.GotConnInfo{})
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				return nil, tc.err
			})}
			result := NewService(client, nil).SendFrozenTelegramWithResult(t.Context(), absorbTelegramTarget(), "message", NotificationTemplateFormatText)
			if result.Status != tc.want || !result.Attempted {
				t.Fatalf("result=%+v want=%s", result, tc.want)
			}
		})
	}
	t.Run("shutdown-cancels-in-flight", func(t *testing.T) {
		entered := make(chan struct{})
		done := make(chan DeliveryResult, 1)
		client := &http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			done <- NewService(client, nil).SendFrozenTelegramWithResult(ctx, absorbTelegramTarget(), "message", NotificationTemplateFormatText)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not begin")
		}
		cancel()
		select {
		case result := <-done:
			if result.Status != DeliveryUnknown || !result.Attempted {
				t.Fatalf("in-flight cancel=%+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("cancel did not release sender")
		}
	})
}

func TestAbsorbTelegramRedactsEncodedSecrets(t *testing.T) {
	target := absorbTelegramTarget()
	target.proxyURL = "http://fixture-proxy-user:fixture-proxy-password@proxy.fixture:8080"
	basic := base64.StdEncoding.EncodeToString([]byte("fixture-proxy-user:fixture-proxy-password"))
	secrets := []string{target.botToken, target.chatID, target.proxyURL, "fixture-proxy-user", "fixture-proxy-password", basic}
	for _, secret := range secrets {
		for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), url.QueryEscape(url.PathEscape(secret))} {
			for _, prefix := range []string{"", "Basic "} {
				message := "explicit refusal: " + prefix + encoded + " safe explanation"
				masked := redactTelegramDescription(message, target)
				if strings.Contains(masked, secret) || strings.Contains(masked, encoded) {
					t.Fatalf("credential encoding was not hidden for fixture kind %d", len(secret))
				}
				if !strings.Contains(masked, "safe explanation") {
					t.Fatal("safe Telegram reason was lost")
				}
			}
		}
	}
}

func TestAbsorbTelegramTargetReasonsAndOld2xxContract(t *testing.T) {
	target := absorbTelegramTarget()
	for _, tc := range []struct {
		name    string
		targets []TelegramTarget
		ids     []string
		reason  string
	}{
		{"not-selected", []TelegramTarget{target}, nil, "尚未选择"},
		{"multiple", []TelegramTarget{target}, []string{target.ID, target.ID}, "只允许一个"},
		{"deleted", nil, []string{target.ID}, "未找到"},
		{"duplicate", []TelegramTarget{target, target}, []string{target.ID}, "重复"},
		{"disabled", []TelegramTarget{{ID: target.ID}}, []string{target.ID}, "已停用"},
		{"missing-token", []TelegramTarget{{ID: target.ID, Enabled: true, chatID: "fixture"}}, []string{target.ID}, "Bot Token"},
		{"missing-chat", []TelegramTarget{{ID: target.ID, Enabled: true, botToken: "fixture"}}, []string{target.ID}, "Chat ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SelectTelegramTarget(tc.targets, tc.ids)
			if !errors.Is(err, ErrTelegramTargetUnavailable) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("known target failure=%v", err)
			}
		})
	}
	for _, proxy := range []string{"//proxy.fixture:8080", "http://proxy.fixture:8080", "https://proxy.fixture:8080", "socks5://proxy.fixture:8080", "socks5h://proxy.fixture:8080"} {
		candidate := target
		candidate.proxyURL = proxy
		if _, err := SelectTelegramTarget([]TelegramTarget{candidate}, []string{candidate.ID}); err != nil {
			t.Fatalf("existing proxy protocol rejected: %v", err)
		}
	}
	for _, status := range []int{200, 201, 204, 299} {
		client := &http.Client{Transport: globalizationTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("not Telegram JSON"))}, nil
		})}
		if err := NewService(client, nil).postJSONWithClient(t.Context(), client, "https://fixture.invalid", map[string]string{"text": "fixture"}); err != nil {
			t.Fatalf("old %d contract changed: %v", status, err)
		}
	}
}

func TestAbsorbTelegramTargetsTrimOnlyFrozenCredentials(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	type fixture struct {
		name, id, token, chat, proxy, reason string
		enabled                              bool
	}
	cases := []fixture{
		{"no-proxy", "bot-a", " \tfixture-token:part \r\n", " \tfixture-chat \n", "", "", true},
		{"blank-proxy", "bot-a", " fixture-token:part ", " fixture-chat ", " \t\r\n ", "", true},
		{"disabled", "bot-a", " fixture-token:part ", " fixture-chat ", "", "已停用", false},
		{"blank-token", "bot-a", " \t\n ", " fixture-chat ", "", "Bot Token", true},
		{"blank-chat", "bot-a", " fixture-token:part ", " \t\n ", "", "Chat ID", true},
		{"strict-id", " bot-a ", " fixture-token:part ", " fixture-chat ", "", "未找到", true},
	}
	for _, protocol := range []string{"http:", "https:", "socks5:", "socks5h:", ""} {
		cases = append(cases, fixture{"proxy-" + protocol, "bot-a", " fixture-token:part ", " fixture-chat ", " \t" + protocol + "//fixture%20user:pa%3Ass%20word@proxy.fixture:8080 \n", "", true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channels := NotificationChannelSettings{Telegram: []TelegramChannelSettings{{ID: tc.id, Name: " unchanged name ", Enabled: tc.enabled, BotToken: tc.token, ChatID: tc.chat, ProxyURL: tc.proxy}}}
			raw, err := json.Marshal(channels)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE notification_channel_settings SET settings=$1::jsonb`, string(raw)); err != nil {
				t.Fatal(err)
			}
			var before, after []byte
			if err := pool.QueryRow(t.Context(), `SELECT settings FROM notification_channel_settings`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			calls := 0
			service := NewService(&http.Client{Transport: globalizationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.EscapedPath() != "/bot"+url.PathEscape(strings.TrimSpace(tc.token))+"/sendMessage" {
					t.Error("frozen request did not use normalized bot token")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["chat_id"] != strings.TrimSpace(tc.chat) {
					t.Error("frozen request did not use normalized chat ID")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Request: r}, nil
			})}, NewRepository(pool))
			for _, locked := range []bool{false, true} {
				targets, err := func() ([]TelegramTarget, error) {
					if !locked {
						return service.TelegramTargets(t.Context(), pool, scope.UserID, scope.AdminAccountID, false)
					}
					tx, err := pool.Begin(t.Context())
					if err != nil {
						return nil, err
					}
					defer tx.Rollback(t.Context())
					return service.TelegramTargets(t.Context(), tx, scope.UserID, scope.AdminAccountID, true)
				}()
				if err != nil || len(targets) != 1 {
					t.Fatal("cannot read exact fixture target")
				}
				target := targets[0]
				if target.ID != tc.id || target.Name != " unchanged name " {
					t.Error("credential normalization changed strict ID or name")
				}
				if target.botToken != strings.TrimSpace(tc.token) || target.chatID != strings.TrimSpace(tc.chat) || target.proxyURL != strings.TrimSpace(tc.proxy) {
					t.Error("frozen credentials did not reuse legacy in-memory trimming")
				}
				selected, err := SelectTelegramTarget(targets, []string{"bot-a"})
				if tc.reason != "" {
					if err == nil || !strings.Contains(err.Error(), tc.reason) {
						t.Error("strict target selection changed disabled/missing/ID behavior")
					}
					continue
				}
				if err != nil {
					t.Error("usable whitespace-padded credentials were rejected")
					continue
				}
				if selected.proxyURL == "" {
					result := service.SendFrozenTelegramWithResult(t.Context(), selected, "isolated fixture message", NotificationTemplateFormatText)
					if result.Status != DeliverySent || !result.Attempted {
						t.Error("frozen normalized credentials did not reach the in-memory transport")
					}
				} else {
					// Inspect the real proxy selection without opening a connection or sending a message.
					transport, ok := service.telegramClient(selected.proxyURL).Transport.(*http.Transport)
					if !ok || transport.Proxy == nil {
						t.Fatal("proxy transport contract changed")
					}
					proxy, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.telegram.org"}})
					if err != nil || proxy.String() != strings.TrimSpace(tc.proxy) {
						t.Error("normalized proxy address or encoded credentials changed")
					}
					if proxy.User == nil {
						t.Error("proxy credentials were dropped")
					} else {
						password, _ := proxy.User.Password()
						if proxy.User.Username() != "fixture user" || password != "pa:ss word" {
							t.Error("normalization changed spaces/colon inside proxy credentials")
						}
					}
				}
			}
			expectedCalls := 0
			if tc.reason == "" && strings.TrimSpace(tc.proxy) == "" {
				expectedCalls = 2
			}
			if calls != expectedCalls {
				t.Errorf("in-memory requests=%d want=%d", calls, expectedCalls)
			}
			if err := pool.QueryRow(t.Context(), `SELECT settings FROM notification_channel_settings`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("frozen credential read rewrote stored channel JSON")
			}
		})
	}
}

func TestAbsorbTelegramRedactionCoversEncodedOverlaps(t *testing.T) {
	proxyBasic := base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
	proxyTarget := TelegramTarget{botToken: proxyBasic[:7], chatID: "fixture-chat", proxyURL: "http://proxy-user:proxy-password@proxy.fixture:8080"}
	for _, tc := range []struct {
		name       string
		target     TelegramTarget
		value      string
		remainders []string
	}{
		{"query-plus-collision", TelegramTarget{botToken: "a b", chatID: "a+b-TAIL-7"}, "a+b-TAIL-7", []string{"TAIL-7"}},
		{"overlapping-occurrences", TelegramTarget{botToken: "ABA", chatID: "ABABA"}, "ABABABA", []string{"ABA", "BA"}},
		{"unicode-crossing", TelegramTarget{botToken: "密钥", chatID: "钥尾部"}, "密钥尾部", []string{"密钥", "尾部"}},
		{"bare-basic-contains-token", proxyTarget, proxyBasic, []string{proxyBasic[:7], proxyBasic[7:]}},
		{"encoded-proxy-password", TelegramTarget{botToken: "UPPER/", chatID: "fixture-chat", proxyURL: "http://proxy%20user:UPPER%2Fproxy%20user%3ATAIL@proxy.fixture:8080"}, "UPPER/proxy user:TAIL", []string{"UPPER", "proxy user", "TAIL"}},
		{"encoded-proxy-url", proxyTarget, proxyTarget.proxyURL, []string{"proxy-user", "proxy-password", "proxy.fixture"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			variants := []string{tc.value, url.QueryEscape(tc.value), url.PathEscape(tc.value), url.QueryEscape(url.QueryEscape(tc.value)), url.QueryEscape(url.PathEscape(tc.value))}
			for _, encoded := range append(append([]string{}, variants...), lowerTelegramFixtureEscapes(variants)...) {
				for _, prefix := range []string{"", "Basic ", "Bearer ", "password="} {
					masked := redactTelegramDescription("refusal "+prefix+encoded+" safe explanation", tc.target)
					if !utf8.ValidString(masked) || !strings.Contains(masked, "safe explanation") || !strings.Contains(masked, "[已隐藏]") {
						t.Fatal("redaction lost safe text, Unicode validity or masking")
					}
					for _, remainder := range tc.remainders {
						if strings.Contains(masked, remainder) {
							t.Fatal("overlapping or encoded credential remainder escaped masking")
						}
					}
				}
			}
		})
	}
}

func lowerTelegramFixtureEscapes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, telegramPercentEscapes.ReplaceAllStringFunc(value, strings.ToLower))
	}
	return result
}
