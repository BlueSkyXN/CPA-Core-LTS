package managementasset

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func githubHostRewritingClient(t *testing.T, serverURL string) *http.Client {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

func latestReleaseServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{Assets: []releaseAsset{{
			Name:               managementAssetName,
			BrowserDownloadURL: "https://example.test/management.html",
			Digest:             "sha256:abcd",
		}}})
	}))
}

func githubReleaseURLForTest(t *testing.T, serverURL string) string {
	t.Helper()
	releaseURL, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse release URL: %v", err)
	}
	releaseURL.Scheme = "https"
	releaseURL.Host = "api.github.com"
	return releaseURL.String()
}

func TestResolveReleaseURLDefaultsToCPAPanelLTS(t *testing.T) {
	t.Parallel()

	if got := resolveReleaseURL(""); got != defaultManagementReleaseURL {
		t.Fatalf("resolveReleaseURL(\"\") = %q, want %q", got, defaultManagementReleaseURL)
	}

	if got := resolveReleaseURL("not a url"); got != defaultManagementReleaseURL {
		t.Fatalf("resolveReleaseURL(invalid) = %q, want %q", got, defaultManagementReleaseURL)
	}
}

func TestResolveReleaseURLAcceptsGitHubRepository(t *testing.T) {
	t.Parallel()

	got := resolveReleaseURL("https://github.com/BlueSkyXN/CPA-Panel-LTS")
	want := "https://api.github.com/repos/BlueSkyXN/CPA-Panel-LTS/releases/latest"
	if got != want {
		t.Fatalf("resolveReleaseURL(repo) = %q, want %q", got, want)
	}
}

func TestAutoUpdateSkipReason(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantReason string
		wantSkip   bool
	}{
		{
			name:       "nil config",
			cfg:        nil,
			wantReason: "config not yet available",
			wantSkip:   true,
		},
		{
			name: "cluster mode",
			cfg: &config.Config{
				Home: config.HomeConfig{Enabled: true},
			},
			wantReason: "cluster mode enabled",
			wantSkip:   true,
		},
		{
			name: "control panel disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableControlPanel: true},
			},
			wantReason: "control panel disabled",
			wantSkip:   true,
		},
		{
			name: "auto update disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableAutoUpdatePanel: true},
			},
			wantReason: "disable-auto-update-panel is enabled",
			wantSkip:   true,
		},
		{
			name:       "enabled",
			cfg:        &config.Config{},
			wantReason: "",
			wantSkip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotSkip := autoUpdateSkipReason(tt.cfg)
			if gotReason != tt.wantReason || gotSkip != tt.wantSkip {
				t.Fatalf("autoUpdateSkipReason() = (%q, %t), want (%q, %t)", gotReason, gotSkip, tt.wantReason, tt.wantSkip)
			}
		})
	}
}

func TestFetchLatestAssetUsesDedicatedPanelToken(t *testing.T) {
	t.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	t.Setenv("GITSTORE_GIT_URL", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()
	client := githubHostRewritingClient(t, server.URL)
	asset, hash, err := fetchLatestAsset(context.Background(), client, githubReleaseURLForTest(t, server.URL))
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("Authorization = %q, want dedicated panel token", gotAuth)
	}
	if asset == nil || asset.Name != managementAssetName || hash != "abcd" {
		t.Fatalf("asset/hash = %#v/%q, want management asset/abcd", asset, hash)
	}
}

func TestFetchLatestAssetUsesLegacyGitStoreTokenOnlyForGitHub(t *testing.T) {
	t.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "https://github.com/example/private-config.git")
	t.Setenv("GITSTORE_GIT_TOKEN", "legacy-token")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()
	client := githubHostRewritingClient(t, server.URL)
	if _, _, err := fetchLatestAsset(context.Background(), client, githubReleaseURLForTest(t, server.URL)); err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer legacy-token" {
		t.Fatalf("Authorization = %q, want legacy GitStore token", gotAuth)
	}
}

func TestPanelAuthorizationHeaderKeepsLegacySSHGitHubCompatibility(t *testing.T) {
	t.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "git@github.com:example/private-config.git")
	t.Setenv("GITSTORE_GIT_TOKEN", "legacy-token")
	if got := panelAuthorizationHeader("https://api.github.com/repos/example/panel/releases/latest"); got != "Bearer legacy-token" {
		t.Fatalf("panelAuthorizationHeader() = %q, want legacy token for SSH GitHub repository", got)
	}
}

func TestFetchLatestAssetDoesNotSendTokensToNonGitHubHosts(t *testing.T) {
	t.Setenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	t.Setenv("GITSTORE_GIT_URL", "https://github.com/example/private-config.git")
	t.Setenv("GITSTORE_GIT_TOKEN", "legacy-token")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()
	if _, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty for non-GitHub host", gotAuth)
	}
	if isHTTPSGitHubURL("https://api.github.com.evil.example/releases/latest") {
		t.Fatal("lookalike GitHub hostname accepted")
	}
}
