package system

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	updateCheckURL      = "https://raw.githubusercontent.com/youdaowudao/transit-hub/main/backend/internal/config/config.go"
	updateCheckInterval = 8 * time.Hour
	updateCheckTimeout  = 15 * time.Second
	updateCheckMaxBytes = 256 * 1024
)

var (
	appVersionPattern       = regexp.MustCompile(`^V(\d+)\.(\d+)\.(\d+)$`)
	remoteAppVersionPattern = regexp.MustCompile(`(?m)^\s*defaultAppVersion\s*=\s*"V(\d+)\.(\d+)\.(\d+)"`)
)

type updateFetchFunc func(ctx context.Context) (string, error)

type updateChecker struct {
	current   string
	fetch     updateFetchFunc // nil 时不取数，供未启动检查的服务和测试使用。
	mu        sync.RWMutex
	available bool
}

func parseAppVersion(s string) ([3]int, bool) {
	return parseVersionParts(appVersionPattern.FindStringSubmatch(s))
}

func parseVersionParts(matches []string) ([3]int, bool) {
	var parts [3]int
	if len(matches) != 4 {
		return parts, false
	}
	for i := range parts {
		value, err := strconv.Atoi(matches[i+1])
		if err != nil {
			return [3]int{}, false
		}
		parts[i] = value
	}
	return parts, true
}

func extractRemoteAppVersion(source string) ([3]int, string, bool) {
	matches := remoteAppVersionPattern.FindStringSubmatch(source)
	parts, ok := parseVersionParts(matches)
	if !ok {
		return [3]int{}, "", false
	}
	return parts, "V" + matches[1] + "." + matches[2] + "." + matches[3], true
}

func versionGreater(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func newHTTPUpdateFetch(client *http.Client, url string) updateFetchFunc {
	return func(ctx context.Context) (string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP status %d", response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, updateCheckMaxBytes))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

func (c *updateChecker) checkOnce(ctx context.Context) {
	if c.fetch == nil {
		return
	}
	current, ok := parseAppVersion(c.current)
	if !ok {
		log.Printf("[system] update check failed: current version unparsable")
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	source, err := c.fetch(checkCtx)
	if err != nil {
		log.Printf("[system] update check failed: %v", err)
		return
	}
	remote, latest, ok := extractRemoteAppVersion(source)
	if !ok {
		log.Printf("[system] update check failed: remote version not found")
		return
	}
	available := versionGreater(remote, current)
	c.mu.Lock()
	c.available = available
	c.mu.Unlock()
	log.Printf("[system] update check ok current=%s latest=%s available=%t", c.current, latest, available)
}

func (c *updateChecker) Available() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.available
}
