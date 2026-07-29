package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// PluginPermissions controls access to sensitive host callbacks exposed to
// dynamic plugins.
type PluginPermissions struct {
	AuthList     bool `yaml:"auth-list,omitempty" json:"auth-list,omitempty"`
	AuthRead     bool `yaml:"auth-read,omitempty" json:"auth-read,omitempty"`
	AuthWrite    bool `yaml:"auth-write,omitempty" json:"auth-write,omitempty"`
	ModelExecute bool `yaml:"model-execute,omitempty" json:"model-execute,omitempty"`
}

func appendPluginInstanceConfigNode(node *yaml.Node, key string, value any) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	var valueNode yaml.Node
	if errEncode := valueNode.Encode(value); errEncode != nil {
		return
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&valueNode,
	)
}

// CodexClientMetadataConfig controls canonical Codex turn metadata handling.
// Mode off preserves canonical requests unchanged, repair rebuilds compatibility
// projections from the body canonical object, and strict additionally rejects
// conflicting projections. WorkspacePolicy controls workspace privacy while
// passthrough still strips credentials, query strings, and fragments from Git
// remotes. Redact uses stable client-installation/credential-scoped workspace
// pseudonyms and retains only has_changes.
type CodexClientMetadataConfig struct {
	Mode            string `yaml:"mode,omitempty" json:"mode,omitempty"`
	WorkspacePolicy string `yaml:"workspace-policy,omitempty" json:"workspace-policy,omitempty"`
}

// EffectiveCodexClientMetadataConfig is the normalized runtime view. Defaults
// are derived in memory so existing config files are not rewritten.
type EffectiveCodexClientMetadataConfig struct {
	Mode            string
	WorkspacePolicy string
}

// Validate rejects unknown explicit values while preserving empty-value
// defaults and supported compatibility aliases.
func (c CodexClientMetadataConfig) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "", CodexClientMetadataModeOff, "disable", "disabled", CodexClientMetadataModeRepair, CodexClientMetadataModeStrict:
	default:
		return fmt.Errorf("invalid codex.client-metadata.mode")
	}
	workspacePolicy := strings.ToLower(strings.TrimSpace(c.WorkspacePolicy))
	switch workspacePolicy {
	case "", CodexClientMetadataWorkspacePolicyPassthrough, CodexClientMetadataWorkspacePolicyRedact, CodexClientMetadataWorkspacePolicyDrop, "remove":
	default:
		return fmt.Errorf("invalid codex.client-metadata.workspace-policy")
	}
	return nil
}

func (c CodexClientMetadataConfig) Effective() EffectiveCodexClientMetadataConfig {
	return EffectiveCodexClientMetadataConfig{
		Mode:            normalizeCodexClientMetadataMode(c.Mode),
		WorkspacePolicy: normalizeCodexClientMetadataWorkspacePolicy(c.WorkspacePolicy),
	}
}

func normalizeCodexClientMetadataMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", CodexClientMetadataModeRepair:
		return CodexClientMetadataModeRepair
	case CodexClientMetadataModeOff, "disable", "disabled":
		return CodexClientMetadataModeOff
	case CodexClientMetadataModeStrict:
		return CodexClientMetadataModeStrict
	default:
		return CodexClientMetadataModeStrict
	}
}

func normalizeCodexClientMetadataWorkspacePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", CodexClientMetadataWorkspacePolicyPassthrough:
		return CodexClientMetadataWorkspacePolicyPassthrough
	case CodexClientMetadataWorkspacePolicyRedact:
		return CodexClientMetadataWorkspacePolicyRedact
	case CodexClientMetadataWorkspacePolicyDrop, "remove":
		return CodexClientMetadataWorkspacePolicyDrop
	default:
		return CodexClientMetadataWorkspacePolicyDrop
	}
}

// CodexModelFallbackConfig controls ordered, opt-in fallback between Codex
// models after a precisely classified quota or capacity failure.
type CodexModelFallbackConfig struct {
	Enabled             bool                        `yaml:"enabled" json:"enabled"`
	Triggers            []string                    `yaml:"triggers" json:"triggers"`
	ReasoningContinuity string                      `yaml:"reasoning-continuity,omitempty" json:"reasoning-continuity,omitempty"`
	Mappings            []CodexModelFallbackMapping `yaml:"mappings" json:"mappings"`
	GlobalTargets       []string                    `yaml:"global-targets,omitempty" json:"global-targets,omitempty"`
}

// CodexModelFallbackMapping defines ordered target models for one requested
// Codex model. From is matched case-insensitively after trimming.
type CodexModelFallbackMapping struct {
	From string   `yaml:"from" json:"from"`
	To   []string `yaml:"to" json:"to"`
}

// EffectiveCodexModelFallbackConfig is the sanitized runtime view of
// CodexModelFallbackConfig. Defaults are derived in memory so existing config
// files are not rewritten.
type EffectiveCodexModelFallbackConfig struct {
	Enabled             bool
	Triggers            []string
	ReasoningContinuity string
	Mappings            []CodexModelFallbackMapping
	GlobalTargets       []string
}

// Effective returns a normalized model-fallback policy.
func (c CodexModelFallbackConfig) Effective() EffectiveCodexModelFallbackConfig {
	triggers := defaultedTrimmedStringList(c.Triggers, []string{
		CodexModelFallbackTriggerUsageLimit,
		CodexModelFallbackTriggerCapacity,
	}, true)
	filteredTriggers := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		switch trigger {
		case CodexModelFallbackTriggerUsageLimit, CodexModelFallbackTriggerCapacity:
			filteredTriggers = append(filteredTriggers, trigger)
		}
	}

	reasoningContinuity := strings.ToLower(strings.TrimSpace(c.ReasoningContinuity))
	if reasoningContinuity != CodexModelFallbackReasoningContinuityContextReset {
		reasoningContinuity = CodexModelFallbackReasoningContinuitySameModelOnly
	}

	mappings := make([]CodexModelFallbackMapping, 0, len(c.Mappings))
	for _, mapping := range c.Mappings {
		from := strings.TrimSpace(mapping.From)
		if from == "" {
			continue
		}
		seen := make(map[string]struct{}, len(mapping.To))
		to := make([]string, 0, len(mapping.To))
		for _, target := range mapping.To {
			target = strings.TrimSpace(target)
			key := strings.ToLower(target)
			if target == "" || strings.EqualFold(target, from) {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			to = append(to, target)
		}
		if len(to) == 0 {
			continue
		}
		mappings = append(mappings, CodexModelFallbackMapping{From: from, To: to})
	}
	globalTargets := make([]string, 0, len(c.GlobalTargets))
	seenGlobalTargets := make(map[string]struct{}, len(c.GlobalTargets))
	for _, target := range c.GlobalTargets {
		target = strings.TrimSpace(target)
		key := strings.ToLower(target)
		if target == "" {
			continue
		}
		if _, ok := seenGlobalTargets[key]; ok {
			continue
		}
		seenGlobalTargets[key] = struct{}{}
		globalTargets = append(globalTargets, target)
	}

	return EffectiveCodexModelFallbackConfig{
		Enabled:             c.Enabled,
		Triggers:            filteredTriggers,
		ReasoningContinuity: reasoningContinuity,
		Mappings:            mappings,
		GlobalTargets:       globalTargets,
	}
}

// AllowsTrigger reports whether the normalized fallback policy accepts trigger.
func (c EffectiveCodexModelFallbackConfig) AllowsTrigger(trigger string) bool {
	if !c.Enabled {
		return false
	}
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	for _, candidate := range c.Triggers {
		if candidate == trigger {
			return true
		}
	}
	return false
}

// TargetsFor returns the ordered fallback targets for model and trigger.
func (c EffectiveCodexModelFallbackConfig) TargetsFor(model, trigger string) []string {
	if strings.TrimSpace(model) == "" || !c.AllowsTrigger(trigger) {
		return nil
	}
	for _, mapping := range c.Mappings {
		if strings.EqualFold(strings.TrimSpace(mapping.From), strings.TrimSpace(model)) {
			return append([]string(nil), mapping.To...)
		}
	}
	return nil
}

// CodexRateLimitContinuityConfig controls the in-memory observation window
// used to distinguish a fresh-session usage limit from an auth/model-wide
// quota failure. It is effective only while routing.session-affinity is enabled.
type CodexRateLimitContinuityConfig struct {
	Enabled                      bool `yaml:"enabled" json:"enabled"`
	ObservationWindowSeconds     int  `yaml:"observation-window-seconds" json:"observation-window-seconds"`
	EstablishedSuccessThreshold  int  `yaml:"established-success-threshold" json:"established-success-threshold"`
	EstablishedSessionTTLSeconds int  `yaml:"established-session-ttl-seconds" json:"established-session-ttl-seconds"`
}

// EffectiveCodexRateLimitContinuityConfig is the sanitized runtime view of
// CodexRateLimitContinuityConfig.
type EffectiveCodexRateLimitContinuityConfig struct {
	Enabled                      bool
	ObservationWindowSeconds     int
	EstablishedSuccessThreshold  int
	EstablishedSessionTTLSeconds int
}

// Effective returns a normalized rate-limit continuity policy.
func (c CodexRateLimitContinuityConfig) Effective() EffectiveCodexRateLimitContinuityConfig {
	observationWindowSeconds := c.ObservationWindowSeconds
	if observationWindowSeconds <= 0 {
		observationWindowSeconds = CodexRateLimitContinuityDefaultObservationWindowSeconds
	}
	establishedSuccessThreshold := c.EstablishedSuccessThreshold
	if establishedSuccessThreshold <= 0 {
		establishedSuccessThreshold = CodexRateLimitContinuityDefaultEstablishedSuccessThreshold
	}
	establishedSessionTTLSeconds := c.EstablishedSessionTTLSeconds
	if establishedSessionTTLSeconds <= 0 {
		establishedSessionTTLSeconds = CodexRateLimitContinuityDefaultEstablishedSessionTTLSeconds
	}
	return EffectiveCodexRateLimitContinuityConfig{
		Enabled:                      c.Enabled,
		ObservationWindowSeconds:     observationWindowSeconds,
		EstablishedSuccessThreshold:  establishedSuccessThreshold,
		EstablishedSessionTTLSeconds: establishedSessionTTLSeconds,
	}
}

// CodexAbnormalReasoningRetryConfig controls the CPA-Core-LTS retry guard for
// suspicious Codex successful responses.
type CodexAbnormalReasoningRetryConfig struct {
	Enabled                bool                                    `yaml:"enabled" json:"enabled"`
	Action                 string                                  `yaml:"action,omitempty" json:"action,omitempty"`
	ModelContains          []string                                `yaml:"model-contains" json:"model-contains"`
	ReasoningEfforts       []string                                `yaml:"reasoning-efforts" json:"reasoning-efforts"`
	ReasoningTokens        []int64                                 `yaml:"reasoning-tokens" json:"reasoning-tokens"`
	AuthKinds              []string                                `yaml:"auth-kinds" json:"auth-kinds"`
	AuthIDs                []string                                `yaml:"auth-ids" json:"auth-ids"`
	StreamBuffer           *bool                                   `yaml:"stream-buffer,omitempty" json:"stream-buffer,omitempty"`
	StreamBufferMaxBytes   *int64                                  `yaml:"stream-buffer-max-bytes,omitempty" json:"stream-buffer-max-bytes,omitempty"`
	MaxRetries             *int                                    `yaml:"max-retries,omitempty" json:"max-retries,omitempty"`
	ExhaustedBehavior      string                                  `yaml:"exhausted-behavior,omitempty" json:"exhausted-behavior,omitempty"`
	ClientUsageAggregation string                                  `yaml:"client-usage-aggregation,omitempty" json:"client-usage-aggregation,omitempty"`
	DeliveryPolicy         string                                  `yaml:"delivery-policy,omitempty" json:"delivery-policy,omitempty"`
	FallbackPolicy         string                                  `yaml:"fallback-policy,omitempty" json:"fallback-policy,omitempty"`
	HedgedRetry            CodexAbnormalReasoningHedgedRetryConfig `yaml:"hedged-retry" json:"hedged-retry"`
}

// CodexAbnormalReasoningHedgedRetryConfig controls the optional hedged retry
// lane launched after an abnormal reasoning retry has already been triggered.
type CodexAbnormalReasoningHedgedRetryConfig struct {
	Enabled             bool   `yaml:"enabled" json:"enabled"`
	Mode                string `yaml:"mode,omitempty" json:"mode,omitempty"`
	HedgeDelayMS        *int   `yaml:"hedge-delay-ms,omitempty" json:"hedge-delay-ms,omitempty"`
	RequireDistinctAuth *bool  `yaml:"require-distinct-auth,omitempty" json:"require-distinct-auth,omitempty"`
}

// EffectiveCodexAbnormalReasoningRetryConfig is the sanitized runtime view of
// CodexAbnormalReasoningRetryConfig. It is intentionally derived in memory so
// existing config.yaml files are not rewritten to add default values.
type EffectiveCodexAbnormalReasoningRetryConfig struct {
	Enabled                bool
	Action                 string
	ModelContains          []string
	ReasoningEfforts       []string
	ReasoningTokens        []int64
	AuthKinds              []string
	AuthIDs                []string
	StreamBuffer           bool
	StreamBufferMaxBytes   int64
	MaxRetries             int
	ExhaustedBehavior      string
	ClientUsageAggregation string
	DeliveryPolicy         string
	FallbackPolicy         string
	HedgedRetry            EffectiveCodexAbnormalReasoningHedgedRetryConfig
}

// EffectiveCodexAbnormalReasoningHedgedRetryConfig is the sanitized runtime view
// of CodexAbnormalReasoningHedgedRetryConfig.
type EffectiveCodexAbnormalReasoningHedgedRetryConfig struct {
	Enabled             bool
	Mode                string
	HedgeDelayMS        int
	RequireDistinctAuth bool
}

// Effective returns the runtime config with LTS defaults applied.
func (c CodexAbnormalReasoningRetryConfig) Effective() EffectiveCodexAbnormalReasoningRetryConfig {
	streamBuffer := true
	if c.StreamBuffer != nil {
		streamBuffer = *c.StreamBuffer
	}
	maxRetries := 2
	if c.MaxRetries != nil {
		maxRetries = *c.MaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	streamBufferMaxBytes := CodexAbnormalReasoningRetryDefaultStreamBufferMaxBytes
	if c.StreamBufferMaxBytes != nil && *c.StreamBufferMaxBytes > 0 {
		streamBufferMaxBytes = *c.StreamBufferMaxBytes
	}
	hedgeDelayMS := 1000
	if c.HedgedRetry.HedgeDelayMS != nil {
		hedgeDelayMS = *c.HedgedRetry.HedgeDelayMS
	}
	if hedgeDelayMS < 0 {
		hedgeDelayMS = 0
	}
	requireDistinctAuth := true
	if c.HedgedRetry.RequireDistinctAuth != nil {
		requireDistinctAuth = *c.HedgedRetry.RequireDistinctAuth
	}
	action := normalizeCodexAbnormalReasoningRetryAction(c.Action, c.Enabled)
	return EffectiveCodexAbnormalReasoningRetryConfig{
		Enabled:                action == CodexAbnormalReasoningRetryActionRetry || action == CodexAbnormalReasoningRetryActionObserveOnly,
		Action:                 action,
		ModelContains:          defaultedTrimmedStringList(c.ModelContains, []string{"gpt-5.5"}, false),
		ReasoningEfforts:       defaultedTrimmedStringList(c.ReasoningEfforts, nil, true),
		ReasoningTokens:        defaultedPositiveInt64List(c.ReasoningTokens, []int64{516, 1034}),
		AuthKinds:              defaultedTrimmedStringList(c.AuthKinds, []string{"oauth"}, true),
		AuthIDs:                defaultedTrimmedStringList(c.AuthIDs, nil, false),
		StreamBuffer:           streamBuffer,
		StreamBufferMaxBytes:   streamBufferMaxBytes,
		MaxRetries:             maxRetries,
		ExhaustedBehavior:      normalizeCodexAbnormalReasoningRetryExhaustedBehavior(c.ExhaustedBehavior),
		ClientUsageAggregation: normalizeCodexAbnormalReasoningRetryClientUsageAggregation(c.ClientUsageAggregation),
		DeliveryPolicy:         normalizeCodexAbnormalReasoningRetryDeliveryPolicy(c.DeliveryPolicy),
		FallbackPolicy:         normalizeCodexAbnormalReasoningRetryFallbackPolicy(c.FallbackPolicy),
		HedgedRetry: EffectiveCodexAbnormalReasoningHedgedRetryConfig{
			Enabled:             c.HedgedRetry.Enabled,
			Mode:                normalizeCodexAbnormalReasoningHedgedRetryMode(c.HedgedRetry.Mode),
			HedgeDelayMS:        hedgeDelayMS,
			RequireDistinctAuth: requireDistinctAuth,
		},
	}
}

func normalizeCodexAbnormalReasoningRetryAction(value string, enabled bool) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexAbnormalReasoningRetryActionRetry:
		return CodexAbnormalReasoningRetryActionRetry
	case "observe_only", "observe-only", "observe":
		return CodexAbnormalReasoningRetryActionObserveOnly
	case CodexAbnormalReasoningRetryActionDisabled, "disable", "off":
		return CodexAbnormalReasoningRetryActionDisabled
	default:
		if enabled {
			return CodexAbnormalReasoningRetryActionRetry
		}
		return CodexAbnormalReasoningRetryActionDisabled
	}
}

func normalizeCodexAbnormalReasoningRetryExhaustedBehavior(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "error":
		return CodexAbnormalReasoningRetryExhaustedBehaviorError
	case "pass-through", "passthrough", "pass_through":
		return CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough
	default:
		return CodexAbnormalReasoningRetryExhaustedBehaviorError
	}
}

func normalizeCodexAbnormalReasoningRetryClientUsageAggregation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "delivered-only", "delivered_only", "delivered":
		return CodexAbnormalReasoningRetryClientUsageAggregationDeliveredOnly
	case "sum":
		return CodexAbnormalReasoningRetryClientUsageAggregationSum
	case "sum-with-delivered-total", "sum_with_delivered_total":
		return CodexAbnormalReasoningRetryClientUsageAggregationSumWithDeliveredTotal
	default:
		return CodexAbnormalReasoningRetryClientUsageAggregationDeliveredOnly
	}
}

func normalizeCodexAbnormalReasoningRetryDeliveryPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "best-non-special", "best_non_special", "normal-first", "normal_first":
		return CodexAbnormalReasoningRetryDeliveryPolicyBestNonSpecial
	case "first-non-special", "first_non_special", "first-normal", "first_normal":
		return CodexAbnormalReasoningRetryDeliveryPolicyFirstNonSpecial
	case "max-output", "max_output", "longest":
		return CodexAbnormalReasoningRetryDeliveryPolicyMaxOutput
	case "latest", "last":
		return CodexAbnormalReasoningRetryDeliveryPolicyLatest
	default:
		return CodexAbnormalReasoningRetryDeliveryPolicyBestNonSpecial
	}
}

func normalizeCodexAbnormalReasoningRetryFallbackPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "best-special", "best_special":
		return CodexAbnormalReasoningRetryFallbackPolicyBestSpecial
	case "max-output-special", "max_output_special", "max-output", "max_output", "longest":
		return CodexAbnormalReasoningRetryFallbackPolicyMaxOutputSpecial
	case "latest-special", "latest_special", "latest", "last":
		return CodexAbnormalReasoningRetryFallbackPolicyLatestSpecial
	default:
		return CodexAbnormalReasoningRetryFallbackPolicyBestSpecial
	}
}

func normalizeCodexAbnormalReasoningHedgedRetryMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "speed":
		return CodexAbnormalReasoningHedgedRetryModeSpeed
	case "quality":
		return CodexAbnormalReasoningHedgedRetryModeQuality
	default:
		return CodexAbnormalReasoningHedgedRetryModeQuality
	}
}

func defaultedTrimmedStringList(values []string, defaults []string, lower bool) []string {
	source := values
	if len(source) == 0 {
		source = defaults
	}
	if len(source) == 0 {
		return nil
	}
	out := make([]string, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, value := range source {
		trimmed := strings.TrimSpace(value)
		if lower {
			trimmed = strings.ToLower(trimmed)
		}
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func defaultedPositiveInt64List(values []int64, defaults []int64) []int64 {
	source := values
	if len(source) == 0 {
		source = defaults
	}
	if len(source) == 0 {
		return nil
	}
	out := make([]int64, 0, len(source))
	seen := make(map[int64]struct{}, len(source))
	for _, value := range source {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// AmpModelMapping defines a model name mapping for Amp CLI requests.
// When Amp requests a model that isn't available locally, this mapping
// allows routing to an alternative model that IS available.
type AmpModelMapping struct {
	// From is the model name that Amp CLI requests (e.g., "claude-opus-4.5").
	From string `yaml:"from" json:"from"`

	// To is the target model name to route to (e.g., "claude-sonnet-4").
	// The target model must have available providers in the registry.
	To string `yaml:"to" json:"to"`

	// Regex indicates whether the 'from' field should be interpreted as a regular
	// expression for matching model names. When true, this mapping is evaluated
	// after exact matches and in the order provided. Defaults to false (exact match).
	Regex bool `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// AmpCode groups Amp CLI integration settings including upstream routing,
// optional overrides, management route restrictions, and model fallback mappings.
type AmpCode struct {
	// UpstreamURL defines the upstream Amp control plane used for non-provider calls.
	UpstreamURL string `yaml:"upstream-url" json:"upstream-url"`

	// UpstreamAPIKey optionally overrides the Authorization header when proxying Amp upstream calls.
	UpstreamAPIKey string `yaml:"upstream-api-key" json:"upstream-api-key"`

	// UpstreamAPIKeys maps client API keys (from top-level api-keys) to upstream API keys.
	// When a request is authenticated with one of the APIKeys, the corresponding UpstreamAPIKey
	// is used for the upstream Amp request.
	UpstreamAPIKeys []AmpUpstreamAPIKeyEntry `yaml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`

	// RestrictManagementToLocalhost restricts Amp management routes (/api/user, /api/threads, etc.)
	// to only accept connections from localhost (127.0.0.1, ::1). When true, prevents drive-by
	// browser attacks and remote access to management endpoints. Default: false (API key auth is sufficient).
	RestrictManagementToLocalhost bool `yaml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`

	// ModelMappings defines model name mappings for Amp CLI requests.
	// When Amp requests a model that isn't available locally, these mappings
	// allow routing to an alternative model that IS available.
	ModelMappings []AmpModelMapping `yaml:"model-mappings" json:"model-mappings"`

	// ForceModelMappings when true, model mappings take precedence over local API keys.
	// When false (default), local API keys are used first if available.
	ForceModelMappings bool `yaml:"force-model-mappings" json:"force-model-mappings"`
}

// AmpUpstreamAPIKeyEntry maps a set of client API keys to a specific upstream API key.
// When a request is authenticated with one of the APIKeys, the corresponding UpstreamAPIKey
// is used for the upstream Amp request.
type AmpUpstreamAPIKeyEntry struct {
	// UpstreamAPIKey is the API key to use when proxying to the Amp upstream.
	UpstreamAPIKey string `yaml:"upstream-api-key" json:"upstream-api-key"`

	// APIKeys are the client API keys (from top-level api-keys) that map to this upstream key.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`
}

// Legacy migration helpers (move deprecated config keys into structured fields).
type legacyConfigData struct {
	LegacyGeminiKeys      []string                    `yaml:"generative-language-api-key"`
	OpenAICompat          []legacyOpenAICompatibility `yaml:"openai-compatibility"`
	AmpUpstreamURL        string                      `yaml:"amp-upstream-url"`
	AmpUpstreamAPIKey     string                      `yaml:"amp-upstream-api-key"`
	AmpRestrictManagement *bool                       `yaml:"amp-restrict-management-to-localhost"`
	AmpModelMappings      []AmpModelMapping           `yaml:"amp-model-mappings"`
}

type legacyOpenAICompatibility struct {
	Name    string   `yaml:"name"`
	BaseURL string   `yaml:"base-url"`
	APIKeys []string `yaml:"api-keys"`
}

func (cfg *Config) migrateLegacyGeminiKeys(legacy []string) bool {
	if cfg == nil || len(legacy) == 0 {
		return false
	}
	changed := false
	seen := make(map[string]struct{}, len(cfg.GeminiKey))
	for i := range cfg.GeminiKey {
		key := strings.TrimSpace(cfg.GeminiKey[i].APIKey)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	for _, raw := range legacy {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		cfg.GeminiKey = append(cfg.GeminiKey, GeminiKey{APIKey: key})
		seen[key] = struct{}{}
		changed = true
	}
	return changed
}

func (cfg *Config) migrateLegacyOpenAICompatibilityKeys(legacy []legacyOpenAICompatibility) bool {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 || len(legacy) == 0 {
		return false
	}
	changed := false
	for _, legacyEntry := range legacy {
		if len(legacyEntry.APIKeys) == 0 {
			continue
		}
		target := findOpenAICompatTarget(cfg.OpenAICompatibility, legacyEntry.Name, legacyEntry.BaseURL)
		if target == nil {
			continue
		}
		if mergeLegacyOpenAICompatAPIKeys(target, legacyEntry.APIKeys) {
			changed = true
		}
	}
	return changed
}

func mergeLegacyOpenAICompatAPIKeys(entry *OpenAICompatibility, keys []string) bool {
	if entry == nil || len(keys) == 0 {
		return false
	}
	changed := false
	existing := make(map[string]struct{}, len(entry.APIKeyEntries))
	for i := range entry.APIKeyEntries {
		key := strings.TrimSpace(entry.APIKeyEntries[i].APIKey)
		if key == "" {
			continue
		}
		existing[key] = struct{}{}
	}
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := existing[key]; ok {
			continue
		}
		entry.APIKeyEntries = append(entry.APIKeyEntries, OpenAICompatibilityAPIKey{APIKey: key})
		existing[key] = struct{}{}
		changed = true
	}
	return changed
}

func findOpenAICompatTarget(entries []OpenAICompatibility, legacyName, legacyBase string) *OpenAICompatibility {
	nameKey := strings.ToLower(strings.TrimSpace(legacyName))
	baseKey := strings.ToLower(strings.TrimSpace(legacyBase))
	if nameKey != "" && baseKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].Name)) == nameKey &&
				strings.ToLower(strings.TrimSpace(entries[i].BaseURL)) == baseKey {
				return &entries[i]
			}
		}
	}
	if baseKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].BaseURL)) == baseKey {
				return &entries[i]
			}
		}
	}
	if nameKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].Name)) == nameKey {
				return &entries[i]
			}
		}
	}
	return nil
}

func (cfg *Config) migrateLegacyAmpConfig(legacy *legacyConfigData) bool {
	if cfg == nil || legacy == nil {
		return false
	}
	changed := false
	if cfg.AmpCode.UpstreamURL == "" {
		if val := strings.TrimSpace(legacy.AmpUpstreamURL); val != "" {
			cfg.AmpCode.UpstreamURL = val
			changed = true
		}
	}
	if cfg.AmpCode.UpstreamAPIKey == "" {
		if val := strings.TrimSpace(legacy.AmpUpstreamAPIKey); val != "" {
			cfg.AmpCode.UpstreamAPIKey = val
			changed = true
		}
	}
	if legacy.AmpRestrictManagement != nil {
		cfg.AmpCode.RestrictManagementToLocalhost = *legacy.AmpRestrictManagement
		changed = true
	}
	if len(cfg.AmpCode.ModelMappings) == 0 && len(legacy.AmpModelMappings) > 0 {
		cfg.AmpCode.ModelMappings = append([]AmpModelMapping(nil), legacy.AmpModelMappings...)
		changed = true
	}
	return changed
}

func removeLegacyAmpKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "amp-upstream-url")
	removeMapKey(root, "amp-upstream-api-key")
	removeMapKey(root, "amp-restrict-management-to-localhost")
	removeMapKey(root, "amp-model-mappings")
}
