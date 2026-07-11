package executor

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const codexClientIdentityMaxLength = 64

var codexClientVersionPattern = regexp.MustCompile(`^[vV]?(\d+)\.(\d+)\.(\d+)`)

// codexOfficialClientOriginators mirrors Codex first-party originators plus
// currently observed official CLI, extension, desktop, exec, and SDK variants.
var codexOfficialClientOriginators = map[string]string{
	"codex_cli_rs":          "codex_cli_rs",
	"codex-tui":             "codex-tui",
	"codex_vscode":          "codex_vscode",
	"codex_vscode_copilot":  "codex_vscode_copilot",
	"codex_app":             "codex_app",
	"codex_chatgpt_desktop": "codex_chatgpt_desktop",
	"codex_atlas":           "codex_atlas",
	"codex_exec":            "codex_exec",
	"codex_sdk_ts":          "codex_sdk_ts",
}

// applyFinalCodexClientHeaders applies model-specific overrides first and then
// normalizes the resulting OAuth identity. Keeping the order in one helper
// prevents new HTTP, streaming, websocket, or image paths from finalizing a
// pre-profile header set by mistake.
func applyFinalCodexClientHeaders(headers http.Header, profile codexModelHeaderProfile, auth *cliproxyauth.Auth) {
	applyModelHeaderOverrides(headers, profile)
	finalizeCodexClientIdentityHeaders(headers, auth)
}

// finalizeCodexClientIdentityHeaders normalizes the final ChatGPT Codex OAuth
// client identity after downstream headers, config defaults, custom auth headers,
// and model-specific header profiles have all been applied.
//
// The upstream Codex backend validates originator together with the first
// User-Agent segment. A mismatch can be reported as a misleading 404 Model not
// found response. When Version is present, keep it at least as new as the engine
// version advertised by the final User-Agent. Requests that omit Version keep it
// omitted because the backend does not require the header unconditionally.
func finalizeCodexClientIdentityHeaders(headers http.Header, auth *cliproxyauth.Auth) {
	if headers == nil || codexAuthUsesAPIKey(auth) {
		return
	}
	if strings.TrimSpace(headers.Get("Originator")) == "" {
		return
	}

	originator, userAgent, ok := pairCodexClientIdentity(headers.Get("User-Agent"))
	if !ok {
		originator = codexOriginator
		userAgent = codexUserAgent
	}
	headers.Set("Originator", originator)
	headers.Set("User-Agent", userAgent)
	alignCodexClientVersion(headers, userAgent)
	if strings.Contains(userAgent, "Mac OS") && codexSessionHeaderValue(headers) == "" {
		headers.Set("Session_id", uuid.NewString())
	}
}

// pairCodexClientIdentity derives the upstream originator from the final
// User-Agent. It also restores the real client identity from the trailing
// codex-rs `(name; version)` group when an originator override changed only the
// leading User-Agent segment.
func pairCodexClientIdentity(userAgent string) (originator string, pairedUserAgent string, ok bool) {
	userAgent = strings.TrimSpace(userAgent)
	slash := strings.IndexByte(userAgent, '/')
	if slash <= 0 {
		return "", "", false
	}

	if leading, valid := canonicalCodexClientOriginator(strings.TrimSpace(userAgent[:slash])); valid {
		return leading, leading + userAgent[slash:], true
	}

	trailer := codexUserAgentTrailerOriginator(userAgent)
	if strings.ContainsRune(trailer, '/') {
		return "", "", false
	}
	if trailer, valid := canonicalCodexClientOriginator(trailer); valid {
		return trailer, trailer + userAgent[slash:], true
	}
	return "", "", false
}

func canonicalCodexClientOriginator(originator string) (string, bool) {
	originator = strings.TrimSpace(originator)
	if !isSaneCodexClientOriginator(originator) {
		return "", false
	}
	if canonical, ok := codexOfficialClientOriginators[strings.ToLower(originator)]; ok {
		return canonical, true
	}
	if strings.HasPrefix(originator, "Codex ") {
		return originator, true
	}
	return "", false
}

func isSaneCodexClientOriginator(originator string) bool {
	if originator == "" || len(originator) > codexClientIdentityMaxLength {
		return false
	}
	for i := 0; i < len(originator); i++ {
		if originator[i] < 0x20 || originator[i] > 0x7e {
			return false
		}
	}
	return true
}

func codexUserAgentTrailerOriginator(userAgent string) string {
	lastOpen := strings.LastIndex(userAgent, "(")
	if lastOpen < 0 {
		return ""
	}
	rest := userAgent[lastOpen+1:]
	closeIndex := strings.IndexByte(rest, ')')
	if closeIndex < 0 {
		return ""
	}
	inner := strings.TrimSpace(rest[:closeIndex])
	if semicolon := strings.IndexByte(inner, ';'); semicolon >= 0 {
		inner = strings.TrimSpace(inner[:semicolon])
	}
	return inner
}

func alignCodexClientVersion(headers http.Header, userAgent string) {
	version := strings.TrimSpace(headers.Get("Version"))
	if version == "" {
		return
	}

	engineVersion, engineParts, ok := codexEngineReleaseVersion(userAgent)
	if !ok {
		return
	}
	currentParts, comparable := parseCodexReleaseVersion(version)
	if !comparable || compareCodexReleaseVersions(currentParts, engineParts) < 0 {
		headers.Set("Version", engineVersion)
	}
}

func codexEngineReleaseVersion(userAgent string) (string, [3]int64, bool) {
	userAgent = strings.TrimSpace(userAgent)
	slash := strings.IndexByte(userAgent, '/')
	if slash < 0 || slash+1 >= len(userAgent) {
		return "", [3]int64{}, false
	}
	rest := strings.TrimSpace(userAgent[slash+1:])
	matches := codexClientVersionPattern.FindStringSubmatch(rest)
	if len(matches) != 4 {
		return "", [3]int64{}, false
	}
	parts, ok := parseCodexVersionMatches(matches)
	if !ok {
		return "", [3]int64{}, false
	}
	return strings.Join(matches[1:4], "."), parts, true
}

func parseCodexReleaseVersion(version string) ([3]int64, bool) {
	matches := codexClientVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if len(matches) != 4 {
		return [3]int64{}, false
	}
	return parseCodexVersionMatches(matches)
}

func parseCodexVersionMatches(matches []string) ([3]int64, bool) {
	var parts [3]int64
	for i := range parts {
		value, err := strconv.ParseInt(matches[i+1], 10, 64)
		if err != nil || value < 0 {
			return [3]int64{}, false
		}
		parts[i] = value
	}
	return parts, true
}

func compareCodexReleaseVersions(left, right [3]int64) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}
