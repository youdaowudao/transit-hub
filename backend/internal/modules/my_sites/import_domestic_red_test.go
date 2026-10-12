package my_sites

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// C5.1 critical business expectations fixed by the primary agent before implementation.
// These use only the existing import contract and an in-memory HTTP transport.
func c51AssertImportedPlatform(t *testing.T, source, requested, target, prefix string) {
	t.Helper()
	remote := &c5RedTransport{platform: source, mainPlatform: target}
	service, repo := c5RedService(t, remote)
	req := c5RedRequest(t, "")
	req.GroupType = requested
	response, err := service.RealConnect(t.Context(), "user-1", req)
	if err != nil {
		t.Fatalf("compatible import %q -> %q must succeed: %v", source, target, err)
	}
	if remote.created["platform"] != target || remote.created["type"] != "apikey" || !strings.HasPrefix(remote.created["name"].(string), prefix+"-【C5-source】-") {
		t.Fatalf("account must use target platform/type/prefix: %v", remote.created)
	}
	credentials := remote.created["credentials"].(map[string]any)
	if credentials["base_url"] != "https://c5-source.invalid" || credentials["api_key"] != "synthetic-c5-key" || credentials["pool_mode"] != true || !reflect.DeepEqual(credentials["model_mapping"], map[string]any{"live-a": "live-a", "live-z": "live-z"}) {
		t.Fatal("account must preserve upstream address/key, pool setting and live identity whitelist")
	}
	if remote.created["priority"] != float64(100) || remote.created["concurrency"] != float64(50) || remote.created["upstream_billing_probe_enabled"] != true || !reflect.DeepEqual(remote.created["group_ids"], []any{float64(7), float64(8)}) {
		t.Fatal("existing account settings and all target groups must remain unchanged")
	}
	extra, _ := remote.created["extra"].(map[string]any)
	for _, key := range []string{"openai_passthrough", "anthropic_passthrough"} {
		value, exists := extra[key]
		if (target == "openai" && key == "openai_passthrough") || (target == "anthropic" && key == "anthropic_passthrough") {
			if !exists || value != false {
				t.Fatal("existing supported passthrough must explicitly remain off")
			}
		} else if exists {
			t.Fatal("target without that passthrough capability must omit its setting")
		}
	}
	if remote.keyCreates != 1 || remote.previews != 1 || remote.accountCreates != 1 || remote.reads != 1 || remote.keyDeletes != 0 || remote.protectedDeletes != 0 || repo.saveCalls != 1 {
		t.Fatal("import must complete the existing single creation/preview/readback/save pipeline")
	}
	if repo.connection == nil || repo.connection.GroupType != target || response.Connection.GroupType != target || !reflect.DeepEqual(repo.connection.OwnGroupIDs, []string{"7", "8"}) {
		t.Fatal("persisted and returned connection types must equal target account platform")
	}
	if response.ConfigurationStatus != "confirmed" || response.Configuration == nil || response.Configuration.Platform != target || response.Configuration.Passthrough || !reflect.DeepEqual(response.Configuration.Models, []string{"live-a", "live-z"}) {
		t.Fatal("readback must confirm target account platform, passthrough off and both live models")
	}
}

func TestC51REDN1DomesticSamePlatform(t *testing.T) {
	c51AssertImportedPlatform(t, "kimi", "", "kimi", "X")
}

func TestC51REDN2OpenAIToDomestic(t *testing.T) {
	c51AssertImportedPlatform(t, "openai", "kimi", "kimi", "X")
}

func TestC51REDN3DomesticToOpenAI(t *testing.T) {
	c51AssertImportedPlatform(t, "deepseek", "openai", "openai", "A")
}

func TestC51REDN4UntypedToDomestic(t *testing.T) {
	c51AssertImportedPlatform(t, "", "zhipu", "zhipu", "X")
}

func TestC51REDE1ExistingFourSamePlatform(t *testing.T) {
	for i, platform := range []string{"openai", "anthropic", "gemini", "antigravity"} {
		t.Run(platform, func(t *testing.T) {
			c51AssertImportedPlatform(t, platform, platform, platform, []string{"A", "B", "C", "D"}[i])
		})
	}
}

func c51AssertRejectedBeforeResources(t *testing.T, remote *c5RedTransport, requested string) {
	t.Helper()
	service, repo := c5RedService(t, remote)
	req := c5RedRequest(t, "")
	req.GroupType = requested
	_, err := service.RealConnect(t.Context(), "user-1", req)
	var failure *ImportFailure
	if !errors.As(err, &failure) || failure.Message != ErrorImportGroupTypeMismatch || failure.Stage != "validation" {
		t.Fatal("invalid platform selection must retain the existing group type validation error")
	}
	if remote.keyCreates != 0 || remote.accountCreates != 0 || remote.previews != 0 || repo.saveCalls != 0 {
		t.Fatal("rejected import must create no upstream Key, main account or successful connection")
	}
}

func TestC51REDE2IncompatiblePlatforms(t *testing.T) {
	c51AssertRejectedBeforeResources(t, &c5RedTransport{platform: "openai", mainPlatform: "anthropic"}, "anthropic")
}

func TestC51REDE3MixedTargetPlatforms(t *testing.T) {
	c51AssertRejectedBeforeResources(t, &c5RedTransport{platform: "openai", secondGroupPlatform: "anthropic"}, "openai")
}

func TestC51REDE4UnsupportedSourcePlatform(t *testing.T) {
	c51AssertRejectedBeforeResources(t, &c5RedTransport{platform: "grok"}, "grok")
}
