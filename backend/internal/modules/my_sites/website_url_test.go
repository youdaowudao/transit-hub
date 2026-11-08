package my_sites

import (
	"context"
	"encoding/json"
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
