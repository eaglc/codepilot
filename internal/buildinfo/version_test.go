package buildinfo

import (
	"strings"
	"testing"
)

func TestFormatSanitizesCanonicalVersionOutput(t *testing.T) {
	if got, want := Format(" 1.2.3 ", "abc\ndef", ""), "codepilot 1.2.3 (commit abc?def, built unknown)"; got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
	long := strings.Repeat("版", maximumValueLength+1)
	got := Format(long, "commit", "date")
	if strings.Count(got, "版") != maximumValueLength {
		t.Fatalf("Format() did not bound metadata by rune: %q", got)
	}
}
