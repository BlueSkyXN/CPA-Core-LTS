package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	log "github.com/sirupsen/logrus"
)

type autoRouteKey struct{}

type autoAccount struct {
	plugin  bool
	targets []string
}

// 一次请求冻结公开名及其获准的账号、真实模型。重试不能重新解释原始 auto。
type autoRoute struct {
	public   string // exact ID registered by the credential (may itself contain a suffix)
	model    string
	explicit bool
	global   bool
	accounts map[string]autoAccount
}

func autoRouteFrom(ctx context.Context) *autoRoute {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(autoRouteKey{}).(*autoRoute)
	return r
}

func bareAutoPrefix(cfg *config.Config, prefix string) bool {
	return !cfg.ForceModelPrefix || prefix == "" || prefix == "auto"
}

// 即使绑定的 key 暂时没有注册，配置仍保留别名所有权。
func (m *Manager) hasExplicitAutoLocked(cfg *config.Config) bool {
	for _, x := range cfg.OpenAICompatibility {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.GeminiKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.InteractionsKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.CodexKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.XAIKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.MetaKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.ClaudeKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for _, x := range cfg.VertexCompatAPIKey {
		if bareAutoPrefix(cfg, x.Prefix) && config.HasAutoAlias(x.Models) {
			return true
		}
	}
	for channel, aliases := range cfg.OAuthModelAlias {
		// A channel with only prefixed credentials does not own bare auto.
		prefixedOnly := false
		if cfg.ForceModelPrefix {
			for _, credential := range m.auths {
				if credential != nil && modelAliasChannel(credential) == channel {
					prefixedOnly = !bareAutoPrefix(cfg, credential.Prefix)
					if !prefixedOnly {
						break
					}
				}
			}
		}
		if !prefixedOnly {
			for _, alias := range aliases {
				if strings.EqualFold(alias.Alias, "auto") && alias.Name != "" && !strings.EqualFold(alias.Name, "auto") {
					return true
				}
			}
		}
	}
	for _, a := range m.auths {
		if a != nil && bareAutoPrefix(cfg, a.Prefix) && m.ownsAutoAlias(a) {
			return true
		}
	}
	return false
}

func (m *Manager) ownsAutoAlias(a *Auth) bool {
	mapped := m.resolveExecutionAliasResult(a, "auto").UpstreamModel
	return mapped != "" && !strings.EqualFold(mapped, "auto")
}

func pluginAllowsAuto(cfg *config.Config, a *Auth) bool {
	if IsConfigAPIKeyAuth(a) {
		return true
	}
	if !EvaluateModelPolicy(cfg, a, a.Provider, "auto").Allowed {
		return false
	}
	channel := channelModelPolicyForProvider(cfg, a.Provider)
	account := parseModelPolicyAttribute(a.Attributes)
	return !(channelPolicyListActive(channel.Allowlist) || account.Allowlist.active()) ||
		registry.GetGlobalRegistry().ClientSupportsModel(a.ID, "auto")
}

type autoCredential struct {
	auth   *Auth
	plugin bool
}

func (m *Manager) autoCredentials(cfg *config.Config) (explicit bool, global bool, credentials []autoCredential) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	explicit = m.hasExplicitAutoLocked(cfg)
	global = !explicit && cfg.Routing.AutoModel.Enabled
	weighted := UnwrapWeightedRoundRobinSelector(m.selector) != nil
	reg := registry.GetGlobalRegistry()
	for _, a := range m.auths {
		if a == nil || a.Disabled || a.Status == StatusDisabled || weighted && authWeight(a) <= 0 {
			continue
		}
		if !global && !reg.ClientSupportsModel(a.ID, "auto") || explicit && !m.ownsAutoAlias(a) {
			continue
		}
		executor, ok := m.executorLocked(executorKeyFromAuth(a))
		if !ok {
			continue
		}
		_, plugin := executor.(interface{ PluginManagedCredentials() })
		credentials = append(credentials, autoCredential{a.Clone(), plugin || IsPluginVirtualAuth(a)})
	}
	return
}

func (m *Manager) availableAutoTargets(ctx context.Context, cfg *config.Config, a *Auth, model string) []string {
	targets, pooled, _, _ := m.resolveExecutionModelCandidates(a, model, false)
	usable := m.filterExecutionModelsForRequest(ctx, a, model, targets, pooled)
	return slices.DeleteFunc(usable, func(target string) bool {
		return !EvaluateModelPolicy(cfg, a, a.Provider, thinking.ParseSuffix(target).ModelName).Allowed
	})
}

// ResolveAutoRoute resolves standalone lowercase auto and binds the allowed dispatch
// targets to the returned context. It never advances a selection or model-pool cursor.
func (m *Manager) ResolveAutoRoute(ctx context.Context, name string, accepts func(id, modelType string, methods []string) bool) (context.Context, string, error) {
	parsed := thinking.ParseSuffix(name)
	if m == nil || parsed.ModelName != "auto" || m.HomeEnabled() {
		return ctx, name, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.CheckResourceModelAccess(ctx, name); err != nil {
		return ctx, "", err
	}
	if r := autoRouteFrom(ctx); r != nil {
		return ctx, r.model, nil
	}
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		cfg = &config.Config{}
	}
	access := m.resourceAccess(ctx)
	explicit, global, credentials := m.autoCredentials(cfg)
	order := make(map[string]int)
	if global {
		for i, id := range cfg.Routing.AutoModel.Candidates {
			if _, exists := order[id]; !exists {
				order[id] = i
			}
		}
	} else {
		order["auto"] = 0
	}

	catalog := registry.GetGlobalRegistry()
	routes := make(map[string]*autoRoute)
	var best string
	var bestRank int
	var newest int64
	denied := false
	published := false
	for _, credential := range credentials {
		a := credential.auth
		acceptsPluginAuto := !global || pluginAllowsAuto(cfg, a)
		for _, info := range catalog.GetModelsForClient(a.ID) {
			plugin := credential.plugin || info.PluginCatalog
			if plugin && !acceptsPluginAuto || explicit && !info.ConfiguredAlias {
				continue
			}
			id := info.ID
			rank, named := order[id]
			if explicit && id != "auto" && strings.EqualFold(id, "auto") {
				rank, named = 1, true
			}
			if len(order) > 0 && !named {
				continue
			}
			if global && accepts != nil && !accepts(info.ID, info.Type, info.SupportedGenerationMethods) {
				continue
			}
			published = true
			if !m.allowsResourceRoute(access, a, id) {
				denied = true
				continue
			}
			if !catalog.ClientCanServeModel(a.ID, id, explicit) {
				continue
			}
			model := preserveRequestedModelSuffix(name, id)
			usable := m.availableAutoTargets(ctx, cfg, a, model)
			if len(usable) == 0 {
				continue
			}
			if routes[id] == nil {
				routes[id] = &autoRoute{public: id, model: model, explicit: explicit, global: global, accounts: make(map[string]autoAccount)}
			}
			routes[id].accounts[a.ID] = autoAccount{plugin: plugin, targets: usable}
			created := max(info.Created, 0)
			if best == "" || (named && (rank < bestRank || rank == bestRank && id < best)) || (!named && (created > newest || created == newest && id < best)) {
				best, bestRank, newest = id, rank, created
			}
		}
	}
	if best != "" {
		r := routes[best]
		log.WithField("explicit_alias", explicit).Infof("Resolved 'auto' model to: %s", r.model)
		return context.WithValue(ctx, autoRouteKey{}, r), r.model, nil
	}
	if denied {
		return ctx, "", ErrResourceAccessDenied
	}
	if !global && !explicit && !published {
		return ctx, "", &Error{Code: "model_not_found", HTTPStatus: http.StatusBadRequest, Message: "No route publishes model auto"}
	}
	return ctx, "", &Error{Code: "auth_unavailable", HTTPStatus: http.StatusServiceUnavailable, Message: "No available account for the configured auto route"}
}

// CheckAutoRouteAccount checks membership without contacting an executor or vendor.
func (m *Manager) CheckAutoRouteAccount(ctx context.Context, a *Auth) bool {
	r := autoRouteFrom(ctx)
	if r == nil {
		return true
	}
	if a == nil || a.Disabled || a.Status == StatusDisabled {
		return false
	}
	owner, ok := r.accounts[a.ID]
	if !ok {
		return false
	}
	cfg := m.runtimeConfigSnapshot()
	public := r.public
	if r.global && (cfg == nil || !cfg.Routing.AutoModel.Enabled || len(cfg.Routing.AutoModel.Candidates) > 0 && !slices.Contains(cfg.Routing.AutoModel.Candidates, public)) {
		return false
	}
	catalog := registry.GetGlobalRegistry()
	if !catalog.ClientCanServeModel(a.ID, public, r.explicit) {
		return false
	}
	if r.explicit {
		return m.ownsAutoAlias(a)
	}
	return !owner.plugin || pluginAllowsAuto(cfg, a)
}

func (m *Manager) checkAutoDispatch(ctx context.Context, a *Auth, actual string) error {
	r := autoRouteFrom(ctx)
	if r == nil {
		return nil
	}
	current, ok := m.GetByID(a.ID)
	access := m.resourceAccess(ctx)
	if ok && (!access.allowsModel("auto") || !access.allowsModel(actual) || !m.allowsResourceRoute(access, current, r.model)) {
		return ErrResourceAccessDenied
	}
	if ok && m.CheckAutoRouteAccount(ctx, current) && slices.Contains(r.accounts[a.ID].targets, actual) {
		targets, _, _, _ := m.resolveExecutionModelCandidates(current, r.model, false)
		if slices.Contains(targets, actual) && EvaluateModelPolicy(m.runtimeConfigSnapshot(), current, current.Provider, thinking.ParseSuffix(actual).ModelName).Allowed {
			return nil
		}
	}
	return wrapRequestStopError(&Error{Code: "auth_unavailable", HTTPStatus: http.StatusServiceUnavailable, Message: "The selected auto route is no longer available"})
}
