package auth

import "testing"

func TestHydrateAuthFromMetadata_Prefix(t *testing.T) {
	cases := []struct {
		name       string
		metadata   map[string]any
		wantPrefix string
	}{
		{name: "nil metadata", metadata: nil},
		{name: "empty metadata", metadata: map[string]any{}},
		{name: "no prefix key", metadata: map[string]any{"email": "test"}},
		{name: "simple prefix", metadata: map[string]any{"prefix": "qwenai"}, wantPrefix: "qwenai"},
		{name: "prefix with spaces", metadata: map[string]any{"prefix": "  qwenai  "}, wantPrefix: "qwenai"},
		{name: "prefix with leading slash", metadata: map[string]any{"prefix": "/qwenai"}, wantPrefix: "qwenai"},
		{name: "prefix with trailing slash", metadata: map[string]any{"prefix": "qwenai/"}, wantPrefix: "qwenai"},
		{name: "prefix with both slashes", metadata: map[string]any{"prefix": "/qwenai/"}, wantPrefix: "qwenai"},
		{name: "prefix containing slash rejected", metadata: map[string]any{"prefix": "qwen/ai"}},
		{name: "empty prefix after trim", metadata: map[string]any{"prefix": "  "}},
		{name: "slash-only prefix", metadata: map[string]any{"prefix": "///"}},
		{name: "non-string prefix ignored", metadata: map[string]any{"prefix": 42}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Metadata: tc.metadata}
			HydrateAuthFromMetadata(auth)
			if auth.Prefix != tc.wantPrefix {
				t.Fatalf("Prefix = %q, want %q", auth.Prefix, tc.wantPrefix)
			}
		})
	}
}

func TestHydrateAuthFromMetadata_CustomHeaders(t *testing.T) {
	auth := &Auth{
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"headers": map[string]any{"X-Custom": "value"},
		},
	}

	HydrateAuthFromMetadata(auth)
	if auth.Attributes["header:X-Custom"] != "value" {
		t.Fatalf("custom header not applied: %v", auth.Attributes)
	}
}

func TestHydrateAuthFromMetadata_Disabled(t *testing.T) {
	auth := &Auth{Metadata: map[string]any{"disabled": true}}

	HydrateAuthFromMetadata(auth)
	if !auth.Disabled {
		t.Fatal("Disabled = false, want true")
	}
	if auth.Status != StatusDisabled {
		t.Fatalf("Status = %q, want %q", auth.Status, StatusDisabled)
	}
}

func TestHydrateAuthFromMetadata_NilAuth(t *testing.T) {
	HydrateAuthFromMetadata(nil)
}
