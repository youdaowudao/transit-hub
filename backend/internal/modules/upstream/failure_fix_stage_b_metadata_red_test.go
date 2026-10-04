package upstream

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestFailureFixStageBMetadataOnlyEditPreservesSessionAndMakesNoRequest(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, mode := range []AuthMode{AuthModePassword, AuthModeToken, AuthModeUserKey} {
		t.Run(string(mode), func(t *testing.T) {
			requests := 0
			platform := failureFixPlatform(func(http.ResponseWriter, *http.Request) { requests++; t.Error("metadata-only edit requested upstream") })
			service, cache, repository := failureFixService(t, platform)
			site := newTestSite("fixture-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: mode, AccessToken: "fixture-access", RefreshToken: "fixture-refresh"})
			site.Platform = PlatformSub2API
			site.RequestedPlatform = PlatformSub2API
			site.Account = "fixture-account"
			site.BaseURL = "http://fixture.invalid"
			dto := UpdateRequest{Name: "验收-仅改名称", SiteURL: site.BaseURL, Platform: site.Platform, AuthMode: mode, Account: site.Account, RechargeRate: 1, Remark: "验收-仅改备注"}
			if mode == AuthModeUserKey {
				site.Platform = PlatformNewAPI
				site.RequestedPlatform = PlatformNewAPI
				site.Account = "42"
				site.Session.Platform = PlatformNewAPI
				site.Session.UserID = "42"
				dto.Platform = PlatformNewAPI
				dto.Account = "42"
				dto.UserID = "42"
			}
			beforeSession := *site.Session
			beforeStatus := site.Status
			cache.add(site)
			response, err := service.Update(context.Background(), "fixture-user", site.ID, dto)
			if err != nil {
				t.Error("metadata-only edit was rejected")
				return
			}
			after, _ := cache.Get(context.Background(), site.ID)
			if after.Name != dto.Name || after.Remark != dto.Remark || len(repository.saved) != 1 {
				t.Error("metadata-only fields not saved")
			}
			if !reflect.DeepEqual(*after.Session, beforeSession) || after.Status != beforeStatus || response.AuthMode != mode {
				t.Error("metadata-only edit changed original session, status or login mode")
			}
			if requests != 0 {
				t.Error("metadata-only edit performed upstream request")
			}
		})
	}
}
