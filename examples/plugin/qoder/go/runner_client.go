package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type runnerRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	ID              string `json:"id"`
	Method          string `json:"method"`
	Params          any    `json:"params,omitempty"`
}

type runnerResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *runnerError    `json:"error,omitempty"`
}

type runnerError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type runnerFrame struct {
	ProtocolVersion int                    `json:"protocol_version"`
	Type            string                 `json:"type"`
	RequestID       string                 `json:"request_id,omitempty"`
	Event           pluginapi.AgentEventV1 `json:"event,omitempty"`
	ID              string                 `json:"id,omitempty"`
	OK              bool                   `json:"ok,omitempty"`
	Result          json.RawMessage        `json:"result,omitempty"`
	Error           *runnerError           `json:"error,omitempty"`
}

type runnerHandshake struct {
	Runner          string   `json:"runner"`
	RunnerVersion   string   `json:"runner_version"`
	ProtocolVersion int      `json:"protocol_version"`
	SDKVersion      string   `json:"sdk_version"`
	Transport       string   `json:"transport"`
	Capabilities    []string `json:"capabilities"`
}

type runnerClient struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
	pending     map[string]chan runnerResponse
	events      chan pluginapi.AgentEventV1
	done        chan struct{}
	waitErr     error
	waitMu      sync.Mutex
	seq         atomic.Uint64
	stopOnce    sync.Once
	runtimeRoot string
}

func newRunnerClient(cfg pluginConfig, auth qoderAuth, extraEnv map[string]string, requestedTransport ...string) (*runnerClient, error) {
	transport := auth.Transport
	if len(requestedTransport) > 0 && strings.TrimSpace(requestedTransport[0]) != "" {
		transport = strings.ToLower(strings.TrimSpace(requestedTransport[0]))
	}
	if transport == "" {
		transport = cfg.Transport
	}
	if transport == "" {
		transport = "sdk_cli"
	}
	if transport != "sdk_cli" && transport != "direct_openai" {
		return nil, newPluginCallError("invalid_transport", "Qoder transport is unsupported", http.StatusBadRequest, false)
	}
	runtimeRoot, errRuntimeRoot := os.MkdirTemp(cfg.WorkingDirectory, ".cpa-qoder-runner-")
	if errRuntimeRoot != nil {
		return nil, newPluginCallError("runner_unavailable", "Qoder runner private runtime directory could not be created", http.StatusServiceUnavailable, true)
	}
	cleanupRuntimeRoot := true
	defer func() {
		if cleanupRuntimeRoot {
			_ = os.RemoveAll(runtimeRoot)
		}
	}()
	if auth.AuthMode == "pat" {
		if errHome := os.Mkdir(filepath.Join(runtimeRoot, "home"), 0o700); errHome != nil {
			return nil, newPluginCallError("runner_unavailable", "Qoder runner private home could not be created", http.StatusServiceUnavailable, true)
		}
	}
	args := append([]string(nil), cfg.RunnerArgs...)
	args = append(args, "--stdio", "--transport", transport, "--cwd", cfg.WorkingDirectory, "--max-queue-frames", strconv.Itoa(cfg.MaxQueueFrames))
	if cfg.OpenAPIEndpoint != "" {
		args = append(args, "--openapi-endpoint", cfg.OpenAPIEndpoint)
	}
	if cfg.OpenAPIUserAgent != "" {
		args = append(args, "--openapi-user-agent", cfg.OpenAPIUserAgent)
	}
	if transport == "sdk_cli" {
		args = append(args, "--cli-path", cfg.QoderCLIPath)
	} else {
		if cfg.DirectEndpoint != "" {
			args = append(args, "--direct-endpoint", cfg.DirectEndpoint)
		}
		if cfg.DirectModelsEndpoint != "" {
			args = append(args, "--direct-models-endpoint", cfg.DirectModelsEndpoint)
		}
		if cfg.DirectAuthEndpoint != "" {
			args = append(args, "--direct-auth-endpoint", cfg.DirectAuthEndpoint)
		}
		if cfg.DirectTokenMode != "" {
			args = append(args, "--direct-token-mode", cfg.DirectTokenMode)
		}
		if modelsJSON, errModels := directModelsJSON(cfg.DirectModels); errModels != nil {
			return nil, newPluginCallError("invalid_config", errModels.Error(), http.StatusInternalServerError, false)
		} else if modelsJSON != "" {
			args = append(args, "--direct-models-json", modelsJSON)
		}
	}
	cmd := exec.Command(cfg.RunnerCommand, args...)
	configureRunnerProcess(cmd)
	cmd.Env = runnerEnvironment(auth, extraEnv, runtimeRoot)
	stdin, errStdin := cmd.StdinPipe()
	if errStdin != nil {
		return nil, fmt.Errorf("open Qoder runner stdin")
	}
	stdout, errStdout := cmd.StdoutPipe()
	if errStdout != nil {
		return nil, fmt.Errorf("open Qoder runner stdout")
	}
	stderr, errStderr := cmd.StderrPipe()
	if errStderr != nil {
		return nil, fmt.Errorf("open Qoder runner stderr")
	}
	client := &runnerClient{
		cmd: cmd, stdin: stdin, pending: make(map[string]chan runnerResponse),
		events: make(chan pluginapi.AgentEventV1, cfg.MaxQueueFrames), done: make(chan struct{}), runtimeRoot: runtimeRoot,
	}
	if errStart := cmd.Start(); errStart != nil {
		return nil, newPluginCallError("runner_unavailable", "Qoder runner could not be started", http.StatusServiceUnavailable, true)
	}
	go client.readLoop(stdout)
	go client.drainStderr(stderr, auth.tokenSource())
	go client.waitLoop()
	cleanupRuntimeRoot = false
	return client, nil
}

func (c *runnerClient) handshake(ctx context.Context, requestedTransport ...string) (runnerHandshake, error) {
	var result runnerHandshake
	if errCall := c.call(ctx, "handshake", map[string]any{}, &result); errCall != nil {
		return runnerHandshake{}, errCall
	}
	if strings.TrimSpace(result.Transport) == "" {
		// Runner protocol v1 predates explicit transport negotiation. Preserve
		// compatibility for existing SDK/CLI runners while still rejecting an
		// old runner when a direct transport was explicitly requested below.
		result.Transport = "sdk_cli"
	}
	if result.Runner != "cpa-qoder-runner" || result.ProtocolVersion != runnerProtocol || result.RunnerVersion == "" || result.Transport != "sdk_cli" && result.Transport != "direct_openai" {
		return runnerHandshake{}, newPluginCallError("runner_version_mismatch", "Qoder runner handshake is incompatible", http.StatusServiceUnavailable, false)
	}
	if len(requestedTransport) > 0 && strings.TrimSpace(requestedTransport[0]) != "" && result.Transport != strings.ToLower(strings.TrimSpace(requestedTransport[0])) {
		return runnerHandshake{}, newPluginCallError("runner_version_mismatch", "Qoder runner transport does not match the selected auth transport", http.StatusServiceUnavailable, false)
	}
	return result, nil
}

func (c *runnerClient) call(ctx context.Context, method string, params any, output any) error {
	id := strconv.FormatUint(c.seq.Add(1), 10)
	response := make(chan runnerResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = response
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()
	raw, errMarshal := json.Marshal(runnerRequest{ProtocolVersion: runnerProtocol, ID: id, Method: method, Params: params})
	if errMarshal != nil {
		return newPluginCallError("runner_protocol_error", "Qoder runner request could not be encoded", http.StatusInternalServerError, false)
	}
	if len(raw) > maxRunnerFrameBytes {
		return newPluginCallError("runner_frame_too_large", "Qoder runner request exceeds the bounded frame limit", http.StatusRequestEntityTooLarge, false)
	}
	c.writeMu.Lock()
	_, errWrite := c.stdin.Write(append(raw, '\n'))
	c.writeMu.Unlock()
	if errWrite != nil {
		return newPluginCallError("runner_lost", "Qoder runner connection was lost", http.StatusServiceUnavailable, true)
	}
	select {
	case <-ctx.Done():
		return newPluginCallError("runner_timeout", "Qoder runner request timed out", http.StatusGatewayTimeout, true)
	case <-c.done:
		return newPluginCallError("runner_lost", "Qoder runner exited", http.StatusServiceUnavailable, true)
	case resp := <-response:
		if !resp.OK {
			return runnerCallError(resp.Error)
		}
		if output != nil && len(resp.Result) > 0 {
			if errDecode := json.Unmarshal(resp.Result, output); errDecode != nil {
				return newPluginCallError("runner_protocol_error", "Qoder runner response is invalid", http.StatusBadGateway, true)
			}
		}
		return nil
	}
}

func (c *runnerClient) readEvent(ctx context.Context) (pluginapi.AgentEventV1, error) {
	select {
	case <-c.done:
		return pluginapi.AgentEventV1{}, newPluginCallError("runner_lost", "Qoder runner exited", http.StatusServiceUnavailable, true)
	default:
	}
	select {
	case <-ctx.Done():
		return pluginapi.AgentEventV1{}, ctx.Err()
	case <-c.done:
		return pluginapi.AgentEventV1{}, newPluginCallError("runner_lost", "Qoder runner exited", http.StatusServiceUnavailable, true)
	case event, ok := <-c.events:
		if !ok {
			return pluginapi.AgentEventV1{}, newPluginCallError("runner_lost", "Qoder runner event stream closed", http.StatusServiceUnavailable, true)
		}
		select {
		case <-c.done:
			return pluginapi.AgentEventV1{}, newPluginCallError("runner_lost", "Qoder runner exited", http.StatusServiceUnavailable, true)
		default:
		}
		return event, nil
	}
}

func (c *runnerClient) ended() bool {
	if c == nil {
		return true
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *runnerClient) shutdown() {
	c.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.call(ctx, "shutdown", map[string]any{}, nil)
		_ = c.stdin.Close()
		select {
		case <-c.done:
		case <-ctx.Done():
			_ = terminateRunnerProcess(c.cmd)
			<-c.done
		}
	})
}

func (c *runnerClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxRunnerFrameBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame runnerFrame
		if errDecode := json.Unmarshal(line, &frame); errDecode != nil || frame.ProtocolVersion != runnerProtocol {
			c.setWaitErr(errors.New("Qoder runner emitted an invalid frame"))
			_ = terminateRunnerProcess(c.cmd)
			return
		}
		switch frame.Type {
		case "response":
			c.pendingMu.Lock()
			pending := c.pending[frame.ID]
			c.pendingMu.Unlock()
			if pending != nil {
				pending <- runnerResponse{ProtocolVersion: frame.ProtocolVersion, Type: frame.Type, ID: frame.ID, OK: frame.OK, Result: frame.Result, Error: frame.Error}
			}
		case "event":
			if errValidate := frame.Event.Validate(); errValidate != nil || frame.Event.Provider != pluginIdentifier || frame.Event.RequestID != frame.RequestID {
				c.setWaitErr(errors.New("Qoder runner emitted an invalid AgentEventV1"))
				_ = terminateRunnerProcess(c.cmd)
				return
			}
			select {
			case c.events <- frame.Event:
			default:
				c.setWaitErr(errors.New("Qoder runner event queue overflow"))
				_ = terminateRunnerProcess(c.cmd)
				return
			}
		default:
			c.setWaitErr(errors.New("Qoder runner emitted an unknown frame"))
			_ = terminateRunnerProcess(c.cmd)
			return
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		c.setWaitErr(errors.New("Qoder runner output exceeded protocol bounds"))
	}
}

func (c *runnerClient) drainStderr(stderr io.Reader, secret string) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		_ = redactRunnerText(scanner.Text(), secret)
	}
}

func (c *runnerClient) waitLoop() {
	errWait := c.cmd.Wait()
	_ = terminateRunnerProcess(c.cmd)
	if c.runtimeRoot != "" {
		_ = os.RemoveAll(c.runtimeRoot)
	}
	if errWait != nil {
		c.setWaitErr(errors.New("Qoder runner exited unexpectedly"))
	}
	close(c.done)
}

func (c *runnerClient) setWaitErr(err error) {
	c.waitMu.Lock()
	if c.waitErr == nil {
		c.waitErr = err
	}
	c.waitMu.Unlock()
}

func runnerCallError(value *runnerError) error {
	if value == nil {
		return newPluginCallError("runner_error", "Qoder runner operation failed", http.StatusBadGateway, true)
	}
	status := http.StatusBadGateway
	switch value.Code {
	case "auth_expired", "auth_not_configured", "direct_auth_failed", "direct_auth_invalid":
		status = http.StatusUnauthorized
	case "quota_or_rate_limit":
		status = http.StatusTooManyRequests
	case "direct_timeout":
		status = http.StatusGatewayTimeout
	case "invalid_request", "invalid_params", "invalid_content", "invalid_configuration", "prompt_required", "content_required", "model_required", "invalid_permission_policy", "unsupported_model", "direct_invalid_request", "direct_models_invalid", "direct_request_missing":
		status = http.StatusBadRequest
	case "frame_too_large", "direct_request_too_large":
		status = http.StatusRequestEntityTooLarge
	case "turn_conflict", "session_configuration_changed":
		status = http.StatusConflict
	case "runner_quiescing", "cli_unavailable", "cli_path_required", "sdk_cli_version_mismatch", "sdk_auth_config", "sdk_auth_payload_incompatible", "direct_endpoint_required", "direct_endpoint_invalid", "direct_auth_config", "models_unavailable", "models_schema_invalid":
		status = http.StatusServiceUnavailable
	}
	message := strings.TrimSpace(value.Message)
	if message == "" {
		message = "Qoder runner operation failed"
	}
	return newPluginCallError(value.Code, message, status, value.Retryable)
}

func runnerEnvironment(auth qoderAuth, extra map[string]string, runtimeRoot string) []string {
	allowed := map[string]struct{}{
		"PATH": {}, "HOME": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {}, "LANG": {}, "LC_ALL": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {}, "http_proxy": {}, "https_proxy": {}, "no_proxy": {},
		"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {}, "NODE_EXTRA_CA_CERTS": {},
	}
	env := make([]string, 0, len(allowed)+6)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if key == "TMPDIR" || key == "TMP" || key == "TEMP" || auth.AuthMode == "pat" && key == "HOME" {
			continue
		}
		if _, keep := allowed[key]; ok && keep {
			env = append(env, item)
		}
	}
	env = append(env,
		"QODER_SKIP_DOWNLOAD=1",
		"TMPDIR="+runtimeRoot,
		"TMP="+runtimeRoot,
		"TEMP="+runtimeRoot,
	)
	if auth.AuthMode == "pat" {
		env = append(env,
			"HOME="+filepath.Join(runtimeRoot, "home"),
			runnerPATEnv+"="+auth.tokenSource(),
		)
	} else if auth.ConfigDir != "" {
		env = append(env, "QODER_CONFIG_DIR="+auth.ConfigDir)
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func redactRunnerText(text, secret string) string {
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[REDACTED_SECRET]")
	}
	for _, marker := range []string{"authorization", "access_token", "api_key", "secret", "password"} {
		lower := strings.ToLower(text)
		if index := strings.Index(lower, marker); index >= 0 {
			return text[:index] + marker + "=[REDACTED_SECRET]"
		}
	}
	if len(text) > 2000 {
		text = text[:2000]
	}
	return text
}
