package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/upstream"
)

type importSelectionDuplicateTx struct{ *c5Transaction }
type importSelectionBoolRow bool

func (row importSelectionBoolRow) Scan(values ...any) error {
	*values[0].(*bool) = bool(row)
	return nil
}
func (tx *importSelectionDuplicateTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "real_connections") {
		return importSelectionBoolRow(true)
	}
	return tx.c5Transaction.QueryRow(ctx, query, args...)
}
func TestImportSelectionAtomicSaveRejectsActiveTargetWithAndWithoutMapping(t *testing.T) {
	for _, mapping := range []bool{false, true} {
		t.Run(map[bool]string{false: "mapping-off", true: "mapping-on"}[mapping], func(t *testing.T) {
			base := &c5Transaction{t: t}
			tx := &importSelectionDuplicateTx{base}
			conn := RealConnection{ID: "second-connection", UserID: "user-1", WorkspaceAdminAccountID: "admin-1", UpstreamSiteID: "site-1", UpstreamGroupID: "70", AdminPlatform: "sub2api", Status: ConnectionStatusActive, PricingMappingEnabled: mapping}
			err := saveRealConnectionWithPricingMappingTx(t.Context(), tx, conn)
			if connectionCommitOutcome(err) != CommitConfirmedNotCommitted || !errors.Is(err, requestError(ErrorConnectionExists)) || len(base.statements) != 0 || base.commits != 0 {
				t.Fatal("atomic save wrote a duplicate active target or pricing mapping")
			}
		})
	}
}

func TestImportSelectionBindingBlocksImportAndNormalizesTarget(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, repo := c5RedService(t, remote)
	entered, release := make(chan struct{}), make(chan struct{})
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			close(entered)
			<-release
			return c5SafeResponse(r, 200, map[string]any{"data": []any{}}), nil, true
		}
		return nil, nil, false
	})
	done := make(chan error, 1)
	go func() {
		_, err := s.RealBind(t.Context(), "user-1", RealBindRequest{UpstreamSiteID: " site-1 ", UpstreamGroupID: " 70 ", UpstreamGroupName: "upstream-only", UpstreamKeyID: "11", UpstreamKey: "synthetic", OwnGroupIDs: []string{"7"}, OperationID: "binding-first"})
		done <- err
	}()
	<-entered
	_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.Message != ErrorImportInProgress || failure.RetryAllowed {
		t.Error("import entered while binding owned target")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if repo.connection == nil || repo.connection.UpstreamSiteID != "site-1" || repo.connection.UpstreamGroupID != "70" || remote.keyCreates != 0 || remote.accountCreates != 0 {
		t.Fatal("binding lost normalized target or created remote resources")
	}
}

func TestImportSelectionProtectionReleaseAndScope(t *testing.T) {
	s := &Service{}
	first, err := s.tryBeginConnectionCreation("user", "workspace", "site", "group", "operation")
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range [][5]string{{"other-user", "workspace", "site", "group", "operation"}, {"user", "other-workspace", "site", "group", "operation"}, {"user", "workspace", "site", "other-group", "other-operation"}} {
		release, err := s.tryBeginConnectionCreation(values[0], values[1], values[2], values[3], values[4])
		if err != nil {
			t.Fatal("unrelated operation blocked")
		}
		release()
	}
	if _, err := s.tryBeginConnectionCreation("user", "workspace", "site", "other", "operation"); err == nil {
		t.Fatal("same operation entered on different target")
	}
	first()
	first()
	release, err := s.tryBeginConnectionCreation("user", "workspace", "site", "group", "operation")
	if err != nil {
		t.Fatal("release did not free both keys")
	}
	release()
	remote := &c5RedTransport{platform: "openai"}
	service, _ := c5RedService(t, remote)
	bad := c5RedRequest(t, "")
	bad.OwnGroupIDs = nil
	if _, err := service.RealConnect(t.Context(), "user-1", bad); err == nil {
		t.Fatal("invalid form accepted")
	}
	if _, err := service.RealConnect(t.Context(), "user-1", c5RedRequest(t, "")); err != nil {
		t.Fatal("validation failure retained creation keys")
	}
}

func TestImportSelectionCurrentNameIsAtomicMappingSource(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, base := c5RedService(t, remote)
	tx := &c5Transaction{t: t}
	s.connRepository = &c5ThreeGroupRepository{testConnRepo: base, tx: tx}
	site := s.upstreamLookup.(testUpstreamLookup).sites["site-1"]
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.URL.Path == "/api/v1/admin/accounts/22" {
			site.Metrics.Groups[0].Name = "renamed-source"
		}
		return nil, nil, false
	})
	req := c5RedRequest(t, "")
	enabled := true
	req.AddToPricingMapping = &enabled
	if _, err := s.RealConnect(t.Context(), "user-1", req); err != nil {
		t.Fatal(err)
	}
	var mappings []GroupMapping
	if json.Unmarshal(tx.mappingJSON, &mappings) != nil || len(mappings) != 2 {
		t.Fatal("atomic pricing mappings missing")
	}
	for _, mapping := range mappings {
		if len(mapping.UpstreamTargets) != 1 || mapping.UpstreamTargets[0].GroupName != "renamed-source" {
			t.Fatal("pricing mapped stale page name")
		}
	}
}

// These expectations are fixed by the primary agent against the approved SPEC.
// All network responses are in-memory; no listener or external process starts.
func TestImportSelectionExplicitPlatformEntirePipeline(t *testing.T) {
	for i, target := range []string{"openai", "anthropic", "gemini", "antigravity", "grok", "kimi", "zhipu", "deepseek", "minimax"} {
		t.Run(target, func(t *testing.T) {
			source := "grok"
			if target == "grok" {
				source = "openai"
			}
			c51AssertImportedPlatform(t, source, " "+strings.ToUpper(target)+" ", target, []string{"A", "B", "C", "D", "X", "X", "X", "X", "X"}[i])
		})
	}
}

func TestImportSelectionCurrentSourceNameAndFinalRecheck(t *testing.T) {
	for _, mode := range []string{"old-page-name", "rename-before-save", "missing-at-start", "duplicate-at-start", "blank-at-start", "missing-before-save", "owner-before-save", "url-before-save", "platform-before-save"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			site := s.upstreamLookup.(testUpstreamLookup).sites["site-1"]
			site.Metrics.Groups[0].Name = "current-source-name"
			switch mode {
			case "missing-at-start":
				site.Metrics.Groups = nil
			case "duplicate-at-start":
				site.Metrics.Groups = append(site.Metrics.Groups, site.Metrics.Groups[0])
			case "blank-at-start":
				site.Metrics.Groups[0].Name = " "
			}
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path == "/api/v1/admin/accounts/22" {
					switch mode {
					case "rename-before-save":
						site.Metrics.Groups[0].Name = "renamed-before-save"
					case "missing-before-save":
						site.Metrics.Groups = nil
					case "owner-before-save":
						site.UserID = "another-user"
					case "url-before-save":
						site.BaseURL = "https://changed.invalid"
					case "platform-before-save":
						site.Platform = upstream.PlatformNewAPI
					}
				}
				return nil, nil, false
			})
			result, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			if strings.HasSuffix(mode, "at-start") {
				if err == nil || remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
					t.Fatal("unconfirmed source created resources")
				}
				return
			}
			if strings.HasSuffix(mode, "before-save") && mode != "rename-before-save" {
				var failure *ImportFailure
				if !errors.As(err, &failure) || failure.Message != "admin.mySites.errors.importSourceGroupUnavailable" || failure.Stage != "persistence" || failure.Cleanup != "retained" || failure.RetryAllowed || failure.AdminResourceID != "22" || failure.UpstreamKeyID != "11" || repo.saveCalls != 0 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
					t.Fatal("changed source was reported successful or deleted")
				}
				return
			}
			name := "current-source-name"
			if mode == "rename-before-save" {
				name = "renamed-before-save"
			}
			if err != nil || repo.connection == nil || repo.connection.UpstreamGroupName != name || result.Connection.UpstreamGroupName != name {
				t.Fatal("connection used stale page name instead of current cached ID")
			}
		})
	}
}

func TestImportSelectionGroupsFailurePreservesCauseAndBound(t *testing.T) {
	for _, reason := range []string{upstream.ErrorAuth, upstream.ErrorNetworkTimeout, upstream.ErrorInvalidResponse, "cancel"} {
		t.Run(reason, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inherited, bounded := false, false
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path != "/api/v1/admin/groups" {
					return nil, nil, false
				}
				deadline, ok := r.Context().Deadline()
				bounded = ok && time.Until(deadline) <= 30*time.Second
				if reason == "cancel" {
					cancel()
					inherited = r.Context().Err() != nil
					return nil, context.Canceled, true
				}
				if reason == upstream.ErrorAuth {
					return c5SafeResponse(r, 401, map[string]any{"code": "UNAUTHORIZED"}), nil, true
				}
				if reason == upstream.ErrorNetworkTimeout {
					return nil, context.DeadlineExceeded, true
				}
				return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": "malformed"}), nil, true
			})
			_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Message != "admin.mySites.errors.importGroupsLoadFailed" || failure.Stage != "validation" || failure.Cleanup != "not_needed" || failure.StatusCode != 502 || !failure.RetryAllowed || reason != "cancel" && failure.Reason != reason {
				t.Error("group read lost the safe failure contract/cause")
			}
			if !bounded || reason == "cancel" && !inherited {
				t.Error("group stage did not inherit cancellation and one 30-second deadline")
			}
			if remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("failed validation created resources")
			}
		})
	}
}

func TestImportSelectionGroupPaginationInheritsOneDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"cancel-first", "cancel-second", "short-parent"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			pages := 0
			var firstDeadline time.Time
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path != "/api/v1/admin/groups" {
					return nil, nil, false
				}
				pages++
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Error("missing group deadline")
				}
				if pages == 1 {
					firstDeadline = deadline
				} else if deadline != firstDeadline {
					t.Error("pagination reset group deadline")
				}
				if mode == "cancel-first" || pages == 2 {
					if mode == "short-parent" {
						<-r.Context().Done()
					} else {
						cancel()
					}
					if r.Context().Err() == nil {
						t.Error("group request ignored parent cancellation")
					}
					return nil, r.Context().Err(), true
				}
				items := []map[string]any{}
				for i := 1; i <= 100; i++ {
					items = append(items, map[string]any{"id": i, "name": "target", "platform": "openai", "status": "active"})
				}
				return c5SafeResponse(r, 200, map[string]any{"code": 0, "data": map[string]any{"items": items, "page": 1, "page_size": 100, "total": 200}}), nil, true
			})
			_, err := s.RealConnect(ctx, "user-1", c5RedRequest(t, ""))
			var failure *ImportFailure
			wantPages := 2
			if mode == "cancel-first" {
				wantPages = 1
			}
			if !errors.As(err, &failure) || failure.Message != ErrorImportGroupsLoadFailed || pages != wantPages || remote.keyCreates != 0 || remote.accountCreates != 0 || repo.saveCalls != 0 {
				t.Fatal("canceled pagination continued or created resources")
			}
		})
	}
}

func TestImportSelectionNewAPIRetainedTokenRemainsUniquelyIdentifiable(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	s, repo := c5RedService(t, remote)
	site := s.upstreamLookup.(testUpstreamLookup).sites["site-1"]
	session := platformTestSession(upstream.PlatformNewAPI, site.BaseURL)
	site.Platform, site.Session = upstream.PlatformNewAPI, &session
	names := []string{}
	connectionIDs := []string{}
	c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
		if r.URL.Path == "/api/token/" {
			if r.Method == "POST" {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				names = append(names, payload["name"].(string))
				return c5SafeResponse(r, 200, map[string]any{"success": true}), nil, true
			}
			items := []map[string]any{}
			for i, name := range names {
				items = append(items, map[string]any{"id": 11 + i, "name": name})
			}
			return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"items": items, "page": 1, "page_size": 100, "total": len(items)}}), nil, true
		}
		if r.URL.Path == "/api/token/11/key" || r.URL.Path == "/api/token/12/key" {
			return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"key": "synthetic-c5-key"}}), nil, true
		}
		return nil, nil, false
	})
	for i := 0; i < 2; i++ {
		repo.connection = nil
		remote.accountCreates = 0
		result, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
		if err != nil {
			t.Fatal(err)
		}
		connectionIDs = append(connectionIDs, result.Connection.ID)
		if result.Connection.UpstreamKeyID != []string{"11", "12"}[i] {
			t.Fatal("new import selected retained old Token")
		}
	}
	if len(names) != 2 || names[0] == names[1] || connectionIDs[0] == connectionIDs[1] {
		t.Fatal("new imports lost unique names")
	}
	for i, name := range names {
		if len(name) > 50 || !utf8.ValidString(name) || !strings.HasSuffix(name, "-"+connectionIDs[i]) {
			t.Fatal("new name lost full connection suffix")
		}
	}
	if remote.keyDeletes != 0 || remote.protectedDeletes != 0 {
		t.Fatal("retained resource deleted on later import")
	}
}

func TestImportSelectionOverlappingTargetOrOperationIsBusy(t *testing.T) {
	for _, mode := range []string{"same-target", "same-operation", "same-operation-other-target", "bind-during-import"} {
		t.Run(mode, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, _ := c5RedService(t, remote)
			site := s.upstreamLookup.(testUpstreamLookup).sites["site-1"]
			site.Metrics.Groups = append(site.Metrics.Groups, upstream.GroupInfo{ID: "71", Name: "second", Platform: stringPointer("openai")})
			entered, release := make(chan struct{}), make(chan struct{})
			firstCtx := t.Context()
			blocked := false
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				if r.URL.Path == "/api/v1/admin/groups" && !blocked {
					blocked = true
					close(entered)
					<-release
				}
				if r.URL.Path == "/api/v1/keys" && r.Method == "GET" {
					return c5SafeResponse(r, 200, map[string]any{"data": map[string]any{"items": []map[string]any{{"id": 11, "key": "synthetic", "group_id": 70}}, "total": 1}}), nil, true
				}
				return nil, nil, false
			})
			first := c5RedRequest(t, "")
			first.OperationID = "selection-first"
			done := make(chan error, 1)
			go func() { _, err := s.RealConnect(firstCtx, "user-1", first); done <- err }()
			<-entered
			second := first
			second.OperationID = "selection-second"
			if strings.HasPrefix(mode, "same-operation") {
				second.OperationID = first.OperationID
			}
			if mode == "same-operation-other-target" {
				second.UpstreamGroupID = "71"
			}
			var err error
			if mode == "bind-during-import" {
				_, err = s.RealBind(t.Context(), "user-1", RealBindRequest{UpstreamSiteID: " site-1 ", UpstreamGroupID: " 70 ", UpstreamGroupName: "upstream-only", UpstreamKeyID: "11", UpstreamKey: "synthetic", OwnGroupIDs: []string{"7"}})
			} else {
				_, err = s.RealConnect(t.Context(), "user-1", second)
			}
			var failure *ImportFailure
			if !errors.As(err, &failure) || failure.Message != "admin.mySites.errors.importInProgress" || failure.Cleanup != "pending" || failure.StatusCode != 409 || failure.RetryAllowed {
				t.Error("overlapping operation entered execution instead of busy")
			}
			close(release)
			if err := <-done; err != nil {
				t.Error("first protected import did not complete successfully")
			}
			if remote.keyCreates != 1 || remote.accountCreates != 1 {
				t.Error("overlapping operation duplicated resource creation")
			}
		})
	}
}

func TestImportSelectionNewAPITokenKeepsCompleteConnectionSuffix(t *testing.T) {
	for _, name := range []string{"short", strings.Repeat("站点", 30), strings.Repeat("long-ascii", 20)} {
		t.Run(name, func(t *testing.T) {
			remote := &c5RedTransport{platform: "openai"}
			s, repo := c5RedService(t, remote)
			site := s.upstreamLookup.(testUpstreamLookup).sites["site-1"]
			session := platformTestSession(upstream.PlatformNewAPI, site.BaseURL)
			site.Platform, site.Session, site.Name = upstream.PlatformNewAPI, &session, name
			createdName := ""
			c5UseOverride(s, remote, func(r *http.Request) (*http.Response, error, bool) {
				switch r.URL.Path {
				case "/api/token/":
					if r.Method == "POST" {
						var body struct {
							Name string `json:"name"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						createdName = body.Name
						return c5SafeResponse(r, 200, map[string]any{"success": true}), nil, true
					}
					return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"items": []map[string]any{{"id": 11, "name": createdName, "group": "70"}}, "total": 1, "page": 1, "page_size": 100}}), nil, true
				case "/api/token/11/key":
					return c5SafeResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"key": "synthetic-c5-key"}}), nil, true
				}
				return nil, nil, false
			})
			_, err := s.RealConnect(t.Context(), "user-1", c5RedRequest(t, ""))
			if err != nil || repo.connection == nil {
				t.Fatalf("token fixture must complete import: %v", err)
			}
			if len(createdName) > 50 || !utf8.ValidString(createdName) || !strings.HasSuffix(createdName, "-"+repo.connection.ID) || len(repo.connection.ID) != 32 {
				t.Fatal("new token name exceeds byte limit or loses full unique connection suffix")
			}
		})
	}
}
