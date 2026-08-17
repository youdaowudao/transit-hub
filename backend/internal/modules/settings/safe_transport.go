package settings

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"transithub/backend/internal/shared/netguard"
)

const notificationRequestTimeout = 30 * time.Second

type settingsIPResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

func newSafeSettingsTransport(resolver settingsIPResolver) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	dialer := &net.Dialer{Timeout: notificationRequestTimeout, KeepAlive: notificationRequestTimeout}
	base.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip, err := resolveSafeSettingsIP(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return base
}

func resolveSafeSettingsIP(ctx context.Context, resolver settingsIPResolver, host string) (net.IP, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if ip := net.ParseIP(host); ip != nil {
		if isSafeSettingsDialIP(ip) {
			return ip, nil
		}
		return nil, errors.New("notification target ip is not public")
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if isSafeSettingsDialIP(address.IP) {
			return address.IP, nil
		}
	}
	return nil, errors.New("notification target resolved to no public addresses")
}

func isSafeSettingsDialIP(ip net.IP) bool {
	return netguard.IsPublicIP(ip)
}
