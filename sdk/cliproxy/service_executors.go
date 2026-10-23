package cliproxy

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

type openAICompatibilityRegistrationCache struct {
	cfg     *config.Config
	byName  map[string]*openAICompatibilityRegistrationEntry
	byIndex map[int]*openAICompatibilityRegistrationEntry
}

// pluginHostHasAuthProvider is overridable in tests to avoid loading real plugins.
var pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
	return host != nil && host.HasAuthProvider(provider)
}

// pluginHostHasAuthModelProvider only checks declarations, never model discovery.
var pluginHostHasAuthModelProvider = func(host *pluginhost.Host, provider string) bool {
	return host != nil && host.HasAuthModelProvider(provider)
}

type openAICompatibilityRegistrationEntry struct {
	providerKey string
	models      []*ModelInfo
}

func (s *Service) newOpenAICompatibilityRegistrationCache() *openAICompatibilityRegistrationCache {
	if s == nil {
		return nil
	}
	return newOpenAICompatibilityRegistrationCacheForConfig(s.currentConfig())
}

func newOpenAICompatibilityRegistrationCacheForConfig(cfg *config.Config) *openAICompatibilityRegistrationCache {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return nil
	}

	cache := &openAICompatibilityRegistrationCache{
		cfg:     cfg,
		byName:  make(map[string]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
		byIndex: make(map[int]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		compatName := strings.TrimSpace(compat.Name)
		key := strings.ToLower(compatName)
		providerName := strings.ToLower(compatName)
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		entry := &openAICompatibilityRegistrationEntry{
			providerKey: util.OpenAICompatibleProviderKey(providerName),
			models:      buildOpenAICompatibilityConfigModels(compat),
		}
		cache.byIndex[i] = entry
		if _, exists := cache.byName[key]; !exists {
			cache.byName[key] = entry
		}
	}
	if len(cache.byName) == 0 {
		return nil
	}
	return cache
}

func (c *openAICompatibilityRegistrationCache) lookup(auth *coreauth.Auth, compatName string) (*openAICompatibilityRegistrationEntry, bool) {
	if c == nil {
		return nil, false
	}
	if auth != nil && auth.AuthSourceKind() == coreauth.AuthSourceConfig && auth.Attributes != nil {
		if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex])); errIndex == nil {
			entry, ok := c.byIndex[index]
			return entry, ok
		}
	}
	entry, ok := c.byName[strings.ToLower(strings.TrimSpace(compatName))]
	return entry, ok
}

func (s *Service) hasNativeOpenAICompatExecutorConfig(a *coreauth.Auth, providerKey string, cfg *config.Config) bool {
	if a == nil {
		return false
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if a.Attributes != nil {
		if strings.TrimSpace(a.Attributes["base_url"]) != "" {
			return true
		}
		if strings.TrimSpace(a.Attributes["compat_name"]) != "" {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return true
	}
	if s == nil || cfg == nil {
		return false
	}

	candidates := make([]string, 0, 3)
	if providerKey != "" {
		candidates = append(candidates, providerKey)
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, strings.ToLower(v))
		}
	}
	if provider := strings.TrimSpace(a.Provider); provider != "" {
		candidates = append(candidates, strings.ToLower(provider))
	}

	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(compat.Name))
		if name == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && candidate == name {
				return true
			}
		}
	}
	return false
}

func (s *Service) unregisterOpenAICompatExecutor(providerKey string) {
	if s == nil || s.coreManager == nil {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		return
	}
	existing, okExecutor := s.coreManager.Executor(providerKey)
	if !okExecutor || existing == nil {
		return
	}
	if _, okOpenAICompat := existing.(*executor.OpenAICompatExecutor); okOpenAICompat {
		s.coreManager.UnregisterExecutor(providerKey)
		return
	}
	if pluginhost.IsPluginRefreshCompatExecutor(existing) {
		s.coreManager.UnregisterExecutor(providerKey)
	}
}

func (s *Service) ensureExecutorsForAuth(a *coreauth.Auth) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, false)
}

func (s *Service) ensureExecutorsForAuthWithMode(a *coreauth.Auth, forceReplace bool) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, forceReplace)
}

func (s *Service) ensureExecutorsForAuthWithContext(ctx context.Context, a *coreauth.Auth, forceReplace bool) {
	if a == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		auths:             []*coreauth.Auth{a},
		forceReplaceAuths: forceReplace,
	})
}

func (s *Service) registerAvailableExecutors(ctx context.Context, opts executorRegistrationOptions) {
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.executorRegistrationMu.Lock()
	defer s.executorRegistrationMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	// Keep all Service-owned executor registration paths here so native, Home,
	// auth-derived, and plugin executors stay in the same binding order.
	if opts.includeBaseline {
		s.registerExecutorsForAuths(baselineExecutorAuths(), opts.forceReplaceAuths)
	}
	if len(opts.auths) > 0 {
		s.registerExecutorsForAuths(opts.auths, opts.forceReplaceAuths)
	}
	if opts.includePlugins && s.pluginHost != nil {
		registerPluginExecutors(s.pluginHost, s.coreManager)
	}
}

func baselineExecutorAuths() []*coreauth.Auth {
	providers := []string{
		"codex",
		"claude",
		constant.Gemini,
		constant.GeminiInteractions,
		"vertex",
		"aistudio",
		"antigravity",
		"kimi",
		"kimi-ai",
		"kimi.ai",
		"xai",
		"devin",
		"meta",
		"openai-compatibility",
	}
	auths := make([]*coreauth.Auth, 0, len(providers))
	for _, provider := range providers {
		auth := &coreauth.Auth{
			ID:       provider,
			Provider: provider,
		}
		if provider == "openai-compatibility" {
			auth.Attributes = map[string]string{"compat_name": "openai-compatibility"}
		}
		auths = append(auths, auth)
	}
	return auths
}

func (s *Service) registerExecutorsForAuths(auths []*coreauth.Auth, forceReplace bool) {
	reboundCodex := false
	reboundXAI := false
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if reboundCodex && forceReplace {
				continue
			}
			reboundCodex = true
		}
		if auth != nil && !auth.Disabled && strings.EqualFold(strings.TrimSpace(auth.Provider), "xai") {
			if reboundXAI && forceReplace {
				continue
			}
			reboundXAI = true
		}
		s.registerExecutorForAuth(auth, forceReplace)
	}
}

func (s *Service) registerExecutorForAuth(a *coreauth.Auth, forceReplace bool) {
	if s == nil || s.coreManager == nil || a == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if strings.EqualFold(strings.TrimSpace(a.Provider), "codex") {
		if !forceReplace {
			existingExecutor, hasExecutor := s.coreManager.Executor("codex")
			if hasExecutor {
				existingCodexAutoExecutor, isCodexAutoExecutor := existingExecutor.(*executor.CodexAutoExecutor)
				if isCodexAutoExecutor && existingCodexAutoExecutor.UsesConfig(cfg) {
					return
				}
			}
		}
		var previous *executor.CodexAutoExecutor
		if existing, ok := s.coreManager.Executor("codex"); ok {
			previous, _ = existing.(*executor.CodexAutoExecutor)
		}
		s.coreManager.RegisterExecutor(executor.NewCodexAutoExecutor(cfg, previous))
		return
	}
	// Skip disabled auth entries when (re)binding executors.
	// Disabled auths can linger during config reloads (e.g., removed OpenAI-compat entries)
	// and must not override active provider executors.
	if a.Disabled {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		s.registerOpenAICompatProviderExecutor(compatProviderKey, a, cfg, forceReplace, false)
		return
	}
	switch strings.ToLower(a.Provider) {
	case constant.Gemini:
		s.coreManager.RegisterExecutor(executor.NewGeminiExecutor(cfg))
	case constant.GeminiInteractions:
		s.coreManager.RegisterExecutor(executor.NewGeminiInteractionsExecutor(cfg))
	case "vertex":
		s.coreManager.RegisterExecutor(executor.NewGeminiVertexExecutor(cfg))
	case "aistudio":
		if s.wsGateway != nil {
			s.coreManager.RegisterExecutor(executor.NewAIStudioExecutor(cfg, a.ID, s.wsGateway))
		}
		return
	case "antigravity":
		s.coreManager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))
	case "claude":
		s.coreManager.RegisterExecutor(executor.NewClaudeExecutor(cfg))
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		s.coreManager.RegisterExecutor(executor.NewKimiExecutor(cfg))
	case "xai":
		if !forceReplace {
			existingExecutor, hasExecutor := s.coreManager.Executor("xai")
			if hasExecutor {
				existingXAIAutoExecutor, isXAIAutoExecutor := existingExecutor.(*executor.XAIAutoExecutor)
				if isXAIAutoExecutor && existingXAIAutoExecutor.UsesConfig(cfg) {
					return
				}
			}
		}
		s.coreManager.RegisterExecutor(executor.NewXAIAutoExecutor(cfg))
	case "devin":
		s.coreManager.RegisterExecutor(executor.NewDevinExecutor(cfg))
	case "meta":
		s.coreManager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		if s.pluginHost != nil &&
			s.pluginHost.HasExecutorCandidateProvider(providerKey) &&
			!s.hasNativeOpenAICompatExecutorConfig(a, providerKey, cfg) {
			s.unregisterOpenAICompatExecutor(providerKey)
			return
		}
		// Keep native OpenAI-compat inference for base_url routing, but delegate
		// OAuth refresh to the plugin AuthProvider when one is registered.
		s.registerOpenAICompatProviderExecutor(providerKey, a, cfg, forceReplace, true)
	}
}

// registerOpenAICompatProviderExecutor binds a native OpenAI-compat executor, optionally
// wrapping it so plugin AuthProvider refresh remains available.
// When respectNonOwned is true, an existing non-owned executor is preserved unless it is a
// bare OpenAI-compat executor that should be upgraded to the plugin-refresh wrapper.
func (s *Service) registerOpenAICompatProviderExecutor(providerKey string, auth *coreauth.Auth, cfg *config.Config, forceReplace bool, respectNonOwned bool) {
	if s == nil || s.coreManager == nil {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = "openai-compatibility"
	}
	compatExecutor := executor.NewOpenAICompatExecutor(providerKey, cfg)
	nextExecutor := s.wrapOpenAICompatIfPluginAuth(compatExecutor, auth, cfg)
	if !forceReplace {
		if existingExecutor, hasExecutor := s.coreManager.Executor(providerKey); hasExecutor {
			if shouldKeepExistingOpenAICompatExecutor(s, existingExecutor, nextExecutor, respectNonOwned) {
				return
			}
		}
	}
	s.coreManager.RegisterExecutor(nextExecutor)
}

func (s *Service) wrapOpenAICompatIfPluginAuth(compatExecutor *executor.OpenAICompatExecutor, auth *coreauth.Auth, cfg *config.Config) coreauth.ProviderExecutor {
	if compatExecutor == nil {
		return nil
	}
	for _, candidate := range pluginAuthProviderLookupKeys(auth, compatExecutor.Identifier()) {
		if pluginHostHasAuthProvider(s.pluginHost, candidate) {
			return pluginhost.NewPluginRefreshCompatExecutor(compatExecutor, s.pluginHost, cfg)
		}
	}
	return compatExecutor
}

func pluginAuthProviderLookupKeys(auth *coreauth.Auth, fallback string) []string {
	keys := make([]string, 0, 4)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		for _, existing := range keys {
			if existing == value {
				return
			}
		}
		keys = append(keys, value)
	}
	if auth != nil {
		add(auth.Provider)
		if auth.Attributes != nil {
			add(auth.Attributes["provider_key"])
			add(auth.Attributes["compat_name"])
		}
	}
	add(fallback)
	return keys
}

func shouldKeepExistingOpenAICompatExecutor(s *Service, existing, next coreauth.ProviderExecutor, respectNonOwned bool) bool {
	if existing == nil || next == nil {
		return false
	}
	if shouldUpgradeOpenAICompatToPluginRefresh(existing, next) {
		return false
	}
	if pluginhost.IsPluginRefreshCompatExecutor(existing) && pluginhost.IsPluginRefreshCompatExecutor(next) {
		return true
	}
	_, existingBare := existing.(*executor.OpenAICompatExecutor)
	_, nextBare := next.(*executor.OpenAICompatExecutor)
	if existingBare && nextBare {
		return true
	}
	if !respectNonOwned {
		// Historical openai-compatibility path only short-circuits bare native executors.
		return existingBare
	}
	if s != nil && s.pluginHost != nil && s.pluginHost.OwnsExecutor(existing) {
		return false
	}
	return true
}

func shouldUpgradeOpenAICompatToPluginRefresh(existing, next coreauth.ProviderExecutor) bool {
	if existing == nil || next == nil {
		return false
	}
	if !pluginhost.IsPluginRefreshCompatExecutor(next) {
		return false
	}
	_, bareOpenAICompat := existing.(*executor.OpenAICompatExecutor)
	return bareOpenAICompat
}

// registerResolvedModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
// LTS model policy (oauth-model-policy + per-account model_policy) is applied here on the real model IDs,
// before alias/prefix projection, so every registration path shares one evaluation.
func (s *Service) registerResolvedModelsForAuth(a *coreauth.Auth, providerKey string, models []*ModelInfo) bool {
	return s.registerResolvedModelsForAuthFenced(a, newModelRegistrationFence(a, s.currentConfig()), providerKey, models)
}

// registerResolvedModelsForAuthFenced publishes models only if fence still
// matches the manager's current auth snapshot and the registry epoch has not
// advanced since the registration began. A superseded registration must not
// overwrite or remove a newer one.
func (s *Service) registerResolvedModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence, providerKey string, models []*ModelInfo) bool {
	return s.registerResolvedModelsForAuthWithConfigFenced(s.currentConfig(), a, fence, providerKey, models)
}

func (s *Service) registerResolvedModelsForAuthWithConfigFenced(cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, providerKey string, models []*ModelInfo) bool {
	if a == nil || a.ID == "" {
		return true
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		return s.unregisterModelsForAuthFenced(a, fence)
	}
	normalizedModels := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		clone := *model
		clone.ID = modelID
		normalizedModels = append(normalizedModels, &clone)
	}
	catalogIDs := make([]string, 0, len(normalizedModels))
	published := make([]*ModelInfo, 0, len(normalizedModels))
	for _, model := range normalizedModels {
		catalogIDs = append(catalogIDs, model.ID)
		if coreauth.EvaluateModelPolicy(cfg, a, providerKey, model.ID).Allowed {
			published = append(published, model)
		}
	}
	published = applyOAuthModelAliasForAuth(cfg, providerKey, a.AuthKind(), a.Attributes, published)
	published = applyOAuthSettingsForAuth(cfg, providerKey, a.AuthKind(), published)
	published = applyModelPrefixes(published, a.Prefix, cfg != nil && cfg.ForceModelPrefix)
	committed := false
	if len(published) == 0 {
		committed = fence.publishUnregister(s, a.ID)
	} else {
		committed = fence.publishModels(s, a.ID, providerKey, published)
	}
	if committed && s != nil && s.accountCatalogs != nil {
		s.accountCatalogs.recordIDs(a.ID, a.RegistrationEpoch, catalogIDs)
	}
	if !committed {
		log.WithField("provider", providerKey).Warnf("model registration for auth %s was superseded; discarded stale publication", a.ID)
	}
	return committed
}

func (s *Service) modelPolicyAllowsModel(a *coreauth.Auth, providerKey, modelID string) bool {
	if s == nil || a == nil {
		return true
	}
	return coreauth.EvaluateModelPolicy(s.currentConfig(), a, providerKey, modelID).Allowed
}

// unregisterModelsForAuthFenced removes a client's models only if fence
// still matches inherited global exclusions and the current auth snapshot, and
// the registry epoch has not advanced since the registration began. The
// returned value is false only when the fence rejected the removal.
func (s *Service) unregisterModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence) bool {
	if a == nil || a.ID == "" {
		return true
	}
	if !fence.publishUnregister(s, a.ID) {
		log.WithFields(log.Fields{"auth_id": a.ID}).Warnf("model unregistration for auth %s was superseded during computation; discarded the stale removal instead of deleting the latest auth state", a.ID)
		return false
	}
	return true
}

func (s *Service) pluginModelsForProvider(providerKey string) []*ModelInfo {
	if s == nil || s.pluginHost == nil {
		return nil
	}
	return s.pluginHost.ModelsForProvider(providerKey)
}

// appendPluginModels appends the provider's plugin static models that are not excluded.
// Callers filter native models before applying OAuth aliases, so plugin models are filtered
// here by their original IDs instead of re-filtering the merged list by alias names.
func (s *Service) appendPluginModels(providerKey string, models []*ModelInfo, excluded []string) []*ModelInfo {
	pluginModels := applyExcludedModels(s.pluginModelsForProvider(providerKey), excluded)
	if len(pluginModels) == 0 {
		return models
	}
	out := make([]*ModelInfo, 0, len(models)+len(pluginModels))
	seen := make(map[string]struct{}, len(models)+len(pluginModels))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID != "" {
			seen[modelID] = struct{}{}
		}
		out = append(out, model)
	}
	for _, model := range pluginModels {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, model)
	}
	return out
}

func (s *Service) tryRegisterPluginModelsForAuth(ctx context.Context, a *coreauth.Auth, provider, authKind string, excluded []string) (bool, error) {
	return s.tryRegisterPluginModelsForAuthWithConfig(ctx, s.currentConfig(), a, provider, authKind, excluded)
}

func (s *Service) tryRegisterPluginModelsForAuthWithConfig(ctx context.Context, cfg *config.Config, a *coreauth.Auth, provider, authKind string, excluded []string) (bool, error) {
	return s.tryRegisterPluginModelsForAuthWithConfigFenced(ctx, cfg, a, newModelRegistrationFence(a, cfg), provider, authKind, excluded)
}

func (s *Service) tryRegisterPluginModelsForAuthWithConfigFenced(ctx context.Context, cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, provider, authKind string, excluded []string) (bool, error) {
	if s == nil || s.pluginHost == nil || a == nil {
		return false, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return true, ctx.Err()
	}
	result := s.pluginHost.ModelsForAuth(ctx, a)
	if ctx != nil && ctx.Err() != nil {
		return true, ctx.Err()
	}
	if !result.Handled {
		return false, nil
	}
	if result.Err != nil {
		s.notePluginModelDiscoveryFailure(a, result.Err)
		return true, result.Err
	}
	if s.coreManager != nil && strings.EqualFold(a.Provider, "antigravity") && !s.antigravityHomeEnabled() {
		defer s.coreManager.ReconcileRegistryModelStates(ctx, a.ID)
	}
	activeAuth := a
	providerKey := strings.ToLower(strings.TrimSpace(result.Provider))
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	pluginAuthCommitted := false
	if result.Auth != nil && s.coreManager != nil {
		result.Auth.ID = a.ID
		if result.Auth.Provider == "" {
			result.Auth.Provider = a.Provider
		}
		if result.Auth.FileName == "" {
			result.Auth.FileName = a.FileName
		}
		if result.Auth.Attributes == nil {
			result.Auth.Attributes = make(map[string]string)
		}
		for key, value := range a.Attributes {
			if _, exists := result.Auth.Attributes[key]; !exists {
				result.Auth.Attributes[key] = value
			}
		}
		updated, committed, errUpdate := s.coreManager.UpdateIfRegistrationCurrent(ctx, a, result.Auth)
		if errUpdate != nil {
			// A real update failure (validation, persistence) is not a
			// fencing race: report it as handled without asking the
			// completion pass to re-invoke the plugin against the same
			// failing input.
			log.WithFields(log.Fields{"auth_id": a.ID, "provider": providerKey}).Warnf("plugin model registration for auth %s failed to commit its auth update: %v", a.ID, errUpdate)
			return true, errUpdate
		}
		if committed && updated != nil {
			activeAuth = updated.Clone()
			pluginAuthCommitted = true
		} else {
			// The plugin computation's starting identity was superseded
			// before its auth update could commit. Its models belong to
			// that old identity and must not be laundered into the new one.
			return true, errModelRegistrationSuperseded
		}
	}
	if activeAuth == nil {
		activeAuth = a
	}
	if activeProvider := strings.ToLower(strings.TrimSpace(activeAuth.Provider)); activeProvider != "" {
		providerKey = activeProvider
	}
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	activeAuthKind := activeAuth.AuthKind()
	activeExcluded := oauthExcludedModelsForConfig(cfg, providerKey, activeAuthKind)
	fallbackExclusionProvider := ""
	if a == activeAuth && len(activeExcluded) == 0 {
		activeExcluded = excluded
		fallbackExclusionProvider = strings.ToLower(strings.TrimSpace(provider))
	}
	// Credential-level exclusions take priority over the global oauth-excluded-models config.
	if activeAuth.Attributes != nil {
		if val, ok := activeAuth.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			activeExcluded = strings.Split(val, ",")
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return true, ctx.Err()
	}
	models := applyExcludedModels(result.Models, activeExcluded)
	s.clearPluginModelDiscoveryFailure(activeAuth.ID)
	publicationFence := fence
	if pluginAuthCommitted {
		// A plugin-returned auth update is a deliberate identity transition,
		// committed conditionally against the starting identity above.
		// Refresh only the auth inputs after that commit; retain the
		// starting exclusion summaries and registry epoch so newer global
		// rules or publications cannot be adopted by this older computation.
		publicationFence = fence.withAuth(activeAuth)
	}
	publicationFence.exclusionProvider = providerKey
	publicationFence.fallbackExclusionProvider = fallbackExclusionProvider
	if len(models) > 0 {
		return true, modelRegistrationResult(s.registerResolvedModelsForAuthWithConfigFenced(cfg, activeAuth, publicationFence, providerKey, models))
	}
	return true, modelRegistrationResult(s.unregisterModelsForAuthFenced(activeAuth, publicationFence))
}

// notePluginModelDiscoveryFailure records a retryable plugin model discovery
// failure for bounded automatic retry. Terminal credential rejections (401/403
// plugin errors) are never scheduled: the auth file did not change, so retrying
// would probe the vendor with a dead key until a manual refresh or restart.
func (s *Service) notePluginModelDiscoveryFailure(a *coreauth.Auth, err error) {
	if s == nil || s.pluginModelRetry == nil || a == nil || a.ID == "" {
		return
	}
	// 调用方取消或超时不能证明凭据已失效，保留此前排队的恢复尝试。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if !pluginModelDiscoveryRetryable(err) {
		s.pluginModelRetry.clear(a.ID)
		return
	}
	s.pluginModelRetry.schedule(a.ID)
}

func (s *Service) clearPluginModelDiscoveryFailure(authID string) {
	if s == nil || s.pluginModelRetry == nil {
		return
	}
	s.pluginModelRetry.clear(authID)
}
