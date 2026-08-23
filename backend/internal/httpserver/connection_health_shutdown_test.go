package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/connection_health"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type shutdownRefreshMySites struct{}

func (shutdownRefreshMySites) ListRealConnections(context.Context, string) ([]my_sites.RealConnection, error) {
	return shutdownRefreshConnections(), nil
}

func (shutdownRefreshMySites) ListRealConnectionsForWorkspace(context.Context, string, string) ([]my_sites.RealConnection, error) {
	return shutdownRefreshConnections(), nil
}

func (shutdownRefreshMySites) MappingOptions(context.Context, string) (my_sites.MappingOptionsResponse, error) {
	return my_sites.MappingOptionsResponse{}, nil
}

func (shutdownRefreshMySites) RequireSession(context.Context, string, string) (upstream.Session, error) {
	return upstream.Session{Platform: upstream.PlatformSub2API}, nil
}

func shutdownRefreshConnections() []my_sites.RealConnection {
	return []my_sites.RealConnection{{
		UserID: "user1", WorkspaceAdminAccountID: "ws1", AdminAccountID: "account-1",
		AdminPlatform: string(upstream.PlatformSub2API), UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
	}}
}

type shutdownRefreshResolver struct{}

func (shutdownRefreshResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "ws1", nil
}

type shutdownRefreshSiteLookup struct{}

func (shutdownRefreshSiteLookup) GetSite(context.Context, string) (*upstream.Site, error) {
	enabled := true
	return &upstream.Site{
		ID: "site-1", UserID: "user1", AdminAccountID: "ws1", Platform: upstream.PlatformSub2API,
		Enabled: &enabled, Session: &upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "test-only"},
	}, nil
}

type shutdownRefreshPlatformReader struct{}

func (shutdownRefreshPlatformReader) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{}, nil
}

func (shutdownRefreshPlatformReader) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	return nil, nil
}

func (shutdownRefreshPlatformReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	return upstream.ProbeCredential{}, nil
}

type shutdownBlockingSync struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newShutdownBlockingSync() *shutdownBlockingSync {
	return &shutdownBlockingSync{started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (s *shutdownBlockingSync) SyncSites(ctx context.Context, _, _ string, _ []string, _ bool) []upstream.SyncSiteResult {
	s.started <- struct{}{}
	select {
	case <-ctx.Done():
		return []upstream.SyncSiteResult{{SiteID: "site-1", Status: "unavailable", ErrorKey: "site_sync_cancelled"}}
	case <-s.release:
		return []upstream.SyncSiteResult{{SiteID: "site-1", Status: "success"}}
	}
}

func (s *shutdownBlockingSync) unblock() { s.once.Do(func() { close(s.release) }) }

func readShutdownTerminal(body io.Reader) (map[string]any, error) {
	reader := bufio.NewReader(body)
	for {
		var eventName string
		var data string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if eventName != "terminal" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
}

func TestServerShutdownTerminatesActiveConnectionHealthRefresh(t *testing.T) {
	syncer := newShutdownBlockingSync()
	t.Cleanup(syncer.unblock)
	service := connection_health.NewService(nil, shutdownRefreshMySites{}, shutdownRefreshSiteLookup{}, nil)
	service.SetAdminAccountResolver(shutdownRefreshResolver{})
	service.SetPlatformGroupReader(shutdownRefreshPlatformReader{})
	service.SetUpstreamSyncCoordinator(syncer)

	mux := http.NewServeMux()
	connection_health.RegisterRoutes(mux, service)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(authctx.WithUserID(r.Context(), "user1"))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	server := &Server{connectionHealthService: service}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/connection-health/admin-groups/refresh", nil)
	if err != nil {
		t.Fatalf("new refresh request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	responses := make(chan *http.Response, 1)
	requestErrors := make(chan error, 1)
	go func() {
		response, requestErr := httpServer.Client().Do(request)
		if requestErr != nil {
			requestErrors <- requestErr
			return
		}
		responses <- response
	}()

	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach the blocked site sync")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Server.Shutdown() error = %v", err)
	}

	select {
	case err := <-requestErrors:
		t.Fatalf("refresh request after shutdown: %v", err)
	case response := <-responses:
		defer response.Body.Close()
		if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("shutdown refresh content type = %q, body=%s", response.Header.Get("Content-Type"), body)
		}
		terminal, err := readShutdownTerminal(response.Body)
		if err != nil {
			t.Fatalf("read shutdown terminal: %v", err)
		}
		if terminal["status"] != "failed" {
			t.Fatalf("shutdown terminal = %#v, want failed", terminal)
		}
		if terminal["errorKey"] == "" {
			t.Fatalf("shutdown terminal = %#v, want safe errorKey", terminal)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("actual Server.Shutdown left the active refresh request blocked")
	}

	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	if err := server.Shutdown(secondCtx); err != nil {
		t.Fatalf("second Server.Shutdown() error = %v", err)
	}
}
