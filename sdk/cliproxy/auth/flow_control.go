package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

type flowRequestContextKey struct{}
type flowAttemptContextKey struct{}
type flowKeepAccountContextKey struct{}

func (m *Manager) configureFlowControl(cfg *internalconfig.Config) {
	if m.flowControl == nil {
		return
	}
	var err error
	if cfg.Home.Enabled && cfg.FlowControl.Enabled {
		err = errors.New("local flow-control does not run alongside Home account admission")
	} else {
		err = m.flowControl.Update(cfg.FlowControl)
	}
	if err != nil {
		m.flowControlError.Store(&flowcontrol.Error{Code: "flow_control_configuration"})
	} else {
		m.flowControlError.Store(nil)
	}
}

// legacyFlowAccountReference is only for an unmigrated version <= 2 policy.
// New policies never use remote account IDs, email, grouping metadata or key bytes.
func legacyFlowAccountReference(auth *Auth) string {
	if auth == nil {
		return "anonymous"
	}
	source := "auth:" + auth.ID
	if value, ok := auth.Metadata["account_id"].(string); ok && strings.TrimSpace(value) != "" {
		source = "account:" + strings.TrimSpace(value)
	} else if value := strings.TrimSpace(auth.Attributes["api_key"]); value != "" {
		source = "api-key:" + value
	}
	if value, ok := auth.Metadata["flow_control_group"].(string); ok && strings.TrimSpace(value) != "" {
		source = "group:" + strings.TrimSpace(value)
	}
	sum := sha256.Sum256([]byte("cpa:flow-account:v1\x00" + strings.ToLower(strings.TrimSpace(auth.Provider)) + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

// FlowAccountReference identifies exactly one Auth.ID. The old single-credential
// namespace is retained so existing references can be migrated without rotation.
func FlowAccountReference(auth *Auth) string { return FlowCredentialReference(auth) }

// FlowAccountProvider matches the executor namespace, including compatibility
// provider names. It is not inferred from a model name or an account label.
func FlowAccountProvider(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return executorKeyFromAuth(auth)
}

// FlowCredentialReference is a compatibility alias; it is not a second v3 object.
func FlowCredentialReference(auth *Auth) string {
	if auth == nil {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte("cpa:flow-credential:v1\x00" + strings.ToLower(strings.TrimSpace(auth.Provider)) + "\x00" + auth.ID))
	return hex.EncodeToString(sum[:])
}
func flowRequestID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, meta := range []map[string]any{opts.Metadata, req.Metadata} {
		if value, ok := meta[cliproxyexecutor.RequestIDMetadataKey].(string); ok {
			return value
		}
	}
	return ""
}
func flowKey(opts cliproxyexecutor.Options, req cliproxyexecutor.Request) string {
	for _, meta := range []map[string]any{opts.Metadata, req.Metadata} {
		if value, ok := meta[cliproxyexecutor.CallerScopeMetadataKey].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "anonymous"
}
func flowModel(value string) string { return strings.TrimSpace(thinking.ParseSuffix(value).ModelName) }
func flowRequestIdentity(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) flowcontrol.Identity {
	return flowcontrol.Identity{Stage: flowcontrol.Request, Key: flowKey(opts, req), Model: flowModel(requestedModelAliasFromOptions(opts, req.Model)), RequestID: flowRequestID(req, opts)}
}
func flowAttemptIdentity(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) flowcontrol.Identity {
	provider := ""
	if auth != nil {
		provider = executorKeyFromAuth(auth)
	}
	return flowcontrol.Identity{Stage: flowcontrol.Attempt, Key: flowKey(opts, req), Model: flowModel(req.Model), Provider: provider, Account: FlowAccountReference(auth), Credential: FlowCredentialReference(auth), AuthKind: auth.AuthKind(), RequestID: flowRequestID(req, opts)}
}
func (m *Manager) flowIdentity(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) flowcontrol.Identity {
	d := flowAttemptIdentity(auth, req, opts)
	if m.flowControl.Version() < 3 {
		d.Account = legacyFlowAccountReference(auth)
	} else {
		d.Credential = ""
	}
	return d
}
func (m *Manager) flowRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, *flowcontrol.Permit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || m.flowControl == nil {
		return ctx, nil, nil
	}
	if err := m.flowControlError.Load(); err != nil {
		return ctx, nil, err
	}
	if ctx.Value(flowRequestContextKey{}) != nil || !m.flowControl.Enabled() {
		return ctx, nil, nil
	}
	if gjson.GetBytes(req.Payload, "previous_response_id").String() != "" {
		ctx = context.WithValue(ctx, flowKeepAccountContextKey{}, true)
	}
	if m.flowControl.Version() >= 3 {
		permit := m.flowControl.BeginRequest(flowRequestIdentity(req, opts))
		return context.WithValue(ctx, flowRequestContextKey{}, permit), permit, nil
	}
	permit, err := m.flowControl.Acquire(ctx, flowRequestIdentity(req, opts), int64(len(req.Payload)))
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, flowRequestContextKey{}, permit), permit, nil
}

// Request slots count one logical Manager operation across its retries. Attempt
// slots below count each executor invocation, including parallel retry branches.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = ensureExecutionRequestID(req, opts)
	ctx, permit, err := m.flowRequest(ctx, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer permit.Release()
	return m.executeRequestUncontrolled(ctx, providers, req, opts)
}
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = ensureExecutionRequestID(req, opts)
	ctx, permit, err := m.flowRequest(ctx, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer permit.Release()
	return m.executeCountRequestUncontrolled(ctx, providers, req, opts)
}
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	req, opts = ensureExecutionRequestID(req, opts)
	ctx, permit, err := m.flowRequest(ctx, req, opts)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			permit.Release()
		}
	}()
	result, err := m.executeStreamRequestUncontrolled(ctx, providers, req, opts)
	if permit != nil && err == nil && result != nil && result.Chunks != nil {
		copyResult := *result
		copyResult.Chunks = flowcontrol.HoldChannel(ctx, result.Chunks, permit.Release, func() { permit.MarkPhase("draining") })
		result = &copyResult
		handedOff = true
	}
	return result, err
}

// admitFlowExecution precedes provider admission and selected-auth publication.
// Local queue errors never mark the credential unavailable or spend retry rounds.
func (m *Manager) admitFlowExecution(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, error) {
	if m == nil || m.flowControl == nil || !m.flowControl.Enabled() {
		ctx, allowed := m.activateFlowContinuity(ctx, auth)
		if !allowed {
			return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
		}
		return admitExecutorExecution(ctx, executor, auth, req, opts)
	}
	if err := m.flowControlError.Load(); err != nil {
		return ctx, err
	}
	if auth == nil {
		return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
	}
	m.mu.RLock()
	before, registered := m.auths[auth.ID]
	var generation uint64
	if before != nil {
		generation = before.generation
		if auth.generation != 0 && auth.generation != generation {
			registered = false
		}
	}
	m.mu.RUnlock()
	if !registered {
		return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
	}
	var p *flowcontrol.Permit
	var err error
	// An attempt reserved by an older/direct integration is already counted as
	// continuity evidence: do not park it in a queue. Normal v2 dispatch carries
	// an unactivated holder and can wait before continuity registration.
	_, reserved := codexRateLimitContinuityAttemptFromContext(ctx)
	requestPermit, _ := ctx.Value(flowRequestContextKey{}).(*flowcontrol.Permit)
	p, err = m.flowControl.AcquireForRequest(ctx, requestPermit, m.flowIdentity(auth, req, opts), int64(len(req.Payload)), !reserved)

	if err != nil {
		return ctx, flowAdmissionError(err)
	}
	transferred := false
	defer func() {
		if !transferred {
			p.CancelBeforeDispatch()
		}
	}()
	if registered {
		m.mu.RLock()
		current := m.auths[auth.ID]
		changed := current == nil || current.Disabled || current.Status == StatusDisabled || current.generation != generation
		if !changed {
			changed = FlowAccountReference(current) != FlowAccountReference(auth)
			if !AntigravityCreditsRequested(ctx) {
				for _, model := range []string{req.Model, authSelectionModelFromOptions(opts, req.Model)} {
					blocked, _, _ := isAuthBlockedForModel(current, model, time.Now())
					changed = changed || blocked
				}
			}
		}
		m.mu.RUnlock()
		if changed {
			p.CancelBeforeDispatch()
			return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
		}
	}
	var continuityAllowed bool
	ctx, continuityAllowed = m.activateFlowContinuity(ctx, auth)
	if !continuityAllowed {
		return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
	}
	if allowed, _ := m.codexRateLimitContinuityDispatchDisposition(ctx); !allowed {
		p.CancelBeforeDispatch()
		return ctx, wrapRequestStopError(&flowcontrol.Error{Code: "flow_control_target_changed"})
	}
	admitted, err := admitExecutorExecution(ctx, executor, auth, req, opts)
	if err != nil {
		p.CancelBeforeDispatch()
		return admitted, err
	}
	if admitted == nil {
		admitted = ctx
	}
	transferred = true
	return context.WithValue(admitted, flowAttemptContextKey{}, p), nil
}
func flowAttemptPermit(ctx context.Context) *flowcontrol.Permit {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(flowAttemptContextKey{}).(*flowcontrol.Permit)
	return p
}
func executeWithFlowSlot(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	defer flowAttemptPermit(ctx).Release()
	flowAttemptPermit(ctx).CommitDispatch()
	return executor.Execute(ctx, auth, req, opts)
}
func countWithFlowSlot(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	defer flowAttemptPermit(ctx).Release()
	flowAttemptPermit(ctx).CommitDispatch()
	return executor.CountTokens(ctx, auth, req, opts)
}
func streamWithFlowSlot(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	p := flowAttemptPermit(ctx)
	handedOff := false
	defer func() {
		if !handedOff {
			p.Release()
		}
	}()
	p.CommitDispatch()
	result, err := executor.ExecuteStream(ctx, auth, req, opts)
	if p != nil && err == nil && result != nil && result.Chunks != nil {
		copyResult := *result
		copyResult.Chunks = flowcontrol.HoldChannel(ctx, result.Chunks, p.Release, func() { p.MarkPhase("draining") })
		result = &copyResult
		handedOff = true
	}
	return result, err
}
func isLocalFlowControlError(err error) bool { return flowcontrol.IsError(err) }
func flowAdmissionError(err error) error {
	if isLocalFlowControlError(err) {
		return wrapRequestStopError(err)
	}
	return err
}
func (m *Manager) CloseFlowControl() {
	if m != nil {
		m.flowControl.Close()
	}
}
func (m *Manager) FlowControlSnapshot() flowcontrol.Snapshot {
	if m == nil {
		return flowcontrol.Snapshot{}
	}
	return m.flowControl.Snapshot()
}
func (m *Manager) FlowControlConfigurationError() bool {
	return m != nil && m.flowControlError.Load() != nil
}

// Keep existing scheduler policy. When no auth is pinned and no scheduler plugin
// overrides selection, skip visibly full accounts opportunistically. If all are
// full, queue against the original eligible selection instead of quota-cooling it.
// Availability is a hint; admission above rechecks every limit atomically.
func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	first, exec, provider, err := m.pickNextMixedUnthrottled(ctx, providers, model, opts, tried)
	if err != nil || first == nil || m.flowControl == nil || !m.flowControl.Enabled() || m.HomeEnabled() || m.hasPluginScheduler() || pinnedAuthIDFromMetadata(opts.Metadata) != "" || cliproxyexecutor.RequiredUpstreamWebsocket(ctx) || executionSessionInFlow(opts) || (ctx != nil && ctx.Value(flowKeepAccountContextKey{}) != nil) {
		return first, exec, provider, err
	}
	// Model-pool resolution advances routing offsets. Never do that for a hint.
	req := cliproxyexecutor.Request{}
	if m.flowControl.AvailableAccount(m.flowIdentity(first, req, opts)) {
		return first, exec, provider, nil
	}
	excluded := make(map[string]struct{}, len(tried)+1)
	for id := range tried {
		excluded[id] = struct{}{}
	}
	excluded[first.ID] = struct{}{}
	for count := 0; count < 63; count++ {
		next, nextExec, nextProvider, pickErr := m.pickNextMixedUnthrottled(ctx, providers, model, opts, excluded)
		if pickErr != nil || next == nil {
			break
		}
		if _, exists := excluded[next.ID]; exists {
			break
		}
		excluded[next.ID] = struct{}{}
		if m.flowControl.AvailableAccount(m.flowIdentity(next, req, opts)) {
			m.releasePreDispatchSelection(first, provider, model, opts)
			return next, nextExec, nextProvider, nil
		}
		m.releasePreDispatchSelection(next, nextProvider, model, opts)
	}
	return first, exec, provider, nil
}

func executionSessionInFlow(opts cliproxyexecutor.Options) bool {
	v, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string)
	return ok && strings.TrimSpace(v) != ""
}

// Read-only management operations do not enter the model flow-control path.
func (m *Manager) ExplainFlowControl(d flowcontrol.Identity) flowcontrol.Explanation {
	if m == nil {
		return (*flowcontrol.Engine)(nil).Explain(d)
	}
	return m.flowControl.Explain(d)
}
func (m *Manager) ServeFlowControlEvents(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "flow control unavailable", http.StatusServiceUnavailable)
		return
	}
	m.flowControl.ServeEvents(w, r)
}

// PreviewFlowMigration is read-only. Ambiguous legacy groups require an explicit
// operator decision; they are never split silently into independent limits.
func (m *Manager) PreviewFlowMigration(cfg flowcontrol.Config) flowcontrol.Migration {
	refs := []flowcontrol.AuthReference{}
	if m != nil {
		for _, a := range m.List() {
			if a != nil {
				refs = append(refs, flowcontrol.AuthReference{Legacy: legacyFlowAccountReference(a), Ref: FlowAccountReference(a), Provider: FlowAccountProvider(a), AuthKind: a.AuthKind()})
			}
		}
	}
	return flowcontrol.Migrate(cfg, refs)
}

func (m *Manager) FlowControlSummary() flowcontrol.Summary {
	if m == nil {
		return (*flowcontrol.Engine)(nil).Summary()
	}
	return m.flowControl.Summary()
}
func (m *Manager) FlowControlDetails(q flowcontrol.DetailsOptions) flowcontrol.Snapshot {
	if m == nil {
		return (*flowcontrol.Engine)(nil).Snapshot()
	}
	return m.flowControl.Details(q)
}
func (m *Manager) FlowControlPolicy() flowcontrol.Config {
	if m == nil {
		return flowcontrol.Config{}
	}
	return m.flowControl.Policy()
}
func (m *Manager) PreviewFlowControl(cfg *flowcontrol.Config, targets []flowcontrol.Identity) ([]flowcontrol.Explanation, error) {
	if m == nil || m.flowControl == nil {
		return nil, errors.New("flow control unavailable")
	}
	resolved := append([]flowcontrol.Identity(nil), targets...)
	accounts := map[string]*Auth{}
	for _, a := range m.List() {
		if a != nil {
			accounts[FlowAccountReference(a)] = a
		}
	}
	issues := make([]string, len(resolved))
	for i := range resolved {
		d := &resolved[i]
		if d.Stage != flowcontrol.Attempt || d.Account == "" || d.Account == "anonymous" {
			continue
		}
		a := accounts[d.Account]
		if a == nil || a.Disabled || a.Status == StatusDisabled {
			issues[i] = "account-unavailable"
			continue
		}
		provider := FlowAccountProvider(a)
		if d.Provider != "" && !strings.EqualFold(d.Provider, provider) {
			issues[i] = "account-provider-mismatch"
			continue
		}
		if d.AuthKind != "" && !strings.EqualFold(d.AuthKind, a.AuthKind()) {
			issues[i] = "account-auth-kind-mismatch"
			continue
		}
		d.Provider = provider
		d.AuthKind = a.AuthKind()
	}
	rows, err := m.flowControl.Preview(cfg, resolved)
	if err != nil {
		return nil, err
	}
	for i, issue := range issues {
		if issue == "" {
			continue
		}
		rows[i].Complete = false
		rows[i].CanStart = false
		rows[i].AdditionalSlots = nil
		rows[i].Unresolved = append(rows[i].Unresolved, issue)
		for j := range rows[i].Matches {
			rows[i].Matches[j].Known = false
			rows[i].Matches[j].Remaining = nil
			rows[i].Matches[j].Unresolved = append(rows[i].Matches[j].Unresolved, issue)
		}
	}
	return rows, nil
}
