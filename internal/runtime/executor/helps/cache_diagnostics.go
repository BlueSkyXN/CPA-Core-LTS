package helps

import (
	"context"
	"net/http"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type attemptCacheUsage struct {
	mu          sync.Mutex
	observation *usage.CacheObservation
}

func (r *UsageReporter) trackAttemptCacheUsage(attempt *diagnostics.AttemptRef, websocket bool) *diagnostics.AttemptRef {
	meter := &attemptCacheUsage{}
	attempt = attempt.WithUsageObserver(r.provider, func(update *usage.CacheObservation) {
		meter.mu.Lock()
		defer meter.mu.Unlock()
		meter.observation = usage.MergeCacheObservations(meter.observation, update)
	})
	r.sizeMu.Lock()
	defer r.sizeMu.Unlock()
	r.attemptCacheUsage = meter
	r.diagnosticWebsocketAttempt = nil
	if websocket {
		r.diagnosticWebsocketAttempt = attempt
	}
	return attempt
}

func (r *UsageReporter) cacheUsageSnapshot() *usage.CacheObservation {
	r.sizeMu.Lock()
	meter := r.attemptCacheUsage
	r.sizeMu.Unlock()
	if meter == nil {
		return nil
	}
	meter.mu.Lock()
	defer meter.mu.Unlock()
	return meter.observation.Clone()
}

// DiagnosticsContext binds transform events to this usage execution without
// modifying the parent selection context shared by later retries.
func (r *UsageReporter) DiagnosticsContext(ctx context.Context) context.Context {
	if r == nil {
		return ctx
	}
	return diagnostics.WithExecution(ctx, r.requestID)
}

func (r *UsageReporter) diagnosticsInstanceID() string {
	if r == nil || r.diagnosticExecution == nil {
		return ""
	}
	return r.diagnosticExecution.Ref.InstanceID()
}

func (r *UsageReporter) diagnosticsID() string {
	if r == nil || r.diagnosticExecution == nil {
		return ""
	}
	return r.diagnosticExecution.Ref.ID()
}

func (r *UsageReporter) observeWebsocketDiagnostics(payload []byte) {
	if r == nil {
		return
	}
	r.sizeMu.Lock()
	attempt := r.diagnosticWebsocketAttempt
	r.sizeMu.Unlock()
	if attempt == nil {
		return
	}
	attempt.Observe(payload)
	switch ojson.GetBytesNoCopy(payload, "type").String() {
	case "response.completed", "response.done":
		attempt.End("completed")
	case "response.incomplete":
		attempt.End("incomplete")
	case "response.failed", "error":
		attempt.End("upstream_error")
	}
}

func (r *UsageReporter) ObserveWebsocketSendFailure() {
	if r == nil {
		return
	}
	r.sizeMu.Lock()
	attempt := r.diagnosticWebsocketAttempt
	r.sizeMu.Unlock()
	attempt.End("send_error")
}

// Duplex creates share the selected connection but own separate execution IDs.
// This observes the send without changing the existing request-size accounting.
func (r *UsageReporter) ObserveWebsocketDiagnosticsRequest(body []byte, endpoint, sessionID string) {
	if r == nil {
		return
	}
	headers := make(http.Header)
	if sessionID != "" {
		headers.Set("Session-Id", sessionID)
	}
	r.ObserveWebsocketHandshakePayload(body, endpoint, headers)
}

func (r *UsageReporter) ObserveWebsocketHandshakePayload(body []byte, endpoint string, headers http.Header) {
	if r == nil {
		return
	}
	attempt := r.diagnosticExecution.Begin("websocket", endpoint, body, headers)
	r.trackAttemptCacheUsage(attempt, true)
}
func (r *UsageReporter) SetDiagnosticsInput(body []byte) {
	if r == nil || r.diagnosticExecution == nil || r.diagnosticExecution.Ref == nil {
		return
	}
	snapshot := diagnostics.Snapshot(body)
	r.diagnosticExecution.Input = &snapshot
}

// 调用方先确定帧所属的执行，再观察原始上游数据；模型或计时观察不隐式写诊断。
func (r *UsageReporter) ObserveWebsocketDiagnostics(payload []byte) {
	r.observeWebsocketDiagnostics(payload)
}
