package helps

import (
	"context"

	"golang.org/x/net/http2"
)

type noAutoAcceptEncodingKey struct{}

// WithoutAutoAcceptEncoding marks a request so the ChatGPT uTLS transport neither adds
// Accept-Encoding: gzip nor transparently decodes responses, matching official Codex clients,
// which send no Accept-Encoding. An Accept-Encoding header set explicitly by the caller is unaffected.
func WithoutAutoAcceptEncoding(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, noAutoAcceptEncodingKey{}, true)
}

// AutoAcceptEncodingDisabled reports whether WithoutAutoAcceptEncoding marked ctx.
func AutoAcceptEncodingDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(noAutoAcceptEncodingKey{}).(bool)
	return disabled
}

// newChatGPTHTTP2Transport builds the per-connection HTTP/2 transport used for chatgpt.com.
func newChatGPTHTTP2Transport(ctx context.Context) *http2.Transport {
	return &http2.Transport{DisableCompression: AutoAcceptEncodingDisabled(ctx)}
}
