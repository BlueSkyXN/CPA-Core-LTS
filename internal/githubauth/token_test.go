package githubauth

import "testing"

func TestGlobalToken(t *testing.T) {
	t.Cleanup(func() { SetToken("") })
	t.Setenv("GITHUB_TOKEN", "env-token")
	SetToken(" config-token ")
	if ResolveToken() != "config-token" {
		t.Fatal("configuration must take precedence")
	}
	for _, raw := range []string{"https://api.github.com/repos/a/b", "https://API.GITHUB.COM:443/repos/a/b"} {
		if TokenForURL(raw) != "config-token" {
			t.Fatalf("missing token for %s", raw)
		}
	}
	for _, raw := range []string{"http://api.github.com/", "https://api.github.com:444/", "https://api.github.com.evil.example/", "https://github.com/", "https://raw.githubusercontent.com/", "https://user@api.github.com/", ":invalid"} {
		if TokenForURL(raw) != "" {
			t.Fatalf("token leaked to %s", raw)
		}
	}
	SetToken("replacement")
	if ResolveToken() != "replacement" {
		t.Fatal("reload did not replace token")
	}
	SetToken("")
	if ResolveToken() != "env-token" {
		t.Fatal("clearing configuration must restore environment fallback")
	}
}

func TestGitStoreTokenRequiresGitHubRepositoryIdentity(t *testing.T) {
	SetToken("")
	t.Cleanup(func() { SetToken("") })
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("github_token", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "synthetic-gitstore-token")
	for _, source := range []string{"https://github.com/example/repo", "git@github.com:example/repo.git", "ssh://git@github.com/example/repo.git"} {
		t.Setenv("GITSTORE_GIT_URL", source)
		if ResolveToken() != "synthetic-gitstore-token" {
			t.Fatalf("missing GitHub token for %s", source)
		}
	}
	for _, source := range []string{"https://github.com.example/repo", "https://example.test/repo?host=github.com", "https://example.test/github.com/repo", "https://gitlab.example/repo", ""} {
		t.Setenv("GITSTORE_GIT_URL", source)
		if ResolveToken() != "" {
			t.Fatalf("accepted non-GitHub GitStore source %s", source)
		}
	}
}
