package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type catalogCountRepository struct {
	*hiddenFakeRepository
	configsRead, hiddenRead int
}

func (r *catalogCountRepository) ListGroupTestConfigurations(ctx context.Context, user, workspace string) ([]GroupTestConfig, error) {
	r.configsRead++
	return r.fakeRepository.ListGroupTestConfigurations(ctx, user, workspace)
}
func (r *catalogCountRepository) ListHiddenAccounts(ctx context.Context, user, workspace string) ([]HiddenAccount, error) {
	r.hiddenRead++
	return r.hiddenFakeRepository.ListHiddenAccounts(ctx, user, workspace)
}

type catalogCountReader struct {
	fakePlatformGroupReader
	groupsRead, accountsRead, credentialsRead int
	groupError                                error
}

func (r *catalogCountReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.groupsRead++
	return r.groups, r.groupError
}
func (r *catalogCountReader) ListAdminGroupAccounts(session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.accountsRead++
	return r.fakePlatformGroupReader.ListAdminGroupAccounts(session, group)
}
func (r *catalogCountReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	r.credentialsRead++
	return upstream.ProbeCredential{}, errors.New("must not export credentials")
}

type catalogSessionReader struct {
	fakeMySitesReader
	workspace string
	err       error
}

func (r *catalogSessionReader) RequireSession(_ context.Context, _ string, workspace string) (upstream.Session, error) {
	r.workspace = workspace
	return r.session, r.err
}

func catalogFixture() (*Service, *catalogCountRepository, *catalogCountReader, *catalogSessionReader) {
	repo := &catalogCountRepository{hiddenFakeRepository: newHiddenFakeRepository()}
	one, nine := 1, 9
	reader := &catalogCountReader{fakePlatformGroupReader: fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "second", Name: "二组", Platform: "openai"}, {ID: "empty", Name: "空组"}, {ID: "first", Name: "一组", Platform: "openai"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"second": {{ID: "9", Name: "九", Priority: &nine}, {ID: "7", Name: "七", Priority: &one}, {ID: "7", Name: "七", Priority: &one}, {ID: "hidden", Name: "屏蔽"}, {ID: "unknown", Name: "未知"}},
			"first":  {{ID: "7", Name: "七", Priority: &one}, {ID: "hidden", Name: "屏蔽"}},
		},
	}}
	sessions := &catalogSessionReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	// No accounts resolver: any extra current-workspace read would fail.
	svc := &Service{repo: repo, mySites: sessions, platformGroups: reader}
	repo.hidden["user|captured|sub2api:captured:hidden"] = HiddenAccount{TargetID: "sub2api:captured:hidden"}
	return svc, repo, reader, sessions
}

func TestAuditCatalogCapturedWorkspaceInventoryAndNoHealthWrites(t *testing.T) {
	svc, repo, reader, sessions := catalogFixture()
	got, err := svc.ListAuditCatalog(context.Background(), "user", "captured")
	if err != nil {
		t.Fatal(err)
	}
	if sessions.workspace != "captured" || got.WorkspaceID != "captured" || len(got.Groups) != 3 {
		t.Fatalf("scope or groups: %+v", got)
	}
	if got.Groups[0].ID != "second" || got.Groups[1].ID != "empty" || got.Groups[1].Accounts == nil || len(got.Groups[1].Accounts) != 0 {
		t.Fatalf("group order/empty group: %+v", got.Groups)
	}
	accounts := got.Groups[0].Accounts
	if len(accounts) != 3 || accounts[0].AccountID != "7" || accounts[1].AccountID != "9" || accounts[2].AccountID != "unknown" || accounts[0].Protocol != "chat" || len(got.Groups[2].Accounts) != 1 {
		t.Fatalf("priority/dedup/hidden/membership: %+v", got)
	}
	if reader.groupsRead != 1 || reader.accountsRead != 3 || reader.credentialsRead != 0 || repo.configsRead != 1 || repo.hiddenRead != 1 {
		t.Fatalf("unexpected read counts: %+v %+v", reader, repo)
	}
	if repo.saves != 0 || len(repo.events) != 0 || len(repo.states) != 0 || len(repo.priorityStates) != 0 {
		t.Fatal("catalog changed health/priority/hidden state")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "Credential") || strings.Contains(string(raw), "baseURL") {
		t.Fatalf("private fields in catalog: %s", raw)
	}
}

func TestAuditCatalogCrossGroupProtocolUsesSameResolver(t *testing.T) {
	svc, repo, _, _ := catalogFixture()
	repo.testConfigurations = []GroupTestConfig{{UserID: "user", AdminAccountID: "captured", AdminGroupID: "first", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 20}, {UserID: "user", AdminAccountID: "captured", AdminGroupID: "second", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20}}
	got, err := svc.ListAuditCatalog(context.Background(), "user", "captured")
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Accounts[0].Protocol != "" || got.Groups[2].Accounts[0].Protocol != "" {
		t.Fatal("conflicting memberships were treated as an executable protocol")
	}
}

func TestAuditCatalogMainSessionFailuresDoNotReadInventoryOrLogoutHub(t *testing.T) {
	for _, key := range []string{my_sites.ErrorAuthRequired, my_sites.ErrorAdminOnly} {
		svc, repo, reader, sessions := catalogFixture()
		sessions.err = errors.New(key)
		_, err := svc.ListAuditCatalog(context.Background(), "user", "captured")
		var detail *AuditCatalogError
		if !errors.As(err, &detail) || detail.Status != 422 || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "调度站登录仍保留") {
			t.Fatalf("main session failure was not readable/isolated: %v", err)
		}
		if reader.groupsRead != 0 || repo.configsRead != 0 || repo.hiddenRead != 0 {
			t.Fatal("unverified session read inventory")
		}
	}
}

func TestAuditCatalogProtocolDefaultsAndInheritance(t *testing.T) {
	svc, repo, reader, _ := catalogFixture()
	reader.groups[0].Platform = "anthropic"
	reader.accountsByGrp["second"][0].Platform = "anthropic"
	reader.accountsByGrp["second"][0].Type = "apikey"
	got, err := svc.ListAuditCatalog(context.Background(), "user", "captured")
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Accounts[1].Protocol != "anthropic" || got.Groups[0].Accounts[0].Protocol != "chat" {
		t.Fatalf("protocol defaults changed: %+v", got.Groups)
	}
	repo.testConfigurations = []GroupTestConfig{{UserID: "user", AdminAccountID: "captured", AdminGroupID: "second", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 20}}
	got, err = svc.ListAuditCatalog(context.Background(), "user", "captured")
	if err != nil || got.Groups[0].Accounts[0].Protocol != "openai" || got.Groups[2].Accounts[0].Protocol != "openai" {
		t.Fatalf("cross-group inherited protocol: %+v %v", got, err)
	}
}

func TestAuditCatalogFailureNeverReturnsPartialGroups(t *testing.T) {
	for _, stage := range []string{"groups", "members", "configs", "hidden"} {
		t.Run(stage, func(t *testing.T) {
			svc, repo, reader, _ := catalogFixture()
			switch stage {
			case "groups":
				reader.groupError = errors.New("database password must not leak")
			case "members":
				reader.errByGrp = map[string]error{"first": errors.New("private request address")}
			case "configs":
				repo.testConfigurationErr = errors.New("private database")
			case "hidden":
				repo.listErr = errors.New("private database")
			}
			got, err := svc.ListAuditCatalog(context.Background(), "user", "captured")
			if err == nil || len(got.Groups) != 0 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "password") {
				t.Fatalf("partial or unsafe failure: %+v %v", got, err)
			}
		})
	}
}

func TestAuditCatalogKnownReasonsDoNotNeedMessageOrGuess403(t *testing.T) {
	for code, expected := range map[string]string{
		"ANNOUNCEMENT_ACK_REQUIRED": "先确认公告", "INSUFFICIENT_BALANCE": "余额不足",
		"API_KEY_QUOTA_EXHAUSTED": "额度已用完", "INSUFFICIENT_QUOTA": "额度已用完", "API_KEY_EXPIRED": "Key已过期",
	} {
		err := auditCatalogReadError("主站分组", &upstream.RequestError{MessageKey: upstream.ErrorRequest, Reason: upstream.KnownUpstreamCodeErrorKey(code), UpstreamCode: code, StatusCode: 403})
		if !strings.Contains(err.Error(), expected) || strings.Contains(err.Error(), "原因尚未确认") {
			t.Fatalf("code without message lost: %s %v", code, err)
		}
	}
	cases := []struct{ reason, expected string }{
		{upstream.ErrorAnnouncementAckRequired, "先确认公告"}, {upstream.ErrorUpstreamInsufficientBalance, "余额不足"},
		{upstream.ErrorUpstreamKeyQuotaExhausted, "额度已用完"}, {upstream.ErrorUpstreamKeyExpired, "Key已过期"},
		{upstream.ErrorInvalidResponse, "分页不完整"}, {upstream.ErrorNotFound, "接口或资源"}, {upstream.ErrorBusinessRejected, "业务拒绝"},
	}
	for _, item := range cases {
		err := auditCatalogReadError("主站分组", &upstream.RequestError{MessageKey: upstream.ErrorRequest, Reason: item.reason, StatusCode: 403})
		if !strings.Contains(err.Error(), item.expected) || strings.Contains(err.Error(), "原因尚未确认") {
			t.Fatalf("lost known reason: %v", err)
		}
	}
	err := auditCatalogReadError("主站分组", &upstream.RequestError{MessageKey: upstream.ErrorRequest, Reason: upstream.ErrorForbidden, StatusCode: 403})
	for _, guessed := range []string{"余额不足", "额度已用完", "Key已过期", "先确认公告"} {
		if strings.Contains(err.Error(), guessed) {
			t.Fatalf("guessed cause: %v", err)
		}
	}
}
