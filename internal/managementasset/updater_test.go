package managementasset

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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

type githubAssetTransport func(*http.Request) (*http.Response, error)

func (f githubAssetTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchLatestAssetSetsGitHubAuthorization(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "asset-token")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authorization = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"management.html","browser_download_url":"https://example.com/management.html","digest":"sha256:abc123"}]}`))
	}))
	defer server.Close()

	asset, remoteHash, err := fetchLatestAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset() error = %v", err)
	}
	if authorization != "" {
		t.Fatal("token leaked to non-GitHub release URL")
	}
	client := &http.Client{Transport: githubAssetTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer asset-token" {
			t.Fatal("GitHub API token missing")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"assets":[{"name":"management.html","digest":"sha256:abc123"}]}`))}, nil
	})}
	if _, _, errGitHub := fetchLatestAsset(t.Context(), client, "https://api.github.com/repos/owner/repo/releases/latest"); errGitHub != nil {
		t.Fatal(errGitHub)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("asset = %#v, want %q", asset, managementAssetName)
	}
	if remoteHash != "abc123" {
		t.Fatalf("remoteHash = %q, want %q", remoteHash, "abc123")
	}
}

func TestFetchLatestAssetOmitsAuthorizationWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("github_token", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authorization = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"management.html","browser_download_url":"https://example.com/management.html","digest":"sha256:abc123"}]}`))
	}))
	defer server.Close()

	asset, remoteHash, err := fetchLatestAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset() error = %v", err)
	}
	if authorization != "" {
		t.Fatalf("Authorization = %q, want empty", authorization)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("asset = %#v, want %q", asset, managementAssetName)
	}
	if remoteHash != "abc123" {
		t.Fatalf("remoteHash = %q, want %q", remoteHash, "abc123")
	}
}

func resetUpdaterThrottleForTest(t *testing.T) {
	t.Helper()
	lastUpdateCheckMu.Lock()
	lastUpdateCheckTime = time.Time{}
	lastUpdateCheckMu.Unlock()
}

func TestResolveReleaseURLPreservesPanelRepository(t *testing.T) {
	t.Parallel()

	const custom = "https://api.github.com/repos/example/custom-panel/releases/latest"
	cases := []struct {
		repo string
		want string
	}{
		{"   ", defaultManagementReleaseURL},
		{config.DefaultPanelGitHubRepository, defaultManagementReleaseURL},
		{"https://github.com:443/blueskyxn/cpa-panel-lts.git/", defaultManagementReleaseURL},
		{"https://api.github.com/repos/BlueSkyXN/CPA-Panel-LTS", defaultManagementReleaseURL},
		{"BlueSkyXN/CPA-Panel-LTS", defaultManagementReleaseURL},
		{"https://github.com/example/custom-panel", custom},
		{"https://github.com:443/example/custom-panel", custom},
		{"https://github.com/person_team/%63ustom-panel", "https://api.github.com/repos/person_team/custom-panel/releases/latest"},
		{"https://api.github.com:443/repos/example/custom-panel/releases/latest", custom},
		{"https://api.github.com/repos/example/custom-panel/releases", custom},
		{"example/custom-panel", custom},
		{"https://github.com:8443/example/custom-panel", ""},
		{"https://example.org/BlueSkyXN/CPA-Panel-LTS", ""},
		{"not a url", ""},
	}

	for _, tc := range cases {
		if got := resolveReleaseURL(tc.repo); !strings.EqualFold(got, tc.want) {
			t.Errorf("resolveReleaseURL(%q) = %q, want %q", tc.repo, got, tc.want)
		}
	}
}

func TestEnsureLatestManagementHTMLCustomRepositorySkipsDefaultFallback(t *testing.T) {
	t.Run("release metadata fetch failure", func(t *testing.T) {
		resetUpdaterThrottleForTest(t)
		var fallbackHits atomic.Int32
		prevFactory := httpClientFactory
		t.Cleanup(func() { httpClientFactory = prevFactory })

		httpClientFactory = func(string) *http.Client {
			return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == defaultManagementFallbackURL {
					fallbackHits.Add(1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("<html>default-fallback</html>")),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("upstream error")),
				}, nil
			})}
		}

		staticDir := t.TempDir()
		ok := EnsureLatestManagementHTML(context.Background(), staticDir, "", "https://github.com/example/custom-panel")
		if ok {
			t.Fatal("EnsureLatestManagementHTML() = true, want false when custom repository fetch fails")
		}
		if fallbackHits.Load() != 0 {
			t.Fatalf("fallback hits = %d, want 0 for custom repository", fallbackHits.Load())
		}
		if _, err := os.Stat(filepath.Join(staticDir, managementAssetName)); !os.IsNotExist(err) {
			t.Fatalf("unexpected local management.html file after failed custom sync: %v", err)
		}
	})

	t.Run("asset download failure", func(t *testing.T) {
		resetUpdaterThrottleForTest(t)
		var fallbackHits atomic.Int32
		var customAssetHits atomic.Int32
		prevFactory := httpClientFactory
		t.Cleanup(func() { httpClientFactory = prevFactory })

		httpClientFactory = func(string) *http.Client {
			return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case "https://api.github.com/repos/example/custom-panel/releases/latest":
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"assets":[{"name":"management.html","browser_download_url":"https://objects.example/custom-management.html","digest":"sha256:abc123"}]}`)),
					}, nil
				case "https://objects.example/custom-management.html":
					customAssetHits.Add(1)
					return &http.Response{
						StatusCode: http.StatusBadGateway,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("download failed")),
					}, nil
				case defaultManagementFallbackURL:
					fallbackHits.Add(1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("<html>default-fallback</html>")),
					}, nil
				default:
					t.Fatalf("unexpected request URL: %s", req.URL)
					return nil, nil
				}
			})}
		}

		staticDir := t.TempDir()
		ok := EnsureLatestManagementHTML(context.Background(), staticDir, "", "https://github.com:443/example/custom-panel")
		if ok {
			t.Fatal("EnsureLatestManagementHTML() = true, want false when custom asset download fails")
		}
		if got := customAssetHits.Load(); got != 1 {
			t.Fatalf("custom asset download attempts = %d, want 1", got)
		}
		if fallbackHits.Load() != 0 {
			t.Fatalf("fallback hits = %d, want 0 for custom repository", fallbackHits.Load())
		}
		if _, err := os.Stat(filepath.Join(staticDir, managementAssetName)); !os.IsNotExist(err) {
			t.Fatalf("unexpected local management.html file after failed custom download: %v", err)
		}
	})
}

func TestUnsupportedPanelRepositoryRetainsLocalAssetWithoutFetching(t *testing.T) {
	resetUpdaterThrottleForTest(t)
	prevFactory := httpClientFactory
	t.Cleanup(func() { httpClientFactory = prevFactory })
	httpClientFactory = func(string) *http.Client {
		t.Fatal("unexpected download for an unsupported explicit repository")
		return nil
	}
	staticDir := t.TempDir()
	localPath := filepath.Join(staticDir, ManagementFileName)
	const existing = "<html>local panel</html>"
	if err := os.WriteFile(localPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if !EnsureLatestManagementHTML(t.Context(), staticDir, "", "https://example.org/org/repo") {
		t.Fatal("existing panel should remain available")
	}
	if data, err := os.ReadFile(localPath); err != nil || string(data) != existing {
		t.Fatalf("local panel was changed: %v", err)
	}
}

func TestEnsureLatestManagementHTMLDefaultRepositoryUsesFallbackWhenMissing(t *testing.T) {
	for _, repo := range []string{"", config.DefaultPanelGitHubRepository, "https://api.github.com:443/repos/BlueSkyXN/CPA-Panel-LTS/releases/latest"} {
		t.Run("repo="+repo, func(t *testing.T) {
			resetUpdaterThrottleForTest(t)
			var fallbackHits atomic.Int32
			prevFactory := httpClientFactory
			t.Cleanup(func() { httpClientFactory = prevFactory })

			httpClientFactory = func(string) *http.Client {
				return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					switch req.URL.String() {
					case defaultManagementReleaseURL:
						return &http.Response{
							StatusCode: http.StatusForbidden,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader("rate limited")),
						}, nil
					case defaultManagementFallbackURL:
						fallbackHits.Add(1)
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader("<html>official-fallback</html>")),
						}, nil
					default:
						t.Fatalf("unexpected request URL: %s", req.URL)
						return nil, nil
					}
				})}
			}

			staticDir := t.TempDir()
			if !EnsureLatestManagementHTML(context.Background(), staticDir, "", repo) {
				t.Fatal("EnsureLatestManagementHTML() = false, want true for default repository fallback")
			}
			if fallbackHits.Load() != 1 {
				t.Fatalf("fallback hits = %d, want 1", fallbackHits.Load())
			}
			saved, err := os.ReadFile(filepath.Join(staticDir, managementAssetName))
			if err != nil {
				t.Fatalf("read saved fallback asset: %v", err)
			}
			if string(saved) != "<html>official-fallback</html>" {
				t.Fatalf("saved asset = %q, want official fallback HTML", string(saved))
			}
		})
	}
}
