package diff

import (
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// BuildConfigChangeDetails computes a redacted, human-readable list of config changes.
// Secrets are never printed; only structural or non-sensitive fields are surfaced.
func BuildConfigChangeDetails(oldCfg, newCfg *config.Config) []string {
	changes := make([]string, 0, 16)
	if oldCfg == nil || newCfg == nil {
		return changes
	}

	// Simple scalars
	if oldCfg.Port != newCfg.Port {
		changes = append(changes, fmt.Sprintf("port: %d -> %d", oldCfg.Port, newCfg.Port))
	}
	if oldCfg.AuthDir != newCfg.AuthDir {
		changes = append(changes, fmt.Sprintf("auth-dir: %s -> %s", oldCfg.AuthDir, newCfg.AuthDir))
	}
	if oldCfg.Debug != newCfg.Debug {
		changes = append(changes, fmt.Sprintf("debug: %t -> %t", oldCfg.Debug, newCfg.Debug))
	}
	if oldCfg.Pprof.Enable != newCfg.Pprof.Enable {
		changes = append(changes, fmt.Sprintf("pprof.enable: %t -> %t", oldCfg.Pprof.Enable, newCfg.Pprof.Enable))
	}
	if strings.TrimSpace(oldCfg.Pprof.Addr) != strings.TrimSpace(newCfg.Pprof.Addr) {
		changes = append(changes, fmt.Sprintf("pprof.addr: %s -> %s", strings.TrimSpace(oldCfg.Pprof.Addr), strings.TrimSpace(newCfg.Pprof.Addr)))
	}
	if oldCfg.LoggingToFile != newCfg.LoggingToFile {
		changes = append(changes, fmt.Sprintf("logging-to-file: %t -> %t", oldCfg.LoggingToFile, newCfg.LoggingToFile))
	}
	if oldCfg.UsageStatisticsEnabled != newCfg.UsageStatisticsEnabled {
		changes = append(changes, fmt.Sprintf("usage-statistics-enabled: %t -> %t", oldCfg.UsageStatisticsEnabled, newCfg.UsageStatisticsEnabled))
	}
	if oldCfg.RedisUsageQueueRetentionSeconds != newCfg.RedisUsageQueueRetentionSeconds {
		changes = append(changes, fmt.Sprintf("redis-usage-queue-retention-seconds: %d -> %d", oldCfg.RedisUsageQueueRetentionSeconds, newCfg.RedisUsageQueueRetentionSeconds))
	}
	if oldCfg.DisableCooling != newCfg.DisableCooling {
		changes = append(changes, fmt.Sprintf("disable-cooling: %t -> %t", oldCfg.DisableCooling, newCfg.DisableCooling))
	}
	if oldCfg.SaveCooldownStatus != newCfg.SaveCooldownStatus {
		changes = append(changes, fmt.Sprintf("save-cooldown-status: %t -> %t", oldCfg.SaveCooldownStatus, newCfg.SaveCooldownStatus))
	}
	if oldCfg.TransientErrorCooldownSeconds != newCfg.TransientErrorCooldownSeconds {
		changes = append(changes, fmt.Sprintf("transient-error-cooldown-seconds: %d -> %d", oldCfg.TransientErrorCooldownSeconds, newCfg.TransientErrorCooldownSeconds))
	}
	if oldCfg.DisableClaudeCloakMode != newCfg.DisableClaudeCloakMode {
		changes = append(changes, fmt.Sprintf("disable-claude-cloak-mode: %t -> %t", oldCfg.DisableClaudeCloakMode, newCfg.DisableClaudeCloakMode))
	}
	if oldCfg.ClaudeCode.DisableCloakingModelList != newCfg.ClaudeCode.DisableCloakingModelList {
		changes = append(changes, fmt.Sprintf("claude-code.disable-cloaking-model-list: %t -> %t", oldCfg.ClaudeCode.DisableCloakingModelList, newCfg.ClaudeCode.DisableCloakingModelList))
	}
	if oldCfg.DisableImageGeneration != newCfg.DisableImageGeneration {
		changes = append(changes, fmt.Sprintf("disable-image-generation: %v -> %v", oldCfg.DisableImageGeneration, newCfg.DisableImageGeneration))
	}
	if strings.TrimSpace(oldCfg.GPTImage2BaseModel) != strings.TrimSpace(newCfg.GPTImage2BaseModel) {
		changes = append(changes, fmt.Sprintf("gpt-image-2-base-model: %s -> %s", strings.TrimSpace(oldCfg.GPTImage2BaseModel), strings.TrimSpace(newCfg.GPTImage2BaseModel)))
	}
	if oldCfg.RequestLog != newCfg.RequestLog {
		changes = append(changes, fmt.Sprintf("request-log: %t -> %t", oldCfg.RequestLog, newCfg.RequestLog))
	}
	if oldCfg.EffectiveAPIRequestBodyMaxBytes() != newCfg.EffectiveAPIRequestBodyMaxBytes() {
		changes = append(changes, fmt.Sprintf("api-request-body-max-bytes: %d -> %d", oldCfg.EffectiveAPIRequestBodyMaxBytes(), newCfg.EffectiveAPIRequestBodyMaxBytes()))
	}
	if oldCfg.LogsMaxTotalSizeMB != newCfg.LogsMaxTotalSizeMB {
		changes = append(changes, fmt.Sprintf("logs-max-total-size-mb: %d -> %d", oldCfg.LogsMaxTotalSizeMB, newCfg.LogsMaxTotalSizeMB))
	}
	if oldCfg.ErrorLogsMaxFiles != newCfg.ErrorLogsMaxFiles {
		changes = append(changes, fmt.Sprintf("error-logs-max-files: %d -> %d", oldCfg.ErrorLogsMaxFiles, newCfg.ErrorLogsMaxFiles))
	}
	if oldCfg.RequestRetry != newCfg.RequestRetry {
		changes = append(changes, fmt.Sprintf("request-retry: %d -> %d", oldCfg.RequestRetry, newCfg.RequestRetry))
	}
	if oldCfg.MaxRetryCredentials != newCfg.MaxRetryCredentials {
		changes = append(changes, fmt.Sprintf("max-retry-credentials: %d -> %d", oldCfg.MaxRetryCredentials, newCfg.MaxRetryCredentials))
	}
	if oldCfg.MaxRetryInterval != newCfg.MaxRetryInterval {
		changes = append(changes, fmt.Sprintf("max-retry-interval: %d -> %d", oldCfg.MaxRetryInterval, newCfg.MaxRetryInterval))
	}
	if oldCfg.ProxyURL != newCfg.ProxyURL {
		changes = append(changes, fmt.Sprintf("proxy-url: %s -> %s", formatProxyURL(oldCfg.ProxyURL), formatProxyURL(newCfg.ProxyURL)))
	}
	if oldCfg.WebsocketAuth != newCfg.WebsocketAuth {
		changes = append(changes, fmt.Sprintf("ws-auth: %t -> %t", oldCfg.WebsocketAuth, newCfg.WebsocketAuth))
	}
	if oldCfg.ForceModelPrefix != newCfg.ForceModelPrefix {
		changes = append(changes, fmt.Sprintf("force-model-prefix: %t -> %t", oldCfg.ForceModelPrefix, newCfg.ForceModelPrefix))
	}
	if oldCfg.NonStreamKeepAliveInterval != newCfg.NonStreamKeepAliveInterval {
		changes = append(changes, fmt.Sprintf("nonstream-keepalive-interval: %d -> %d", oldCfg.NonStreamKeepAliveInterval, newCfg.NonStreamKeepAliveInterval))
	}

	// Quota-exceeded behavior
	if oldCfg.QuotaExceeded.SwitchProject != newCfg.QuotaExceeded.SwitchProject {
		changes = append(changes, fmt.Sprintf("quota-exceeded.switch-project: %t -> %t", oldCfg.QuotaExceeded.SwitchProject, newCfg.QuotaExceeded.SwitchProject))
	}
	if oldCfg.QuotaExceeded.SwitchPreviewModel != newCfg.QuotaExceeded.SwitchPreviewModel {
		changes = append(changes, fmt.Sprintf("quota-exceeded.switch-preview-model: %t -> %t", oldCfg.QuotaExceeded.SwitchPreviewModel, newCfg.QuotaExceeded.SwitchPreviewModel))
	}
	if oldCfg.QuotaExceeded.AntigravityCredits != newCfg.QuotaExceeded.AntigravityCredits {
		changes = append(changes, fmt.Sprintf("quota-exceeded.antigravity-credits: %t -> %t", oldCfg.QuotaExceeded.AntigravityCredits, newCfg.QuotaExceeded.AntigravityCredits))
	}
	if !reflect.DeepEqual(oldCfg.Antigravity.SensitiveWords, newCfg.Antigravity.SensitiveWords) {
		changes = append(changes, fmt.Sprintf("antigravity.sensitive-words: %d -> %d", len(oldCfg.Antigravity.SensitiveWords), len(newCfg.Antigravity.SensitiveWords)))
	}

	if oldCfg.Codex.IdentityConfuse != newCfg.Codex.IdentityConfuse {
		changes = append(changes, fmt.Sprintf("codex.identity-confuse: %t -> %t", oldCfg.Codex.IdentityConfuse, newCfg.Codex.IdentityConfuse))
	}
	changes = appendCodexClientMetadataChanges(changes, oldCfg.Codex.ClientMetadata.Effective(), newCfg.Codex.ClientMetadata.Effective())
	changes = appendCodexModelFallbackChanges(changes, oldCfg.Codex.ModelFallback.Effective(), newCfg.Codex.ModelFallback.Effective())
	changes = appendCodexRateLimitContinuityChanges(changes, oldCfg.Codex.RateLimitContinuity.Effective(), newCfg.Codex.RateLimitContinuity.Effective())
	changes = appendCodexAbnormalReasoningRetryChanges(changes, oldCfg.Codex.AbnormalReasoningRetry.Effective(), newCfg.Codex.AbnormalReasoningRetry.Effective())
	if oldCfg.Codex.DisableCodexCloaking != newCfg.Codex.DisableCodexCloaking {
		changes = append(changes, fmt.Sprintf("codex.disable-codex-cloaking: %t -> %t", oldCfg.Codex.DisableCodexCloaking, newCfg.Codex.DisableCodexCloaking))
	}
	if oldCfg.Codex.StreamBootstrapBuffering != newCfg.Codex.StreamBootstrapBuffering {
		changes = append(changes, fmt.Sprintf("codex.stream-bootstrap-buffering: %t -> %t", oldCfg.Codex.StreamBootstrapBuffering, newCfg.Codex.StreamBootstrapBuffering))
	}
	if oldCfg.Codex.OptimizeMultiAgentV2 != newCfg.Codex.OptimizeMultiAgentV2 {
		changes = append(changes, fmt.Sprintf("codex.optimize-multi-agent-v2: %t -> %t", oldCfg.Codex.OptimizeMultiAgentV2, newCfg.Codex.OptimizeMultiAgentV2))
	}
	if oldCfg.Codex.DesktopToolOverlay.Enabled != newCfg.Codex.DesktopToolOverlay.Enabled {
		changes = append(changes, fmt.Sprintf("codex.desktop-tool-overlay.enabled: %t -> %t", oldCfg.Codex.DesktopToolOverlay.Enabled, newCfg.Codex.DesktopToolOverlay.Enabled))
	}
	if !reflect.DeepEqual(oldCfg.Codex.DesktopToolOverlay.Tools, newCfg.Codex.DesktopToolOverlay.Tools) {
		changes = append(changes, fmt.Sprintf(
			"codex.desktop-tool-overlay.tools: [%s] -> [%s] (%d -> %d entries)",
			strings.Join(oldCfg.Codex.DesktopToolOverlay.Tools, ", "),
			strings.Join(newCfg.Codex.DesktopToolOverlay.Tools, ", "),
			len(oldCfg.Codex.DesktopToolOverlay.Tools),
			len(newCfg.Codex.DesktopToolOverlay.Tools),
		))
	}
	if oldCfg.XAI.InjectXSearch != newCfg.XAI.InjectXSearch {
		changes = append(changes, fmt.Sprintf("xai.inject-x-search: %t -> %t", oldCfg.XAI.InjectXSearch, newCfg.XAI.InjectXSearch))
	}
	oldLiveRelay := oldCfg.Codex.LiveMediaRelay
	newLiveRelay := newCfg.Codex.LiveMediaRelay
	if oldLiveRelay.Enabled != newLiveRelay.Enabled {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.enabled: %t -> %t", oldLiveRelay.Enabled, newLiveRelay.Enabled))
	}
	if oldLiveRelay.MaxSessions != newLiveRelay.MaxSessions {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.max-sessions: %d -> %d", oldLiveRelay.MaxSessions, newLiveRelay.MaxSessions))
	}
	if oldLiveRelay.DisablePrivateRemoteIPs != newLiveRelay.DisablePrivateRemoteIPs {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.disable-private-remote-ips: %t -> %t", oldLiveRelay.DisablePrivateRemoteIPs, newLiveRelay.DisablePrivateRemoteIPs))
	}
	if strings.TrimSpace(oldLiveRelay.PublicIP) != strings.TrimSpace(newLiveRelay.PublicIP) {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.public-ip: %s -> %s", displayOptionalValue(oldLiveRelay.PublicIP), displayOptionalValue(newLiveRelay.PublicIP)))
	}
	if oldLiveRelay.UDPPortMin != newLiveRelay.UDPPortMin {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.udp-port-min: %d -> %d", oldLiveRelay.UDPPortMin, newLiveRelay.UDPPortMin))
	}
	if oldLiveRelay.UDPPortMax != newLiveRelay.UDPPortMax {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.udp-port-max: %d -> %d", oldLiveRelay.UDPPortMax, newLiveRelay.UDPPortMax))
	}
	if !reflect.DeepEqual(oldLiveRelay.ICEServers, newLiveRelay.ICEServers) {
		changes = append(changes, fmt.Sprintf("codex.live-media-relay.ice-servers: updated (%d -> %d entries, credentials redacted)", len(oldLiveRelay.ICEServers), len(newLiveRelay.ICEServers)))
	}

	if oldCfg.Routing.Strategy != newCfg.Routing.Strategy {
		changes = append(changes, fmt.Sprintf("routing.strategy: %s -> %s", oldCfg.Routing.Strategy, newCfg.Routing.Strategy))
	}
	if !reflect.DeepEqual(oldCfg.Payload, newCfg.Payload) {
		changes = appendPayloadConfigChanges(changes, oldCfg.Payload, newCfg.Payload)
	}

	// API keys (redacted) and counts
	if len(oldCfg.APIKeys) != len(newCfg.APIKeys) {
		changes = append(changes, fmt.Sprintf("api-keys count: %d -> %d", len(oldCfg.APIKeys), len(newCfg.APIKeys)))
	} else if !reflect.DeepEqual(trimStrings(oldCfg.APIKeys), trimStrings(newCfg.APIKeys)) {
		changes = append(changes, "api-keys: values updated (count unchanged, redacted)")
	}
	if len(oldCfg.GeminiKey) != len(newCfg.GeminiKey) {
		changes = append(changes, fmt.Sprintf("gemini-api-key count: %d -> %d", len(oldCfg.GeminiKey), len(newCfg.GeminiKey)))
	} else {
		for i := range oldCfg.GeminiKey {
			o := oldCfg.GeminiKey[i]
			n := newCfg.GeminiKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("gemini[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("gemini[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("gemini[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("gemini[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("gemini[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("gemini[%d].headers: updated", i))
			}
			oldModels := SummarizeGeminiModels(o.Models)
			newModels := SummarizeGeminiModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("gemini[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("gemini[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			changes = appendOptionalIntChange(changes, fmt.Sprintf("gemini[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
		}
	}
	if len(oldCfg.InteractionsKey) != len(newCfg.InteractionsKey) {
		changes = append(changes, fmt.Sprintf("interactions-api-key count: %d -> %d", len(oldCfg.InteractionsKey), len(newCfg.InteractionsKey)))
	} else {
		for i := range oldCfg.InteractionsKey {
			o := oldCfg.InteractionsKey[i]
			n := newCfg.InteractionsKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("interactions[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("interactions[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("interactions[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("interactions[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("interactions[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("interactions[%d].headers: updated", i))
			}
			oldModels := SummarizeGeminiModels(o.Models)
			newModels := SummarizeGeminiModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("interactions[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("interactions[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			changes = appendOptionalIntChange(changes, fmt.Sprintf("interactions[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
		}
	}

	// Claude keys (do not print key material)
	if len(oldCfg.ClaudeKey) != len(newCfg.ClaudeKey) {
		changes = append(changes, fmt.Sprintf("claude-api-key count: %d -> %d", len(oldCfg.ClaudeKey), len(newCfg.ClaudeKey)))
	} else {
		for i := range oldCfg.ClaudeKey {
			o := oldCfg.ClaudeKey[i]
			n := newCfg.ClaudeKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("claude[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("claude[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("claude[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("claude[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("claude[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("claude[%d].headers: updated", i))
			}
			oldModels := SummarizeClaudeModels(o.Models)
			newModels := SummarizeClaudeModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("claude[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("claude[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			if o.RebuildMidSystemMessage != n.RebuildMidSystemMessage {
				changes = append(changes, fmt.Sprintf("claude[%d].rebuild-mid-system-message: %t -> %t", i, o.RebuildMidSystemMessage, n.RebuildMidSystemMessage))
			}
			if strings.TrimSpace(o.FingerprintProfile) != strings.TrimSpace(n.FingerprintProfile) {
				changes = append(changes, fmt.Sprintf("claude[%d].fingerprint-profile: %s -> %s", i, strings.TrimSpace(o.FingerprintProfile), strings.TrimSpace(n.FingerprintProfile)))
			}
			changes = appendOptionalIntChange(changes, fmt.Sprintf("claude[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
			if o.Cloak != nil && n.Cloak != nil {
				if strings.TrimSpace(o.Cloak.Mode) != strings.TrimSpace(n.Cloak.Mode) {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.mode: %s -> %s", i, o.Cloak.Mode, n.Cloak.Mode))
				}
				if o.Cloak.StrictMode != n.Cloak.StrictMode {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.strict-mode: %t -> %t", i, o.Cloak.StrictMode, n.Cloak.StrictMode))
				}
				if len(o.Cloak.SensitiveWords) != len(n.Cloak.SensitiveWords) {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.sensitive-words: %d -> %d", i, len(o.Cloak.SensitiveWords), len(n.Cloak.SensitiveWords)))
				}
			}
		}
	}

	// Codex keys (do not print key material)
	if len(oldCfg.CodexKey) != len(newCfg.CodexKey) {
		changes = append(changes, fmt.Sprintf("codex-api-key count: %d -> %d", len(oldCfg.CodexKey), len(newCfg.CodexKey)))
	} else {
		for i := range oldCfg.CodexKey {
			o := oldCfg.CodexKey[i]
			n := newCfg.CodexKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("codex[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("codex[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("codex[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if o.Websockets != n.Websockets {
				changes = append(changes, fmt.Sprintf("codex[%d].websockets: %t -> %t", i, o.Websockets, n.Websockets))
			}
			if o.AlphaSearch != n.AlphaSearch {
				changes = append(changes, fmt.Sprintf("codex[%d].alpha-search: %t -> %t", i, o.AlphaSearch, n.AlphaSearch))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("codex[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("codex[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("codex[%d].headers: updated", i))
			}
			oldModels := SummarizeCodexModels(o.Models)
			newModels := SummarizeCodexModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("codex[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("codex[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			changes = appendOptionalIntChange(changes, fmt.Sprintf("codex[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
		}
	}

	// xAI keys (do not print key material)
	if len(oldCfg.XAIKey) != len(newCfg.XAIKey) {
		changes = append(changes, fmt.Sprintf("xai-api-key count: %d -> %d", len(oldCfg.XAIKey), len(newCfg.XAIKey)))
	} else {
		for i := range oldCfg.XAIKey {
			o := oldCfg.XAIKey[i]
			n := newCfg.XAIKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("xai[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("xai[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("xai[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if o.Priority != n.Priority {
				changes = append(changes, fmt.Sprintf("xai[%d].priority: %d -> %d", i, o.Priority, n.Priority))
			}
			if o.Websockets != n.Websockets {
				changes = append(changes, fmt.Sprintf("xai[%d].websockets: %t -> %t", i, o.Websockets, n.Websockets))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("xai[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			changes = appendOptionalIntChange(changes, fmt.Sprintf("xai[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("xai[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("xai[%d].headers: updated", i))
			}
			oldModels := SummarizeCodexModels(o.Models)
			newModels := SummarizeCodexModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("xai[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("xai[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
		}
	}

	// AmpCode settings (redacted where needed)
	oldAmpURL := strings.TrimSpace(oldCfg.AmpCode.UpstreamURL)
	newAmpURL := strings.TrimSpace(newCfg.AmpCode.UpstreamURL)
	if oldAmpURL != newAmpURL {
		changes = append(changes, fmt.Sprintf("ampcode.upstream-url: %s -> %s", oldAmpURL, newAmpURL))
	}
	oldAmpKey := strings.TrimSpace(oldCfg.AmpCode.UpstreamAPIKey)
	newAmpKey := strings.TrimSpace(newCfg.AmpCode.UpstreamAPIKey)
	switch {
	case oldAmpKey == "" && newAmpKey != "":
		changes = append(changes, "ampcode.upstream-api-key: added")
	case oldAmpKey != "" && newAmpKey == "":
		changes = append(changes, "ampcode.upstream-api-key: removed")
	case oldAmpKey != newAmpKey:
		changes = append(changes, "ampcode.upstream-api-key: updated")
	}
	if oldCfg.AmpCode.RestrictManagementToLocalhost != newCfg.AmpCode.RestrictManagementToLocalhost {
		changes = append(changes, fmt.Sprintf("ampcode.restrict-management-to-localhost: %t -> %t", oldCfg.AmpCode.RestrictManagementToLocalhost, newCfg.AmpCode.RestrictManagementToLocalhost))
	}
	oldMappings := SummarizeAmpModelMappings(oldCfg.AmpCode.ModelMappings)
	newMappings := SummarizeAmpModelMappings(newCfg.AmpCode.ModelMappings)
	if oldMappings.hash != newMappings.hash {
		changes = append(changes, fmt.Sprintf("ampcode.model-mappings: updated (%d -> %d entries)", oldMappings.count, newMappings.count))
	}
	if oldCfg.AmpCode.ForceModelMappings != newCfg.AmpCode.ForceModelMappings {
		changes = append(changes, fmt.Sprintf("ampcode.force-model-mappings: %t -> %t", oldCfg.AmpCode.ForceModelMappings, newCfg.AmpCode.ForceModelMappings))
	}
	oldUpstreamAPIKeysCount := len(oldCfg.AmpCode.UpstreamAPIKeys)
	newUpstreamAPIKeysCount := len(newCfg.AmpCode.UpstreamAPIKeys)
	if !equalUpstreamAPIKeys(oldCfg.AmpCode.UpstreamAPIKeys, newCfg.AmpCode.UpstreamAPIKeys) {
		changes = append(changes, fmt.Sprintf("ampcode.upstream-api-keys: updated (%d -> %d entries)", oldUpstreamAPIKeysCount, newUpstreamAPIKeysCount))
	}

	if entries, _ := DiffOAuthExcludedModelChanges(oldCfg.OAuthExcludedModels, newCfg.OAuthExcludedModels); len(entries) > 0 {
		changes = append(changes, entries...)
	}
	if entries, _ := DiffOAuthModelAliasChanges(oldCfg.OAuthModelAlias, newCfg.OAuthModelAlias); len(entries) > 0 {
		changes = append(changes, entries...)
	}
	if entries, _ := DiffOAuthRequestScopedErrorsChanges(oldCfg.OAuthRequestScopedErrors, newCfg.OAuthRequestScopedErrors); len(entries) > 0 {
		changes = append(changes, entries...)
	}

	// Remote management (never print the key)
	if oldCfg.RemoteManagement.AllowRemote != newCfg.RemoteManagement.AllowRemote {
		changes = append(changes, fmt.Sprintf("remote-management.allow-remote: %t -> %t", oldCfg.RemoteManagement.AllowRemote, newCfg.RemoteManagement.AllowRemote))
	}
	if oldCfg.RemoteManagement.DisableControlPanel != newCfg.RemoteManagement.DisableControlPanel {
		changes = append(changes, fmt.Sprintf("remote-management.disable-control-panel: %t -> %t", oldCfg.RemoteManagement.DisableControlPanel, newCfg.RemoteManagement.DisableControlPanel))
	}
	if oldCfg.RemoteManagement.DisableAutoUpdatePanel != newCfg.RemoteManagement.DisableAutoUpdatePanel {
		changes = append(changes, fmt.Sprintf("remote-management.disable-auto-update-panel: %t -> %t", oldCfg.RemoteManagement.DisableAutoUpdatePanel, newCfg.RemoteManagement.DisableAutoUpdatePanel))
	}
	oldPanelRepo := strings.TrimSpace(oldCfg.RemoteManagement.PanelGitHubRepository)
	newPanelRepo := strings.TrimSpace(newCfg.RemoteManagement.PanelGitHubRepository)
	if oldPanelRepo != newPanelRepo {
		changes = append(changes, fmt.Sprintf("remote-management.panel-github-repository: %s -> %s", formatURL(oldPanelRepo), formatURL(newPanelRepo)))
	}
	if oldCfg.RemoteManagement.SecretKey != newCfg.RemoteManagement.SecretKey {
		switch {
		case oldCfg.RemoteManagement.SecretKey == "" && newCfg.RemoteManagement.SecretKey != "":
			changes = append(changes, "remote-management.secret-key: created")
		case oldCfg.RemoteManagement.SecretKey != "" && newCfg.RemoteManagement.SecretKey == "":
			changes = append(changes, "remote-management.secret-key: deleted")
		default:
			changes = append(changes, "remote-management.secret-key: updated")
		}
	}

	// OpenAI compatibility providers (summarized)
	if compat := DiffOpenAICompatibility(oldCfg.OpenAICompatibility, newCfg.OpenAICompatibility); len(compat) > 0 {
		changes = append(changes, "openai-compatibility:")
		for _, c := range compat {
			changes = append(changes, "  "+c)
		}
	}

	// Vertex-compatible API keys
	if len(oldCfg.VertexCompatAPIKey) != len(newCfg.VertexCompatAPIKey) {
		changes = append(changes, fmt.Sprintf("vertex-api-key count: %d -> %d", len(oldCfg.VertexCompatAPIKey), len(newCfg.VertexCompatAPIKey)))
	} else {
		for i := range oldCfg.VertexCompatAPIKey {
			o := oldCfg.VertexCompatAPIKey[i]
			n := newCfg.VertexCompatAPIKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("vertex[%d].base-url: %s -> %s", i, formatURL(o.BaseURL), formatURL(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("vertex[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("vertex[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			changes = appendOptionalBoolChange(changes, fmt.Sprintf("vertex[%d].disable-cooling", i), o.DisableCooling, n.DisableCooling)
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("vertex[%d].api-key: updated", i))
			}
			oldModels := SummarizeVertexModels(o.Models)
			newModels := SummarizeVertexModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("vertex[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("vertex[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("vertex[%d].headers: updated", i))
			}
			changes = appendOptionalIntChange(changes, fmt.Sprintf("vertex[%d].request-retry", i), o.RequestRetry, n.RequestRetry)
		}
	}

	return changes
}

func appendCodexClientMetadataChanges(changes []string, oldCfg, newCfg config.EffectiveCodexClientMetadataConfig) []string {
	if oldCfg.Mode != newCfg.Mode {
		changes = append(changes, fmt.Sprintf("codex.client-metadata.mode: %s -> %s", oldCfg.Mode, newCfg.Mode))
	}
	if oldCfg.WorkspacePolicy != newCfg.WorkspacePolicy {
		changes = append(changes, fmt.Sprintf("codex.client-metadata.workspace-policy: %s -> %s", oldCfg.WorkspacePolicy, newCfg.WorkspacePolicy))
	}
	return changes
}

func trimStrings(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = strings.TrimSpace(in[i])
	}
	return out
}

func appendPayloadConfigChanges(changes []string, oldPayload, newPayload config.PayloadConfig) []string {
	changes = appendPayloadRuleChanges(changes, "default", oldPayload.Default, newPayload.Default)
	changes = appendPayloadRuleChanges(changes, "default-raw", oldPayload.DefaultRaw, newPayload.DefaultRaw)
	changes = appendPayloadRuleChanges(changes, "override", oldPayload.Override, newPayload.Override)
	changes = appendPayloadRuleChanges(changes, "override-raw", oldPayload.OverrideRaw, newPayload.OverrideRaw)
	changes = appendPayloadFilterRuleChanges(changes, "filter", oldPayload.Filter, newPayload.Filter)
	return changes
}

func appendPayloadRuleChanges(changes []string, section string, oldRules, newRules []config.PayloadRule) []string {
	if reflect.DeepEqual(oldRules, newRules) {
		return changes
	}
	return append(changes, fmt.Sprintf("payload.%s: updated (%d -> %d rules)", section, len(oldRules), len(newRules)))
}

func appendPayloadFilterRuleChanges(changes []string, section string, oldRules, newRules []config.PayloadFilterRule) []string {
	if reflect.DeepEqual(oldRules, newRules) {
		return changes
	}
	return append(changes, fmt.Sprintf("payload.%s: updated (%d -> %d rules)", section, len(oldRules), len(newRules)))
}

func appendCodexAbnormalReasoningRetryChanges(changes []string, oldCfg, newCfg config.EffectiveCodexAbnormalReasoningRetryConfig) []string {
	if reflect.DeepEqual(oldCfg, newCfg) {
		return changes
	}
	if oldCfg.Enabled != newCfg.Enabled {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.enabled: %t -> %t", oldCfg.Enabled, newCfg.Enabled))
	}
	if oldCfg.Action != newCfg.Action {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.action: %s -> %s", oldCfg.Action, newCfg.Action))
	}
	if oldCfg.StreamBuffer != newCfg.StreamBuffer {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.stream-buffer: %t -> %t", oldCfg.StreamBuffer, newCfg.StreamBuffer))
	}
	if oldCfg.StreamBufferMaxBytes != newCfg.StreamBufferMaxBytes {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.stream-buffer-max-bytes: %d -> %d", oldCfg.StreamBufferMaxBytes, newCfg.StreamBufferMaxBytes))
	}
	if oldCfg.MaxRetries != newCfg.MaxRetries {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.max-retries: %d -> %d", oldCfg.MaxRetries, newCfg.MaxRetries))
	}
	if oldCfg.ExhaustedBehavior != newCfg.ExhaustedBehavior {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.exhausted-behavior: %s -> %s", oldCfg.ExhaustedBehavior, newCfg.ExhaustedBehavior))
	}
	if oldCfg.DeliveryPolicy != newCfg.DeliveryPolicy {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.delivery-policy: %s -> %s", oldCfg.DeliveryPolicy, newCfg.DeliveryPolicy))
	}
	if oldCfg.FallbackPolicy != newCfg.FallbackPolicy {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.fallback-policy: %s -> %s", oldCfg.FallbackPolicy, newCfg.FallbackPolicy))
	}
	if oldCfg.ClientUsageAggregation != newCfg.ClientUsageAggregation {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.client-usage-aggregation: %s -> %s", oldCfg.ClientUsageAggregation, newCfg.ClientUsageAggregation))
	}
	if oldCfg.HedgedRetry.Enabled != newCfg.HedgedRetry.Enabled {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.hedged-retry.enabled: %t -> %t", oldCfg.HedgedRetry.Enabled, newCfg.HedgedRetry.Enabled))
	}
	if oldCfg.HedgedRetry.Mode != newCfg.HedgedRetry.Mode {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.hedged-retry.mode: %s -> %s", oldCfg.HedgedRetry.Mode, newCfg.HedgedRetry.Mode))
	}
	if oldCfg.HedgedRetry.HedgeDelayMS != newCfg.HedgedRetry.HedgeDelayMS {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.hedged-retry.hedge-delay-ms: %d -> %d", oldCfg.HedgedRetry.HedgeDelayMS, newCfg.HedgedRetry.HedgeDelayMS))
	}
	if oldCfg.HedgedRetry.RequireDistinctAuth != newCfg.HedgedRetry.RequireDistinctAuth {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.hedged-retry.require-distinct-auth: %t -> %t", oldCfg.HedgedRetry.RequireDistinctAuth, newCfg.HedgedRetry.RequireDistinctAuth))
	}
	if !reflect.DeepEqual(oldCfg.ModelContains, newCfg.ModelContains) {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.model-contains: updated (%d -> %d entries)", len(oldCfg.ModelContains), len(newCfg.ModelContains)))
	}
	if !reflect.DeepEqual(oldCfg.ReasoningEfforts, newCfg.ReasoningEfforts) {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.reasoning-efforts: updated (%d -> %d entries)", len(oldCfg.ReasoningEfforts), len(newCfg.ReasoningEfforts)))
	}
	if !reflect.DeepEqual(oldCfg.ReasoningTokens, newCfg.ReasoningTokens) {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.reasoning-tokens: updated (%d -> %d entries)", len(oldCfg.ReasoningTokens), len(newCfg.ReasoningTokens)))
	}
	if !reflect.DeepEqual(oldCfg.AuthKinds, newCfg.AuthKinds) {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.auth-kinds: updated (%d -> %d entries)", len(oldCfg.AuthKinds), len(newCfg.AuthKinds)))
	}
	if !reflect.DeepEqual(oldCfg.AuthIDs, newCfg.AuthIDs) {
		changes = append(changes, fmt.Sprintf("codex.abnormal-reasoning-retry.auth-ids: updated (%d -> %d entries)", len(oldCfg.AuthIDs), len(newCfg.AuthIDs)))
	}
	return changes
}

func appendCodexModelFallbackChanges(changes []string, oldCfg, newCfg config.EffectiveCodexModelFallbackConfig) []string {
	if reflect.DeepEqual(oldCfg, newCfg) {
		return changes
	}
	if oldCfg.Enabled != newCfg.Enabled {
		changes = append(changes, fmt.Sprintf("codex.model-fallback.enabled: %t -> %t", oldCfg.Enabled, newCfg.Enabled))
	}
	if oldCfg.ReasoningContinuity != newCfg.ReasoningContinuity {
		changes = append(changes, fmt.Sprintf("codex.model-fallback.reasoning-continuity: %s -> %s", oldCfg.ReasoningContinuity, newCfg.ReasoningContinuity))
	}
	if !reflect.DeepEqual(oldCfg.Triggers, newCfg.Triggers) {
		changes = append(changes, fmt.Sprintf("codex.model-fallback.triggers: updated (%d -> %d entries)", len(oldCfg.Triggers), len(newCfg.Triggers)))
	}
	if !reflect.DeepEqual(oldCfg.Mappings, newCfg.Mappings) {
		changes = append(changes, fmt.Sprintf("codex.model-fallback.mappings: updated (%d -> %d entries)", len(oldCfg.Mappings), len(newCfg.Mappings)))
	}
	if !reflect.DeepEqual(oldCfg.GlobalTargets, newCfg.GlobalTargets) {
		changes = append(changes, fmt.Sprintf("codex.model-fallback.global-targets: updated (%d -> %d entries)", len(oldCfg.GlobalTargets), len(newCfg.GlobalTargets)))
	}
	return changes
}

func appendCodexRateLimitContinuityChanges(changes []string, oldCfg, newCfg config.EffectiveCodexRateLimitContinuityConfig) []string {
	if oldCfg == newCfg {
		return changes
	}
	if oldCfg.Enabled != newCfg.Enabled {
		changes = append(changes, fmt.Sprintf("codex.rate-limit-continuity.enabled: %t -> %t", oldCfg.Enabled, newCfg.Enabled))
	}
	if oldCfg.ObservationWindowSeconds != newCfg.ObservationWindowSeconds {
		changes = append(changes, fmt.Sprintf("codex.rate-limit-continuity.observation-window-seconds: %d -> %d", oldCfg.ObservationWindowSeconds, newCfg.ObservationWindowSeconds))
	}
	if oldCfg.EstablishedSuccessThreshold != newCfg.EstablishedSuccessThreshold {
		changes = append(changes, fmt.Sprintf("codex.rate-limit-continuity.established-success-threshold: %d -> %d", oldCfg.EstablishedSuccessThreshold, newCfg.EstablishedSuccessThreshold))
	}
	if oldCfg.EstablishedSessionTTLSeconds != newCfg.EstablishedSessionTTLSeconds {
		changes = append(changes, fmt.Sprintf("codex.rate-limit-continuity.established-session-ttl-seconds: %d -> %d", oldCfg.EstablishedSessionTTLSeconds, newCfg.EstablishedSessionTTLSeconds))
	}
	return changes
}

func appendOptionalIntChange(changes []string, field string, oldVal, newVal *int) []string {
	if optionalIntEqual(oldVal, newVal) {
		return changes
	}
	return append(changes, fmt.Sprintf("%s: %s -> %s", field, formatOptionalInt(oldVal), formatOptionalInt(newVal)))
}

func appendOptionalBoolChange(changes []string, field string, oldVal, newVal *bool) []string {
	if optionalBoolEqual(oldVal, newVal) {
		return changes
	}
	return append(changes, fmt.Sprintf("%s: %s -> %s", field, formatOptionalBool(oldVal), formatOptionalBool(newVal)))
}

func optionalBoolEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func formatOptionalBool(value *bool) string {
	if value == nil {
		return "inherit"
	}
	return fmt.Sprintf("%t", *value)
}

func optionalIntEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func formatOptionalInt(v *int) string {
	if v == nil {
		return "<unset>"
	}
	return strconv.Itoa(*v)
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func displayOptionalValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "<none>"
	}
	return trimmed
}

func formatProxyURL(raw string) string {
	return formatURL(raw)
}

func formatURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "<none>"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "<redacted>"
	}
	host := strings.TrimSpace(parsed.Host)
	scheme := strings.TrimSpace(parsed.Scheme)
	if host == "" {
		// Allow host:port style without scheme.
		parsed2, err2 := url.Parse("http://" + trimmed)
		if err2 == nil {
			host = strings.TrimSpace(parsed2.Host)
		}
		scheme = ""
	}
	if host == "" {
		return "<redacted>"
	}
	if scheme == "" {
		return host
	}
	return scheme + "://" + host
}

func equalStringSet(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	aSet := make(map[string]struct{}, len(a))
	for _, k := range a {
		aSet[strings.TrimSpace(k)] = struct{}{}
	}
	bSet := make(map[string]struct{}, len(b))
	for _, k := range b {
		bSet[strings.TrimSpace(k)] = struct{}{}
	}
	if len(aSet) != len(bSet) {
		return false
	}
	for k := range aSet {
		if _, ok := bSet[k]; !ok {
			return false
		}
	}
	return true
}

// equalUpstreamAPIKeys compares two slices of AmpUpstreamAPIKeyEntry for equality.
// Comparison is done by count and content (upstream key and client keys).
func equalUpstreamAPIKeys(a, b []config.AmpUpstreamAPIKeyEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].UpstreamAPIKey) != strings.TrimSpace(b[i].UpstreamAPIKey) {
			return false
		}
		if !equalStringSet(a[i].APIKeys, b[i].APIKeys) {
			return false
		}
	}
	return true
}
