package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type execution struct {
	req      executorRequest
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	upstream string
	done     chan struct{}
	once     sync.Once
}
type pluginRuntime struct {
	mu         sync.Mutex
	caller     hostCaller
	accepting  bool
	active     map[string]*execution
	config     *config
	configPath string
	authOwner  string
	signer     *signer
	management managementConfig
	// 内联（单文件）账号形态：首个生效账号的凭据快照，供 reconfigure 后重建 signer。
	inlineAuth     bool
	inlineAPIKey   string
	inlineDeviceID string
}

func newRuntime(c hostCaller) *pluginRuntime {
	return &pluginRuntime{caller: c, accepting: true, active: map[string]*execution{}}
}
func (r *pluginRuntime) configuration(raw []byte) (*config, error) {
	a, err := parseAuth(raw)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a.ConfigFile, err = r.authConfigPath(a)
	if err != nil {
		return nil, err
	}
	if r.config != nil {
		if r.configPath != a.ConfigFile {
			return nil, problem(409, "single_account_only", "This plugin instance supports one configured account")
		}
		if a.inline() && (a.APIKey != r.inlineAPIKey || a.DeviceID != r.inlineDeviceID) {
			return nil, problem(409, "single_account_only", "Only one inline account is supported")
		}
		return r.config, nil
	}
	var c *config
	if a.inline() {
		// 与私有文件模式同语义的日志审计门：确认原始请求/错误体日志已关闭后，内联凭据才可加载。
		if r.management.HostLoggingDisabled == nil || !*r.management.HostLoggingDisabled {
			return nil, problem(503, "unsafe_host_logging", "Enable host_logging_disabled in the plugin management config before loading inline credentials")
		}
		c = defaultConfig()
		r.inlineAuth = true
		r.inlineAPIKey, r.inlineDeviceID = a.APIKey, a.DeviceID
		c.APIKey, c.DeviceID = a.APIKey, a.DeviceID
	} else {
		c, err = loadConfig(a.ConfigFile)
		if err != nil {
			return nil, err
		}
	}
	if err = r.management.apply(c); err != nil {
		return nil, err
	}
	r.config = c
	r.configPath = a.ConfigFile
	r.signer = &signer{apiKey: c.APIKey, endpoint: c.Endpoint}
	return c, nil
}
func (r *pluginRuntime) admit(req executorRequest, c *config) (*execution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return nil, problem(503, "unavailable", "Plugin is stopping")
	}
	// 校验与 admission 之间可能发生重配置，旧快照不能配合新签名器执行。
	if r.config != nil && r.config != c {
		return nil, problem(409, "config_changed", "Configuration changed before admission; submit again")
	}
	if len(r.active) >= c.MaxInflight {
		return nil, problem(429, "rate_limit_error", "Local concurrency limit reached")
	}
	if req.RequestID == "" || req.CallbackID == "" {
		return nil, problem(400, "invalid_request", "Host execution context is required")
	}
	owner := req.AuthID + "\x00" + req.AuthIndex
	if r.authOwner != "" && r.authOwner != owner {
		return nil, problem(409, "single_account_only", "Only one auth record is supported")
	}
	r.authOwner = owner
	for _, e := range r.active {
		if e.req.RequestID == req.RequestID && e.req.AuthID == req.AuthID {
			return nil, problem(409, "duplicate_execution", "Request is already active")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &execution{req: req, id: uuid(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	r.active[e.id] = e
	return e, nil
}
func (r *pluginRuntime) closeStream(id string) {
	if id != "" {
		_, _ = r.caller.Call("host.http.stream_close", map[string]any{"stream_id": id})
	}
}
func (r *pluginRuntime) releaseStream(e *execution, id string) {
	e.mu.Lock()
	if e.upstream != id {
		e.mu.Unlock()
		return
	}
	e.upstream = ""
	e.mu.Unlock()
	r.closeStream(id)
}
func (r *pluginRuntime) cancelOne(e *execution) {
	e.cancel()
	e.mu.Lock()
	id := e.upstream
	e.upstream = ""
	e.mu.Unlock()
	r.closeStream(id)
}
func (r *pluginRuntime) bind(e *execution, id string) error {
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		r.closeStream(id)
		return e.ctx.Err()
	}
	e.upstream = id
	e.mu.Unlock()
	return nil
}
func (r *pluginRuntime) finish(e *execution) {
	e.once.Do(func() { r.cancelOne(e); r.mu.Lock(); delete(r.active, e.id); r.mu.Unlock(); close(e.done) })
}
func matches(e *execution, q cancelRequest) bool {
	p := e.req
	return (q.Provider == "" || q.Provider == provider) && (q.RequestID == "" || q.RequestID == p.RequestID) && (q.AuthID == "" || q.AuthID == p.AuthID) && (q.AuthIndex == "" || q.AuthIndex == p.AuthIndex) && (q.ExecutionSessionID == "" || q.ExecutionSessionID == p.ExecutionSessionID) && (q.CallerScope == "" || q.CallerScope == p.CallerScope) && (q.WorkspaceIdentity == "" || q.WorkspaceIdentity == p.WorkspaceIdentity)
}
func (r *pluginRuntime) cancelMatching(q cancelRequest) {
	r.mu.Lock()
	var found []*execution
	for _, e := range r.active {
		if matches(e, q) {
			found = append(found, e)
		}
	}
	r.mu.Unlock()
	for _, e := range found {
		r.cancelOne(e)
	}
}
func (r *pluginRuntime) stop() {
	r.mu.Lock()
	r.accepting = false
	var pending []*execution
	for _, e := range r.active {
		pending = append(pending, e)
	}
	s := r.signer
	r.mu.Unlock()
	for _, e := range pending {
		r.cancelOne(e)
	}
	if s != nil {
		s.close()
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for _, e := range pending {
		select {
		case <-e.done:
		case <-timer.C:
			return
		}
	}
}

func (r *pluginRuntime) execute(req executorRequest) (any, error) {
	if req.Format != "" && req.Format != "claude" {
		return nil, problem(400, "unsupported_format", "Expected host-translated Anthropic Messages")
	}
	c, err := r.configuration(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	e, err := r.admit(req, c)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			r.finish(e)
		}
	}()
	session := sessionID(c.AccountScope, req.CallerScope, req.WorkspaceIdentity, req.ExecutionSessionID)
	body, err := transformExecution(req, c, session)
	if err != nil {
		return nil, err
	}
	stream, _ := body["stream"].(bool)
	if stream != req.Stream {
		return nil, problem(400, "invalid_request", "Payload stream flag disagrees with host")
	}
	if stream && req.StreamID == "" {
		return nil, problem(400, "invalid_request", "Missing host stream ID")
	}
	r.mu.Lock()
	s := r.signer
	r.mu.Unlock()
	signed, err := s.headers(r, e, session)
	if err != nil {
		return nil, err
	}
	headers := identityHeaders(c)
	for k, v := range signed {
		headers[k] = v
	}
	headers.Set("X-Request-Id", e.id)
	headers.Set("X-Query-Id", uuid())
	headers.Set("X-Zcode-Trace-Id", uuid())
	if err = e.ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := r.caller.Call("host.http.do_stream", httpRequest{CallbackID: req.CallbackID, Method: "POST", URL: c.Endpoint, Headers: headers, Body: encode(body), DisableRedirects: true})
	if err != nil {
		return nil, problem(502, "connection_lifecycle", "Upstream connection failed")
	}
	var upstream httpStream
	if json.Unmarshal(raw, &upstream) != nil || upstream.StreamID == "" {
		return nil, problem(502, "invalid_response", "Invalid host stream response")
	}
	if err = r.bind(e, upstream.StreamID); err != nil {
		return nil, err
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		status := upstream.StatusCode
		if status < 400 || status > 599 {
			status = 502
		}
		return nil, problem(status, "upstream_error", "Upstream rejected the request")
	}
	if stream {
		if !strings.Contains(strings.ToLower(upstream.Headers.Get("Content-Type")), "text/event-stream") {
			return nil, problem(502, "invalid_response", "Expected an event stream")
		}
		handedOff = true
		go func() {
			err := r.consume(e, upstream.StreamID, func(chunk []byte) error {
				_, err := r.caller.Call("host.stream.emit", map[string]any{"stream_id": req.StreamID, "payload": chunk})
				return err
			})
			r.finish(e)
			result := map[string]any{"stream_id": req.StreamID}
			if err != nil {
				safe := safeError(err)
				result["error"] = safe.Message
				result["error_code"] = rpcErrorCode(safe)
				result["http_status"] = safe.Status
				result["retryable"] = false
			}
			_, _ = r.caller.Call("host.stream.close", result)
		}()
		return map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}}, nil
	}
	data, err := r.readAll(e, upstream.StreamID, maxBody)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil || value["type"] != "message" {
		return nil, problem(502, "invalid_response", "Invalid upstream message")
	}
	if _, ok := value["content"].([]any); !ok {
		return nil, problem(502, "invalid_response", "Invalid upstream content")
	}
	return map[string]any{"Payload": data, "Headers": http.Header{"Content-Type": []string{"application/json"}}}, nil
}
func (r *pluginRuntime) readChunk(e *execution, id string) (httpChunk, error) {
	if err := e.ctx.Err(); err != nil {
		return httpChunk{}, err
	}
	raw, err := r.caller.Call("host.http.stream_read", map[string]any{"stream_id": id})
	if err != nil {
		return httpChunk{}, problem(502, "connection_lifecycle", "Upstream read failed")
	}
	var c httpChunk
	if json.Unmarshal(raw, &c) != nil || c.Error != "" {
		return c, problem(502, "connection_lifecycle", "Upstream read failed")
	}
	if err = e.ctx.Err(); err != nil {
		return c, err
	}
	return c, nil
}
func (r *pluginRuntime) readAll(e *execution, id string, limit int) ([]byte, error) {
	var out []byte
	for {
		c, err := r.readChunk(e, id)
		if err != nil {
			return nil, err
		}
		if len(out)+len(c.Payload) > limit {
			return nil, problem(502, "response_too_large", "Upstream response exceeds size limit")
		}
		out = append(out, c.Payload...)
		if c.Done {
			return out, nil
		}
	}
}
func (r *pluginRuntime) consume(e *execution, id string, emit func([]byte) error) error {
	p := sseParser{emit: emit}
	for {
		c, err := r.readChunk(e, id)
		if err != nil {
			return err
		}
		if err = p.feed(c.Payload); err != nil {
			return err
		}
		if p.terminal {
			return nil
		}
		if c.Done {
			return p.finish()
		}
	}
}
func modelList(c *config) []any {
	out := []any{}
	for _, m := range c.Models {
		limits := limitsFor(c, m)
		input := []string{"text"}
		if builtinModelImageInput(m) {
			input = append(input, "image")
		}
		out = append(out, map[string]any{"ID": m, "Name": m, "DisplayName": m, "Object": "model", "OwnedBy": provider, "Type": "agent", "UserDefined": true, "IsCompat": true, "ContextLength": limits.Context, "MaxCompletionTokens": limits.Output, "SupportedParameters": []string{"max_tokens", "tools", "tool_choice", "temperature", "top_p"}, "SupportedInputModalities": input, "SupportedOutputModalities": []string{"text"}, "Thinking": map[string]any{"Levels": append([]string(nil), builtinThinkingLevels...), "ZeroAllowed": false}})
	}
	return out
}
func (r *pluginRuntime) dispatch(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var req struct {
			Schema     uint32   `json:"schema_version"`
			Features   []string `json:"host_features"`
			ConfigJSON []byte   `json:"config_json"`
		}
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Schema < 6 {
			return nil, problem(400, "unsupported_host", "Host schema 6 or newer is required")
		}
		features := map[string]bool{}
		for _, feature := range req.Features {
			features[feature] = true
		}
		for _, feature := range []string{"anthropic-plugin-responses-v1", "plugin-model-compat-v1", "http-disable-redirects-v1", "sensitive-endpoints-v1", "plugin-management-v1"} {
			if !features[feature] {
				return nil, problem(400, "unsupported_host", "Required host feature is missing")
			}
		}
		cfg, err := parseManagementConfig(req.ConfigJSON)
		if err != nil {
			return nil, err
		}
		if err = r.reconfigure(cfg); err != nil {
			return nil, err
		}
		return registration(), nil
	case "plugin.quiesce", "plugin.shutdown":
		r.stop()
		return map[string]any{}, nil
	case "auth.identifier", "executor.identifier":
		return map[string]any{"identifier": provider}, nil
	case "auth.parse":
		var req struct {
			RawJSON  []byte
			FileName string
		}
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		var discriminator struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.RawJSON, &discriminator) != nil || discriminator.Type != provider {
			return map[string]any{"Handled": false}, nil
		}
		a, err := r.resolveAuth(req.RawJSON)
		if err != nil {
			return nil, err
		}
		return map[string]any{"Handled": true, "Auth": map[string]any{"Provider": provider, "Label": a.Label, "FileName": req.FileName, "StorageJSON": req.RawJSON, "Metadata": map[string]any{"type": provider, "config_file": a.ConfigFile, "request_retry": 0}}}, nil
	case "auth.refresh":
		var req struct {
			StorageJSON []byte
			AuthID      string
			Metadata    map[string]any
			Attributes  map[string]string
		}
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		a, err := r.resolveAuth(req.StorageJSON)
		if err != nil {
			return nil, err
		}
		return map[string]any{"Auth": map[string]any{"Provider": provider, "ID": req.AuthID, "StorageJSON": req.StorageJSON, "Metadata": map[string]any{"type": provider, "config_file": a.ConfigFile, "request_retry": 0}, "Attributes": req.Attributes}}, nil
	case "model.static":
		return map[string]any{"Provider": provider, "Models": []any{}}, nil
	case "model.for_auth":
		var req struct{ StorageJSON []byte }
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		c, err := r.configuration(req.StorageJSON)
		if err != nil {
			return nil, err
		}
		return map[string]any{"Provider": provider, "Models": modelList(c)}, nil
	case "executor.execute", "executor.execute_stream":
		var req executorRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if (method == "executor.execute_stream") != req.Stream {
			return nil, problem(400, "invalid_request", "Stream method mismatch")
		}
		return r.execute(req)
	case "executor.cancel", "executor.close_session":
		var q cancelRequest
		if err := decode(raw, &q); err != nil {
			return nil, err
		}
		if method == "executor.cancel" && q.RequestID == "" {
			return nil, problem(400, "invalid_cancel", "RequestID is required")
		}
		if method == "executor.close_session" {
			switch q.Scope {
			case "session":
				if q.ExecutionSessionID == "" {
					return nil, problem(400, "invalid_close", "Session ID required")
				}
			case "auth":
				if q.AuthID == "" && q.AuthIndex == "" {
					return nil, problem(400, "invalid_close", "Auth scope required")
				}
			case "provider":
				if q.Provider != provider {
					return nil, problem(400, "invalid_close", "Provider scope required")
				}
			default:
				return nil, problem(400, "invalid_close", "Unknown scope")
			}
		}
		r.cancelMatching(q)
		return map[string]any{}, nil
	case "executor.readiness":
		return r.readiness(raw)
	default:
		return nil, problem(501, "unsupported_operation", "Operation is not supported")
	}
}
