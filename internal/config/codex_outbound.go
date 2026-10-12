package config

import (
	"fmt"
	"strings"
)

// Codex outbound transport modes for requests whose downstream client connected over HTTP.
const (
	// CodexOutboundTransportFollow keeps the upstream transport equal to the downstream one:
	// HTTP requests go upstream over HTTP and websocket sessions over websocket. Default.
	CodexOutboundTransportFollow = "follow"
	// CodexOutboundTransportWebsocket sends every eligible HTTP request upstream over the
	// Responses websocket and translates the events back for the HTTP client.
	CodexOutboundTransportWebsocket = "websocket"
	// CodexOutboundTransportAuto uses the websocket transport only for HTTP requests whose
	// payload reaches WebsocketThresholdBytes, keeping small requests on HTTP.
	CodexOutboundTransportAuto = "auto"

	// DefaultCodexOutboundWebsocketThresholdBytes is the auto-mode switch point. It sits below the
	// ~1 MiB request size at which HTTP requests from some upstream regions were observed to fail
	// while the same payload over websocket succeeded, leaving room for fields the executor adds.
	DefaultCodexOutboundWebsocketThresholdBytes = 768 * 1024
)

// Codex outbound request-body compression modes.
const (
	// CodexRequestCompressionAuto compresses Codex OAuth HTTP /responses bodies sent to the ChatGPT
	// Codex backend with zstd, matching official Codex clients. Default.
	CodexRequestCompressionAuto = "auto"
	// CodexRequestCompressionNone sends request bodies uncompressed.
	CodexRequestCompressionNone = "none"
	// CodexRequestCompressionZstd is the per-API-key opt-in value for codex-api-key entries.
	CodexRequestCompressionZstd = "zstd"
)

// Codex outbound Accept-Encoding modes.
const (
	// CodexAcceptEncodingLegacy lets the HTTP transport request gzip responses and decode them
	// transparently, the historical behavior.
	CodexAcceptEncodingLegacy = "legacy"
	// CodexAcceptEncodingOfficial sends no Accept-Encoding to chatgpt.com, matching official Codex
	// clients, so responses arrive uncompressed. Default.
	CodexAcceptEncodingOfficial = "official"
)

// CodexOutboundConfig selects how Codex requests are sent upstream.
type CodexOutboundConfig struct {
	// Transport chooses the upstream transport for requests arriving over HTTP: follow (default),
	// websocket, or auto. Websocket sessions are unaffected and keep following the credential's
	// websockets attribute. Only credentials with websockets enabled are eligible; others stay on HTTP.
	Transport string `yaml:"transport,omitempty" json:"transport,omitempty"`
	// WebsocketThresholdBytes is the payload size at which auto mode switches an HTTP request to
	// the websocket transport. Zero or omitted uses DefaultCodexOutboundWebsocketThresholdBytes.
	WebsocketThresholdBytes int `yaml:"websocket-threshold-bytes,omitempty" json:"websocket-threshold-bytes,omitempty"`
	// RequestCompression controls zstd compression of Codex OAuth HTTP /responses request bodies
	// sent to the ChatGPT Codex backend: auto (default) or none. API-key credentials use their own
	// codex-api-key request-compression setting instead.
	RequestCompression string `yaml:"request-compression,omitempty" json:"request-compression,omitempty"`
	// AcceptEncoding controls the Accept-Encoding sent to chatgpt.com: official (default) or legacy.
	AcceptEncoding string `yaml:"accept-encoding,omitempty" json:"accept-encoding,omitempty"`
}

func (c *CodexOutboundConfig) normalizeAndValidate() error {
	if c == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(c.Transport))
	switch mode {
	case "":
	case CodexOutboundTransportFollow, CodexOutboundTransportWebsocket, CodexOutboundTransportAuto:
	default:
		return fmt.Errorf("invalid codex.outbound.transport %q: want follow, websocket, or auto", c.Transport)
	}
	c.Transport = mode
	if c.WebsocketThresholdBytes < 0 {
		return fmt.Errorf("invalid codex.outbound.websocket-threshold-bytes %d: must not be negative", c.WebsocketThresholdBytes)
	}
	compression := strings.ToLower(strings.TrimSpace(c.RequestCompression))
	switch compression {
	case "", CodexRequestCompressionAuto, CodexRequestCompressionNone:
	default:
		return fmt.Errorf("invalid codex.outbound.request-compression %q: want auto or none", c.RequestCompression)
	}
	c.RequestCompression = compression
	acceptEncoding := strings.ToLower(strings.TrimSpace(c.AcceptEncoding))
	switch acceptEncoding {
	case "", CodexAcceptEncodingLegacy, CodexAcceptEncodingOfficial:
	default:
		return fmt.Errorf("invalid codex.outbound.accept-encoding %q: want legacy or official", c.AcceptEncoding)
	}
	c.AcceptEncoding = acceptEncoding
	return nil
}

// EffectiveTransport returns the normalized transport mode, defaulting to follow.
func (c CodexOutboundConfig) EffectiveTransport() string {
	mode := strings.ToLower(strings.TrimSpace(c.Transport))
	switch mode {
	case CodexOutboundTransportWebsocket, CodexOutboundTransportAuto:
		return mode
	default:
		return CodexOutboundTransportFollow
	}
}

// EffectiveWebsocketThresholdBytes returns the auto-mode threshold, applying the default.
func (c CodexOutboundConfig) EffectiveWebsocketThresholdBytes() int {
	if c.WebsocketThresholdBytes > 0 {
		return c.WebsocketThresholdBytes
	}
	return DefaultCodexOutboundWebsocketThresholdBytes
}

// EffectiveRequestCompression returns the normalized OAuth compression mode, defaulting to auto.
func (c CodexOutboundConfig) EffectiveRequestCompression() string {
	if strings.EqualFold(strings.TrimSpace(c.RequestCompression), CodexRequestCompressionNone) {
		return CodexRequestCompressionNone
	}
	return CodexRequestCompressionAuto
}

// EffectiveAcceptEncoding returns the normalized Accept-Encoding mode, defaulting to official.
func (c CodexOutboundConfig) EffectiveAcceptEncoding() string {
	if strings.EqualFold(strings.TrimSpace(c.AcceptEncoding), CodexAcceptEncodingLegacy) {
		return CodexAcceptEncodingLegacy
	}
	return CodexAcceptEncodingOfficial
}

// NormalizeCodexKeyRequestCompression 统一配置加载和 Management 写入的压缩模式校验。
func NormalizeCodexKeyRequestCompression(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", CodexRequestCompressionNone, CodexRequestCompressionZstd:
		return value, nil
	default:
		return "", fmt.Errorf("want none or zstd")
	}
}

// validateCodexKeyRequestCompression normalizes codex-api-key request-compression values.
func validateCodexKeyRequestCompression(entries []CodexKey) error {
	for i := range entries {
		value, err := NormalizeCodexKeyRequestCompression(entries[i].RequestCompression)
		if err != nil {
			return fmt.Errorf("invalid codex-api-key[%d].request-compression %q: %w", i, entries[i].RequestCompression, err)
		}
		entries[i].RequestCompression = value
	}
	return nil
}
