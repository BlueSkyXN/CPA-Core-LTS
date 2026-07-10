package executor

import (
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveCodexModelHeaderProfileUsesCodexProviderRegardlessOfRegistrationOrder(t *testing.T) {
	tests := []struct {
		name       string
		providers  []string
		modelID    string
		clientBase string
	}{
		{
			name:       "codex-first",
			providers:  []string{"codex", "openai-compatibility"},
			modelID:    "test-codex-header-profile-order-a",
			clientBase: "test-codex-header-profile-order-a",
		},
		{
			name:       "codex-last",
			providers:  []string{"openai-compatibility", "codex"},
			modelID:    "test-codex-header-profile-order-b",
			clientBase: "test-codex-header-profile-order-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.GetGlobalRegistry()
			clientIDs := make([]string, 0, len(tt.providers))
			for _, provider := range tt.providers {
				clientID := tt.clientBase + "-" + provider
				clientIDs = append(clientIDs, clientID)
				userAgent := "other-provider/1.0"
				if provider == "codex" {
					userAgent = "codex-provider/1.0"
				}
				reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
					ID: tt.modelID,
					Config: &registry.ModelConfig{OverrideHeader: map[string]string{
						"user-agent": userAgent,
					}},
				}})
			}
			defer func() {
				for _, clientID := range clientIDs {
					reg.UnregisterClient(clientID)
				}
			}()

			profile := resolveCodexModelHeaderProfile(tt.modelID)
			headers := http.Header{}
			applyModelHeaderOverrides(headers, profile)

			if got := headers.Get("User-Agent"); got != "codex-provider/1.0" {
				t.Fatalf("User-Agent = %q, want Codex provider override", got)
			}
			if profile.digest == ([sha256.Size]byte{}) {
				t.Fatal("profile digest is empty, want resolved override digest")
			}
		})
	}
}

func TestCodexModelHeaderProfileIsStableSnapshot(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-codex-header-profile-snapshot"
	modelID := "test-codex-header-profile-snapshot-model"
	defer reg.UnregisterClient(clientID)

	register := func(userAgent string) {
		reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
			ID: modelID,
			Config: &registry.ModelConfig{OverrideHeader: map[string]string{
				"user-agent": userAgent,
			}},
		}})
	}

	register("snapshot-a/1.0")
	profileA := resolveCodexModelHeaderProfile(modelID)
	register("snapshot-b/1.0")
	profileB := resolveCodexModelHeaderProfile(modelID)

	headers := http.Header{}
	applyModelHeaderOverrides(headers, profileA)
	if got := headers.Get("User-Agent"); got != "snapshot-a/1.0" {
		t.Fatalf("snapshot A User-Agent = %q, want snapshot-a/1.0", got)
	}
	if profileA.digest == profileB.digest {
		t.Fatal("profile digest did not change after override update")
	}

	applyModelHeaderOverrides(headers, profileB)
	if got := headers.Get("User-Agent"); got != "snapshot-b/1.0" {
		t.Fatalf("snapshot B User-Agent = %q, want snapshot-b/1.0", got)
	}
	applyModelHeaderOverrides(http.Header{}, profileA)
	if got := profileA.overrides["User-Agent"]; got != "snapshot-a/1.0" {
		t.Fatalf("applying profile mutated snapshot: User-Agent = %q", got)
	}
}

func TestCodexModelHeaderProfileDigestNormalizesHeaderKeys(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-codex-header-profile-normalization"
	defer reg.UnregisterClient(clientID)

	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{
			ID: "test-codex-header-profile-normalization-a",
			Config: &registry.ModelConfig{OverrideHeader: map[string]string{
				"user-agent": "normalized/1.0",
				"ORIGINATOR": "codex",
			}},
		},
		{
			ID: "test-codex-header-profile-normalization-b",
			Config: &registry.ModelConfig{OverrideHeader: map[string]string{
				"User-Agent": "normalized/1.0",
				"originator": "codex",
			}},
		},
	})

	profileA := resolveCodexModelHeaderProfile("test-codex-header-profile-normalization-a")
	profileB := resolveCodexModelHeaderProfile("test-codex-header-profile-normalization-b")
	if profileA.digest != profileB.digest {
		t.Fatalf("equivalent header profiles have different digests: %x != %x", profileA.digest, profileB.digest)
	}
}
