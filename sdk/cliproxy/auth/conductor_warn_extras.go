package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// upstreamFailureWarnExtras renders the optional context fields appended
// between auth= and err= on the upstream failure warn line. Every field is
// omitted when absent, and turn-state is reduced to a fingerprint so the raw
// header value (a bearer-equivalent sticky token) is never logged.
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
		if server := safeResponseHeaderToken(headers.Get("Server")); server != "" {
			parts = append(parts, "server="+server)
		}
		if ray := safeResponseHeaderToken(headers.Get("Cf-Ray")); ray != "" {
			parts = append(parts, "cf_ray="+ray)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// safeResponseHeaderToken reduces an upstream response header value to a
// bounded printable token. Values that still look secret-bearing after
// SafeDiagnosticForLog redaction, or that contain characters outside the
// conservative display set, drop the field entirely rather than leak or
// truncate mid-secret.
func safeResponseHeaderToken(value string) string {
	sanitized := strings.TrimSpace(logging.SafeDiagnosticForLog(value))
	if sanitized == "" {
		return ""
	}
	if runes := []rune(sanitized); len(runes) > 64 {
		sanitized = string(runes[:64])
	}
	for i := 0; i < len(sanitized); i++ {
		ch := sanitized[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '.' || ch == '_' || ch == '-' || ch == '/' || ch == ' ':
		default:
			return ""
		}
	}
	return sanitized
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
