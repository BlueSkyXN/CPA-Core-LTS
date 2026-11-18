package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type resourceAccessDenied struct{}

func (resourceAccessDenied) Error() string       { return "This API key cannot use the requested resource." }
func (resourceAccessDenied) ErrorCode() string   { return "resource_access_denied" }
func (resourceAccessDenied) StatusCode() int     { return http.StatusForbidden }
func (resourceAccessDenied) IsRequestStop() bool { return true }

// ErrResourceAccessDenied is a local authorization error before upstream execution.
var ErrResourceAccessDenied error = resourceAccessDenied{}

type resourceAccess struct {
	rules config.ResourceAllowlists
	scope string
}

// ResourceAllowlistsEnabled reports whether this Manager has native caller-key restrictions.
func (m *Manager) ResourceAllowlistsEnabled() bool {
	cfg := m.runtimeConfigSnapshot()
	return cfg != nil && cfg.ResourceAllowlists.Enabled()
}

func (m *Manager) resourceAccess(ctx context.Context) resourceAccess {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || (!cfg.ResourceAllowlists.Enabled() && !cfg.ResourceAllowlists.TracksConfiguredUpstreamAPIKeys()) {
		return resourceAccess{}
	}
	access := resourceAccess{rules: *cfg.ResourceAllowlists}
	// Use the principal authenticated by Core instead of the request headers or executor metadata.
	if access.rules.Enabled() && coreusage.IsNativeAccessProvider(coreusage.AccessProviderFromContext(ctx)) {
		access.scope = coresession.CallerScope(coreusage.APIKeyFromContext(ctx))
	}
	return access
}

func (a resourceAccess) hasRestrictions() bool {
	return a.rules.Enabled() || a.rules.TracksConfiguredUpstreamAPIKeys()
}

func (a resourceAccess) allows(rules map[string][]string, name string) bool {
	refs, reserved := rules[strings.ToLower(strings.TrimSpace(name))]
	return !reserved || (a.scope != "" && slices.Contains(refs, a.scope))
}

func (a resourceAccess) allowsAll(rules map[string][]string) bool {
	for _, refs := range rules {
		if !slices.Contains(refs, a.scope) {
			return false
		}
	}
	return true
}

func (a resourceAccess) allowsModel(model string) bool {
	if len(a.rules.Models) == 0 {
		return true
	}
	return a.allows(a.rules.Models, model) && a.allows(a.rules.Models, thinking.ParseSuffix(model).ModelName)
}

func (a resourceAccess) allowsRoutedModel(credential *Auth, model string) bool {
	return a.allowsModel(model) && a.allowsModel(rewriteModelForAuth(resourceModelWithCanonicalPrefix(credential, model), credential))
}

func (a resourceAccess) allowsAuth(credential *Auth) bool {
	if credential == nil || (len(a.rules.AuthFiles) == 0 && !a.rules.TracksConfiguredUpstreamAPIKeys()) {
		return true
	}
	if credential.AuthSourceKind() == AuthSourceConfig {
		return a.rules.AllowsConfiguredUpstreamAPIKey(credential.Attributes[AttributeAPIKey], a.scope)
	}
	if len(a.rules.AuthFiles) == 0 {
		return true
	}
	name := credential.Attributes[AttributeVirtualSource]
	if name == "" {
		name = credential.FileSnapshotPath()
	}
	if name == "" {
		name = credential.FileName
	}
	if name == "" {
		name = credential.ID
	}
	return a.allows(a.rules.AuthFiles, filepath.Base(name))
}

// CheckResourceModelAccess authorizes a client-visible model name before routing.
func (m *Manager) CheckResourceModelAccess(ctx context.Context, model string) error {
	if !m.resourceAccess(ctx).allowsModel(model) {
		return ErrResourceAccessDenied
	}
	return nil
}

// CheckUnresolvedResourceAccess protects direct forwarding paths that cannot
// identify the upstream model or credential before sending a request.
func (m *Manager) CheckUnresolvedResourceAccess(ctx context.Context) error {
	access := m.resourceAccess(ctx)
	if !access.allowsAll(access.rules.AuthFiles) || !access.allowsAll(access.rules.UpstreamAPIKeys) || !access.allowsAll(access.rules.Models) {
		return ErrResourceAccessDenied
	}
	return nil
}

// CheckUnresolvedModelAccess authorizes forwarding to an unspecified model.
func (m *Manager) CheckUnresolvedModelAccess(ctx context.Context) error {
	access := m.resourceAccess(ctx)
	if !access.allowsAll(access.rules.Models) {
		return ErrResourceAccessDenied
	}
	return nil
}

func (m *Manager) resourceAuthFilter(ctx context.Context, model string) func(*Auth) bool {
	access := m.resourceAccess(ctx)
	if !access.hasRestrictions() {
		return nil
	}
	return func(credential *Auth) bool {
		return m.allowsResourceRoute(access, credential, model)
	}
}

func (m *Manager) allowsResourceRoute(access resourceAccess, credential *Auth, model string) bool {
	if !access.allowsAuth(credential) {
		return false
	}
	if len(access.rules.Models) == 0 || model == "" {
		return true
	}
	if !access.allowsRoutedModel(credential, model) {
		return false
	}
	targets, _, _, _ := m.resolveExecutionModelCandidates(credential, resourceModelWithCanonicalPrefix(credential, model), false)
	return slices.ContainsFunc(targets, access.allowsModel)
}

func resourceModelWithCanonicalPrefix(credential *Auth, model string) string {
	if credential == nil {
		return model
	}
	prefix := strings.TrimSpace(credential.Prefix)
	if before, after, found := strings.Cut(model, "/"); found && prefix != "" && strings.EqualFold(before, prefix) {
		return prefix + "/" + after
	}
	return model
}

func (m *Manager) checkResourceExecution(ctx context.Context, credential *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	access := m.resourceAccess(ctx)
	if !access.hasRestrictions() {
		return nil
	}
	requested := requestedModelAliasFromOptions(opts, req.Model)
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	if !access.allowsAuth(credential) || !access.allowsModel(req.Model) ||
		!access.allowsRoutedModel(credential, requested) || !access.allowsRoutedModel(credential, routeModel) {
		return ErrResourceAccessDenied
	}
	if len(access.rules.Models) != 0 {
		if !access.allowsModel(ojson.GetBytes(req.Payload, "model").String()) {
			return ErrResourceAccessDenied
		}
		// Responses image generation may dispatch a second model named in the tool payload.
		for _, tool := range ojson.GetBytes(req.Payload, "tools").Array() {
			if tool.Get("type").String() == "image_generation" && !access.allowsModel(tool.Get("model").String()) {
				return ErrResourceAccessDenied
			}
		}
	}
	return nil
}

// CheckSelectedResourceAccess rechecks a target for a direct Core transport.
func (m *Manager) CheckSelectedResourceAccess(ctx context.Context, credential *Auth, model string) error {
	if !m.allowsResourceRoute(m.resourceAccess(ctx), credential, model) {
		return ErrResourceAccessDenied
	}
	return nil
}

// ResourceModelVisibility returns a predicate for the caller's registered model catalog.
// The predicate uses a single policy snapshot and does not advance the routing cursors.
func (m *Manager) ResourceModelVisibility(ctx context.Context, candidates []string) func(string) bool {
	access := m.resourceAccess(ctx)
	if !access.rules.Enabled() {
		return nil
	}
	requested := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		if access.allowsModel(id) {
			requested[strings.ToLower(id)] = struct{}{}
		}
	}
	visible := make(map[string]struct{}, len(requested))
	m.mu.RLock()
	defer m.mu.RUnlock()
	registrations := registry.GetGlobalRegistry()
	for _, credential := range m.auths {
		if credential == nil || credential.Disabled || credential.Status == StatusDisabled || !access.allowsAuth(credential) {
			continue
		}
		for _, id := range registrations.GetModelIDsForClient(credential.ID) {
			key := strings.ToLower(id)
			if _, wanted := requested[key]; !wanted {
				continue
			}
			if m.allowsResourceRoute(access, credential, id) {
				visible[key] = struct{}{}
				delete(requested, key)
			}
		}
		if len(requested) == 0 {
			break
		}
	}
	return func(id string) bool {
		_, allowed := visible[strings.ToLower(id)]
		return allowed
	}
}
