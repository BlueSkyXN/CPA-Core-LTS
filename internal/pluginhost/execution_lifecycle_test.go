package pluginhost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type executionCancellerFunc func(context.Context, pluginapi.CancelExecutionRequest) error

func (f executionCancellerFunc) CancelExecution(ctx context.Context, req pluginapi.CancelExecutionRequest) error {
	return f(ctx, req)
}

type executionSessionCloserFunc func(context.Context, pluginapi.CloseExecutionSessionRequest) error

func (f executionSessionCloserFunc) CloseExecutionSession(ctx context.Context, req pluginapi.CloseExecutionSessionRequest) error {
	return f(ctx, req)
}

type providerReadinessFunc func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error)

func (f providerReadinessFunc) ProbeReadiness(ctx context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
	return f(ctx, req)
}

func TestRPCCapabilitiesIncludeExecutionLifecycle(t *testing.T) {
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		ExecutionCanceller: executionCancellerFunc(func(context.Context, pluginapi.CancelExecutionRequest) error {
			return nil
		}),
		ExecutionSessionCloser: executionSessionCloserFunc(func(context.Context, pluginapi.CloseExecutionSessionRequest) error {
			return nil
		}),
		ProviderReadiness: providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
			return pluginapi.ReadinessResponse{}, nil
		}),
	}}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.ExecutionCanceller || !caps.ExecutionSessionCloser || !caps.ProviderReadiness {
		t.Fatalf("lifecycle capabilities = %#v, want all enabled", caps)
	}
	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("marshal lifecycle capabilities: %v", errMarshal)
	}
	var fields map[string]any
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil {
		t.Fatalf("unmarshal lifecycle capabilities: %v", errUnmarshal)
	}
	for _, field := range []string{"execution_canceller", "execution_session_closer", "provider_readiness"} {
		if fields[field] != true {
			t.Fatalf("capability %s = %#v, want true", field, fields[field])
		}
	}
}

func TestRegisterRPCPluginAttachesExecutionLifecycleAdapters(t *testing.T) {
	plugin := validTestPlugin("execution-lifecycle")
	plugin.Capabilities.ExecutionCanceller = executionCancellerFunc(func(context.Context, pluginapi.CancelExecutionRequest) error {
		return nil
	})
	plugin.Capabilities.ExecutionSessionCloser = executionSessionCloserFunc(func(context.Context, pluginapi.CloseExecutionSessionRequest) error {
		return nil
	})
	plugin.Capabilities.ProviderReadiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		return pluginapi.ReadinessResponse{}, nil
	})
	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})

	registered, errRegister := registerRPCPlugin(context.Background(), nil, "execution-lifecycle", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if registered.Capabilities.ExecutionCanceller == nil || registered.Capabilities.ExecutionSessionCloser == nil || registered.Capabilities.ProviderReadiness == nil {
		t.Fatalf("registered lifecycle capabilities = %#v", registered.Capabilities)
	}
}

func TestRegisterRPCPluginIgnoresExecutionLifecycleCapabilitiesBeforeSchema5(t *testing.T) {
	plugin := validTestPlugin("legacy-execution-lifecycle")
	plugin.Capabilities.ExecutionCanceller = executionCancellerFunc(func(context.Context, pluginapi.CancelExecutionRequest) error {
		return nil
	})
	plugin.Capabilities.ExecutionSessionCloser = executionSessionCloserFunc(func(context.Context, pluginapi.CloseExecutionSessionRequest) error {
		return nil
	})
	plugin.Capabilities.ProviderReadiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		return pluginapi.ReadinessResponse{}, nil
	})
	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	lookup.schemaVersion = pluginabi.SchemaVersionWebSocketResponseObserver

	registered, errRegister := registerRPCPlugin(context.Background(), nil, "legacy-execution-lifecycle", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if registered.Capabilities.ExecutionCanceller != nil || registered.Capabilities.ExecutionSessionCloser != nil || registered.Capabilities.ProviderReadiness != nil {
		t.Fatalf("legacy schema unexpectedly attached lifecycle capabilities: %#v", registered.Capabilities)
	}
}

func TestExecutorRequestLifecycleFieldsRespectSchemaVersion(t *testing.T) {
	req := pluginapi.ExecutorRequest{
		RequestID:          "request-1",
		ExecutionSessionID: "session-1",
		CallerScope:        "caller-1",
		WorkspaceIdentity:  "workspace-1",
		AuthID:             "auth-1",
		AuthIndex:          "index-1",
	}

	legacy := (&rpcPluginAdapter{schemaVersion: pluginabi.SchemaVersionWebSocketResponseObserver}).executorRequestForSchema(req)
	rawLegacy, errMarshalLegacy := json.Marshal(rpcExecutorRequest{ExecutorRequest: legacy})
	if errMarshalLegacy != nil {
		t.Fatalf("marshal legacy executor request: %v", errMarshalLegacy)
	}
	var legacyFields map[string]json.RawMessage
	if errUnmarshalLegacy := json.Unmarshal(rawLegacy, &legacyFields); errUnmarshalLegacy != nil {
		t.Fatalf("unmarshal legacy executor request: %v", errUnmarshalLegacy)
	}
	for _, field := range []string{"RequestID", "ExecutionSessionID", "CallerScope", "WorkspaceIdentity", "AuthIndex"} {
		if _, exists := legacyFields[field]; exists {
			t.Fatalf("legacy executor request unexpectedly contains %s: %s", field, rawLegacy)
		}
	}
	if _, exists := legacyFields["AuthID"]; !exists {
		t.Fatalf("legacy executor request lost AuthID: %s", rawLegacy)
	}

	current := (&rpcPluginAdapter{schemaVersion: pluginabi.SchemaVersionExecutionLifecycle}).executorRequestForSchema(req)
	rawCurrent, errMarshalCurrent := json.Marshal(rpcExecutorRequest{ExecutorRequest: current})
	if errMarshalCurrent != nil {
		t.Fatalf("marshal current executor request: %v", errMarshalCurrent)
	}
	var currentFields map[string]json.RawMessage
	if errUnmarshalCurrent := json.Unmarshal(rawCurrent, &currentFields); errUnmarshalCurrent != nil {
		t.Fatalf("unmarshal current executor request: %v", errUnmarshalCurrent)
	}
	for _, field := range []string{"RequestID", "ExecutionSessionID", "CallerScope", "WorkspaceIdentity", "AuthIndex"} {
		if _, exists := currentFields[field]; !exists {
			t.Fatalf("current executor request missing %s: %s", field, rawCurrent)
		}
	}
}

type executionLifecycleRPCClient struct {
	methods   []string
	cancelReq pluginapi.CancelExecutionRequest
	closeReq  pluginapi.CloseExecutionSessionRequest
	readiness pluginapi.ReadinessRequest
}

func (c *executionLifecycleRPCClient) Call(_ context.Context, method string, request []byte) ([]byte, error) {
	c.methods = append(c.methods, method)
	switch method {
	case pluginabi.MethodExecutorCancel:
		if errUnmarshal := json.Unmarshal(request, &c.cancelReq); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return marshalRPCResult(rpcEmptyResponse{})
	case pluginabi.MethodExecutorCloseSession:
		if errUnmarshal := json.Unmarshal(request, &c.closeReq); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return marshalRPCResult(rpcEmptyResponse{})
	case pluginabi.MethodExecutorReadiness:
		if errUnmarshal := json.Unmarshal(request, &c.readiness); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return marshalRPCResult(pluginapi.ReadinessResponse{Provider: c.readiness.Provider, Ready: true})
	default:
		return nil, &rpcError{Code: "unexpected_method", message: method}
	}
}

func (c *executionLifecycleRPCClient) Shutdown() {}

func TestRPCExecutionLifecycleMethods(t *testing.T) {
	client := &executionLifecycleRPCClient{}
	adapter := &rpcPluginAdapter{client: client, schemaVersion: pluginabi.SchemaVersionExecutionLifecycle}
	cancelReq := pluginapi.CancelExecutionRequest{RequestID: "request-1", ExecutionSessionID: "session-1"}
	if errCancel := adapter.CancelExecution(context.Background(), cancelReq); errCancel != nil {
		t.Fatalf("CancelExecution() error = %v", errCancel)
	}
	closeReq := pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeAuth, Provider: "qoder", AuthID: "auth-1"}
	if errClose := adapter.CloseExecutionSession(context.Background(), closeReq); errClose != nil {
		t.Fatalf("CloseExecutionSession() error = %v", errClose)
	}
	readinessReq := pluginapi.ReadinessRequest{Provider: "qoder", AuthID: "auth-1"}
	readiness, errReadiness := adapter.ProbeReadiness(context.Background(), readinessReq)
	if errReadiness != nil {
		t.Fatalf("ProbeReadiness() error = %v", errReadiness)
	}
	if client.cancelReq != cancelReq || client.closeReq != closeReq || !reflect.DeepEqual(client.readiness, readinessReq) {
		t.Fatalf("captured lifecycle requests = cancel:%#v close:%#v readiness:%#v", client.cancelReq, client.closeReq, client.readiness)
	}
	if readiness.Provider != "qoder" || !readiness.Ready {
		t.Fatalf("readiness response = %#v, want qoder ready", readiness)
	}
	wantMethods := []string{pluginabi.MethodExecutorCancel, pluginabi.MethodExecutorCloseSession, pluginabi.MethodExecutorReadiness}
	if len(client.methods) != len(wantMethods) {
		t.Fatalf("methods = %#v, want %#v", client.methods, wantMethods)
	}
	for index := range wantMethods {
		if client.methods[index] != wantMethods[index] {
			t.Fatalf("methods = %#v, want %#v", client.methods, wantMethods)
		}
	}
}
