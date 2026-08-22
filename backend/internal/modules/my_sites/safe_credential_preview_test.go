package my_sites

import (
	"strings"
	"testing"
)

func TestSafeCredentialPreviewMasksShortAndMediumSecrets(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "length 4", secret: "aB3$", want: "****"},
		{name: "length 5", secret: "aB3$z", want: "****"},
		{name: "length 8", secret: "aB3$zX7!", want: "****"},
		{name: "length 9", secret: "aB3$zX7!q", want: "****X7!q"},
		{name: "length 10", secret: "abcDEF1234", want: "****1234"},
		{name: "length 11", secret: "abcDEF12345", want: "****2345"},
		{name: "length 12", secret: "abcDEF123456", want: "****3456"},
		{name: "length 14", secret: "abcDEF12345678", want: "****5678"},
		{name: "length 15", secret: "abcDEF123456789", want: "abcDEF...6789"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeCredentialPreview(tt.secret)
			if got != tt.want {
				t.Fatalf("safeCredentialPreview() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, tt.secret) {
				t.Fatalf("preview %q contains the complete secret", got)
			}
			if len(tt.secret) > 8 && !strings.HasSuffix(got, tt.secret[len(tt.secret)-4:]) {
				t.Fatalf("preview %q does not preserve the distinguishable last four characters", got)
			}
			if len(tt.secret) == 5 || len(tt.secret) == 8 {
				for _, character := range tt.secret {
					if strings.ContainsRune(got, character) {
						t.Fatalf("preview %q exposes content from a short secret", got)
					}
				}
			}
			if (len(tt.secret) == 11 || len(tt.secret) == 12) && strings.Contains(got, tt.secret[:6]) {
				t.Fatalf("preview %q exposes the first six characters of a medium secret", got)
			}
		})
	}
}
