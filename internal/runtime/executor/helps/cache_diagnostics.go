package helps

import (
	"context"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"net/http"
)

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
	attempt := r.diagnosticExecution.Begin("websocket", diagnostics.EndpointKind(endpoint), body, headers)
	r.sizeMu.Lock()
	r.diagnosticWebsocketAttempt = attempt
	r.sizeMu.Unlock()
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
