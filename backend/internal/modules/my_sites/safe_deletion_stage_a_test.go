package my_sites

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type stageATestSafeDeletion struct {
	apply   func(context.Context, string, string, upstream.Session, string, string) error
	calls   int
	source  string
	pending map[string]string
}

func (d *stageATestSafeDeletion) DeleteManagedSub2APIAccount(ctx context.Context, user, workspace string, session upstream.Session, id, source string) error {
	d.calls++
	d.source = source
	if d.apply != nil {
		return d.apply(ctx, user, workspace, session, id, source)
	}
	return nil
}

func TestStageACompensationRejectedOrUnknownPreservesResources(t *testing.T) {
	for _, outcome := range []string{"rejected", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			service, repo, deletes := stageAResourceService(t)
			repo.saveErr = errors.New("persist failed")
			safe := &stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error {
				return errors.New(outcome)
			}}
			service.SetSafeAdminAccountDeletion(safe)
			_, err := service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}})
			var pending *ManagedResourcePendingError
			if !errors.As(err, &pending) || pending.AdminResourceID != "22" || pending.UpstreamKeyID != "11" || strings.Contains(err.Error(), "synthetic-test-key") {
				t.Fatal("compensation failure did not return safe resource IDs")
			}
			if safe.calls != 1 || safe.source != "compensate_delete" || *deletes != 0 || repo.connection != nil {
				t.Fatal("rejected compensation deleted remote resources or persisted local connection")
			}
		})
	}
}

func TestStageAFullDeleteRejectedOrUnknownPreservesKeyAndLocalConnection(t *testing.T) {
	for _, outcome := range []string{"rejected", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			service, repo, deletes := stageAResourceService(t)
			repo.connection = &RealConnection{ID: "connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", AdminAccountID: "22", UpstreamKeyID: "11", Status: ConnectionStatusActive, ProvisioningMode: ProvisioningModeManaged, AdminPlatform: string(upstream.PlatformSub2API), UpstreamPlatform: string(upstream.PlatformSub2API)}
			safe := &stageATestSafeDeletion{apply: func(context.Context, string, string, upstream.Session, string, string) error {
				return errors.New(outcome)
			}}
			service.SetSafeAdminAccountDeletion(safe)
			err := service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: "connection", Mode: "full"})
			var pending *ManagedResourcePendingError
			if !errors.As(err, &pending) || safe.calls != 1 || safe.source != "manual_delete" || *deletes != 0 || repo.connection == nil || repo.deleteCalls != 0 {
				t.Fatal("unsafe full delete lost key or local connection")
			}
		})
	}
}

func TestStageAUnlinkPreservesRemotePending(t *testing.T) {
	service, repo, deletes := stageAResourceService(t)
	repo.connection = &RealConnection{ID: "connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", AdminAccountID: "22", UpstreamKeyID: "11", Status: ConnectionStatusActive, ProvisioningMode: ProvisioningModeManaged}
	safe := &stageATestSafeDeletion{pending: map[string]string{"22": "persistent-uncertain-dispatch"}, apply: func(context.Context, string, string, upstream.Session, string, string) error {
		t.Fatal("unlink entered remote pending mutation")
		return nil
	}}
	service.SetSafeAdminAccountDeletion(safe)
	if err := service.RealDisconnect(t.Context(), "user-1", RealDisconnectRequest{ConnectionID: "connection", Mode: "unlink"}); err != nil {
		t.Fatal(err)
	}
	if safe.calls != 0 || *deletes != 0 || safe.pending["22"] != "persistent-uncertain-dispatch" || repo.deleteCalls != 1 || repo.connection != nil {
		t.Fatal("unlink changed remote protection or did not unlink local row")
	}
}

func stageAResourceService(t *testing.T) (*Service, *testConnRepo, *int) {
	t.Helper()
	deletes := new(int)
	transport := stageAResourceTransport(func(req *http.Request) (*http.Response, error) {
		payload := `{"data":{"role":"admin"}}`
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/auth/me":
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/admin/groups":
			payload = `{"data":[{"id":7,"name":"vip","platform":"openai","status":"active"}]}`
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/keys":
			payload = `{"data":{"id":11,"key":"synthetic-test-key"}}`
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/admin/accounts":
			payload = `{"data":{"id":22}}`
		case req.Method == http.MethodDelete:
			*deletes++
			payload = `{"data":{"message":"Account deleted successfully"}}`
		default:
			t.Fatalf("unexpected fake request %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	})
	session := platformTestSession(upstream.PlatformSub2API, "http://127.0.0.1:8080")
	states := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: session, Mappings: []GroupMapping{}}}
	repo := &testConnRepo{stateRepo: states}
	lookup := testUpstreamLookup{sites: map[string]*upstream.Site{"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "source", BaseURL: session.BaseURL, Platform: upstream.PlatformSub2API, Session: &session, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "7", Name: "vip", Platform: stringPointer("openai")}}}}}}
	service := NewService(states, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
	service.connRepository = repo
	return service, repo, deletes
}

func TestStageACreateFailureCleansOnlyProvenAbsentResources(t *testing.T) {
	for _, sample := range []struct {
		name         string
		groupID      string
		status       int
		body         string
		transportErr error
		wantCleanup  bool
		noKey        bool
	}{
		{name: "local-validation", groupID: "not-numeric", noKey: true},
		{name: "dial-not-sent", transportErr: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, wantCleanup: true},
		{name: "dns-not-sent", transportErr: &net.DNSError{Err: "not found", Name: "fixture.invalid"}, wantCleanup: true},
		{name: "middleware-unauthorized", status: 401, body: `{"code":"UNAUTHORIZED"}`, wantCleanup: true},
		{name: "middleware-forbidden", status: 403, body: `{"code":"FORBIDDEN"}`, wantCleanup: true},
		{name: "unproven-400", status: 400, body: `{"message":"could have applied"}`},
		{name: "server-unknown", status: 500, body: `{"code":"INTERNAL_ERROR"}`},
		{name: "written-disconnect", transportErr: io.EOF},
		{name: "invalid-receipt", status: 200, body: `not-json`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service, repo, _ := stageAResourceService(t)
			groupID := sample.groupID
			if groupID == "" {
				groupID = "7"
			}
			keyDeletes, accountWrites, keyCreates := 0, 0, 0
			service.platformService = upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: stageAResourceTransport(func(req *http.Request) (*http.Response, error) {
				status, payload := 200, `{"data":{"role":"admin"}}`
				switch {
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/auth/me":
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/admin/groups":
					payload = `{"data":[{"id":"` + groupID + `","name":"vip","platform":"openai","status":"active"}]}`
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/keys":
					keyCreates++
					payload = `{"data":{"id":11,"key":"synthetic-test-key"}}`
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/admin/accounts":
					accountWrites++
					if sample.transportErr != nil {
						trace := httptrace.ContextClientTrace(req.Context())
						var op *net.OpError
						var dns *net.DNSError
						if errors.As(sample.transportErr, &op) && trace != nil && trace.ConnectDone != nil {
							trace.ConnectDone("tcp", "fixture.invalid:443", sample.transportErr)
						} else if errors.As(sample.transportErr, &dns) && trace != nil && trace.DNSDone != nil {
							trace.DNSDone(httptrace.DNSDoneInfo{Err: sample.transportErr})
						} else if trace != nil && trace.WroteRequest != nil {
							trace.WroteRequest(httptrace.WroteRequestInfo{})
						}
						return nil, sample.transportErr
					}
					status, payload = sample.status, sample.body
				case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/keys/11":
					keyDeletes++
					payload = `{"data":{}}`
				default:
					t.Fatalf("unexpected fixture request %s %s", req.Method, req.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
			})}))
			_, err := service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{groupID}})
			if err == nil {
				t.Fatal("create failure reported success")
			}
			var pending *ManagedResourcePendingError
			if sample.noKey {
				if keyCreates != 0 || keyDeletes != 0 || errors.As(err, &pending) {
					t.Fatalf("local validation created or retained resources: creates=%d deletes=%d err=%v", keyCreates, keyDeletes, err)
				}
			} else if sample.wantCleanup {
				if keyDeletes != 1 || errors.As(err, &pending) {
					t.Fatalf("proven absent account retained orphan key: deletes=%d err=%v", keyDeletes, err)
				}
			} else {
				if keyDeletes != 0 || !errors.As(err, &pending) || pending.UpstreamKeyID != "11" {
					t.Fatalf("unknown account result lost key/verification metadata: deletes=%d err=%v", keyDeletes, err)
				}
			}
			if repo.connection != nil {
				t.Fatal("failed create persisted local connection")
			}
			if sample.groupID != "" && accountWrites != 0 {
				t.Fatal("local validation sent create")
			}
		})
	}
}
