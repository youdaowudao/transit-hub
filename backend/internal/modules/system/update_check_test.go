package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/config"
)

func TestVersionUpdateAvailableDefault(t *testing.T) {
	service := newServiceWithExecutors(config.Config{AppVersion: "V2.9.11"}, nil, nil, nil)
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "V2.9.11" {
		t.Fatalf("version = %v, want V2.9.11", body["version"])
	}
	available, exists := body["updateAvailable"]
	if !exists || available != false {
		t.Fatalf("updateAvailable exists=%t value=%v, want existing false", exists, available)
	}
}

func TestVersionUpdateAvailableAfterCheck(t *testing.T) {
	service := newServiceWithExecutors(config.Config{AppVersion: "V2.9.11"}, nil, nil, nil)
	service.updateChecker.fetch = func(context.Context) (string, error) {
		return `defaultAppVersion = "V2.9.12"`, nil
	}
	service.updateChecker.checkOnce(context.Background())
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "V2.9.11" || body["updateAvailable"] != true {
		t.Fatalf("response = %v, want original version and updateAvailable=true", body)
	}
	if len(body) != 2 {
		t.Fatalf("response has extra fields: %v", body)
	}
}

func TestAppVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"V2.9.12", "V2.9.11", true},
		{"V2.10.0", "V2.9.99", true},
		{"V3.0.0", "V2.99.99", true},
		{"V2.9.11", "V2.9.11", false},
		{"V2.9.10", "V2.9.11", false},
		{"V2.8.99", "V2.9.0", false},
		{"V1.99.99", "V2.0.0", false},
	} {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			a, okA := parseAppVersion(tc.a)
			b, okB := parseAppVersion(tc.b)
			if !okA || !okB || versionGreater(a, b) != tc.want {
				t.Fatalf("comparison %s > %s = %t, want %t", tc.a, tc.b, versionGreater(a, b), tc.want)
			}
		})
	}
	for _, invalid := range []string{"", "v2.9.11", "2.9.11", "V2.9", "V2.9.11-beta", " V2.9.11", "V2.9.11\n", "V999999999999999999999999.9.11"} {
		if _, ok := parseAppVersion(invalid); ok {
			t.Errorf("accepted invalid current version %q", invalid)
		}
	}
}

func TestExtractRemoteAppVersion(t *testing.T) {
	for _, tc := range []struct {
		name, source, version string
		want                  [3]int
		ok                    bool
	}{
		{"const block", "package config\nconst (\n\tdefaultPort = \"10621\"\n\tdefaultAppVersion = \"V2.9.11\"\n)\n", "V2.9.11", [3]int{2, 9, 11}, true},
		{"comment before declaration", "// defaultAppVersion = \"V9.9.9\"\n\tdefaultAppVersion = \"V2.9.11\"\n", "V2.9.11", [3]int{2, 9, 11}, true},
		{"first match", "defaultAppVersion = \"V2.9.11\"\ndefaultAppVersion = \"V9.9.9\"", "V2.9.11", [3]int{2, 9, 11}, true},
		{"missing", "package config", "", [3]int{}, false},
		{"lowercase", `defaultAppVersion = "v2.9.11"`, "", [3]int{}, false},
		{"no prefix", `defaultAppVersion = "2.9.11"`, "", [3]int{}, false},
		{"comment only", `// defaultAppVersion = "V9.9.9"`, "", [3]int{}, false},
		{"not line start", `other = defaultAppVersion = "V9.9.9"`, "", [3]int{}, false},
		{"overflow", `defaultAppVersion = "V999999999999999999999999.9.11"`, "", [3]int{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, version, ok := extractRemoteAppVersion(tc.source)
			if parts != tc.want || version != tc.version || ok != tc.ok {
				t.Fatalf("got %v %q %t, want %v %q %t", parts, version, ok, tc.want, tc.version, tc.ok)
			}
		})
	}
}

func TestUpdateCheckPreservesLastSuccess(t *testing.T) {
	checker := &updateChecker{current: "V2.9.11"}
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	for _, tc := range []struct {
		name, source string
		err          error
		want         bool
		logText      string
	}{
		{"newer", `defaultAppVersion = "V2.9.12"`, nil, true, "[system] update check ok current=V2.9.11 latest=V2.9.12 available=true"},
		{"fetch fails after true", "", errors.New("offline"), true, "[system] update check failed: offline"},
		{"format fails after true", `defaultAppVersion = "v2.9.12"`, nil, true, "[system] update check failed: remote version not found"},
		{"equal", `defaultAppVersion = "V2.9.11"`, nil, false, "[system] update check ok current=V2.9.11 latest=V2.9.11 available=false"},
		{"fetch fails after false", "", errors.New("offline"), false, "[system] update check failed: offline"},
		{"older", `defaultAppVersion = "V2.9.10"`, nil, false, "[system] update check ok current=V2.9.11 latest=V2.9.10 available=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			calls := 0
			checker.fetch = func(ctx context.Context) (string, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > updateCheckTimeout {
					t.Fatal("fetch context must have a single 15-second deadline")
				}
				return tc.source, tc.err
			}
			checker.checkOnce(context.Background())
			if checker.Available() != tc.want || calls != 1 {
				t.Fatalf("available=%t calls=%d, want %t and 1", checker.Available(), calls, tc.want)
			}
			if strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), tc.logText) {
				t.Fatalf("want one log containing %q, got %q", tc.logText, logs.String())
			}
		})
	}
}

func TestUpdateCheckBeforeFirstSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, current, source string
		err                   error
		wantCalls             int
	}{
		{"network failure", "V2.9.11", "", errors.New("offline"), 1},
		{"deadline failure", "V2.9.11", "", context.DeadlineExceeded, 1},
		{"invalid remote", "V2.9.11", "no version", nil, 1},
		{"invalid current", "invalid", `defaultAppVersion = "V2.9.12"`, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			checker := &updateChecker{current: tc.current, fetch: func(context.Context) (string, error) {
				calls++
				return tc.source, tc.err
			}}
			checker.checkOnce(context.Background())
			if checker.Available() || calls != tc.wantCalls {
				t.Fatalf("available=%t calls=%d, want false and %d", checker.Available(), calls, tc.wantCalls)
			}
		})
	}
	checker := &updateChecker{current: "V2.9.11"}
	checker.checkOnce(context.Background())
	if checker.Available() {
		t.Fatal("nil fetch must leave available=false")
	}
}

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type updateResponseBody struct {
	io.Reader
	closed bool
}

func (b *updateResponseBody) Close() error { b.closed = true; return nil }

func TestHTTPUpdateFetch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		err    bool
	}{
		{"404", http.StatusNotFound, "not found", "", true},
		{"200", http.StatusOK, `defaultAppVersion = "V2.9.12"`, `defaultAppVersion = "V2.9.12"`, false},
		{"bounded body", http.StatusOK, strings.Repeat("x", updateCheckMaxBytes) + "excess", strings.Repeat("x", updateCheckMaxBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &updateResponseBody{Reader: strings.NewReader(tc.body)}
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "test context")
			client := &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != updateCheckURL || r.Context().Value(contextKey{}) != "test context" {
					t.Fatal("GET must use the supplied URL and context")
				}
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			})}
			got, err := newHTTPUpdateFetch(client, updateCheckURL)(ctx)
			if (err != nil) != tc.err || got != tc.want || !body.closed {
				t.Fatalf("bytes=%d err=%v closed=%t, want bytes=%d error=%t closed=true", len(got), err, body.closed, len(tc.want), tc.err)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &http.Client{Transport: updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		})}
		_, err := newHTTPUpdateFetch(client, updateCheckURL)(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation error, got %v", err)
		}
	})
}
