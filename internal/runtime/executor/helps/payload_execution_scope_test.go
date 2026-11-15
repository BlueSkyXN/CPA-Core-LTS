package helps

import (
	"bytes"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func scopedPayloadModel() config.PayloadModelRule {
	return config.PayloadModelRule{Name: "scope-model", Scope: "upstream", Protocol: "codex", AuthKind: "apikey", RequestPaths: []string{"/v1/responses", "/backend-api/codex/responses", "/v1/chat/completions"}}
}

func TestPayloadExecutionScopeAllRuleKinds(t *testing.T) {
	for _, section := range []string{"default", "default-raw", "override", "override-raw", "filter"} {
		for _, tc := range []struct {
			name, authKind, path string
			match                bool
		}{
			{"api key responses", "apikey", "/v1/responses", true},
			{"api key native path", "apikey", "/backend-api/codex/responses", true},
			{"api key chat", "apikey", "/v1/chat/completions", true},
			{"subscription", "oauth", "/v1/responses", false},
			{"unknown credential", "", "/v1/responses", false},
			{"missing path", "apikey", "", false},
			{"image bridge", "apikey", "/v1/images/generations", false},
			{"image edit", "apikey", "/v1/images/edits", false},
			{"compact", "apikey", "/v1/responses/compact", false},
			{"no suffix match", "apikey", "/prefix/v1/responses", false},
		} {
			t.Run(section+"/"+tc.name, func(t *testing.T) {
				cfg := &config.Config{}
				rule := config.PayloadRule{Models: []config.PayloadModelRule{scopedPayloadModel()}, Params: map[string]any{"service_tier": "flex"}}
				payload := []byte(`{"model":"scope-model","input":"synthetic"}`)
				if strings.HasSuffix(section, "-raw") {
					rule.Params["service_tier"] = `"flex"`
				}
				switch section {
				case "default":
					cfg.Payload.Default = []config.PayloadRule{rule}
				case "default-raw":
					cfg.Payload.DefaultRaw = []config.PayloadRule{rule}
				case "override":
					cfg.Payload.Override = []config.PayloadRule{rule}
				case "override-raw":
					cfg.Payload.OverrideRaw = []config.PayloadRule{rule}
				case "filter":
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: rule.Models, Params: []string{"service_tier"}}}
					payload = []byte(`{"model":"scope-model","service_tier":"priority"}`)
				}
				before := bytes.Clone(payload)
				out, touched := ApplyPayloadConfigWithAuthTrackedPaths(cfg, tc.authKind, "scope-model", "codex", "responses", "", payload, payload, "client-alias", tc.path, http.Header{"X-Auth-Kind": {"apikey"}}, "service_tier")
				if !bytes.Equal(payload, before) {
					t.Fatal("mutated original request")
				}
				if !tc.match {
					if !bytes.Equal(out, before) || &out[0] != &payload[0] || touched["service_tier"] {
						t.Fatal("unmatched rule changed payload or tracking")
					}
				} else if section == "filter" {
					if ojson.GetBytes(out, "service_tier").Exists() || !touched["service_tier"] {
						t.Fatal("matching filter was not applied")
					}
				} else if ojson.GetBytes(out, "service_tier").String() != "flex" || !touched["service_tier"] {
					t.Fatal("matching rule was not applied")
				}
			})
		}
	}
}

func TestPayloadFinalizerSelectedAuthOverridesUntrustedHints(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{scopedPayloadModel()}, Params: map[string]any{"service_tier": "flex"}}}}}
	payload := []byte(`{"model":"scope-model","service_tier":"priority","metadata":{"auth-kind":"apikey"}}`)
	req := cliproxyexecutor.Request{Model: "scope-model", Payload: payload, Metadata: map[string]any{"auth_kind": "apikey"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/responses", "auth_kind": "apikey"}, Headers: http.Header{"Auth-Kind": {"apikey"}}}
	for _, kind := range []string{"apikey", "oauth", "", "apikey", "oauth"} {
		kindAuth := &cliproxyauth.Auth{Attributes: map[string]string{"auth_kind": kind}}
		out := NewPayloadFinalizer(cfg, "codex", "scope-model", "codex", "", payload, req, opts, kindAuth)(payload)
		want := "priority"
		if kind == "apikey" {
			want = "flex"
		}
		if got := ojson.GetBytes(out, "service_tier").String(); got != want {
			t.Fatalf("kind=%q tier=%q want=%q", kind, got, want)
		}
		if ojson.GetBytes(payload, "service_tier").String() != "priority" {
			t.Fatal("attempt leaked tier into original request")
		}
	}
}

func TestPayloadExecutionScopePreservesRuleOrderAndLegacy(t *testing.T) {
	model := scopedPayloadModel()
	second := model
	second.Match = []map[string]any{{"service_tier": "flex"}}
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{
		{Models: []config.PayloadModelRule{model}, Params: map[string]any{"service_tier": "flex"}},
		{Models: []config.PayloadModelRule{second}, Params: map[string]any{"audit": "matched-after-override"}},
	}}}
	body := []byte(`{"model":"scope-model"}`)
	out := ApplyPayloadConfigWithRequest(cfg, "scope-model", "codex", "responses", "", body, body, "alias", "/v1/responses", nil, "apikey")
	if ojson.GetBytes(out, "audit").String() != "matched-after-override" {
		t.Fatal("later rule did not see earlier override")
	}
	cfg.Payload.Override = cfg.Payload.Override[:1]
	cfg.Payload.Override[0].Models[0].AuthKind = ""
	cfg.Payload.Override[0].Models[0].RequestPaths = nil
	out = ApplyPayloadConfigWithRequest(cfg, "scope-model", "codex", "responses", "", body, body, "alias", "", nil)
	if ojson.GetBytes(out, "service_tier").String() != "flex" {
		t.Fatal("legacy rule stopped matching")
	}
	for _, invalid := range []config.PayloadModelRule{
		{AuthKind: "unknown"}, {RequestPaths: []string{}}, {RequestPaths: []string{""}},
	} {
		if payloadExecutionScopeMatches(invalid, "unknown", "") {
			t.Fatalf("invalid runtime gate matched: %+v", invalid)
		}
	}
}

func BenchmarkPayloadExecutionScopeSkip(b *testing.B) {
	for _, size := range []int{4096, 1048576, 4194304} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			payload := []byte(`{"input":"` + strings.Repeat("x", size) + `"}`)
			for _, scoped := range []bool{false, true} {
				b.Run(fmt.Sprintf("scoped=%t", scoped), func(b *testing.B) {
					model := config.PayloadModelRule{Name: "different-model"}
					if scoped {
						model = scopedPayloadModel()
					}
					cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{model}, Params: map[string]any{"service_tier": "flex"}}}}}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						out := ApplyPayloadConfigWithRequest(cfg, "scope-model", "codex", "responses", "", payload, payload, "scope-model", "/v1/responses", nil, "oauth")
						if len(out) != len(payload) {
							b.Fatal("unexpected mutation")
						}
					}
				})
			}
		})
	}
}
