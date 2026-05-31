package misc

import (
	"testing"
	"time"
)

func TestIsSupportedAntigravityVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "v2", version: "2.10.0", want: true},
		{name: "v3", version: "3.0.1", want: true},
		{name: "trimmed", version: " 2.11.0 ", want: true},
		{name: "v1 deprecated", version: "1.21.9", want: false},
		{name: "v0 deprecated", version: "0.8.2", want: false},
		{name: "empty", version: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSupportedAntigravityVersion(tt.version); got != tt.want {
				t.Fatalf("isSupportedAntigravityVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestAntigravityLatestVersionRejectsDeprecatedCachedVersion(t *testing.T) {
	withAntigravityVersionState(t, "1.21.9", time.Now().Add(time.Hour))

	if got := AntigravityLatestVersion(); got != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want fallback %q", got, antigravityFallbackVersion)
	}
}

func TestAntigravityLatestVersionKeepsSupportedCachedVersion(t *testing.T) {
	withAntigravityVersionState(t, "3.0.1", time.Now().Add(time.Hour))

	if got := AntigravityLatestVersion(); got != "3.0.1" {
		t.Fatalf("AntigravityLatestVersion() = %q, want cached supported version", got)
	}
}

func withAntigravityVersionState(t *testing.T, version string, expiry time.Time) {
	t.Helper()

	antigravityVersionMu.Lock()
	oldVersion := cachedAntigravityVersion
	oldExpiry := antigravityVersionExpiry
	cachedAntigravityVersion = version
	antigravityVersionExpiry = expiry
	antigravityVersionMu.Unlock()

	t.Cleanup(func() {
		antigravityVersionMu.Lock()
		cachedAntigravityVersion = oldVersion
		antigravityVersionExpiry = oldExpiry
		antigravityVersionMu.Unlock()
	})
}
