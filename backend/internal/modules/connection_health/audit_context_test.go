package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func auditFixture() (*Service, *fakeRepository) {
	repo := newFakeRepository()
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "GPT组", Platform: "openai"}, {ID: "g2", Name: "另一个组", Platform: "openai"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "1", Name: "账号A", Platform: "openai", Type: "apikey"}}, "g2": {{ID: "1", Name: "账号A", Platform: "openai", Type: "apikey"}}},
		credByAccount: map[string]upstream.ProbeCredential{"1": {BaseURL: "https://upstream.invalid", Key: "private-key-marker", AnthropicAuth: "authorization_bearer"}},
	}
	return newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo), repo
}
func TestAuditContextUsesExistingInventoryAndCredentialWithoutHealthWrites(t *testing.T) {
	s, repo := auditFixture()
	targets, err := s.ListAuditTargets(context.Background(), "user1")
	if err != nil || len(targets) != 1 || len(targets[0].Groups) != 2 {
		t.Fatalf("dedup inventory=%+v error=%v", targets, err)
	}
	ctx, err := s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g2", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.CanRun || ctx.GroupID != "g2" || ctx.Protocol != "chat" || ctx.Credential.Key != "private-key-marker" {
		t.Fatalf("wrong server context: %+v", ctx)
	}
	raw, _ := json.Marshal(ctx)
	if strings.Contains(string(raw), "private-key-marker") || strings.Contains(string(raw), "AnthropicAuth") {
		t.Fatal("credential leaked to public JSON")
	}
	if len(repo.events) != 0 || len(repo.states) != 0 || len(repo.priorityStates) != 0 {
		t.Fatal("read-only context changed health or priority")
	}
	public, err := s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", false)
	if err != nil || public.Credential.Key != "" {
		t.Fatal("preview retained credential")
	}
}
func TestAuditContextRejectsForeignTargetAndRemovedMembership(t *testing.T) {
	s, _ := auditFixture()
	for _, item := range []struct{ target, group string }{{"sub2api:foreign:1", "g1"}, {"sub2api:ws1:1", "removed"}, {"newapi:ws1:1", "g1"}} {
		if _, err := s.ResolveAuditContext(context.Background(), "user1", item.target, item.group, true); err == nil {
			t.Fatalf("unexpected allowed target %+v", item)
		}
	}
}
func TestAuditContextMessagesAuthAndOAuthRestriction(t *testing.T) {
	for _, auth := range []string{"x_api_key", "authorization_bearer"} {
		s, repo := auditFixture()
		r := s.platformGroups.(fakePlatformGroupReader)
		r.groups = r.groups[:1]
		r.groups[0].Platform = "anthropic"
		r.accountsByGrp["g1"][0].Platform = "anthropic"
		r.credByAccount["1"] = upstream.ProbeCredential{BaseURL: "https://upstream.invalid", Key: "private-key-marker", AnthropicAuth: auth}
		s.platformGroups = r
		ctx, err := s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", true)
		if err != nil || !ctx.CanRun || ctx.Protocol != "anthropic" || ctx.Credential.AnthropicAuth != auth {
			t.Fatalf("Messages auth changed: %+v %v", ctx, err)
		}
		repo.testConfigurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20}}
		r.accountsByGrp["g1"][0].Type = "oauth"
		s.platformGroups = r
		ctx, err = s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", true)
		if err == nil && ctx.CanRun {
			t.Fatal("OAuth Messages request allowed")
		}
	}
}
func TestAuditContextBlocksIncompleteInventoryAndCredentialBearingURL(t *testing.T) {
	s, _ := auditFixture()
	r := s.platformGroups.(fakePlatformGroupReader)
	r.errByGrp = map[string]error{"g2": errors.New("fixture unreadable")}
	s.platformGroups = r
	if _, err := s.ListAuditTargets(context.Background(), "user1"); err == nil {
		t.Fatal("incomplete list accepted")
	}
	if _, err := s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", true); err == nil {
		t.Fatal("incomplete context accepted")
	}
	r.errByGrp = nil
	r.credByAccount["1"] = upstream.ProbeCredential{BaseURL: "https://upstream.invalid?api_key=secret-marker", Key: "private"}
	s.platformGroups = r
	ctx, err := s.ResolveAuditContext(context.Background(), "user1", "sub2api:ws1:1", "g1", true)
	if err != nil || ctx.CanRun || strings.Contains(ctx.BaseURL, "secret-marker") {
		t.Fatal("credential-bearing URL returned")
	}
}
