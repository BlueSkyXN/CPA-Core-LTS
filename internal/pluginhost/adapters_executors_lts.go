package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	claudecommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const pluginExecutionLifecycleTimeout = 5 * time.Second

// pluginStreamUsageTailWindow bounds how long a failed translated stream may
// keep reading the upstream solely to capture a provider usage tail frame
// (include_usage sends usage after the finish_reason chunk). The window is a
// hang safety net, not a latency budget: the client already received the
// failure terminal. Tests shorten it via t.Cleanup.
var pluginStreamUsageTailWindow = 5 * time.Second

// ProbeProviderReadiness probes the active plugin executor for provider. A
// loaded legacy plugin without the readiness capability remains fail-closed.
func (h *Host) ProbeProviderReadiness(ctx context.Context, provider string, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
	return h.probeProviderReadiness(ctx, "", provider, req)
}

// ProbePluginReadiness prevents a management probe from reaching a different plugin that owns the same provider.
func (h *Host) ProbePluginReadiness(ctx context.Context, pluginID, provider string, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
	if strings.TrimSpace(pluginID) == "" {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin id is required")
	}
	return h.probeProviderReadiness(ctx, pluginID, provider, req)
}

func (h *Host) probeProviderReadiness(ctx context.Context, pluginID, provider string, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
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
	if pluginID != "" && adapter.pluginID != pluginID {
		return pluginapi.ReadinessResponse{}, fmt.Errorf("plugin does not own selected executor")
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

// PluginManagedCredentials marks a plugin-owned inference binding for account policy checks.
func (*executorAdapter) PluginManagedCredentials() {}

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

// pluginCancelOnce 返回本请求 CancelExecution 只发一次的发送闭包。同一执行
// 可能挂多个取消观察者（bootstrap watch、流式 watch、SSE 桥 stop、建流桥接
// 转发），它们各自持有 once 无法互相去重；统一收敛到该闭包后，重复取消在
// 请求层被吸收（lts-contract flake：TestExecutorStreamEarlyTerminationCancelsUpstream
// 报 duplicate cancellation，两个 watch 对同一 streamCtx 终止各发一次）。
func (a *executorAdapter) pluginCancelOnce(ctx context.Context, req pluginapi.ExecutorRequest) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			a.cancelExecutionAsync(req, executionCancelReason(ctx))
		})
	}
}

func (a *executorAdapter) watchExecutionCancellation(ctx context.Context, req pluginapi.ExecutorRequest) func() {
	return a.watchExecutionCancellationShared(ctx, req, nil)
}

// watchExecutionCancellationShared 允许同一执行的多处观察共享一个取消发送
// 闭包（见 pluginCancelOnce）；shared 为 nil 时行为与独立调用完全一致。
func (a *executorAdapter) watchExecutionCancellationShared(ctx context.Context, req pluginapi.ExecutorRequest, shared func()) func() {
	if a == nil || a.canceller == nil || ctx == nil || ctx.Done() == nil || (strings.TrimSpace(req.RequestID) == "" && strings.TrimSpace(req.ExecutionSessionID) == "") {
		return func() {}
	}
	stopped := make(chan struct{})
	var stopOnce sync.Once
	var cancelOnce sync.Once
	sendCancel := shared
	if sendCancel == nil {
		sendCancel = func() {
			cancelOnce.Do(func() {
				a.cancelExecutionAsync(req, executionCancelReason(ctx))
			})
		}
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

func (a *executorAdapter) prepareTranslatedExecutorEnvelope(
	ctx context.Context,
	runtimeCfg *config.Config,
	auth *coreauth.Auth,
	req coreexecutor.Request,
	opts coreexecutor.Options,
	inputRequested sdktranslator.Format,
	inputFormat sdktranslator.Format,
	baseModel string,
	modelInfo *registry.ModelInfo,
) (sdktranslator.RequestEnvelope, *registry.ModelInfo, bool, zcodeSearchReplay, error) {
	envelope := sdktranslator.RequestEnvelope{Format: inputRequested, Model: baseModel, Body: req.Payload, Stream: opts.Stream}
	if (inputRequested != sdktranslator.FormatOpenAIResponse && inputRequested != sdktranslator.FormatOpenAI) || inputFormat != sdktranslator.FormatClaude {
		return envelope, modelInfo, false, zcodeSearchReplay{}, nil
	}
	resolvedModel, errModel := a.executionModelInfo(auth, req, opts)
	if errModel != nil {
		return sdktranslator.RequestEnvelope{}, nil, false, zcodeSearchReplay{}, errModel
	}
	envelope.ModelInfo = resolvedModel
	var zcodeReplay zcodeSearchReplay
	envelope.Body, zcodeReplay = a.prepareZcodeResponsesRequest(ctx, runtimeCfg, auth, req, opts, inputRequested, baseModel, resolvedModel, envelope.Body)
	return envelope, resolvedModel, true, zcodeReplay, nil
}

func (a *executorAdapter) applyPreparedExecutorThinking(
	doc *ojson.Doc,
	runtimeCfg *config.Config,
	req coreexecutor.Request,
	opts coreexecutor.Options,
	nativePayload []byte,
	inputRequested sdktranslator.Format,
	inputFormat sdktranslator.Format,
	modelInfo *registry.ModelInfo,
	translatedClaude bool,
	updatesChanged bool,
) (*ojson.Doc, error) {
	var errThinking error
	if _, active := helps.RequestEffortOverride(runtimeCfg, req, opts); active {
		// LTS: reasoning-effort override stays authoritative over model defaults.
		var body []byte
		body, errThinking = helps.ApplyConfiguredRequestThinkingWithModelInfo(runtimeCfg, doc.Bytes(), req, opts, inputRequested.String(), inputFormat.String(), a.provider, modelInfo, updatesChanged)
		if errThinking == nil {
			doc = ojson.NewDoc(body)
		}
	} else if translatedClaude {
		// LTS: translation already used the selected account's capabilities. Its
		// normalized native controls remain authoritative in this final pass.
		errThinking = thinking.ApplyThinkingWithModelInfoAndSummaryDoc(
			doc, nativePayload, req.Model,
			inputFormat.String(), inputFormat.String(), a.provider, modelInfo,
			thinking.ExtractSummaryConfig(nativePayload, inputFormat.String()), updatesChanged,
		)
	} else {
		errThinking = helps.ApplyRequestThinkingWithModelInfoDoc(
			doc,
			req,
			opts,
			inputRequested.String(),
			inputFormat.String(),
			a.provider,
			modelInfo,
			updatesChanged,
		)
	}
	if errThinking != nil {
		return nil, errThinking
	}
	return doc, nil
}

// Final payload rules can remove or replace model. Preserve native extensions that carry no model field.
func finalizePluginModel(doc *ojson.Doc, format sdktranslator.Format, model string, original []byte, hasAccount bool) error {
	if model == "" || doc.Len() == 0 {
		return nil
	}
	hasModel := doc.Get("model").Exists() || ojson.GetBytesNoCopy(original, "model").Exists()
	switch format {
	case sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatCodex, "embeddings":
		if !hasAccount && !hasModel {
			return nil
		}
	case sdktranslator.FormatGemini, sdktranslator.FormatAntigravity, sdktranslator.FormatInteractions:
		if !hasModel {
			return nil
		}
	default:
		return nil
	}
	root := ojson.ParseBytes(doc.Bytes())
	if !root.IsObject() {
		return nil
	}
	var current ojson.Result
	ambiguous := false
	root.ForEach(func(key, value ojson.Result) bool {
		if strings.EqualFold(key.String(), "model") {
			if current.Exists() || key.String() != "model" {
				ambiguous = true
				return false
			}
			current = value
		}
		return true
	})
	if ambiguous {
		return coreauth.NewRequestScopedError("Supply one lowercase model field in the request body", http.StatusBadRequest)
	}
	if current.Type == ojson.String && current.String() == model {
		return nil
	}
	return doc.Set("model", model)
}

func normalizeClaudeChatStreamPayload(prepared preparedExecutorCall, payload []byte) []byte {
	// Chat 插件可以输出裸 JSON；现有 OpenAI→Claude 流转换器按 SSE data 行读取。
	if prepared.outputFormat == sdktranslator.FormatOpenAI && prepared.requestedFormat == sdktranslator.FormatClaude {
		trimmed := bytes.TrimSpace(payload)
		if json.Valid(trimmed) {
			return append([]byte("data: "), trimmed...)
		}
	}
	return payload
}

// executorStreamPassthrough 与 translateExecutorStreamChunksWithUsage 的直通
// 分支共用同一判定；ExecuteStream 依据它的反例决定是否派生执行 ctx。
func (a *executorAdapter) executorStreamPassthrough(prepared preparedExecutorCall) bool {
	return prepared.requestedFormat == "" ||
		(prepared.outputFormat == prepared.requestedFormat &&
			prepared.requestedFormat != sdktranslator.FormatOpenAIResponse &&
			!sdktranslator.HasStreamResponseTransformer(prepared.outputFormat, prepared.requestedFormat) &&
			!a.host.hasResponseTranslationHooks())
}

func (a *executorAdapter) translateExecutorStreamChunksWithUsage(ctx context.Context, prepared preparedExecutorCall, in <-chan pluginapi.ExecutorStreamChunk, reporter *helps.UsageReporter, cancelStream context.CancelFunc) <-chan pluginapi.ExecutorStreamChunk {
	if prepared.requestedFormat == "" {
		// 直通没有转换协程接管清理，ExecuteStream 不会为它派生执行 ctx。
		return observeFormalPluginStreamUsage(ctx, reporter, prepared.outputFormat, in)
	}
	if in == nil {
		// 空流没有协程，执行已结束，这里同步释放派生 ctx。
		if cancelStream != nil {
			cancelStream()
		}
		return observeFormalPluginStreamUsage(ctx, reporter, prepared.outputFormat, nil)
	}
	sameFormat := prepared.outputFormat == prepared.requestedFormat
	// Responses always enters translation so its usage-details normalization
	// in translateExecutorStreamPayload keeps running for same-format streams.
	// Same-format streams also enter translation when a registered same-format
	// transformer or response-translation hook exists (SSE framing normalization);
	// otherwise the stream is returned verbatim. 直通分支同样观察 usage 并释放派生
	// ctx（有界观察协程负责结算，见 observeFormalPluginStreamUsage），不泄漏取消。
	if sameFormat && prepared.requestedFormat != sdktranslator.FormatOpenAIResponse && !sdktranslator.HasStreamResponseTransformer(prepared.outputFormat, prepared.requestedFormat) && !a.host.hasResponseTranslationHooks() {
		return observeFormalPluginStreamUsageWithCancel(ctx, reporter, prepared.outputFormat, in, cancelStream)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var deframer *executorSSEDeframer
	if sameFormat && sdktranslator.HasStreamResponseTransformer(prepared.outputFormat, prepared.requestedFormat) {
		deframer = &executorSSEDeframer{}
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var param any
		usage := formalPluginStreamUsage{reporter: reporter, format: prepared.outputFormat}
		var streamErr error
		usageHandedOff := false
		// 结算先于取消：终止派生 ctx 前必须先落定统计终态（对齐 Claude 转换路径）。
		defer func() {
			if usageHandedOff {
				return
			}
			usage.publish(ctx, streamErr)
			if cancelStream != nil {
				cancelStream()
			}
		}()
		var completionDelta, completionStop []byte
		deferClaudeCompletion := prepared.outputFormat == sdktranslator.FormatOpenAI && prepared.requestedFormat == sdktranslator.FormatClaude
		processPayload := func(payload []byte) bool {
			frames := a.translateExecutorStreamPayload(ctx, prepared, payload, &param)
			if helps.ApplyPatchTranslationError(param) != nil {
				streamErr = pluginResponseConversionError()
			}
			for _, frame := range frames {
				// usage 尾帧之后仍可能报告失败；完成事件等待插件无错误关闭。
				// 正文和工具参数继续直接交付。
				if deferClaudeCompletion {
					switch executorClaudeCompletionEvent(frame) {
					case "message_delta":
						completionDelta = bytes.Clone(frame)
						continue
					case "message_stop":
						completionStop = bytes.Clone(frame)
						continue
					}
				}
				if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
					return false
				}
			}
			if streamErr != nil {
				_ = sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Err: streamErr})
				if reporter != nil {
					// 转换失败后 upstream 常见的 include_usage 尾帧仍需计入统计；
					// 有界收尾只观察不再交付，失败帧已交付、下游不受影响。
					usageHandedOff = true
					go finalizeExecutorStreamFailure(ctx, cancelStream, in, &usage, streamErr)
				}
				return false
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					if ctx.Err() != nil {
						return
					}
					if deframer != nil {
						if tail := deframer.flush(); len(tail) > 0 {
							if !processPayload(tail) {
								return
							}
						}
					}
					frames := a.translatedExecutorStreamTail(ctx, prepared, &param)
					if helps.ApplyPatchTranslationError(param) != nil {
						streamErr = pluginResponseConversionError()
						completionDelta, completionStop = nil, nil
					}
					// 先记录已知结果，避免客户端收到结束事件后取消请求改变统计。
					usage.publish(ctx, streamErr)
					for _, frame := range [][]byte{completionDelta, completionStop} {
						if len(frame) > 0 && !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
							return
						}
					}
					for _, frame := range frames {
						if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
							return
						}
					}
					if streamErr != nil {
						_ = sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Err: streamErr})
					}
					return
				}
				if chunk.Err != nil {
					streamErr = chunk.Err
					usage.publish(ctx, streamErr)
					if deframer != nil {
						if tail := deframer.flush(); len(tail) > 0 {
							for _, frame := range a.translateExecutorStreamPayload(ctx, prepared, tail, &param) {
								if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
									return
								}
							}
						}
					}
					_ = sendExecutorPluginStreamChunk(ctx, out, chunk)
					return
				}
				// usage 必须观察原始分片（如不带换行符的完整 JSON 块），而翻译接收 deframed 事件。
				usage.observe(chunk.Payload)
				if deframer != nil {
					for _, payload := range deframer.feed(chunk.Payload) {
						if len(payload) == 0 {
							continue
						}
						if !processPayload(payload) {
							return
						}
					}
				} else {
					if !processPayload(chunk.Payload) {
						return
					}
				}
			}
		}
	}()
	return out
}

func executorClaudeCompletionEvent(frame []byte) string {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		payload := helps.ExtractStreamJSONPayload(line)
		switch event := ojson.GetBytes(payload, "type").String(); event {
		case "message_delta", "message_stop":
			return event
		}
	}
	return ""
}

func (a *executorAdapter) translatedExecutorStreamTail(ctx context.Context, prepared preparedExecutorCall, param *any) [][]byte {
	tail := executorStreamDonePayload(prepared.outputFormat)
	if len(tail) == 0 {
		return nil
	}
	return a.translateExecutorStreamPayload(ctx, prepared, tail, param)
}

// finalizeExecutorStreamFailure 在本地转换失败后有界地继续读取上游，只为捕获
// 独立的 usage 尾帧；随后发布唯一一次失败结算，再取消派生执行 ctx——取消经
// watch 协程触发插件 CancelExecution。收尾不跟随请求 ctx：客户端收到失败终止
// 后取消请求是常态，不能反过来掐断仍在产出尾帧的上游；缓冲中的历史用量也不
// 能作为收口依据，退出只由源关闭、上游错误或窗口超时驱动。
func finalizeExecutorStreamFailure(ctx context.Context, cancelStream context.CancelFunc, in <-chan pluginapi.ExecutorStreamChunk, usage *formalPluginStreamUsage, streamErr error) {
	defer func() {
		if cancelStream != nil {
			cancelStream()
		}
	}()
	defer func() { usage.publish(ctx, streamErr) }()
	timer := time.NewTimer(pluginStreamUsageTailWindow)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case chunk, ok := <-in:
			if !ok || chunk.Err != nil {
				return
			}
			usage.observe(chunk.Payload)
		}
	}
}

func (a *executorAdapter) settleAndTranslateExecutorResponse(
	ctx context.Context,
	prepared preparedExecutorCall,
	pluginReq pluginapi.ExecutorRequest,
	pluginResp pluginapi.ExecutorResponse,
	reporter *helps.UsageReporter,
) ([]byte, error) {
	var translated []byte
	if isClaudePluginOpenAI(prepared) {
		checked := &claudecommon.PluginResponseState{Chat: prepared.requestedFormat == sdktranslator.FormatOpenAI}
		var param any = checked
		translated = a.translateExecutorResponse(ctx, prepared, pluginResp.Payload, false, &param)
		if checked.Err != nil || !validClaudePluginOpenAIJSON(prepared, translated) {
			err := claudePluginConversionError()
			if reporter != nil {
				if pluginExecutorUsageReported(prepared.outputFormat, pluginResp.Payload) {
					reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
				}
				reporter.PublishFailureWithDetail(ctx, helps.ParsePluginExecutorResponseUsage(prepared.outputFormat.String(), pluginResp.Payload), err)
			}
			return nil, err
		}
	}
	if translated == nil && prepared.requestedFormat != "" && prepared.outputFormat != prepared.requestedFormat {
		// Convert before reporting success: a translator tool-input failure or
		// empty output is a request-scoped failure of a completed plugin call.
		var param any
		translated = a.translateExecutorResponse(ctx, prepared, pluginResp.Payload, false, &param)
		if helps.ApplyPatchTranslationError(param) != nil || len(translated) == 0 {
			err := pluginResponseConversionError()
			if reporter != nil {
				if pluginExecutorUsageReported(prepared.outputFormat, pluginResp.Payload) {
					reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
				}
				reporter.PublishFailureWithDetail(ctx, helps.ParsePluginExecutorResponseUsage(prepared.outputFormat.String(), pluginResp.Payload), err)
			}
			return nil, err
		}
	}
	if reporter != nil {
		if pluginExecutorUsageReported(prepared.outputFormat, pluginResp.Payload) {
			reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
		}
		reporter.Publish(ctx, helps.ParsePluginExecutorResponseUsage(prepared.outputFormat.String(), pluginResp.Payload))
		reporter.EnsurePublished(ctx)
	}
	if translated == nil {
		translated = a.translateExecutorResponse(ctx, prepared, pluginResp.Payload, false, nil)
	}
	translated = a.finalizeZcodeExecuteResponse(ctx, prepared, pluginReq, translated)
	return translated, nil
}

// prepareExecutorStreamExecutionContext derives the plugin stream execution
// context and bootstrap caller-cancellation bridge when translation/settlement
// owns cleanup; pure passthrough streams keep caller ctx directly.
func (a *executorAdapter) prepareExecutorStreamExecutionContext(ctx context.Context, prepared preparedExecutorCall) (context.Context, context.CancelFunc, func()) {
	streamCtx := ctx
	if !isClaudePluginOpenAI(prepared) && a.executorStreamPassthrough(prepared) {
		return streamCtx, nil, nil
	}
	if streamCtx == nil {
		streamCtx = context.Background()
	}
	if isClaudePluginOpenAI(prepared) {
		streamCtx, cancelStream := context.WithCancel(streamCtx)
		return streamCtx, cancelStream, nil
	}
	streamCtx, cancelStream := context.WithCancel(context.WithoutCancel(streamCtx))
	var stopCallerBridge func()
	// 建流桥接：插件 ExecuteStream 返回前转换协程尚未接管，请求取消
	// 必须显式转发到解耦的执行 ctx；桥接在插件调用返回后立即解除，
	// 此后由跟随请求 ctx 的转换协程负责清理。
	if ctx != nil && ctx.Done() != nil {
		bridgeStop := make(chan struct{})
		var bridgeOnce sync.Once
		stopCallerBridge = func() {
			bridgeOnce.Do(func() { close(bridgeStop) })
		}
		go func() {
			select {
			case <-ctx.Done():
				cancelStream()
			case <-bridgeStop:
			}
		}()
	}
	return streamCtx, cancelStream, stopCallerBridge
}

func (a *executorAdapter) settleAndTranslateExecutorStream(
	ctx context.Context,
	streamCtx context.Context,
	cancelStream context.CancelFunc,
	pluginReq pluginapi.ExecutorRequest,
	prepared preparedExecutorCall,
	reporter *helps.UsageReporter,
	pluginResp pluginapi.ExecutorStreamResponse,
	cancelStreamToPlugin func(),
) *coreexecutor.StreamResult {
	if isClaudePluginOpenAI(prepared) {
		parent := ctx
		if parent == nil {
			parent = context.Background()
		}
		chunks := a.translateClaudePluginStream(parent, cancelStream, pluginReq, prepared, reporter, pluginResp.Chunks, cancelStreamToPlugin)
		return &coreexecutor.StreamResult{Headers: cloneHeader(pluginResp.Headers), Chunks: mapExecutorStreamChunks(parent, chunks)}
	}
	if !a.executorStreamPassthrough(prepared) {
		// 所有非直通流（跨格式、同格式 OpenAIResponse、含流转换器或响应 hook 的同格式）
		// 统一走单协程结算与转换路径，避免上游先关闭时抢先发布成功；
		// 仅纯直通流落入下方的 observeFormalPluginStreamUsage。
		pluginChunks := a.watchExecutorStreamCancellationShared(streamCtx, pluginReq, pluginResp.Chunks, cancelStreamToPlugin)
		// 转换协程的 select/发送跟随请求 ctx（带 reporter 值）；插件与 watch
		// 留在与请求取消解耦的执行 ctx 上，收尾期间不受客户端取消影响。
		convCtx := ctx
		if reporter != nil {
			convCtx = helps.WithUsageReporter(ctx, reporter)
		}
		translated := a.translateExecutorStreamChunksWithUsage(convCtx, prepared, pluginChunks, reporter, cancelStream)
		return &coreexecutor.StreamResult{Headers: cloneHeader(pluginResp.Headers), Chunks: mapExecutorStreamChunks(ctx, translated)}
	}
	rawPluginChunks := observeFormalPluginStreamUsage(streamCtx, reporter, prepared.outputFormat, pluginResp.Chunks)
	pluginChunks := a.watchExecutorStreamCancellationShared(streamCtx, pluginReq, rawPluginChunks, cancelStreamToPlugin)
	return &coreexecutor.StreamResult{
		Headers: cloneHeader(pluginResp.Headers),
		// 转换协程同样跟随请求 ctx：客户端取消要能终止转换与上游执行。
		Chunks: mapExecutorStreamChunks(ctx, a.translateExecutorStreamChunksWithUsage(ctx, prepared, pluginChunks, nil, cancelStream)),
	}
}

func (a *executorAdapter) watchExecutorStreamCancellation(ctx context.Context, req pluginapi.ExecutorRequest, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	return a.watchExecutorStreamCancellationShared(ctx, req, in, nil)
}

// watchExecutorStreamCancellationShared 允许与 bootstrap watch 共享同一个
// 请求级取消闭包（pluginCancelOnce）：两个 watch 对同一 streamCtx 终止各持
// once 仍会双发；共享后重复取消在请求层被吸收。
func (a *executorAdapter) watchExecutorStreamCancellationShared(ctx context.Context, req pluginapi.ExecutorRequest, in <-chan pluginapi.ExecutorStreamChunk, shared func()) <-chan pluginapi.ExecutorStreamChunk {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	if in == nil {
		close(out)
		return out
	}
	sendCancel := shared
	if sendCancel == nil {
		sendCancel = a.pluginCancelOnce(ctx, req)
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				sendCancel()
				return
			case chunk, ok := <-in:
				if !ok {
					if ctx.Err() != nil {
						sendCancel()
					}
					return
				}
				select {
				case <-ctx.Done():
					sendCancel()
					return
				case out <- chunk:
				}
			}
		}
	}()
	return out
}
