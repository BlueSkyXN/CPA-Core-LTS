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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

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
	updateMu.Lock()
	clear(panelUpdates)
	updateMu.Unlock()
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

func TestPanelSourceChangesDuringDownload(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name, oldRepo := "release asset", "example/panel-a"
		if fallback {
			name, oldRepo = "official fallback", config.DefaultPanelGitHubRepository
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("MANAGEMENT_STATIC_PATH", dir)
				configFile := filepath.Join(dir, "config.yaml")
				const newRepo = "example/panel-b"
				started, resume := make(chan struct{}), make(chan struct{})
				var newRequests atomic.Int32
				mockPanelTransport(t, func(r *http.Request) (int, string) {
					switch r.URL.String() {
					case resolveReleaseURL(oldRepo):
						if fallback {
							return http.StatusServiceUnavailable, ""
						}
						return http.StatusOK, panelReleaseFixture("a")
					case "https://assets.example/a", defaultManagementFallbackURL:
						close(started)
						<-resume
						return http.StatusOK, "old panel"
					case resolveReleaseURL(newRepo), resolveReleaseURL("EXAMPLE/panel-b"):
						newRequests.Add(1)
						return http.StatusOK, panelReleaseFixture("b")
					case "https://assets.example/b":
						return http.StatusOK, "new panel"
					default:
						return http.StatusNotFound, ""
					}
				})
				setRepo := func(repo string) {
					SetCurrentConfigForFile(&config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: repo}}, configFile)
				}
				t.Cleanup(func() { SetCurrentConfigForFile(nil, configFile) })
				start := func(repo string) <-chan bool {
					done := make(chan bool, 1)
					go func() { done <- EnsureLatestManagementHTML(t.Context(), dir, "", repo) }()
					return done
				}
				setRepo(oldRepo)
				old := start(oldRepo)
				<-started
				setRepo(newRepo)
				newPanel := start(newRepo)
				alsoNew := start("https://github.com:443/EXAMPLE/panel-b")
				synctest.Wait()
				before, _ := os.ReadFile(filepath.Join(dir, ManagementFileName))
				close(resume)
				<-old
				ready, alsoReady := <-newPanel, <-alsoNew
				data, err := os.ReadFile(filepath.Join(dir, ManagementFileName))
				if err != nil || !ready || !alsoReady || string(before) != "new panel" || string(data) != "new panel" || newRequests.Load() != 1 {
					t.Fatalf("new source ready=(%t,%t), metadata requests=%d, file before=%q after=%q, err=%v", ready, alsoReady, newRequests.Load(), before, data, err)
				}
			})
		})
	}
}

func TestCorrectedPanelSourceBootstrapsImmediately(t *testing.T) {
	dir := t.TempDir()
	const valid = "example/panel"
	var requests int
	mockPanelTransport(t, func(r *http.Request) (int, string) {
		requests++
		if r.URL.String() == resolveReleaseURL(valid) {
			return http.StatusOK, panelReleaseFixture("panel")
		}
		return http.StatusOK, "corrected panel"
	})
	if EnsureLatestManagementHTML(t.Context(), dir, "", "https://example.org/unsupported") || requests != 0 {
		t.Fatal("unsupported source downloaded a panel")
	}
	if !EnsureLatestManagementHTML(t.Context(), dir, "", valid) || requests != 2 {
		t.Fatalf("corrected source did not bootstrap immediately: %d HTTP requests", requests)
	}
	if !EnsureLatestManagementHTML(t.Context(), dir, "", valid) || requests != 2 {
		t.Fatal("the same source and destination must share the throttle")
	}
	if !EnsureLatestManagementHTML(t.Context(), t.TempDir(), "", valid) || requests != 4 {
		t.Fatal("a second destination must bootstrap independently")
	}
}

func panelDirectoryAlias(t *testing.T, dir string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(dir, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return alias
}

func TestPanelTargetResolvesDirectoryAliasesBeforeBootstrap(t *testing.T) {
	dir := t.TempDir()
	alias := panelDirectoryAlias(t, dir)
	first := panelUpdatePath(filepath.Join(dir, "new", "static"))
	linked := filepath.Join(alias, "new", "static")
	if got := panelUpdatePath(linked); got != first {
		t.Fatalf("before bootstrap: alias target %q, real target %q", got, first)
	}
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := panelUpdatePath(linked); got != first {
		t.Fatalf("after bootstrap: alias target %q, real target %q", got, first)
	}
}

func TestLatePanelDownloadAfterDirectoryAliasHandover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, key := range []string{"MANAGEMENT_STATIC_PATH", "WRITABLE_PATH", "writable_path"} {
			t.Setenv(key, "")
		}
		dir := t.TempDir()
		alias := panelDirectoryAlias(t, dir)
		started, resume := make(chan struct{}), make(chan struct{})
		mockPanelTransport(t, func(req *http.Request) (int, string) {
			switch req.URL.Path {
			case "/repos/example/a/releases/latest":
				return http.StatusOK, panelReleaseFixture("a")
			case "/repos/example/b/releases/latest":
				return http.StatusOK, panelReleaseFixture("b")
			case "/a":
				close(started)
				<-resume
				return http.StatusOK, "old panel"
			case "/b":
				return http.StatusOK, "new panel"
			}
			return http.StatusNotFound, ""
		})
		start := func(dir, repo string) (*Registration, <-chan bool) {
			cfg := &config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: repo}}
			file := filepath.Join(dir, "config.yaml")
			r := Register(cfg, file)
			t.Cleanup(r.Close)
			done := make(chan bool, 1)
			go func() { done <- r.Ensure(t.Context(), StaticDir(file)) }()
			return r, done
		}
		old, oldDone := start(dir, "example/a")
		<-started
		_, nextDone := start(alias, "example/b")
		synctest.Wait()
		target := filepath.Join(dir, "static", ManagementFileName)
		before, _ := os.ReadFile(target)
		old.Close()
		close(resume)
		<-oldDone
		ready := <-nextDone
		after, err := os.ReadFile(target)
		if err != nil || !ready || string(before) != "new panel" || string(after) != "new panel" {
			t.Fatalf("successor ready=%v; before old completion=%q, after=%q, err=%v", ready, before, after, err)
		}
	})
}

func TestPanelRegistrationReleasesOnlyItsOwnSettingsAndDownload(t *testing.T) {
	for _, key := range []string{"MANAGEMENT_STATIC_PATH", "WRITABLE_PATH", "writable_path"} {
		t.Setenv(key, "")
	}
	cfg := &config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: "example/panel"}}
	for range 3 {
		file := filepath.Join(t.TempDir(), "config.yaml")
		target := configPanelTarget(file)
		first := Register(cfg, file)
		replacement := Register(cfg, file)
		first.Update(&config.Config{})
		first.Close()
		updateMu.Lock()
		settings := configuredPanels[target]
		updateMu.Unlock()
		if settings == nil || settings.owner != replacement || settings.releaseURL != resolveReleaseURL("example/panel") {
			t.Fatal("a superseded registration changed its replacement")
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		pending, ownsDownload := beginPanelUpdate(target, settings.panelSource, settings, cancel)
		if !ownsDownload {
			t.Fatal("replacement could not start its download")
		}
		replacement.Close()
		replacement.Close()
		replacement.Update(cfg)
		updateMu.Lock()
		_, registered := configuredPanels[target]
		_, canPublish := panelUpdates[target]
		updateMu.Unlock()
		close(pending.done)
		if ctx.Err() == nil || registered || canPublish || replacement.Ensure(t.Context(), StaticDir(file)) {
			t.Fatal("closed server retained its registration or could start another download")
		}
	}
}

func TestCorrectedPanelProxyRetriesImmediately(t *testing.T) {
	const repo = "example/panel"
	const bad, good = "http://old.invalid", "http://new.invalid"
	dir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", dir)
	file := filepath.Join(dir, "config.yaml")
	configure := func(proxy string) {
		cfg := &config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: repo}}
		cfg.ProxyURL = proxy
		SetCurrentConfigForFile(cfg, file)
	}
	t.Cleanup(func() { SetCurrentConfigForFile(nil, file) })
	previous := httpClientFactory
	t.Cleanup(func() { httpClientFactory = previous })
	var goodCalls int
	httpClientFactory = func(proxy string) *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			code, body := http.StatusServiceUnavailable, ""
			if proxy == good {
				goodCalls++
				code, body = http.StatusOK, "panel"
				if r.URL.String() == resolveReleaseURL(repo) {
					body = panelReleaseFixture("panel")
				}
			}
			return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
	}
	configure(bad)
	if EnsureLatestManagementHTML(t.Context(), dir, bad, repo) {
		t.Fatal("unavailable proxy returned a panel")
	}
	configure(good)
	if !EnsureLatestManagementHTML(t.Context(), dir, good, repo) || goodCalls != 2 {
		t.Fatalf("corrected proxy received %d requests, want release metadata and asset", goodCalls)
	}
}

func TestPanelSourceChangeWithoutAnotherDownload(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ManagementFileName)
	if err := os.WriteFile(target, []byte("installed panel"), 0o600); err != nil {
		t.Fatal(err)
	}
	const repo = "example/panel-a"
	started, resume := make(chan struct{}), make(chan struct{})
	mockPanelTransport(t, func(req *http.Request) (int, string) {
		if req.URL.String() == resolveReleaseURL(repo) {
			return http.StatusOK, panelReleaseFixture("a")
		}
		close(started)
		<-resume
		return http.StatusOK, "obsolete download"
	})
	setRepo := func(repo string) {
		SetCurrentConfig(&config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: repo}})
	}
	t.Cleanup(func() { SetCurrentConfig(nil) })
	setRepo(repo)
	done := make(chan bool, 1)
	go func() { done <- EnsureLatestManagementHTML(t.Context(), dir, "", repo) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("the selected download did not start")
	}
	setRepo("example/panel-b")
	setRepo(repo)
	close(resume)
	if !<-done {
		t.Fatal("preinstalled local panel should remain available")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "installed panel" {
		t.Fatalf("download from before the configuration changes replaced the local file: %q, %v", data, err)
	}
}

func TestAutoUpdaterUsesScheduledConfigFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, key := range []string{"MANAGEMENT_STATIC_PATH", "WRITABLE_PATH", "writable_path"} {
			t.Setenv(key, "")
		}
		fileA := filepath.Join(t.TempDir(), "config.yaml")
		fileB := filepath.Join(t.TempDir(), "config.yaml")
		configure := func(file, repo string) {
			cfg := &config.Config{RemoteManagement: config.RemoteManagement{PanelGitHubRepository: repo}}
			SetCurrentConfigForFile(cfg, file)
			t.Cleanup(func() { SetCurrentConfigForFile(nil, file) })
		}
		configure(fileA, "example/panel-a")
		configure(fileB, "example/panel-b")
		previous, _ := schedulerConfigPath.Load().(string)
		schedulerConfigPath.Store(fileA)
		t.Cleanup(func() { schedulerConfigPath.Store(previous) })
		mockPanelTransport(t, func(r *http.Request) (int, string) {
			switch r.URL.Path {
			case "/repos/example/panel-a/releases/latest":
				return http.StatusOK, panelReleaseFixture("a")
			case "/a":
				return http.StatusOK, "panel A"
			default:
				return http.StatusNotFound, ""
			}
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); runAutoUpdater(ctx) }()
		synctest.Wait()
		cancel()
		<-done
		if data, err := os.ReadFile(FilePath(fileA)); err != nil || string(data) != "panel A" {
			t.Fatalf("scheduled panel = %q, err = %v", data, err)
		}
		if _, err := os.Stat(FilePath(fileB)); !os.IsNotExist(err) {
			t.Fatalf("unexpected panel for unscheduled file: %v", err)
		}
	})
}

func panelReleaseFixture(asset string) string {
	return `{"assets":[{"name":"management.html","browser_download_url":"https://assets.example/` + asset + `"}]}`
}

func mockPanelTransport(t *testing.T, serve func(*http.Request) (int, string)) {
	t.Helper()
	resetUpdaterThrottleForTest(t)
	old := httpClientFactory
	t.Cleanup(func() { httpClientFactory = old })
	httpClientFactory = func(string) *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			code, body := serve(req)
			return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
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
