package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type confirmTestSessionReader struct {
	fakeMySitesReader
}

func (r confirmTestSessionReader) StoredSession(_ context.Context, _, _ string) (upstream.Session, bool, error) {
	return r.session, true, nil
}

func TestResolveUncertainActionHTTPPostgres(t *testing.T) {
	for _, tc := range []struct {
		name        string
		targetID    string
		rawStatus   string
		current     string
		wantReason  string
		updateAt    bool
		wantApplied bool
	}{
		{name: "rejected", rawStatus: `"inactive"`, current: "inactive"},
		{name: "numeric active", rawStatus: "1", current: "active", wantApplied: true},
		{name: "numeric inactive", rawStatus: "0", current: "inactive"},
		{name: "fractional inactive", rawStatus: "0.5", current: "inactive", wantReason: "main_site_status_unknown"},
		{name: "fractional active", rawStatus: "1.5", current: "active", wantReason: "main_site_status_unknown"},
		{name: "legacy alias", targetID: "sub2api:w: 42 ", rawStatus: `"inactive"`, wantReason: "legacy_alias"},
		{name: "rejected cannot use old observation", rawStatus: `"inactive"`, current: "inactive", updateAt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := stageAPostgresPool(t)
			repo := NewRepository(pool)
			ctx := t.Context()
			if err := repo.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			targetID := tc.targetID
			if targetID == "" {
				targetID = "sub2api:w:42"
			}
			_, err := pool.Exec(ctx, `
				INSERT INTO connection_health_target_action_states
				(user_id, admin_account_id, target_id, original_status, last_applied_status, pending_status,
				 pending_dispatch_id, pending_owner_id, pending_dispatch_phase, pending_action_kind, pending_source, updated_at)
				VALUES ('u', 'w', $1, 'active', 'inactive', 'active', 'fixture-dispatch', 'expired-owner',
				 'uncertain', 'status', '', clock_timestamp() - interval '10 minutes')`, targetID)
			if err != nil {
				t.Fatal(err)
			}
			readCount := 0
			writeCount := 0
			client := &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					writeCount++
					return nil, fmt.Errorf("unexpected main-site write: %s", request.Method)
				}
				if request.URL.Path != "/api/v1/admin/accounts" {
					return nil, fmt.Errorf("unexpected main-site path: %s", request.URL.Path)
				}
				readCount++
				if tc.updateAt && readCount == 2 {
					_, err := pool.Exec(request.Context(), `UPDATE connection_health_target_action_states
						SET updated_at=clock_timestamp()+interval '1 second' WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, targetID)
					if err != nil {
						return nil, err
					}
				}
				header := make(http.Header)
				header.Set("Content-Type", "application/json")
				header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
				body := fmt.Sprintf(`{"code":0,"data":{"items":[{"id":42,"name":"fixture","status":%s}],"total":1}}`, tc.rawStatus)
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			service := &Service{
				repo: repo, accounts: fakeAdminAccountResolver{id: "w"},
				mySites: confirmTestSessionReader{fakeMySitesReader: fakeMySitesReader{
					session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "synthetic-test-token"},
				}},
				platformGroups: upstream.NewPlatformService(upstream.NewHTTPClient(client)),
			}
			guard := service.sub2APIFloorGuardFor("u", "w")
			guard.mu.Lock()
			guard.reserveUnavailableLocked(targetID)
			guard.mu.Unlock()
			mux := http.NewServeMux()
			RegisterRoutes(mux, service)
			call := func(method, path, body string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(method, "/api/connection-health/accounts/42/remote-action/"+path, strings.NewReader(body))
				request = request.WithContext(authctx.WithUserID(ctx, "u"))
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				return response
			}
			preview := call(http.MethodGet, "current", "")
			var previewPayload struct {
				CanConfirm    bool   `json:"canConfirm"`
				BlockedReason string `json:"blockedReason"`
				DispatchID    string `json:"dispatchId"`
				Source        string `json:"source"`
			}
			if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &previewPayload) != nil {
				t.Fatalf("failed to preview existing uncertain action: HTTP %d %s", preview.Code, preview.Body.String())
			}
			if tc.wantReason != "" {
				if previewPayload.CanConfirm || previewPayload.BlockedReason != tc.wantReason {
					t.Fatalf("preview allowed an unsupported record or value: %s", preview.Body.String())
				}
			} else if !previewPayload.CanConfirm || previewPayload.DispatchID != "fixture-dispatch" || previewPayload.Source != "health_probe" {
				t.Fatalf("preview did not identify the selected pending action: %s", preview.Body.String())
			}

			confirm := call(http.MethodPost, "confirm-current", fmt.Sprintf(`{"dispatchId":"fixture-dispatch","currentValue":%q}`, tc.current))
			wantCode := http.StatusOK
			wantFailure := tc.wantReason
			if tc.updateAt {
				wantFailure = "changed"
			}
			if wantFailure != "" {
				wantCode = http.StatusConflict
			}
			if confirm.Code != wantCode || (wantFailure != "" && !strings.Contains(confirm.Body.String(), wantFailure)) {
				t.Fatalf("confirm response = HTTP %d %s; want %d %q", confirm.Code, confirm.Body.String(), wantCode, wantFailure)
			}

			row, err := repo.GetTargetActionState(ctx, "u", "w", targetID)
			if err != nil {
				t.Fatal(err)
			}
			_, reserved := guard.reservationVersion(targetID)
			if wantFailure != "" {
				if row.PendingDispatchPhase != DispatchUncertain || row.PendingDispatchID != "fixture-dispatch" || row.LastAppliedStatus != "inactive" || !reserved {
					t.Fatalf("failed confirmation changed record or reservation: %+v reserved=%v", row, reserved)
				}
			} else {
				wantLastApplied := "inactive"
				if tc.wantApplied {
					wantLastApplied = "active"
				}
				if targetActionPending(row) || row.OriginalStatus != "active" || row.LastAppliedStatus != wantLastApplied || reserved != tc.wantApplied {
					t.Fatalf("confirmation did not apply the expected reconciliation: %+v reserved=%v", row, reserved)
				}
			}
			wantReads := 2
			if tc.wantReason == "legacy_alias" {
				wantReads = 0
			}
			if readCount != wantReads || writeCount != 0 {
				t.Fatalf("main-site access reads=%d (want %d) writes=%d (want 0)", readCount, wantReads, writeCount)
			}
		})
	}
}
