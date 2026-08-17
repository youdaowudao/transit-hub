package settings

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type settingsStaticResolver struct {
	addresses []net.IPAddr
}

func (r settingsStaticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

func TestResolveSafeSettingsIPRejectsNonPublicAddresses(t *testing.T) {
	for name, address := range map[string]string{
		"private IPv4":       "192.168.1.10",
		"loopback IPv4":      "127.0.0.1",
		"CGNAT IPv4":         "100.127.0.1",
		"protocol IPv4":      "192.0.0.8",
		"benchmark IPv4":     "198.19.255.254",
		"documentation IPv4": "198.51.100.1",
		"loopback IPv6":      "::1",
		"private IPv6":       "fd00::10",
		"site-local IPv6":    "fec0::10",
		"documentation IPv6": "2001:db8::10",
	} {
		t.Run(name, func(t *testing.T) {
			resolver := settingsStaticResolver{addresses: []net.IPAddr{{IP: net.ParseIP(address)}}}
			if _, err := resolveSafeSettingsIP(context.Background(), resolver, "notify.example.com"); err == nil {
				t.Fatalf("resolved address %s was accepted", address)
			}
		})
	}
}

func TestSettingsServiceUsesBoundedSafeDefaults(t *testing.T) {
	client := NewService(nil, nil).client
	if client.Timeout <= 0 || client.Timeout > time.Minute {
		t.Fatalf("client.Timeout = %s, want a positive timeout no greater than one minute", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("safe transport must not use environment proxies")
	}
	if client.CheckRedirect == nil || client.CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("safe settings client must reject redirects")
	}
}

func TestNotificationWebhookDoesNotReachLocalTarget(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	err := NewService(nil, nil).TestNotification(context.Background(), TestNotificationRequest{
		Channel: NotificationChannelWecom,
		Webhook: server.URL,
	})
	if err == nil {
		t.Fatal("TestNotification() accepted a loopback webhook")
	}
	if requests != 0 {
		t.Fatalf("local webhook received %d requests, want 0", requests)
	}
}

func TestTelegramProxyDoesNotReachLocalTarget(t *testing.T) {
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)

	err := NewService(nil, nil).TestNotification(context.Background(), TestNotificationRequest{
		Channel:          NotificationChannelTelegram,
		TelegramBotToken: "bot-token",
		TelegramChatID:   "chat-id",
		TelegramProxyURL: proxy.URL,
	})
	if err == nil {
		t.Fatal("TestNotification() accepted a loopback Telegram proxy")
	}
	if requests != 0 {
		t.Fatalf("local proxy received %d requests, want 0", requests)
	}
}
