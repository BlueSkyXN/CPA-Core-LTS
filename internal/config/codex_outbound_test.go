package config

import (
	"strings"
	"testing"
)

func TestCodexOutboundDefaultsToFollow(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.Codex.Outbound.EffectiveTransport(); got != CodexOutboundTransportFollow {
		t.Fatalf("EffectiveTransport() = %q, want %q", got, CodexOutboundTransportFollow)
	}
	if got := cfg.Codex.Outbound.EffectiveWebsocketThresholdBytes(); got != DefaultCodexOutboundWebsocketThresholdBytes {
		t.Fatalf("EffectiveWebsocketThresholdBytes() = %d, want %d", got, DefaultCodexOutboundWebsocketThresholdBytes)
	}
}

func TestCodexOutboundParsesModesAndThreshold(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "follow", want: CodexOutboundTransportFollow},
		{raw: "websocket", want: CodexOutboundTransportWebsocket},
		{raw: " Auto ", want: CodexOutboundTransportAuto},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			yaml := "codex:\n  outbound:\n    transport: \"" + tc.raw + "\"\n    websocket-threshold-bytes: 4096\n"
			cfg, err := ParseConfigBytes([]byte(yaml))
			if err != nil {
				t.Fatalf("ParseConfigBytes() error = %v", err)
			}
			if got := cfg.Codex.Outbound.Transport; got != tc.want {
				t.Fatalf("normalized Transport = %q, want %q", got, tc.want)
			}
			if got := cfg.Codex.Outbound.EffectiveTransport(); got != tc.want {
				t.Fatalf("EffectiveTransport() = %q, want %q", got, tc.want)
			}
			if got := cfg.Codex.Outbound.EffectiveWebsocketThresholdBytes(); got != 4096 {
				t.Fatalf("EffectiveWebsocketThresholdBytes() = %d, want 4096", got)
			}
		})
	}
}

func TestCodexOutboundRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"unknown transport":  "codex:\n  outbound:\n    transport: grpc\n",
		"negative threshold": "codex:\n  outbound:\n    transport: auto\n    websocket-threshold-bytes: -1\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfigBytes([]byte(yaml))
			if err == nil || !strings.Contains(err.Error(), "codex.outbound") {
				t.Fatalf("ParseConfigBytes() error = %v, want codex.outbound validation error", err)
			}
		})
	}
}

func TestCodexOutboundCompressionAndAcceptEncoding(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.Codex.Outbound.EffectiveRequestCompression(); got != CodexRequestCompressionAuto {
		t.Fatalf("default request-compression = %q, want auto", got)
	}
	if got := cfg.Codex.Outbound.EffectiveAcceptEncoding(); got != CodexAcceptEncodingOfficial {
		t.Fatalf("default accept-encoding = %q, want official", got)
	}

	cfg, err = ParseConfigBytes([]byte("codex:\n  outbound:\n    accept-encoding: Legacy\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.Codex.Outbound.EffectiveAcceptEncoding(); got != CodexAcceptEncodingLegacy {
		t.Fatalf("accept-encoding = %q, want legacy", got)
	}

	cfg, err = ParseConfigBytes([]byte("codex:\n  outbound:\n    request-compression: NONE\n    accept-encoding: Official\ncodex-api-key:\n  - api-key: k\n    base-url: https://relay.example.com/v1\n    request-compression: ZSTD\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.Codex.Outbound.EffectiveRequestCompression(); got != CodexRequestCompressionNone {
		t.Fatalf("request-compression = %q, want none", got)
	}
	if got := cfg.Codex.Outbound.EffectiveAcceptEncoding(); got != CodexAcceptEncodingOfficial {
		t.Fatalf("accept-encoding = %q, want official", got)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].RequestCompression != CodexRequestCompressionZstd {
		t.Fatalf("codex-api-key request-compression = %+v, want normalized zstd", cfg.CodexKey)
	}

	for name, yaml := range map[string]string{
		"bad compression":     "codex:\n  outbound:\n    request-compression: gzip\n",
		"bad accept-encoding": "codex:\n  outbound:\n    accept-encoding: br\n",
		"bad key compression": "codex-api-key:\n  - api-key: k\n    base-url: https://relay.example.com/v1\n    request-compression: gzip\n",
	} {
		if _, errParse := ParseConfigBytes([]byte(yaml)); errParse == nil {
			t.Fatalf("%s: ParseConfigBytes() error = nil, want validation error", name)
		}
	}
}
