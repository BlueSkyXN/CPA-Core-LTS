package executor

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// codexZstdEncoder is shared: EncodeAll is safe for concurrent use. SpeedDefault matches the
// level official Codex clients use (zstd level 3).
var codexZstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
})

// codexRequestCompressionEnabled reports whether an HTTP /responses body should be sent with
// zstd. OAuth credentials follow codex.outbound.request-compression and only compress requests to
// the ChatGPT Codex backend, as official Codex does; API-key credentials compress only when their
// codex-api-key entry opts in with request-compression: zstd.
func codexRequestCompressionEnabled(cfg *config.Config, auth *cliproxyauth.Auth, rawURL string) bool {
	if cfg == nil {
		return false
	}
	if codexAuthUsesAPIKey(auth) {
		entry := resolveCodexKeyConfig(cfg, auth)
		return entry != nil && strings.EqualFold(strings.TrimSpace(entry.RequestCompression), config.CodexRequestCompressionZstd)
	}
	if cfg.Codex.Outbound.EffectiveRequestCompression() != config.CodexRequestCompressionAuto {
		return false
	}
	return isCodexBackendResponsesURL(rawURL)
}

// isCodexBackendResponsesURL matches https://chatgpt.com/backend-api/codex/responses and its
// chatgpt.com subdomains, the endpoint official Codex compresses.
func isCodexBackendResponsesURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "chatgpt.com" && !strings.HasSuffix(host, ".chatgpt.com") {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/backend-api/codex/responses")
}

// compressCodexRequestBody replaces the request body with its zstd encoding. It leaves the request
// untouched when the body is empty, already encoded, or the encoder is unavailable.
func compressCodexRequestBody(req *http.Request, body []byte) bool {
	if req == nil || len(body) == 0 || strings.TrimSpace(req.Header.Get("Content-Encoding")) != "" {
		return false
	}
	encoder, err := codexZstdEncoder()
	if err != nil || encoder == nil {
		log.Debugf("codex outbound: zstd encoder unavailable, sending request uncompressed: %v", err)
		return false
	}
	compressed := encoder.EncodeAll(body, make([]byte, 0, len(body)/2))
	setCodexRequestBody(req, compressed)
	req.Header.Set("Content-Encoding", "zstd")
	return true
}

func setCodexRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
}

// prepareCodexOutboundHTTPRequest applies the codex.outbound HTTP strategy to a built request:
// request-body compression and the Accept-Encoding mode. It returns the request to send and
// whether its body was compressed.
func prepareCodexOutboundHTTPRequest(cfg *config.Config, auth *cliproxyauth.Auth, rawURL string, req *http.Request, body []byte, allowCompression bool) (*http.Request, bool) {
	if req == nil {
		return req, false
	}
	compressed := allowCompression && codexRequestCompressionEnabled(cfg, auth, rawURL) && compressCodexRequestBody(req, body)
	if compressed {
		req = req.WithContext(helps.WithUpstreamPayloadBytes(req.Context(), len(body)))
	}
	if cfg != nil && cfg.Codex.Outbound.EffectiveAcceptEncoding() == config.CodexAcceptEncodingOfficial {
		req = req.WithContext(helps.WithoutAutoAcceptEncoding(req.Context()))
	}
	return req, compressed
}

// doCodexHTTPRequest sends the request. When the body was compressed and the upstream answers
// 415 Unsupported Media Type, it resends once with the original uncompressed body.
func doCodexHTTPRequest(client *http.Client, req *http.Request, rawBody []byte, compressed bool) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil || !compressed || resp == nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		return resp, err
	}
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}
	log.Warn("codex outbound: upstream rejected the zstd request body (415); retrying uncompressed")
	retry := req.Clone(req.Context())
	retry.Header.Del("Content-Encoding")
	setCodexRequestBody(retry, rawBody)
	return client.Do(retry)
}
