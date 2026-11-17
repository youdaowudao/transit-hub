package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type auditOwnedSessionReader struct{ fakeMySitesReader }

func (f auditOwnedSessionReader) RequireSession(ctx context.Context, user, workspace string) (upstream.Session, error) {
	if user != "user1" || workspace != "ws1" {
		return upstream.Session{}, errors.New("workspace does not belong to user")
	}
	return f.fakeMySitesReader.RequireSession(ctx, user, workspace)
}

func TestAuditQueuedContextUsesCapturedOwnedWorkspaceWithoutSwitchingSelection(t *testing.T) {
	s, repo := auditFixture()
	s.mySites = auditOwnedSessionReader{fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	s.accounts = fakeAdminAccountResolver{id: "another-current-workspace"}
	v, err := s.ResolveAuditContextInWorkspace(context.Background(), "user1", "ws1", "sub2api:ws1:1", "g1", true)
	if err != nil || !v.CanRun || v.WorkspaceID != "ws1" || v.Credential.Key != "private-key-marker" {
		t.Fatalf("captured workspace failed: workspace=%s runnable=%v error=%v", v.WorkspaceID, v.CanRun, err)
	}
	current, err := s.currentAdminAccountID(context.Background(), "user1")
	if err != nil || current != "another-current-workspace" {
		t.Fatal("queued lookup changed current workspace")
	}
	if _, err = s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", true); err == nil {
		t.Fatal("original current-workspace entry was broadened")
	}
	for _, c := range []struct{ user, workspace, target string }{{"other", "ws1", "sub2api:ws1:1"}, {"user1", "foreign", "sub2api:foreign:1"}, {"user1", "ws1", "sub2api:foreign:1"}} {
		if _, err = s.ResolveAuditContextInWorkspace(context.Background(), c.user, c.workspace, c.target, "g1", true); err == nil {
			t.Fatalf("foreign queued context allowed: %+v", c)
		}
	}
	if len(repo.events) != 0 || len(repo.states) != 0 || len(repo.priorityStates) != 0 {
		t.Fatal("queued context changed health or priority")
	}
}
