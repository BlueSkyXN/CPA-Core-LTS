package managementasset

import "testing"

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
