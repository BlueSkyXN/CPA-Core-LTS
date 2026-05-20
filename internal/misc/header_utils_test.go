package misc

import (
	"strings"
	"testing"
)

func TestGeminiCLIUserAgentIncludesTerminalMarker(t *testing.T) {
	ua := GeminiCLIUserAgent("gemini-test")

	if !strings.Contains(ua, "GeminiCLI/0.34.0/gemini-test") {
		t.Fatalf("GeminiCLIUserAgent() = %q, want Gemini CLI 0.34.0 model marker", ua)
	}
	if !strings.Contains(ua, "; terminal)") {
		t.Fatalf("GeminiCLIUserAgent() = %q, want terminal marker", ua)
	}
}
