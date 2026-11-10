package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// shouldSelfHealReplayRetry reports whether a terminal upstream failure may be
// retried once without the reasoning replay cache. A retry is offered only
// when the failed attempt positively injected reasoning items from the
// CPA-owned replay cache (injectedReplay) and the failure classifies as an
// upstream signature/encrypted-content rejection. Those injected items are the
// only content the proxy may remove on its own: client-provided ciphertext is
// never stripped and never retried through this path, so an attempt without
// replay injection surfaces the original error unchanged.
func shouldSelfHealReplayRetry(scope codexReasoningReplayScope, injectedReplay bool, statusCode int, body []byte) bool {
	if !scope.valid() || !injectedReplay {
		return false
	}
	code, _, ok := codexStatusErrorClassification(statusCode, body)
	return ok && code == "thinking_signature_invalid"
}

// retryOnceWithoutReplay marks the request as a self-heal retry and re-runs the
// executor once on the same credential. clearCodexReasoningReplayOnInvalidSignature
// has already removed the poisoned cache entry, and the marker forces skipReplay
// so the rebuilt body carries only the client's original history. The marker is
// also the recursion guard: a retry that fails again keeps its error instead of
// starting another retry.
func (e *CodexExecutor) retryOnceWithoutReplay(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	helps.LogWithRequestID(ctx).Debugf("codex executor: signature rejection with CPA replay injection; retrying once without replay")
	return e.Execute(ctx, auth, markCodexReplaySelfHeal(req), opts)
}

// retryStreamOnceWithoutReplay is the ExecuteStream counterpart. It is reached
// only from failure points before any chunk reached the client: the non-2xx
// status handling and the bootstrap terminal scan, both of which run before the
// delivery goroutine starts.
func (e *CodexExecutor) retryStreamOnceWithoutReplay(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	helps.LogWithRequestID(ctx).Debugf("codex executor: stream signature rejection with CPA replay injection; retrying once without replay")
	return e.ExecuteStream(ctx, auth, markCodexReplaySelfHeal(req), opts)
}

// markCodexReplaySelfHeal clones request metadata with the self-heal marker set.
func markCodexReplaySelfHeal(req cliproxyexecutor.Request) cliproxyexecutor.Request {
	retryReq := req
	if len(req.Metadata) > 0 {
		retryReq.Metadata = make(map[string]any, len(req.Metadata)+1)
		for key, value := range req.Metadata {
			retryReq.Metadata[key] = value
		}
	} else {
		retryReq.Metadata = make(map[string]any, 1)
	}
	retryReq.Metadata[cliproxyexecutor.CodexReplaySelfHealMetadataKey] = true
	return retryReq
}
