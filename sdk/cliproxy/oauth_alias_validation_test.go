package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestValidateOAuthAliasExclusions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		aliases        map[string][]config.OAuthModelAlias
		excluded       map[string][]string
		wantCount      int
		wantSubstrings []string
	}{
		{
			name:      "empty aliases returns nil",
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "empty exclusions returns nil",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			wantCount: 0,
		},
		{
			name: "wildcard matches alias upstream in same channel",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			excluded:  map[string][]string{"antigravity": {"claude-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="opus"`,
				`channel="antigravity"`,
				`upstream="claude-opus-4-6-thinking"`,
				`pattern="claude-*"`,
			},
		},
		{
			name: "exact match without wildcard",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5", Alias: "sonnet"}},
			},
			excluded:  map[string][]string{"claude": {"claude-sonnet-4-5"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="sonnet"`,
				`upstream="claude-sonnet-4-5"`,
				`pattern="claude-sonnet-4-5"`,
			},
		},
		{
			name: "different channel does not warn",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {{Name: "claude-opus-4-6-thinking", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "case-insensitive matching preserves original text in warning",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "Claude-Opus-4-6", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"CLAUDE-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`upstream="Claude-Opus-4-6"`,
				`pattern="CLAUDE-*"`,
			},
		},
		{
			name: "self alias is filtered",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "Claude-Opus-4-6", Alias: "claude-opus-4-6"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 0,
		},
		{
			name: "multiple aliases only colliding aliases warn",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {
					{Name: "claude-opus-4-6-thinking", Alias: "opus"},
					{Name: "claude-haiku-4-5", Alias: "haiku"},
				},
			},
			excluded:  map[string][]string{"claude": {"claude-opus-*"}},
			wantCount: 1,
			wantSubstrings: []string{
				`alias="opus"`,
			},
		},
		{
			name: "empty pattern or alias data is skipped",
			aliases: map[string][]config.OAuthModelAlias{
				"claude": {
					{Name: "", Alias: "opus"},
					{Name: "claude-opus-4-6", Alias: ""},
					{Name: "claude-opus-4-6", Alias: "opus"},
				},
			},
			excluded:  map[string][]string{"claude": {"", "claude-*"}},
			wantCount: 1,
		},
		{
			name: "channel key is normalized",
			aliases: map[string][]config.OAuthModelAlias{
				" CLAUDE ": {{Name: "claude-opus-4-6", Alias: "opus"}},
			},
			excluded:  map[string][]string{"claude": {"claude-*"}},
			wantCount: 1,
		},
		{
			name: "documented antigravity case warns for non-self aliases",
			aliases: map[string][]config.OAuthModelAlias{
				"antigravity": {
					{Name: "claude-opus-4-6-thinking", Alias: "opus"},
					{Name: "claude-opus-4-6-thinking", Alias: "opus[1m]"},
					{Name: "claude-opus-4-6-thinking", Alias: "claude-opus-4-6-thinking"},
				},
			},
			excluded:  map[string][]string{"antigravity": {"claude-*"}},
			wantCount: 2,
			wantSubstrings: []string{
				`alias="opus"`,
				`alias="opus[1m]"`,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateOAuthAliasExclusions(tt.aliases, tt.excluded)
			if len(got) != tt.wantCount {
				t.Fatalf("warning count: got %d, want %d; warnings=%v", len(got), tt.wantCount, got)
			}
			for _, want := range tt.wantSubstrings {
				found := false
				for _, warning := range got {
					if strings.Contains(warning, want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected warning containing %q, got %v", want, got)
				}
			}
		})
	}
}
