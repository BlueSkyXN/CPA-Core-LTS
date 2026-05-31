package managementasset

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

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
	base := http.DefaultTransport
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return base.RoundTrip(clone)
	})}
}

func setTestEnv(t *testing.T, key string, value string) {
	t.Helper()

	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func unsetTestEnv(t *testing.T, key string) {
	t.Helper()

	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func latestReleaseServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseResponse{
			Assets: []releaseAsset{{
				Name:               managementAssetName,
				BrowserDownloadURL: "https://example.test/management.html",
				Digest:             "sha256:abcd",
			}},
		})
	}))
}

func githubReleaseURLForTest(t *testing.T, serverURL string) string {
	t.Helper()

	releaseURL, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse release URL: %v", err)
	}
	releaseURL.Host = "api.github.com"
	return releaseURL.String()
}

func TestFetchLatestAssetUsesPanelGitHubTokenWithoutGitStoreMode(t *testing.T) {
	setTestEnv(t, "CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	unsetTestEnv(t, "GITSTORE_GIT_URL")
	unsetTestEnv(t, "GITSTORE_GIT_TOKEN")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()

	client := githubHostRewritingClient(t, server.URL)
	asset, hash, err := fetchLatestAsset(context.Background(), client, githubReleaseURLForTest(t, server.URL))
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("Authorization header = %q, want panel token", gotAuth)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("unexpected asset: %#v", asset)
	}
	if hash != "abcd" {
		t.Fatalf("hash = %q, want abcd", hash)
	}
}

func TestFetchLatestAssetPrefersPanelTokenOverGitStoreToken(t *testing.T) {
	setTestEnv(t, "CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")
	setTestEnv(t, "GITSTORE_GIT_URL", "https://github.com/BlueSkyXN/CPA-Panel-LTS")
	setTestEnv(t, "GITSTORE_GIT_TOKEN", "gitstore-token")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()

	client := githubHostRewritingClient(t, server.URL)
	if _, _, err := fetchLatestAsset(context.Background(), client, githubReleaseURLForTest(t, server.URL)); err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("Authorization header = %q, want panel token", gotAuth)
	}
}

func TestFetchLatestAssetDoesNotLeakPanelTokenToNonGitHubHosts(t *testing.T) {
	setTestEnv(t, "CLIPROXYAPI_PANEL_GITHUB_TOKEN", "panel-token")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()

	if _, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty for non-GitHub host", gotAuth)
	}
}

func TestFetchLatestAssetDoesNotLeakGitStoreTokenToNonGitHubHosts(t *testing.T) {
	setTestEnv(t, "GITSTORE_GIT_URL", "https://github.com/BlueSkyXN/CPA-Panel-LTS")
	setTestEnv(t, "GITSTORE_GIT_TOKEN", "gitstore-token")

	var gotAuth string
	server := latestReleaseServer(t, &gotAuth)
	defer server.Close()

	if _, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty for non-GitHub host", gotAuth)
	}
}
