package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (m *Manager) withCacheDiagnostics(ctx context.Context, req exec.Request, opts exec.Options) context.Context {
	if m == nil || m.cacheDiagnostics == nil {
		return ctx
	}
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if cfg == nil || !cfg.UsageStatisticsEnabled {
		return ctx
	}
	requestID := stringMetadataValue(opts.Metadata, exec.RequestIDMetadataKey)
	caller := stringMetadataValue(opts.Metadata, exec.CallerScopeMetadataKey)
	session := stringMetadataValue(opts.Metadata, exec.CanonicalSessionIDMetadataKey)
	body := opts.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}
	ctx = m.cacheDiagnostics.Begin(ctx, requestID, caller, session, req.Model, opts.SourceFormat.String(), body)
	if !cfg.Routing.SessionAffinity {
		diagnostics.Record(ctx, diagnostics.Event{Layer: "account", Method: "session_affinity", Result: "not_run", Reason: "strategy_disabled"})
	}
	return ctx
}

// CacheDiagnostics returns a detached snapshot and never refreshes its retention.
func (m *Manager) CacheDiagnostics(id string) (diagnostics.Trace, bool) {
	if m == nil {
		return diagnostics.Trace{}, false
	}
	return m.cacheDiagnostics.Get(id)
}
func (m *Manager) CacheDiagnosticsOverview() diagnostics.Overview {
	if m == nil {
		return diagnostics.Overview{}
	}
	return m.cacheDiagnostics.Overview()
}

func (m *Manager) LookupCacheDiagnostics(q diagnostics.TraceQuery) ([]diagnostics.TraceRow, string) {
	if m == nil {
		return []diagnostics.TraceRow{}, ""
	}
	return m.cacheDiagnostics.Lookup(q)
}

func recordCacheSelection(ctx context.Context, auth *Auth, opts exec.Options, err error) {
	event := diagnostics.Event{Layer: "account", Method: "selection", Result: "selected", SelectionID: selectionIDFromOptions(opts)}
	if err != nil {
		event.Result = "failed"
	} else if auth != nil {
		event.AuthIndex = sessionAffinityAuthLogID(auth)
	}
	diagnostics.Record(ctx, event)
}
