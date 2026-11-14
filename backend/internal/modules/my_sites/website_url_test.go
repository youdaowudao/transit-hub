package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestWebsiteURLImportKeepsAPIAddress(t *testing.T) {
	remote := &c5RedTransport{platform: "openai"}
	service, _ := c5RedService(t, remote)
	site := service.upstreamLookup.(testUpstreamLookup).sites["site-1"]
	api := site.BaseURL
	// Exercise the full import flow with a separate website on the source site.
	raw, _ := json.Marshal(map[string]string{"websiteUrl": "https://website.fixture.invalid"})
	if err := json.Unmarshal(raw, site); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, "")); err != nil {
		t.Fatal(err)
	}
	credentials, ok := remote.created["credentials"].(map[string]any)
	if !ok || credentials["base_url"] != api || credentials["base_url"] == "https://website.fixture.invalid" {
		t.Fatal("main account received website instead of API")
	}
	if remote.previews != 1 || remote.accountCreates != 1 {
		t.Fatal("normal preview/import convenience changed")
	}
}

func TestAPIURLImportUsesAPIAddressForMainAccount(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", previewBaseURL: "https://gateway.fixture.invalid"}
	service, _ := c5RedService(t, remote)
	site := service.upstreamLookup.(testUpstreamLookup).sites["site-1"]
	website := site.BaseURL
	site.APIURL = "https://gateway.fixture.invalid"
	if _, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, "")); err != nil {
		t.Fatal(err)
	}
	credentials, ok := remote.created["credentials"].(map[string]any)
	if !ok || credentials["base_url"] != "https://gateway.fixture.invalid" || credentials["base_url"] == website {
		t.Fatalf("main account base_url=%v, want the API address", credentials["base_url"])
	}
	if remote.previews != 1 || remote.accountCreates != 1 {
		t.Fatal("normal preview/import convenience changed")
	}
}

func TestAPIURLChangedDuringImportKeepsResourcesWithoutLink(t *testing.T) {
	remote := &c5RedTransport{platform: "openai", previewBaseURL: "https://gateway.fixture.invalid"}
	service, repo := c5RedService(t, remote)
	site := service.upstreamLookup.(testUpstreamLookup).sites["site-1"]
	site.APIURL = "https://gateway.fixture.invalid"
	// Another edit changes only the API address while the main account is being created.
	remote.onAccountCreate = func() { site.APIURL = "https://changed.fixture.invalid" }
	_, err := service.RealConnect(context.Background(), "user-1", c5RedRequest(t, ""))
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.Message != ErrorImportSourceGroupUnavailable || failure.Cleanup != "retained" {
		t.Fatalf("API address change during import must stop linking with retained resources, got %v", err)
	}
	if failure.AdminResourceID != "22" || repo.saveCalls != 0 || remote.protectedDeletes != 0 || remote.keyDeletes != 0 {
		t.Fatalf("resources must be kept and the link unsaved: account=%q saves=%d deletes=%d/%d", failure.AdminResourceID, repo.saveCalls, remote.protectedDeletes, remote.keyDeletes)
	}
}
