package config

import "testing"

func TestC5APIOnlyRequiresExplicitOne(t *testing.T) {
	for _, value := range []string{"", "0", "false", "true", " 1", "1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TRANSITHUB_API_ONLY", value)
			if got := Load().APIOnly; got != (value == "1") {
				t.Fatalf("API-only enabled=%t for non-explicit value", got)
			}
		})
	}
}
