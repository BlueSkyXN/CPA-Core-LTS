package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const pluginExecutionLifecycleTimeout = 5 * time.Second

type executorManager interface {
	Executor(provider string) (coreauth.ProviderExecutor, bool)
	RegisterExecutor(coreauth.ProviderExecutor)
	UnregisterExecutor(provider string)
}

type executorRegistration struct {
	provider string
	adapter  *executorAdapter
}

func (h *Host) RegisterExecutors(manager executorManager, modelRegistry modelProviderRegistry) {
	if h == nil || manager == nil {
		return
	}

	snap := h.Snapshot()
	records := h.activeRecordsFromSnapshot(snap)
	registrations := h.snapshotModelRegistrations()
	selectedModels := make(map[string][]*registry.ModelInfo)
	providerModels := make(map[string][]*registry.ModelInfo)
	claimedModels := make(map[string]struct{})
	claimedProviders := make(map[string]string)
	for _, registration := range registrations {
		if !registration.hasExecutor {
			appendModelsForProvider(providerModels, registration.provider, registration.models)
		}
	}
	for _, record := range records {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}
		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			continue
		}
		registration := h.modelRegistration(record.id)
		if h.providerHasNativeExecutor(manager, provider) {
			appendModelsForProvider(providerModels, provider, registration.models)
			continue
		}
		if len(registration.models) == 0 {
			continue
		}
		if owner := claimedProviders[provider]; owner != "" && owner != record.id {
			continue
		}
		for _, model := range registration.models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				continue
			}
			if _, claimed := claimedModels[modelID]; claimed {
				continue
			}
			if h.modelHasNativeExecutor(manager, modelRegistry, modelID) {
				continue
			}
			claimedModels[modelID] = struct{}{}
			claimedProviders[provider] = record.id
			selectedModels[record.id] = append(selectedModels[record.id], model)
		}
	}

	seenProviders := make(map[string]struct{})
	nextProviders := make(map[string]struct{})
	nextModelClients := make(map[string]struct{})
	executorRegistrations := make([]executorRegistration, 0)
	modelClientRegistrations := make([]modelClientRegistration, 0)
	for _, record := range records {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}

		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			continue
		}
		registration := h.modelRegistration(record.id)
		if len(registration.models) > 0 && len(selectedModels[record.id]) == 0 {
			continue
		}
		if _, seenProvider := seenProviders[provider]; seenProvider {
			continue
		}
		seenProviders[provider] = struct{}{}
		if h.providerHasNativeExecutor(manager, provider) {
			continue
		}

		nextProviders[provider] = struct{}{}
		executorRegistrations = append(executorRegistrations, newExecutorAdapterRegistration(h, record, provider, executor))
		appendModelsForProvider(providerModels, provider, selectedModels[record.id])
		if len(selectedModels[record.id]) > 0 {
			clientID := pluginExecutorModelClientID(record.id, provider)
			modelClientRegistrations = append(modelClientRegistrations, modelClientRegistration{
				clientID: clientID,
				provider: provider,
				models:   selectedModels[record.id],
			})
			nextModelClients[clientID] = struct{}{}
		}
	}
	h.commitExecutorState(snap, manager, modelRegistry, providerModels, executorRegistrations, nextProviders, modelClientRegistrations, nextModelClients)
}

func pluginExecutorModelClientID(pluginID, provider string) string {
	return "plugin:" + pluginID + ":" + provider + ":executor"
}

func (h *Host) commitExecutorState(snap *Snapshot, manager executorManager, modelRegistry modelRegistry, providerModels map[string][]*registry.ModelInfo, registrations []executorRegistration, nextProviders map[string]struct{}, modelClientRegistrations []modelClientRegistration, nextModelClients map[string]struct{}) {
	if h == nil || manager == nil {
		return
	}

	h.executorCommitMu.Lock()
	defer h.executorCommitMu.Unlock()

	h.mu.Lock()
	if h.Snapshot() != snap {
		h.mu.Unlock()
		return
	}

	h.providerModels = make(map[string][]*registryModelInfo, len(providerModels))
	for provider, models := range providerModels {
		h.providerModels[provider] = cloneRegistryModels(models)
	}

	staleProviders := make([]string, 0)
	for provider := range h.executorProviders {
		if _, okProvider := nextProviders[provider]; !okProvider {
			staleProviders = append(staleProviders, provider)
		}
	}
	h.executorProviders = nextProviders
	if nextModelClients == nil {
		nextModelClients = make(map[string]struct{})
	}
	staleModelClients := make([]string, 0)
	for clientID := range h.executorModelClientIDs {
		if _, okClient := nextModelClients[clientID]; !okClient {
			staleModelClients = append(staleModelClients, clientID)
		}
	}
	h.executorModelClientIDs = nextModelClients
	h.mu.Unlock()

	for _, registration := range registrations {
		if registration.adapter == nil || registration.provider == "" {
			continue
		}
		manager.RegisterExecutor(registration.adapter)
	}
	for _, provider := range staleProviders {
		existing, okExecutor := manager.Executor(provider)
		if !okExecutor || !h.ownsExecutor(existing) {
			continue
		}
		manager.UnregisterExecutor(provider)
	}

	if modelRegistry == nil {
		return
	}
	for _, registration := range modelClientRegistrations {
		modelRegistry.RegisterClient(registration.clientID, registration.provider, registration.models)
	}
	for _, clientID := range staleModelClients {
		modelRegistry.UnregisterClient(clientID)
	}
}

func newExecutorAdapterRegistration(h *Host, record capabilityRecord, provider string, executor pluginapi.ProviderExecutor) executorRegistration {
	return executorRegistration{
		provider: provider,
		adapter: &executorAdapter{
			host:          h,
			pluginID:      record.id,
			path:          record.path,
			version:       record.version,
			provider:      provider,
			executor:      executor,
			canceller:     record.plugin.Capabilities.ExecutionCanceller,
			sessionCloser: record.plugin.Capabilities.ExecutionSessionCloser,
			readiness:     record.plugin.Capabilities.ProviderReadiness,
			inputFormats:  normalizeExecutorFormats(record.plugin.Capabilities.ExecutorInputFormats),
			outputFormats: normalizeExecutorFormats(record.plugin.Capabilities.ExecutorOutputFormats),
		},
	}
}

func (h *Host) snapshotModelRegistrations() []pluginModelRegistration {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	registrations := make([]pluginModelRegistration, 0, len(h.modelRegistrations))
	for _, registration := range h.modelRegistrations {
		registration.models = cloneRegistryModels(registration.models)
		registrations = append(registrations, registration)
	}
	sort.SliceStable(registrations, func(i, j int) bool {
		if registrations[i].priority == registrations[j].priority {
			return registrations[i].pluginID < registrations[j].pluginID
		}
		return registrations[i].priority > registrations[j].priority
	})
	return registrations
}

func (h *Host) modelRegistration(pluginID string) pluginModelRegistration {
	if h == nil {
		return pluginModelRegistration{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	registration := h.modelRegistrations[pluginID]
	registration.models = cloneRegistryModels(registration.models)
	return registration
}

func (h *Host) executorProvider(record capabilityRecord, executor pluginapi.ProviderExecutor) (string, bool) {
	if h == nil || !h.recordCurrent(record) {
		return "", false
	}
	provider := h.modelProvider(record.id)
	if provider == "" {
		identifier, okIdentifier := h.callExecutorIdentifier(record.id, executor)
		if !okIdentifier {
			return "", false
		}
		provider = identifier
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider, provider != ""
}

func (h *Host) callExecutorIdentifier(pluginID string, executor pluginapi.ProviderExecutor) (provider string, ok bool) {
	if h == nil || executor == nil || h.isPluginFused(pluginID) {
		return "", false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "Executor.Identifier", recovered)
			provider = ""
			ok = false
		}
	}()
	return executor.Identifier(), true
}

func (h *Host) providerHasNativeExecutor(manager executorManager, provider string) bool {
	if h == nil || manager == nil {
		return false
	}
	existing, okExecutor := manager.Executor(provider)
	return okExecutor && existing != nil && !h.ownsExecutor(existing)
}

func (h *Host) modelHasNativeExecutor(manager executorManager, modelRegistry modelProviderRegistry, modelID string) bool {
	if h == nil || manager == nil || modelRegistry == nil {
		return false
	}
	for _, provider := range modelRegistry.GetModelProviders(modelID) {
		if h.providerHasNativeExecutor(manager, provider) {
			return true
		}
	}
	return false
}

func appendModelsForProvider(out map[string][]*registry.ModelInfo, provider string, models []*registry.ModelInfo) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(models) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(out[provider])+len(models))
	for _, model := range out[provider] {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			seen[strings.TrimSpace(model.ID)] = struct{}{}
		}
	}
	for _, model := range models {
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
		out[provider] = append(out[provider], cloneRegistryModels([]*registry.ModelInfo{model})...)
	}
}

func (h *Host) ModelsForProvider(provider string) []*registry.ModelInfo {
	if h == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneRegistryModels(h.providerModels[provider])
}

func (h *Host) HasExecutorCandidateProvider(provider string) bool {
	if h == nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	for _, record := range h.activeRecords() {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}
		candidate, okCandidate := h.executorProvider(record, executor)
		if okCandidate && candidate == provider {
			return true
		}
	}
	return false
}

// ProbeProviderReadiness probes the active plugin executor for provider. A
// loaded legacy plugin without the readiness capability remains fail-closed.
func (h *Host) ProbeProviderReadiness(ctx context.Context, provider string, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
	if h == nil {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin host is unavailable")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("provider is required")
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin auth manager is unavailable")
	}
	executor, okExecutor := manager.Executor(provider)
	if !okExecutor || executor == nil {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin executor %s is not registered", provider)
	}
	adapter, okAdapter := executor.(*executorAdapter)
	if !okAdapter || adapter == nil || adapter.host != h {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("provider %s is not owned by the plugin host", provider)
	}
	if adapter.readiness == nil {
		return legacyProviderReadiness(provider, adapter.version), nil
	}
	if req.Purpose == "" {
		req.Purpose = pluginapi.ReadinessPurposeDiagnostic
	}
	if errAuth := h.enrichReadinessAuth(provider, &req); errAuth != nil {
		return pluginapi.ReadinessResponse{}, errAuth
	}
	return adapter.ProbeReadiness(ctx, req)
}

func (h *Host) enrichReadinessAuth(provider string, req *pluginapi.ReadinessRequest) error {
	if h == nil || req == nil {
		return nil
	}
	authID := strings.TrimSpace(req.AuthID)
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authID == "" && authIndex == "" {
		req.AuthProvider = ""
		req.StorageJSON = nil
		req.AuthMetadata = nil
		req.AuthAttributes = nil
		return nil
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return fmt.Errorf("plugin auth manager is unavailable")
	}
	var selected *coreauth.Auth
	if authID != "" {
		selected, _ = manager.GetByID(authID)
	} else {
		for _, candidate := range manager.List() {
			if candidate != nil && strings.TrimSpace(candidate.Index) == authIndex {
				selected = candidate
				break
			}
		}
	}
	if selected == nil {
		return fmt.Errorf("provider %s readiness auth was not found", provider)
	}
	if authID != "" && strings.TrimSpace(selected.ID) != authID {
		return fmt.Errorf("provider %s readiness auth id does not match the selected auth", provider)
	}
	if authIndex != "" && strings.TrimSpace(selected.Index) != authIndex {
		return fmt.Errorf("provider %s readiness auth index does not match the selected auth", provider)
	}
	if selectedProvider := strings.ToLower(strings.TrimSpace(selected.Provider)); selectedProvider != provider {
		return fmt.Errorf("readiness auth provider %s does not match executor %s", selectedProvider, provider)
	}
	req.AuthID = strings.TrimSpace(selected.ID)
	req.AuthIndex = strings.TrimSpace(selected.Index)
	req.AuthProvider = authProvider(selected)
	req.StorageJSON = storageJSONFromAuth(selected)
	req.AuthMetadata = cloneAnyMap(authMetadata(selected))
	req.AuthAttributes = authAttributes(selected)
	return nil
}

func legacyProviderReadiness(provider, pluginVersion string) pluginapi.ReadinessResponse {
	return pluginapi.ReadinessResponse{
		Provider: provider,
		Ready:    false,
		Checks: []pluginapi.ReadinessCheck{
			{Level: pluginapi.ReadinessLevelPluginInstalled, State: pluginapi.ReadinessStateReady, Version: pluginVersion},
			{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateUnsupported},
			{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateUnsupported},
			{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateUnsupported},
			{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnsupported},
		},
	}
}

// CancelProviderExecution routes an explicit control-plane cancellation to the
// active plugin executor without closing the containing session.
func (h *Host) CancelProviderExecution(ctx context.Context, provider string, req pluginapi.CancelExecutionRequest) error {
	if h == nil {
		return fmt.Errorf("plugin host is unavailable")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return fmt.Errorf("plugin auth manager is unavailable")
	}
	executor, okExecutor := manager.Executor(provider)
	if !okExecutor || executor == nil {
		return fmt.Errorf("plugin executor %s is not registered", provider)
	}
	adapter, okAdapter := executor.(*executorAdapter)
	if !okAdapter || adapter == nil || adapter.host != h {
		return fmt.Errorf("provider %s is not owned by the plugin host", provider)
	}
	if requestedProvider := strings.ToLower(strings.TrimSpace(req.Provider)); requestedProvider != "" && requestedProvider != provider {
		return fmt.Errorf("cancel provider %s does not match executor %s", requestedProvider, provider)
	}
	req.Provider = provider
	if req.Reason == "" {
		req.Reason = pluginapi.ExecutionCancelReasonExplicit
	}
	return adapter.CancelExecution(ctx, req)
}

// OwnsExecutor reports whether executor is an adapter managed by this host.
func (h *Host) OwnsExecutor(executor coreauth.ProviderExecutor) bool {
	return h.ownsExecutor(executor)
}

func (h *Host) ownsExecutor(executor coreauth.ProviderExecutor) bool {
	adapter, okAdapter := executor.(*executorAdapter)
	return okAdapter && adapter != nil && adapter.host == h
}

func (h *Host) modelProvider(pluginID string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modelProviders[pluginID]
}

type executorAdapter struct {
	host          *Host
	pluginID      string
	path          string
	version       string
	provider      string
	executor      pluginapi.ProviderExecutor
	canceller     pluginapi.ExecutionCanceller
	sessionCloser pluginapi.ExecutionSessionCloser
	readiness     pluginapi.ProviderReadiness
	inputFormats  []sdktranslator.Format
	outputFormats []sdktranslator.Format
}

func (a *executorAdapter) Identifier() string {
	if a == nil {
		return ""
	}
	return a.provider
}

func (a *executorAdapter) CompatibleExecutorReplacement(next coreauth.ProviderExecutor) bool {
	nextAdapter, ok := next.(*executorAdapter)
	return ok && a != nil && nextAdapter != nil &&
		a.host == nextAdapter.host &&
		a.pluginID == nextAdapter.pluginID &&
		a.path == nextAdapter.path &&
		a.version == nextAdapter.version &&
		a.provider == nextAdapter.provider
}

func (a *executorAdapter) CloseExecutionSession(sessionID string) {
	a.CloseExecutionSessionScoped(sessionID, "", "")
}

func (a *executorAdapter) CloseExecutionSessionScoped(sessionID, callerScope, workspaceIdentity string) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	req := pluginapi.CloseExecutionSessionRequest{
		Scope:              pluginapi.ExecutionSessionCloseScopeSession,
		ExecutionSessionID: strings.TrimSpace(sessionID),
		CallerScope:        strings.TrimSpace(callerScope),
		WorkspaceIdentity:  strings.TrimSpace(workspaceIdentity),
		Provider:           a.provider,
	}
	if req.ExecutionSessionID == coreauth.CloseAllExecutionSessionsID {
		req.Scope = pluginapi.ExecutionSessionCloseScopeProvider
		req.ExecutionSessionID = ""
	}
	a.closeExecutionSessions(req)
}

func (a *executorAdapter) CloseExecutionSessionsForAuth(authID, authIndex string) {
	if a == nil || (strings.TrimSpace(authID) == "" && strings.TrimSpace(authIndex) == "") {
		return
	}
	a.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{
		Scope:     pluginapi.ExecutionSessionCloseScopeAuth,
		Provider:  a.provider,
		AuthID:    strings.TrimSpace(authID),
		AuthIndex: strings.TrimSpace(authIndex),
	})
}

func (a *executorAdapter) closeExecutionSessions(req pluginapi.CloseExecutionSessionRequest) {
	if a == nil || a.sessionCloser == nil || a.host == nil {
		return
	}
	if !validCloseExecutionSessionRequest(req) {
		log.WithField("plugin", a.pluginID).Warn("ignored invalid plugin execution session close request")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginExecutionLifecycleTimeout)
	defer cancel()
	result := make(chan pluginLifecycleCallResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- pluginLifecycleCallResult{recovered: recovered}
			}
		}()
		result <- pluginLifecycleCallResult{err: a.sessionCloser.CloseExecutionSession(ctx, req)}
	}()
	select {
	case callResult := <-result:
		if callResult.recovered != nil {
			a.fuseCurrentPlugin("ExecutionSessionCloser.CloseExecutionSession", callResult.recovered)
			return
		}
		if callResult.err != nil {
			log.WithError(callResult.err).WithField("plugin", a.pluginID).Warn("plugin execution session close failed")
		}
	case <-ctx.Done():
		log.WithError(ctx.Err()).WithField("plugin", a.pluginID).Warn("plugin execution session close timed out")
	}
}

func validCloseExecutionSessionRequest(req pluginapi.CloseExecutionSessionRequest) bool {
	switch req.Scope {
	case pluginapi.ExecutionSessionCloseScopeSession:
		return strings.TrimSpace(req.ExecutionSessionID) != ""
	case pluginapi.ExecutionSessionCloseScopeAuth:
		return strings.TrimSpace(req.ExecutionSessionID) == "" && strings.TrimSpace(req.CallerScope) == "" && strings.TrimSpace(req.WorkspaceIdentity) == "" && (strings.TrimSpace(req.AuthID) != "" || strings.TrimSpace(req.AuthIndex) != "")
	case pluginapi.ExecutionSessionCloseScopeProvider:
		return strings.TrimSpace(req.ExecutionSessionID) == "" && strings.TrimSpace(req.CallerScope) == "" && strings.TrimSpace(req.WorkspaceIdentity) == "" && strings.TrimSpace(req.AuthID) == "" && strings.TrimSpace(req.AuthIndex) == "" && strings.TrimSpace(req.Provider) != ""
	default:
		return false
	}
}

type pluginLifecycleCallResult struct {
	err       error
	recovered any
}

func (a *executorAdapter) cancelExecutionAsync(req pluginapi.ExecutorRequest, reason pluginapi.ExecutionCancelReason) {
	if a == nil || a.canceller == nil || a.host == nil || (strings.TrimSpace(req.RequestID) == "" && strings.TrimSpace(req.ExecutionSessionID) == "") {
		return
	}
	cancelReq := pluginapi.CancelExecutionRequest{
		RequestID:          strings.TrimSpace(req.RequestID),
		ExecutionSessionID: strings.TrimSpace(req.ExecutionSessionID),
		CallerScope:        strings.TrimSpace(req.CallerScope),
		WorkspaceIdentity:  strings.TrimSpace(req.WorkspaceIdentity),
		Provider:           a.provider,
		AuthID:             strings.TrimSpace(req.AuthID),
		AuthIndex:          strings.TrimSpace(req.AuthIndex),
		Reason:             reason,
	}
	go func() {
		if errCancel := a.CancelExecution(context.Background(), cancelReq); errCancel != nil {
			log.WithError(errCancel).WithField("plugin", a.pluginID).Warn("plugin execution cancel failed")
		}
	}()
}

func (a *executorAdapter) CancelExecution(ctx context.Context, req pluginapi.CancelExecutionRequest) error {
	if a == nil || a.host == nil || a.canceller == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return fmt.Errorf("plugin executor %s cancellation is unavailable", a.Identifier())
	}
	if strings.TrimSpace(req.RequestID) == "" && strings.TrimSpace(req.ExecutionSessionID) == "" {
		return fmt.Errorf("request_id or execution_session_id is required")
	}
	if requestedProvider := strings.ToLower(strings.TrimSpace(req.Provider)); requestedProvider != "" && requestedProvider != a.provider {
		return fmt.Errorf("cancel provider %s does not match executor %s", requestedProvider, a.provider)
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.ExecutionSessionID = strings.TrimSpace(req.ExecutionSessionID)
	req.CallerScope = strings.TrimSpace(req.CallerScope)
	req.WorkspaceIdentity = strings.TrimSpace(req.WorkspaceIdentity)
	req.Provider = a.provider
	req.AuthID = strings.TrimSpace(req.AuthID)
	req.AuthIndex = strings.TrimSpace(req.AuthIndex)
	if req.Reason == "" {
		req.Reason = pluginapi.ExecutionCancelReasonExplicit
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, pluginExecutionLifecycleTimeout)
	defer cancel()
	result := make(chan pluginLifecycleCallResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- pluginLifecycleCallResult{recovered: recovered}
			}
		}()
		result <- pluginLifecycleCallResult{err: a.canceller.CancelExecution(callCtx, req)}
	}()
	select {
	case callResult := <-result:
		if callResult.recovered != nil {
			a.fuseCurrentPlugin("ExecutionCanceller.CancelExecution", callResult.recovered)
			return fmt.Errorf("plugin executor %s cancellation failed", a.Identifier())
		}
		return callResult.err
	case <-callCtx.Done():
		return callCtx.Err()
	}
}

func (a *executorAdapter) watchExecutionCancellation(ctx context.Context, req pluginapi.ExecutorRequest) func() {
	if a == nil || a.canceller == nil || ctx == nil || ctx.Done() == nil || (strings.TrimSpace(req.RequestID) == "" && strings.TrimSpace(req.ExecutionSessionID) == "") {
		return func() {}
	}
	stopped := make(chan struct{})
	var stopOnce sync.Once
	var cancelOnce sync.Once
	sendCancel := func() {
		cancelOnce.Do(func() {
			a.cancelExecutionAsync(req, executionCancelReason(ctx))
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			sendCancel()
		case <-stopped:
		}
	}()
	return func() {
		stopOnce.Do(func() {
			if ctx.Err() != nil {
				sendCancel()
			}
			close(stopped)
		})
	}
}

func (a *executorAdapter) fuseCurrentPlugin(method string, recovered any) {
	if a == nil || a.host == nil {
		return
	}
	if a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		a.host.fusePlugin(a.pluginID, method, recovered)
		return
	}
	log.WithField("plugin", a.pluginID).WithField("method", method).Errorf("retired plugin lifecycle panic recovered: %v", recovered)
}

func executionCancelReason(ctx context.Context) pluginapi.ExecutionCancelReason {
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return pluginapi.ExecutionCancelReasonDeadlineExceeded
	}
	return pluginapi.ExecutionCancelReasonContextCanceled
}

func (a *executorAdapter) ProbeReadiness(ctx context.Context, req pluginapi.ReadinessRequest) (resp pluginapi.ReadinessResponse, err error) {
	if a == nil || a.host == nil || a.readiness == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin executor %s readiness is unavailable", a.Identifier())
	}
	if requestedProvider := strings.ToLower(strings.TrimSpace(req.Provider)); requestedProvider != "" && requestedProvider != a.provider {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("readiness provider %s does not match executor %s", requestedProvider, a.provider)
	}
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = a.provider
	}
	if req.Purpose == "" {
		req.Purpose = pluginapi.ReadinessPurposeDiagnostic
	}
	if req.Purpose != pluginapi.ReadinessPurposeAdmission && req.Purpose != pluginapi.ReadinessPurposeDiagnostic {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("unsupported readiness purpose %q", req.Purpose)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.fuseCurrentPlugin("ProviderReadiness.ProbeReadiness", recovered)
			resp = pluginapi.ReadinessResponse{}
			err = fmt.Errorf("plugin executor %s readiness failed", a.Identifier())
		}
	}()
	resp, err = a.readiness.ProbeReadiness(ctx, req)
	if err != nil {
		return pluginapi.ReadinessResponse{}, err
	}
	if responseProvider := strings.ToLower(strings.TrimSpace(resp.Provider)); responseProvider != "" && responseProvider != a.provider {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin readiness provider %s does not match executor %s", responseProvider, a.provider)
	}
	if strings.TrimSpace(resp.Provider) == "" {
		resp.Provider = a.provider
	}
	resp.Checks, resp.Ready = normalizedReadinessChecks(a.version, req, resp.Checks, resp.Ready)
	return resp, nil
}

type executorReadinessAdmissionContextKey struct{}

type executorReadinessAdmission struct {
	adapter            *executorAdapter
	requestID          string
	executionSessionID string
	callerScope        string
	workspaceIdentity  string
	authID             string
	authIndex          string
	model              string
	stream             bool
	token              *executorReadinessAdmissionToken
}

type executorReadinessAdmissionToken struct {
	consumed atomic.Bool
}

func (a *executorAdapter) AdmitExecution(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	pluginReq := buildExecutorRequest(a.host, a.provider, auth, req, opts)
	if errReady := a.ensureReady(ctx, pluginReq); errReady != nil {
		return ctx, errReady
	}
	return context.WithValue(ctx, executorReadinessAdmissionContextKey{}, executorReadinessAdmission{
		adapter:            a,
		requestID:          pluginReq.RequestID,
		executionSessionID: pluginReq.ExecutionSessionID,
		callerScope:        pluginReq.CallerScope,
		workspaceIdentity:  pluginReq.WorkspaceIdentity,
		authID:             pluginReq.AuthID,
		authIndex:          pluginReq.AuthIndex,
		model:              pluginReq.Model,
		stream:             pluginReq.Stream,
		token:              &executorReadinessAdmissionToken{},
	}), nil
}

func (a *executorAdapter) executionAdmitted(ctx context.Context, req pluginapi.ExecutorRequest) bool {
	if a == nil || ctx == nil {
		return false
	}
	admission, okAdmission := ctx.Value(executorReadinessAdmissionContextKey{}).(executorReadinessAdmission)
	return okAdmission &&
		admission.adapter == a &&
		admission.requestID == req.RequestID &&
		admission.executionSessionID == req.ExecutionSessionID &&
		admission.callerScope == req.CallerScope &&
		admission.workspaceIdentity == req.WorkspaceIdentity &&
		admission.authID == req.AuthID &&
		admission.authIndex == req.AuthIndex &&
		admission.model == req.Model &&
		admission.stream == req.Stream &&
		admission.token != nil &&
		admission.token.consumed.CompareAndSwap(false, true)
}

func (a *executorAdapter) ensureReady(ctx context.Context, req pluginapi.ExecutorRequest) error {
	if a == nil || a.readiness == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, pluginExecutionLifecycleTimeout)
	defer cancel()
	probeReq := pluginapi.ReadinessRequest{
		Purpose:           pluginapi.ReadinessPurposeAdmission,
		Provider:          a.provider,
		Model:             req.Model,
		AuthID:            req.AuthID,
		AuthIndex:         req.AuthIndex,
		AuthProvider:      req.AuthProvider,
		StorageJSON:       bytes.Clone(req.StorageJSON),
		AuthMetadata:      cloneAnyMap(req.AuthMetadata),
		AuthAttributes:    cloneStringMap(req.AuthAttributes),
		CallerScope:       req.CallerScope,
		WorkspaceIdentity: req.WorkspaceIdentity,
	}
	// Execute/ExecuteStream create or attach the vendor-native session. Requiring
	// session_ready before that call would make a new explicit Core session
	// impossible to start. Session-scoped readiness remains available through an
	// explicit ProbeProviderReadiness request that supplies ExecutionSessionID.
	resp, errProbe := a.ProbeReadiness(probeCtx, probeReq)
	if errProbe != nil {
		return pluginReadinessError{provider: a.provider}
	}
	if !resp.Ready {
		return pluginReadinessError{
			provider: a.provider,
			level:    firstUnreadyRequiredLevel(probeReq, resp.Checks),
		}
	}
	return nil
}

type pluginReadinessError struct {
	provider string
	level    pluginapi.ReadinessLevel
}

func (e pluginReadinessError) Error() string {
	if e.level != "" {
		return fmt.Sprintf("plugin executor %s is not ready at %s", e.provider, e.level)
	}
	return fmt.Sprintf("plugin executor %s is not ready", e.provider)
}

func (pluginReadinessError) StatusCode() int { return http.StatusServiceUnavailable }

// Provider-, runner-, protocol-, and session-scoped readiness failures cannot
// be fixed by selecting another credential, so they stop auth fallback without
// penalizing the selected auth. An auth-scoped failure may be fixed by another
// credential and therefore remains eligible for availability-neutral rotation.
func (e pluginReadinessError) IsRequestScoped() bool {
	return e.level != pluginapi.ReadinessLevelAuthReady
}

func (e pluginReadinessError) Unwrap() error {
	if e.level != pluginapi.ReadinessLevelAuthReady {
		return nil
	}
	return &coreauth.Error{
		Code:       coreauth.ErrorCodeConnectionLifecycle,
		Message:    e.Error(),
		HTTPStatus: http.StatusServiceUnavailable,
	}
}

func normalizedReadinessChecks(pluginVersion string, req pluginapi.ReadinessRequest, checks []pluginapi.ReadinessCheck, reportedReady bool) ([]pluginapi.ReadinessCheck, bool) {
	byLevel := make(map[pluginapi.ReadinessLevel]pluginapi.ReadinessCheck, len(checks))
	for _, check := range checks {
		if check.Level != "" && check.Level != pluginapi.ReadinessLevelPluginInstalled {
			byLevel[check.Level] = check
		}
	}
	byLevel[pluginapi.ReadinessLevelPluginInstalled] = pluginapi.ReadinessCheck{
		Level:   pluginapi.ReadinessLevelPluginInstalled,
		State:   pluginapi.ReadinessStateReady,
		Version: pluginVersion,
	}
	levels := []pluginapi.ReadinessLevel{
		pluginapi.ReadinessLevelPluginInstalled,
		pluginapi.ReadinessLevelRunnerInstalled,
		pluginapi.ReadinessLevelProtocolReady,
		pluginapi.ReadinessLevelAuthReady,
		pluginapi.ReadinessLevelSessionReady,
	}
	out := make([]pluginapi.ReadinessCheck, 0, len(levels))
	for _, level := range levels {
		check, exists := byLevel[level]
		if !exists {
			check = pluginapi.ReadinessCheck{Level: level, State: pluginapi.ReadinessStateUnknown}
		}
		out = append(out, check)
	}
	ready := reportedReady
	for _, level := range requiredReadinessLevels(req) {
		if byLevel[level].State != pluginapi.ReadinessStateReady {
			ready = false
			break
		}
	}
	return out, ready
}

func requiredReadinessLevels(req pluginapi.ReadinessRequest) []pluginapi.ReadinessLevel {
	required := []pluginapi.ReadinessLevel{
		pluginapi.ReadinessLevelPluginInstalled,
		pluginapi.ReadinessLevelRunnerInstalled,
		pluginapi.ReadinessLevelProtocolReady,
	}
	if strings.TrimSpace(req.AuthID) != "" || strings.TrimSpace(req.AuthIndex) != "" {
		required = append(required, pluginapi.ReadinessLevelAuthReady)
	}
	if strings.TrimSpace(req.ExecutionSessionID) != "" {
		required = append(required, pluginapi.ReadinessLevelSessionReady)
	}
	return required
}

func firstUnreadyRequiredLevel(req pluginapi.ReadinessRequest, checks []pluginapi.ReadinessCheck) pluginapi.ReadinessLevel {
	byLevel := make(map[pluginapi.ReadinessLevel]pluginapi.ReadinessState, len(checks))
	for _, check := range checks {
		byLevel[check.Level] = check.State
	}
	for _, level := range requiredReadinessLevels(req) {
		if byLevel[level] != pluginapi.ReadinessStateReady {
			return level
		}
	}
	return ""
}

type preparedExecutorCall struct {
	req             coreexecutor.Request
	opts            coreexecutor.Options
	inputRequested  sdktranslator.Format
	requestedFormat sdktranslator.Format
	inputFormat     sdktranslator.Format
	outputFormat    sdktranslator.Format
}

func (a *executorAdapter) prepareExecutorCall(req coreexecutor.Request, opts coreexecutor.Options) (preparedExecutorCall, error) {
	inputRequested := executorInputFormat(req, opts)
	requestedFormat := executorRequestedFormat(req, opts)
	inputFormat, errInput := a.selectExecutorInputFormat(inputRequested)
	if errInput != nil {
		return preparedExecutorCall{}, errInput
	}
	outputFormat, errOutput := a.selectExecutorOutputFormat(requestedFormat, inputFormat)
	if errOutput != nil {
		return preparedExecutorCall{}, errOutput
	}

	nativeReq := req
	nativeOpts := opts
	if inputRequested != "" && inputRequested != inputFormat {
		nativeReq.Payload = sdktranslator.TranslateRequest(inputRequested, inputFormat, req.Model, req.Payload, opts.Stream)
	}
	nativeReq.Format = outputFormat
	nativeOpts.SourceFormat = inputFormat
	nativeOpts.ResponseFormat = outputFormat

	return preparedExecutorCall{
		req:             nativeReq,
		opts:            nativeOpts,
		inputRequested:  inputRequested,
		requestedFormat: requestedFormat,
		inputFormat:     inputFormat,
		outputFormat:    outputFormat,
	}, nil
}

func (a *executorAdapter) RequestToFormat(req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	if a == nil {
		return ""
	}
	inputRequested := executorInputFormat(req, opts)
	inputFormat, errInput := a.selectExecutorInputFormat(inputRequested)
	if errInput != nil {
		return ""
	}
	return inputFormat
}

func executorInputFormat(req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	if opts.SourceFormat != "" {
		return normalizeExecutorFormatName(opts.SourceFormat.String())
	}
	if req.Format != "" {
		return normalizeExecutorFormatName(req.Format.String())
	}
	return sdktranslator.FormatOpenAI
}

func executorRequestedFormat(req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	if format := coreexecutor.ResponseFormatOrSource(opts); format != "" {
		return normalizeExecutorFormatName(format.String())
	}
	if req.Format != "" {
		return normalizeExecutorFormatName(req.Format.String())
	}
	return sdktranslator.FormatOpenAI
}

func (a *executorAdapter) selectExecutorInputFormat(requested sdktranslator.Format) (sdktranslator.Format, error) {
	if len(a.inputFormats) == 0 {
		return "", fmt.Errorf("plugin executor %s declares no input formats", a.Identifier())
	}
	if executorFormatContains(a.inputFormats, requested) {
		return requested, nil
	}
	for _, format := range a.inputFormats {
		if requested == "" || sdktranslator.HasRequestTransformer(requested, format) {
			return format, nil
		}
	}
	return "", fmt.Errorf("plugin executor %s does not support input format %q", a.Identifier(), requested)
}

func (a *executorAdapter) selectExecutorOutputFormat(requested, inputFormat sdktranslator.Format) (sdktranslator.Format, error) {
	if len(a.outputFormats) == 0 {
		return "", fmt.Errorf("plugin executor %s declares no output formats", a.Identifier())
	}
	if executorFormatContains(a.outputFormats, requested) {
		return requested, nil
	}
	if executorFormatContains(a.outputFormats, inputFormat) && a.executorResponseTranslationAvailable(inputFormat, requested) {
		return inputFormat, nil
	}
	for _, format := range a.outputFormats {
		if requested == "" || a.executorResponseTranslationAvailable(format, requested) {
			return format, nil
		}
	}
	return "", fmt.Errorf("plugin executor %s does not support output format %q", a.Identifier(), requested)
}

func (a *executorAdapter) executorResponseTranslationAvailable(from, to sdktranslator.Format) bool {
	if from == "" || to == "" || from == to {
		return true
	}
	if sdktranslator.HasResponseTransformer(to, from) {
		return true
	}
	return a != nil && a.host.hasResponseTranslator()
}

func (h *Host) hasResponseTranslator() bool {
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.ResponseTranslator == nil {
			continue
		}
		return true
	}
	return false
}

func executorNativeStreamResponseTranslatorExists(from, to sdktranslator.Format) bool {
	if from == "" || to == "" || from == to {
		return true
	}
	return sdktranslator.HasStreamResponseTransformer(to, from)
}

func (a *executorAdapter) translateExecutorResponse(ctx context.Context, prepared preparedExecutorCall, payload []byte, stream bool, param *any) []byte {
	if prepared.requestedFormat == "" || prepared.outputFormat == prepared.requestedFormat {
		out := bytes.Clone(payload)
		if prepared.requestedFormat == sdktranslator.FormatOpenAIResponse {
			out = helps.EnsureResponsesUsageDetails(out)
		}
		return out
	}
	originalRequest := prepared.opts.OriginalRequest
	if len(originalRequest) == 0 {
		originalRequest = prepared.req.Payload
	}
	if stream {
		frames := a.translateExecutorStreamPayload(ctx, prepared, payload, param)
		if len(frames) == 0 {
			return nil
		}
		if len(frames) == 1 {
			return bytes.Clone(frames[0])
		}
		return bytes.Join(frames, nil)
	}
	out := sdktranslator.TranslateNonStream(ctx, prepared.outputFormat, prepared.requestedFormat, prepared.req.Model, originalRequest, prepared.req.Payload, payload, param)
	if prepared.requestedFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return out
}

func (a *executorAdapter) translateExecutorStreamChunks(ctx context.Context, prepared preparedExecutorCall, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if prepared.requestedFormat == "" || (prepared.outputFormat == prepared.requestedFormat && prepared.requestedFormat != sdktranslator.FormatOpenAIResponse) {
		return in
	}
	if in == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var param any
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					a.emitTranslatedExecutorStreamTail(ctx, prepared, out, &param)
					return
				}
				if chunk.Err != nil {
					_ = sendExecutorPluginStreamChunk(ctx, out, chunk)
					continue
				}
				frames := a.translateExecutorStreamPayload(ctx, prepared, chunk.Payload, &param)
				for _, frame := range frames {
					if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
						return
					}
				}
			}
		}
	}()
	return out
}

func (a *executorAdapter) translateExecutorStreamPayload(ctx context.Context, prepared preparedExecutorCall, payload []byte, param *any) [][]byte {
	if prepared.requestedFormat != "" && prepared.outputFormat == prepared.requestedFormat {
		out := payload
		if prepared.requestedFormat == sdktranslator.FormatOpenAIResponse {
			out = helps.EnsureResponsesUsageDetails(out)
		}
		return [][]byte{out}
	}
	originalRequest := prepared.opts.OriginalRequest
	if len(originalRequest) == 0 {
		originalRequest = prepared.req.Payload
	}
	frames := sdktranslator.TranslateStream(ctx, prepared.outputFormat, prepared.requestedFormat, prepared.req.Model, originalRequest, prepared.req.Payload, payload, param)
	if executorStreamTranslationFellBack(prepared, payload, frames) {
		return nil
	}
	if prepared.requestedFormat == sdktranslator.FormatOpenAIResponse {
		for i, frame := range frames {
			frames[i] = helps.EnsureResponsesUsageDetails(frame)
		}
	}
	return frames
}

func executorStreamTranslationFellBack(prepared preparedExecutorCall, payload []byte, frames [][]byte) bool {
	if prepared.requestedFormat == "" || prepared.outputFormat == "" || prepared.outputFormat == prepared.requestedFormat {
		return false
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], payload) {
		return false
	}
	// A plugin executor only reaches this path after host-side response translation
	// has been selected. An unchanged single frame is the SDK registry fallback,
	// not a valid translated frame to send to the client.
	return executorNativeStreamResponseTranslatorExists(prepared.outputFormat, prepared.requestedFormat)
}

func (a *executorAdapter) emitTranslatedExecutorStreamTail(ctx context.Context, prepared preparedExecutorCall, out chan<- pluginapi.ExecutorStreamChunk, param *any) {
	tail := executorStreamDonePayload(prepared.outputFormat)
	if len(tail) == 0 {
		return
	}
	frames := a.translateExecutorStreamPayload(ctx, prepared, tail, param)
	for _, frame := range frames {
		if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
			return
		}
	}
}

func executorStreamDonePayload(format sdktranslator.Format) []byte {
	switch format {
	case sdktranslator.FormatOpenAI:
		return []byte("data: [DONE]")
	default:
		return nil
	}
}

func sendExecutorPluginStreamChunk(ctx context.Context, out chan<- pluginapi.ExecutorStreamChunk, chunk pluginapi.ExecutorStreamChunk) bool {
	select {
	case out <- pluginapi.ExecutorStreamChunk{Payload: bytes.Clone(chunk.Payload), Err: chunk.Err}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *executorAdapter) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (resp coreexecutor.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	var reporter *helps.UsageReporter
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.Execute", recovered)
			resp = coreexecutor.Response{}
			err = fmt.Errorf("plugin executor %s panic: %v", a.Identifier(), recovered)
			if reporter != nil {
				reporter.PublishFailure(ctx, err)
			}
			return
		}
		if err != nil && reporter != nil {
			reporter.PublishFailure(ctx, err)
		}
	}()

	prepared, errPrepare := a.prepareExecutorCall(req, opts)
	if errPrepare != nil {
		return coreexecutor.Response{}, errPrepare
	}
	pluginReq := buildExecutorRequest(a.host, a.provider, auth, prepared.req, prepared.opts)
	if !a.executionAdmitted(ctx, pluginReq) {
		if errReady := a.ensureReady(ctx, pluginReq); errReady != nil {
			return coreexecutor.Response{}, errReady
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return coreexecutor.Response{}, ctx.Err()
	}
	stopCancellationWatch := a.watchExecutionCancellation(ctx, pluginReq)
	defer stopCancellationWatch()
	reporter = a.formalUsageReporter(ctx, auth, prepared)
	if reporter != nil {
		reporter.StartResponseTTFT()
	}
	pluginResp, errExecute := a.executor.Execute(ctx, pluginReq)
	if ctx != nil && ctx.Err() != nil {
		if reporter != nil {
			reporter.PublishFailure(ctx, ctx.Err())
		}
		return coreexecutor.Response{}, ctx.Err()
	}
	if errExecute != nil {
		if reporter != nil {
			reporter.PublishFailure(ctx, errExecute)
		}
		return coreexecutor.Response{}, errExecute
	}
	internallogging.SetResponseHeaders(ctx, cloneHeader(pluginResp.Headers))
	if reporter != nil {
		reporter.RecordFirstPacket()
		if pluginExecutorUsageReported(prepared.outputFormat, pluginResp.Payload) {
			reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
		}
		reporter.Publish(ctx, helps.ParsePluginExecutorResponseUsage(prepared.outputFormat.String(), pluginResp.Payload))
		reporter.EnsurePublished(ctx)
	}
	return coreexecutor.Response{
		Payload:  a.translateExecutorResponse(ctx, prepared, pluginResp.Payload, false, nil),
		Metadata: cloneAnyMap(pluginResp.Metadata),
		Headers:  cloneHeader(pluginResp.Headers),
	}, nil
}

func (a *executorAdapter) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (result *coreexecutor.StreamResult, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	var reporter *helps.UsageReporter
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.ExecuteStream", recovered)
			result = nil
			err = fmt.Errorf("plugin executor %s stream panic: %v", a.Identifier(), recovered)
			if reporter != nil {
				reporter.PublishFailure(ctx, err)
			}
			return
		}
		if err != nil && reporter != nil {
			reporter.PublishFailure(ctx, err)
		}
	}()

	prepared, errPrepare := a.prepareExecutorCall(req, opts)
	if errPrepare != nil {
		return nil, errPrepare
	}
	pluginReq := buildExecutorRequest(a.host, a.provider, auth, prepared.req, prepared.opts)
	if !a.executionAdmitted(ctx, pluginReq) {
		if errReady := a.ensureReady(ctx, pluginReq); errReady != nil {
			return nil, errReady
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stopBootstrapCancellationWatch := a.watchExecutionCancellation(ctx, pluginReq)
	defer stopBootstrapCancellationWatch()
	reporter = a.formalUsageReporter(ctx, auth, prepared)
	if reporter != nil {
		reporter.StartResponseTTFT()
	}
	pluginResp, errExecuteStream := a.executor.ExecuteStream(ctx, pluginReq)
	if ctx != nil && ctx.Err() != nil {
		if reporter != nil {
			reporter.PublishFailure(ctx, ctx.Err())
		}
		return nil, ctx.Err()
	}
	if errExecuteStream != nil {
		if reporter != nil {
			reporter.PublishFailure(ctx, errExecuteStream)
		}
		return nil, errExecuteStream
	}
	internallogging.SetResponseHeaders(ctx, cloneHeader(pluginResp.Headers))
	rawPluginChunks := observeFormalPluginStreamUsage(ctx, reporter, prepared.outputFormat, pluginResp.Chunks)
	pluginChunks := a.watchExecutorStreamCancellation(ctx, pluginReq, rawPluginChunks)
	return &coreexecutor.StreamResult{
		Headers: cloneHeader(pluginResp.Headers),
		Chunks:  mapExecutorStreamChunks(ctx, a.translateExecutorStreamChunks(ctx, prepared, pluginChunks)),
	}, nil
}

func (a *executorAdapter) watchExecutorStreamCancellation(ctx context.Context, req pluginapi.ExecutorRequest, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	if in == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				a.cancelExecutionAsync(req, executionCancelReason(ctx))
				return
			case chunk, ok := <-in:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					a.cancelExecutionAsync(req, executionCancelReason(ctx))
					return
				case out <- chunk:
				}
			}
		}
	}()
	return out
}

func (a *executorAdapter) Refresh(ctx context.Context, auth *coreauth.Auth) (refreshed *coreauth.Auth, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	record := a.host.authProviderRecord(authProvider(auth))
	if record == nil || record.plugin.Capabilities.AuthProvider == nil {
		return auth.Clone(), nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(record.id, "AuthProvider.RefreshAuth", recovered)
			refreshed = nil
			err = fmt.Errorf("plugin executor %s refresh panic: %v", a.Identifier(), recovered)
		}
	}()

	pluginResp, errRefresh := record.plugin.Capabilities.AuthProvider.RefreshAuth(ctx, pluginapi.AuthRefreshRequest{
		AuthID:       authID(auth),
		AuthProvider: authProvider(auth),
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(authMetadata(auth)),
		Attributes:   authAttributes(auth),
		Host:         a.host.hostConfigSummary(),
		HTTPClient:   a.host.newHTTPClient(auth),
	})
	if errRefresh != nil {
		return nil, errRefresh
	}
	data := pluginResp.Auth
	if strings.TrimSpace(data.Provider) == "" {
		data.Provider = authProvider(auth)
	}
	if strings.TrimSpace(data.ID) == "" {
		data.ID = authID(auth)
	}
	if strings.TrimSpace(data.FileName) == "" && auth != nil {
		data.FileName = auth.FileName
	}
	if strings.TrimSpace(data.Label) == "" && auth != nil {
		data.Label = auth.Label
	}
	if strings.TrimSpace(data.Prefix) == "" && auth != nil {
		data.Prefix = auth.Prefix
	}
	if strings.TrimSpace(data.ProxyURL) == "" && auth != nil {
		data.ProxyURL = auth.ProxyURL
	}
	if len(data.Metadata) == 0 && auth != nil {
		data.Metadata = cloneAnyMap(auth.Metadata)
	}
	if len(data.Attributes) == 0 && auth != nil {
		data.Attributes = cloneStringMap(auth.Attributes)
	}
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	if pluginResp.NextRefreshAfter.IsZero() && auth != nil {
		data.NextRefreshAfter = auth.NextRefreshAfter
	}
	if !pluginResp.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = pluginResp.NextRefreshAfter
	}
	next := a.host.AuthDataToCoreAuth(data, "", data.FileName)
	if next == nil {
		return nil, fmt.Errorf("plugin executor %s refresh returned invalid auth data", a.Identifier())
	}
	if auth != nil {
		next.CreatedAt = auth.CreatedAt
		next.UpdatedAt = auth.UpdatedAt
	}
	return next, nil
}

func (a *executorAdapter) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (resp coreexecutor.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.CountTokens", recovered)
			resp = coreexecutor.Response{}
			err = fmt.Errorf("plugin executor %s count tokens panic: %v", a.Identifier(), recovered)
		}
	}()

	prepared, errPrepare := a.prepareExecutorCall(req, opts)
	if errPrepare != nil {
		return coreexecutor.Response{}, errPrepare
	}
	pluginReq := buildExecutorRequest(a.host, a.provider, auth, prepared.req, prepared.opts)
	if !a.executionAdmitted(ctx, pluginReq) {
		if errReady := a.ensureReady(ctx, pluginReq); errReady != nil {
			return coreexecutor.Response{}, errReady
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return coreexecutor.Response{}, ctx.Err()
	}
	stopCancellationWatch := a.watchExecutionCancellation(ctx, pluginReq)
	defer stopCancellationWatch()
	pluginResp, errCountTokens := a.executor.CountTokens(ctx, pluginReq)
	if ctx != nil && ctx.Err() != nil {
		return coreexecutor.Response{}, ctx.Err()
	}
	if errCountTokens != nil {
		return coreexecutor.Response{}, errCountTokens
	}
	return coreexecutor.Response{
		Payload:  a.translateExecutorResponse(ctx, prepared, pluginResp.Payload, false, nil),
		Metadata: cloneAnyMap(pluginResp.Metadata),
		Headers:  cloneHeader(pluginResp.Headers),
	}, nil
}

func (a *executorAdapter) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (resp *http.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	if req == nil {
		return nil, fmt.Errorf("plugin executor %s received nil HTTP request", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.HttpRequest", recovered)
			resp = nil
			err = fmt.Errorf("plugin executor %s http request panic: %v", a.Identifier(), recovered)
		}
	}()
	body, errReadAll := readAndRestoreRequestBody(req)
	if errReadAll != nil {
		return nil, fmt.Errorf("read plugin http request body: %w", errReadAll)
	}
	pluginResp, errHTTPRequest := a.executor.HttpRequest(ctx, pluginapi.ExecutorHTTPRequest{
		AuthID:       authID(auth),
		AuthProvider: authProvider(auth),
		Method:       req.Method,
		URL:          req.URL.String(),
		Headers:      cloneHeader(req.Header),
		Body:         bytes.Clone(body),
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(authMetadata(auth)),
		Attributes:   authAttributes(auth),
		HTTPClient:   a.host.newHTTPClient(auth, a.provider),
	})
	if errHTTPRequest != nil {
		return nil, errHTTPRequest
	}
	status := pluginResp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	resp = &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     cloneHeader(pluginResp.Headers),
		Body:       io.NopCloser(bytes.NewReader(bytes.Clone(pluginResp.Body))),
		Request:    req,
	}
	return resp, nil
}

func buildExecutorRequest(host *Host, provider string, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) pluginapi.ExecutorRequest {
	metadata := mergeExecutorMetadata(req.Metadata, opts.Metadata)
	return pluginapi.ExecutorRequest{
		RequestID:          executorMetadataID(metadata, coreexecutor.RequestIDMetadataKey),
		ExecutionSessionID: executorExecutionSessionID(metadata),
		CallerScope:        executorMetadataID(metadata, coreexecutor.CallerScopeMetadataKey),
		WorkspaceIdentity:  executorMetadataID(metadata, coreexecutor.WorkspaceIdentityMetadataKey),
		AuthID:             authID(auth),
		AuthIndex:          authIndex(auth),
		AuthProvider:       authProvider(auth),
		Model:              req.Model,
		Format:             req.Format.String(),
		Stream:             opts.Stream,
		Alt:                opts.Alt,
		Headers:            cloneHeader(opts.Headers),
		Query:              cloneValues(opts.Query),
		OriginalRequest:    bytes.Clone(opts.OriginalRequest),
		SourceFormat:       opts.SourceFormat.String(),
		Payload:            bytes.Clone(req.Payload),
		Metadata:           metadata,
		StorageJSON:        storageJSONFromAuth(auth),
		AuthMetadata:       cloneAnyMap(authMetadata(auth)),
		AuthAttributes:     authAttributes(auth),
		HTTPClient:         host.newHTTPClient(auth, provider),
	}
}

func executorExecutionSessionID(metadata map[string]any) string {
	return executorMetadataID(metadata, coreexecutor.ExecutionSessionMetadataKey)
}

func executorMetadataID(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func storageJSONFromAuth(auth *coreauth.Auth) []byte {
	if auth == nil {
		return nil
	}
	if rawProvider, okRaw := auth.Storage.(interface{ RawJSON() []byte }); okRaw {
		return bytes.Clone(rawProvider.RawJSON())
	}
	if len(auth.Metadata) == 0 {
		return nil
	}
	data, errMarshal := json.Marshal(auth.Metadata)
	if errMarshal != nil {
		return nil
	}
	return data
}

func authAttributes(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return cloneStringMap(auth.Attributes)
}

func mergeExecutorMetadata(reqMetadata, optsMetadata map[string]any) map[string]any {
	if len(reqMetadata) == 0 && len(optsMetadata) == 0 {
		return nil
	}
	merged := make(map[string]any, len(reqMetadata)+len(optsMetadata))
	for key, value := range reqMetadata {
		merged[key] = value
	}
	for key, value := range optsMetadata {
		merged[key] = value
	}
	return merged
}

func mapExecutorStreamChunks(ctx context.Context, in <-chan pluginapi.ExecutorStreamChunk) <-chan coreexecutor.StreamChunk {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan coreexecutor.StreamChunk)
	if in == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for {
			var mapped coreexecutor.StreamChunk
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					return
				}
				mapped = coreexecutor.StreamChunk{
					Payload: bytes.Clone(chunk.Payload),
					Err:     chunk.Err,
				}
			}
			select {
			case <-ctx.Done():
				return
			case out <- mapped:
			}
		}
	}()
	return out
}
