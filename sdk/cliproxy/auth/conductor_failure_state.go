package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// upstreamFailureTracker collapses repeated upstream failure warn lines per
// (provider, auth, model): the first failure logs, identical follow-up
// failures are suppressed while the failing state persists, and the first
// success logs one recovery line carrying the suppression count. The full
// per-request failure timeline stays available through usage statistics
// (failure_reason/failure_status), so the console only needs state changes.
// State is in-memory only and must never be serialized into Auth/Manager
// snapshots or management views.
type upstreamFailureTracker struct {
	mu     sync.Mutex
	states map[string]*upstreamFailureState
}

type upstreamFailureState struct {
	failing      bool
	since        time.Time
	suppressed   int
	lastActivity time.Time
}

// upstreamFailureStaleWindow re-arms a failing key whose recovery was never
// observed (for example a success attributed under a different model key after
// codex model fallback, or a result that skipped MarkResult), so a key can
// never be silenced forever.
const upstreamFailureStaleWindow = time.Hour

func upstreamFailureKey(provider, authIdent, model string) string {
	return provider + "|" + authIdent + "|" + model
}

// recordFailure reports whether the caller should emit a failure log line:
// true on the transition into a failing state (or after the stale window
// re-arm), false while the state persists.
func (t *upstreamFailureTracker) recordFailure(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	state := t.stateLocked(key)
	state.lastActivity = now
	if state.failing {
		if now.Sub(state.since) < upstreamFailureStaleWindow {
			state.suppressed++
			return false
		}
		state.since = now
		state.suppressed = 0
		return true
	}
	state.failing = true
	state.since = now
	state.suppressed = 0
	return true
}

// recordSuccess reports a recovery when the key was failing.
func (t *upstreamFailureTracker) recordSuccess(key string) (recovered bool, after time.Duration, suppressed int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.states[key]
	if !ok || !state.failing {
		return false, 0, 0
	}
	now := time.Now()
	after = now.Sub(state.since)
	suppressed = state.suppressed
	state.failing = false
	state.suppressed = 0
	state.lastActivity = now
	return true, after, suppressed
}

func (t *upstreamFailureTracker) stateLocked(key string) *upstreamFailureState {
	if t.states == nil {
		t.states = make(map[string]*upstreamFailureState)
	}
	state, ok := t.states[key]
	if !ok {
		state = &upstreamFailureState{}
		t.states[key] = state
	}
	return state
}

// noteUpstreamRecovery is invoked from result attribution on success to close
// a failing key and emit the recovery summary line. result.Model carries the
// upstream execution model; keys attributed through codex model fallback may
// resolve differently and rely on the stale window re-arm instead.
func (m *Manager) noteUpstreamRecovery(ctx context.Context, auth *Auth, provider, model string) {
	if m == nil || auth == nil || provider == "" || model == "" {
		return
	}
	authIdent := formatAuthIdentity(auth, provider)
	recovered, after, suppressed := m.upstreamFailures.recordSuccess(upstreamFailureKey(provider, authIdent, model))
	if !recovered {
		return
	}
	logEntryWithRequestID(ctx).Infof(
		"upstream recovered: provider=%s model=%s auth=%s after=%s suppressed=%d upstream failure lines",
		provider, model, authIdent, after.Round(time.Second), suppressed,
	)
}

// upstreamFailureWarnExtras renders the optional context fields appended
// between auth= and err= on the failure warn line. Every field is omitted
// when absent, and turn-state is reduced to a fingerprint so the raw header
// value (a bearer-equivalent sticky token) is never logged.
func upstreamFailureWarnExtras(ctx context.Context, execOpts *cliproxyexecutor.Options, attempt string) string {
	var parts []string
	if session := upstreamSessionLogIdentity(ctx, execOpts); session != "" {
		parts = append(parts, "session="+session)
	}
	if turn := upstreamTurnStateFingerprint(execOpts); turn != "" {
		parts = append(parts, "turn="+turn)
	}
	if attempt = strings.TrimSpace(attempt); attempt != "" {
		parts = append(parts, "attempt="+attempt)
	}
	if headers := logging.GetResponseHeaders(ctx); len(headers) > 0 {
		if server := strings.TrimSpace(headers.Get("Server")); server != "" {
			parts = append(parts, "server="+server)
		}
		if ray := strings.TrimSpace(headers.Get("Cf-Ray")); ray != "" {
			parts = append(parts, "cf_ray="+ray)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func upstreamSessionLogIdentity(ctx context.Context, execOpts *cliproxyexecutor.Options) string {
	var sessionID string
	if ctx != nil {
		sessionID = strings.TrimSpace(logging.GetClientRequestMetadata(ctx).SessionID)
	}
	if sessionID == "" && execOpts != nil && execOpts.Metadata != nil {
		if raw, ok := execOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
			if s, ok := raw.(string); ok {
				sessionID = strings.TrimSpace(s)
			} else if s := fmt.Sprintf("%v", raw); strings.TrimSpace(s) != "" {
				sessionID = strings.TrimSpace(s)
			}
		}
	}
	if sessionID == "" {
		return ""
	}
	return sessionLogIdentity(sessionID)
}

func upstreamTurnStateFingerprint(execOpts *cliproxyexecutor.Options) string {
	if execOpts == nil || execOpts.Headers == nil {
		return ""
	}
	value := strings.TrimSpace(execOpts.Headers.Get("X-Codex-Turn-State"))
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "present:" + hex.EncodeToString(sum[:4])
}
