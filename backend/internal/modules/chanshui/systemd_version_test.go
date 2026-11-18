package chanshui

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func managerVersionProps(version string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"Version": dbus.MakeVariant(version)}
}

// Ubuntu 22.04 ships systemd 249; every D-Bus property and method used by the
// detection runner exists there, so it must not be refused by a version gate.
func TestChanshuiManagerVersionAcceptsUbuntu2204AndNewer(t *testing.T) {
	for _, version := range []string{"249", "249.11-0ubuntu3.12", "252.39-1~deb12u1", "257.13-1~deb13u1"} {
		if err := checkManagerVersion(managerVersionProps(version), nil); err != nil {
			t.Fatalf("systemd %s was refused: %v", version, err)
		}
	}
}

func TestChanshuiManagerVersionRejectsOlderWithActualVersion(t *testing.T) {
	err := checkManagerVersion(managerVersionProps("248.3-1ubuntu8"), nil)
	if err == nil {
		t.Fatal("systemd 248 was accepted below the verified minimum")
	}
	for _, want := range []string{"249", "248.3-1ubuntu8", "尚未启动检测"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
}

func TestChanshuiManagerVersionExplainsUnreadableVersion(t *testing.T) {
	if err := checkManagerVersion(map[string]dbus.Variant{}, nil); err == nil || !strings.Contains(err.Error(), "无法识别") {
		t.Fatalf("missing version was not explained: %v", err)
	}
	err := checkManagerVersion(managerVersionProps("custom-build"), nil)
	if err == nil || !strings.Contains(err.Error(), "custom-build") || !strings.Contains(err.Error(), "systemctl --version") {
		t.Fatalf("unrecognized version lost its text or next step: %v", err)
	}
	err = checkManagerVersion(nil, errors.New("access denied"))
	if err == nil || !strings.Contains(err.Error(), "无法读取") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("read failure lost its cause: %v", err)
	}
}
