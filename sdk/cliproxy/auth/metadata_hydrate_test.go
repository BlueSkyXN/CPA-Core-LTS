package auth

import "testing"

func TestNormalizeAuthPrefix(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty"},
		{name: "simple", raw: "team", want: "team"},
		{name: "spaces and slashes", raw: " /team/ ", want: "team"},
		{name: "nested rejected", raw: "team/child"},
		{name: "slash only", raw: "///"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeAuthPrefix(tt.raw); got != tt.want {
				t.Fatalf("NormalizeAuthPrefix(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestHydrateAuthFromMetadata(t *testing.T) {
	auth := &Auth{
		Status:     StatusActive,
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"prefix":   " /team/ ",
			"disabled": true,
			"headers":  map[string]any{"X-Team": "blue"},
		},
	}
	HydrateAuthFromMetadata(auth)
	if auth.Prefix != "team" {
		t.Fatalf("Prefix = %q, want team", auth.Prefix)
	}
	if !auth.Disabled || auth.Status != StatusDisabled {
		t.Fatalf("Disabled/Status = %v/%q, want true/%q", auth.Disabled, auth.Status, StatusDisabled)
	}
	if got := auth.Attributes["header:X-Team"]; got != "blue" {
		t.Fatalf("header:X-Team = %q, want blue", got)
	}
}

func TestHydrateAuthFromMetadataNilAuth(t *testing.T) {
	HydrateAuthFromMetadata(nil)
}
