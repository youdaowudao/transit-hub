package chanshui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/modules/connection_health"
	"transithub/backend/internal/shared/authctx"
)

type catalogTargets struct {
	*fakeTargets
	user, workspace string
	reads           int
	legacyReads     int
	err             error
}

func (r *catalogTargets) ListAuditTargets(ctx context.Context, user string) ([]connection_health.AuditTarget, error) {
	r.legacyReads++
	return r.fakeTargets.ListAuditTargets(ctx, user)
}

func (r *catalogTargets) ListAuditCatalog(_ context.Context, user, workspace string) (connection_health.AuditCatalog, error) {
	r.reads++
	r.user, r.workspace = user, workspace
	return connection_health.AuditCatalog{WorkspaceID: workspace, Groups: []connection_health.AuditCatalogGroup{{ID: "g", Name: "只读组", Accounts: []connection_health.AuditCatalogAccount{}}}}, r.err
}

func TestCatalogTargetsOptionalQueryPreservesLegacyAndOwner(t *testing.T) {
	s, executor, targets := fixture(t)
	reader := &catalogTargets{fakeTargets: targets}
	s.targets = reader
	for _, query := range []string{"", "?unrelated=%ZZ", "?catalog=1", "?catalog=0", "?catalog=1&catalog=1", "?catalog="} {
		r := httptest.NewRequest("GET", "/api/chanshui/targets"+query, nil)
		r = r.WithContext(authctx.WithUserID(r.Context(), "user-owner"))
		r.Header.Set(WorkspaceHeader, "workspace-a")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		switch query {
		case "", "?unrelated=%ZZ":
			if w.Code != 200 || body["items"] == nil || body["catalog"] != nil {
				t.Fatalf("legacy response changed: %d %s", w.Code, w.Body.String())
			}
		case "?catalog=1":
			if w.Code != 200 || body["catalog"] == nil || body["items"] != nil {
				t.Fatalf("catalog response: %d %s", w.Code, w.Body.String())
			}
		default:
			if w.Code != 422 {
				t.Fatalf("invalid query accepted: %s %d", query, w.Code)
			}
		}
	}
	if reader.reads != 1 || reader.legacyReads != 2 || reader.workspace != "workspace-a" || reader.user != "user-owner" || executor.starts.Load() != 0 || targets.calls.Load() != 0 {
		t.Fatalf("unexpected scope/reads or started detection: %+v", reader)
	}
}

func TestCatalogTargetsRejectsMalformedCatalogWithoutReading(t *testing.T) {
	for _, query := range []string{"?catalog=%ZZ", "?catalog=1&catalog=%ZZ", "?%63atalog=%ZZ", "?catalog=1;unexpected=2"} {
		t.Run(query, func(t *testing.T) {
			s, executor, targets := fixture(t)
			reader := &catalogTargets{fakeTargets: targets}
			s.targets = reader
			r := httptest.NewRequest("GET", "/api/chanshui/targets"+query, nil)
			r = r.WithContext(authctx.WithUserID(r.Context(), "user"))
			r.Header.Set(WorkspaceHeader, "workspace-a")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 422 || reader.reads != 0 || reader.legacyReads != 0 || executor.starts.Load() != 0 {
				t.Fatalf("malformed catalog must reject before either list read: status=%d catalog=%d legacy=%d", w.Code, reader.reads, reader.legacyReads)
			}
		})
	}
}

func TestCatalogTargetsSessionFailureDoesNotReturnHub401(t *testing.T) {
	s, _, targets := fixture(t)
	s.targets = &catalogTargets{fakeTargets: targets, err: &connection_health.AuditCatalogError{Status: 422, Message: "主站登录尚未就绪，请在工作区重新登录；没有开始检测"}}
	r := httptest.NewRequest("GET", "/api/chanshui/targets?catalog=1", nil)
	r = r.WithContext(authctx.WithUserID(r.Context(), "user"))
	r.Header.Set(WorkspaceHeader, "workspace-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "主站登录尚未就绪") {
		t.Fatalf("session failure: %d %s", w.Code, w.Body.String())
	}
	reader := s.targets.(*catalogTargets)
	reader.reads = 0
	r.Header.Set(WorkspaceHeader, "another")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 || reader.reads != 0 {
		t.Fatal("mismatched workspace reached inventory")
	}
}
