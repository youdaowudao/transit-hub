package tickets

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type staticIPResolver struct {
	addresses []net.IPAddr
	err       error
}

func (r staticIPResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, r.err
}

func TestNormalizeSrcHostRejectsLocalhost(t *testing.T) {
	for _, value := range []string{"localhost", "http://localhost:8080", "127.0.0.1", "[::1]"} {
		t.Run(value, func(t *testing.T) {
			if _, err := normalizeSrcHost(value); err == nil {
				t.Fatalf("normalizeSrcHost(%q) accepted a local target", value)
			}
		})
	}
}

func TestResolveSafeTicketIPRejectsNonPublicAddresses(t *testing.T) {
	tests := map[string]string{
		"private IPv4":       "10.0.0.8",
		"loopback IPv4":      "127.0.0.1",
		"link-local IPv4":    "169.254.1.2",
		"CGNAT IPv4":         "100.64.0.1",
		"protocol IPv4":      "192.0.0.8",
		"benchmark IPv4":     "198.18.0.1",
		"documentation IPv4": "203.0.113.9",
		"loopback IPv6":      "::1",
		"private IPv6":       "fd00::1",
		"site-local IPv6":    "fec0::1",
		"documentation IPv6": "2001:db8::1",
	}
	for name, address := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := staticIPResolver{addresses: []net.IPAddr{{IP: net.ParseIP(address)}}}
			if _, err := resolveSafeTicketIP(context.Background(), resolver, "tickets.example.com"); err == nil {
				t.Fatalf("resolved address %s was accepted", address)
			}
		})
	}
}

func TestResolveSafeTicketIPReturnsPublicAddress(t *testing.T) {
	want := net.ParseIP("8.8.8.8")
	resolver := staticIPResolver{addresses: []net.IPAddr{{IP: want}}}
	got, err := resolveSafeTicketIP(context.Background(), resolver, "tickets.example.com")
	if err != nil {
		t.Fatalf("resolveSafeTicketIP() error = %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("resolveSafeTicketIP() = %s, want %s", got, want)
	}
}

func TestNewSub2APIClientUsesBoundedSafeDefaults(t *testing.T) {
	client := NewSub2APIClient(nil).client
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
	if client.CheckRedirect == nil {
		t.Fatal("safe client must reject redirects")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("safe client accepted a redirect")
	}
}

func TestSub2APIClientDoesNotSendBearerTokenToLocalTarget(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"42"}}`))
	}))
	t.Cleanup(server.Close)

	if _, err := NewSub2APIClient(nil).FetchCurrentUser(server.URL, "secret-token"); err == nil {
		t.Fatal("FetchCurrentUser() accepted a loopback target")
	}
	if requests != 0 {
		t.Fatalf("local server received %d requests, want 0", requests)
	}
}
