package config

import "testing"

func TestChanshuiManagerRemainsUnsetUntilRuntimeIdentityIsKnown(t *testing.T) {
	for _, manager := range []string{"", "user", "system", "invalid-explicit"} {
		t.Setenv("CHANSHUI_SYSTEMD_MANAGER", manager)
		if got := Load().ChanshuiSystemdManager; got != manager {
			t.Fatalf("explicit manager %q changed to %q", manager, got)
		}
	}
}
