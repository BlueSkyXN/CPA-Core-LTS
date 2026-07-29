package handlers

import "golang.org/x/net/context"

type codexModelFallbackContextResetReplayContextKey struct{}

// WithCodexModelFallbackContextResetReplay marks a CPA-mediated Responses
// websocket turn as a complete transcript that can be replayed with an
// explicit context reset. The marker is deliberately internal-only: callers
// cannot supply it through the wire protocol.
func WithCodexModelFallbackContextResetReplay(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexModelFallbackContextResetReplayContextKey{}, true)
}
