package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var globalRuntime = newPluginRuntime(cgoHostCaller{})

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope(newPluginCallError("invalid_method", "plugin method is required", 400, false)))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errDispatch := globalRuntime.dispatch(C.GoString(method), requestBytes)
	if errDispatch != nil {
		raw = errorEnvelope(errDispatch)
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	globalRuntime.shutdown()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

type cgoHostCaller struct{}

func (cgoHostCaller) Call(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response", method)
	}
	var env envelope
	if errDecode := json.Unmarshal(rawResponse, &env); errDecode != nil || !env.OK || callCode != 0 {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func (r *pluginRuntime) dispatch(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := r.configure(raw); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		r.quiesce()
		return okEnvelope(struct{}{})
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginIdentifier})
	case pluginabi.MethodAuthParse:
		resp, errParse := parseAuthRequest(raw)
		if errParse != nil {
			return nil, errParse
		}
		return okEnvelope(resp)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return nil, newPluginCallError("unsupported_auth_flow", "Qoder plugin never starts login or changes credential state", http.StatusNotImplemented, false)
	case pluginabi.MethodAuthRefresh:
		resp, errRefresh := refreshAuthRequest(raw)
		if errRefresh != nil {
			return nil, errRefresh
		}
		return okEnvelope(resp)
	case pluginabi.MethodModelStatic:
		models := canonicalQoderModels()
		cfg := r.loadedConfig()
		if cfg.Transport == "direct_openai" && len(cfg.DirectModels) > 0 {
			models = configuredDirectModels(cfg.DirectModels)
		}
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier, Models: models})
	case pluginabi.MethodModelForAuth:
		resp, errModels := r.modelsForAuth(raw)
		if errModels != nil {
			return nil, errModels
		}
		return okEnvelope(resp)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{Routes: []managementRoute{{
			Method: http.MethodGet,
			Path:   "/plugins/qoder/summary",
		}}})
	case pluginabi.MethodManagementHandle:
		resp, errManagement := r.handleManagement(raw)
		if errManagement != nil {
			return nil, errManagement
		}
		return okEnvelope(resp)
	case pluginabi.MethodExecutorExecute:
		resp, errExecute := r.execute(raw)
		if errExecute != nil {
			return nil, errExecute
		}
		return okEnvelope(resp)
	case pluginabi.MethodExecutorExecuteStream:
		resp, errStream := r.executeStream(raw)
		if errStream != nil {
			return nil, errStream
		}
		return okEnvelope(resp)
	case pluginabi.MethodExecutorCountTokens:
		return nil, newPluginCallError("unsupported_operation", "Qoder runner does not expose an independent exact token counter", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorHTTPRequest:
		return nil, newPluginCallError("unsupported_operation", "Qoder plugin does not expose arbitrary HTTP forwarding", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorCancel:
		var req pluginapi.CancelExecutionRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_cancel", "Qoder cancel request is invalid", http.StatusBadRequest, false)
		}
		if errCancel := r.cancelExecution(req); errCancel != nil {
			return nil, errCancel
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodExecutorCloseSession:
		var req pluginapi.CloseExecutionSessionRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_close", "Qoder close-session request is invalid", http.StatusBadRequest, false)
		}
		if errClose := r.closeExecutionSessions(req); errClose != nil {
			return nil, errClose
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodExecutorReadiness:
		var req pluginapi.ReadinessRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_readiness", "Qoder readiness request is invalid", http.StatusBadRequest, false)
		}
		return okEnvelope(r.readiness(req))
	default:
		return nil, newPluginCallError("unknown_method", "unknown method: "+method, http.StatusNotFound, false)
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name: pluginName, Version: pluginVersion, Author: "BlueSkyXN",
			GitHubRepository: "https://github.com/BlueSkyXN/CPA-Core-LTS",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "transport", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"sdk_cli", "direct_openai"}, Description: "Default Qoder transport; an auth file may explicitly select a transport. No request-side transport override is accepted."},
				{Name: "runner_command", Type: pluginapi.ConfigFieldTypeString, Description: "Explicit cpa-qoder-runner executable or absolute path."},
				{Name: "runner_args", Type: pluginapi.ConfigFieldTypeArray, Description: "Fixed runner arguments; model requests cannot override them."},
				{Name: "qoder_cli_path", Type: pluginapi.ConfigFieldTypeString, Description: "Required external Qoder CLI absolute path. SDK bundled CLI fallback is disabled."},
				{Name: "direct_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Explicit HTTPS OpenAI-compatible Qoder endpoint for direct_openai; loopback HTTP is allowed only for local tests."},
				{Name: "direct_models_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Optional OpenAI-compatible model catalog endpoint for direct_openai; otherwise direct_models must be configured."},
				{Name: "direct_auth_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Legacy alias for the Qoder OpenAPI base used by direct PAT exchange; retained for existing configurations."},
				{Name: "direct_token_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"auto", "bearer", "pat_exchange"}, Description: "Legacy direct credential mode; auto exchanges pt- PATs and bearer passes opaque access_token values through."},
				{Name: "openapi_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Qoder OpenAPI base used for PAT exchange and account/plan/quota queries; never put a token in this URL."},
				{Name: "openapi_user_agent", Type: pluginapi.ConfigFieldTypeString, Description: "Non-secret User-Agent sent to the Qoder OpenAPI base."},
				{Name: "direct_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional administrator-supplied exact direct model records; no guessed aliases or silent auto fallback."},
				{Name: "working_directory", Type: pluginapi.ConfigFieldTypeString, Description: "Fixed runner workspace root."},
				{Name: "max_queue_frames", Type: pluginapi.ConfigFieldTypeInteger, Description: "Bounded runner event queue capacity."},
				{Name: "request_timeout", Type: pluginapi.ConfigFieldTypeString, Description: "Runner control request timeout."},
				{Name: "model_cache_ttl", Type: pluginapi.ConfigFieldTypeString, Description: "Safe typed live-model cache TTL, at most 10m."},
				{Name: "permission_default", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"deny", "cancel_turn"}, Description: "Fail-closed fixed permission default."},
				{Name: "permission_rules", Type: pluginapi.ConfigFieldTypeArray, Description: "Fixed tool rules; no interactive permission resume API is exposed."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelProvider: true, AuthProvider: true, Executor: true,
			ExecutionCanceller: true, ProviderReadiness: true, ExecutionSessionCloser: true,
			ExecutorModelScope:   pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats: []string{"chat-completions"}, ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI: true,
		},
	}
}

func trimmed(value string) string { return strings.TrimSpace(value) }
