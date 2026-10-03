package upstream

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSub2APIInventoryEstablishedEnvelopeContracts(t *testing.T) {
	for _, name := range []string{"sub2api_group_inventory_full.json", "sub2api_account_inventory_short_page.json"} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var payload any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if name == "sub2api_group_inventory_full.json" {
			groups, ok := strictSub2APIGroupItems(payload)
			if !ok || len(groups) != 2 {
				t.Fatal("full nonpaginated group array contract rejected")
			}
		} else {
			done, ok := sub2APIInventoryPageFinished(payload, 1, 100, 1, 1)
			if !ok || !done {
				t.Fatal("established array short-page EOF contract rejected")
			}
		}
	}
}

func TestSub2APIInventoryPaginationContradictions(t *testing.T) {
	for _, body := range []string{
		`{"data":{"items":[],"total":0,"next":"unknown"}}`,
		`{"data":{"items":[],"total":0,"has_more":false,"next":2}}`,
		`{"data":{"items":[],"total":"0"}}`,
		`{"data":{"items":[],"page":1}}`,
		`{"data":{"items":[],"total":0,"page_size":99}}`,
		`{"data":{"items":[],"total":2,"has_more":false}}`,
	} {
		var payload any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := sub2APIInventoryPageFinished(payload, 1, 100, 0, 0); ok {
			t.Fatalf("accepted incomplete proof: %s", body)
		}
	}
}
