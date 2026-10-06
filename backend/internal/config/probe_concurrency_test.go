package config

import "testing"

func TestProbeGlobalConcurrencyConfiguration(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{{"", 12}, {"invalid", 12}, {"0", 12}, {"-1", 12}, {"8", 8}, {"24", 24}} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("TRANSITHUB_PROBE_GLOBAL_CONCURRENCY", test.value)
			if got := envPositiveInt("TRANSITHUB_PROBE_GLOBAL_CONCURRENCY", 12); got != test.want {
				t.Fatalf("configured global probe cap=%d want %d", got, test.want)
			}
		})
	}
}
