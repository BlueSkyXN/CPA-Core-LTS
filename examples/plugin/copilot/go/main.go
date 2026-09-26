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
		writeResponse(response, errorEnvelope(newPluginCallError("invalid_method", "plugin method is required", http.StatusBadRequest, false)))
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
	case pluginabi.MethodAuthLoginStart:
		resp, errStart := r.startLoginRequest(raw)
		if errStart != nil {
			return nil, errStart
		}
		return okEnvelope(resp)
	case pluginabi.MethodAuthLoginPoll:
		resp, errPoll := r.pollLoginRequest(raw)
		if errPoll != nil {
			return nil, errPoll
		}
		return okEnvelope(resp)
	case pluginabi.MethodAuthRefresh:
		resp, errRefresh := refreshAuthRequest(raw)
		if errRefresh != nil {
			return nil, errRefresh
		}
		return okEnvelope(resp)
	case pluginabi.MethodModelStatic:
		// 目录只来自账号实时接口；静态模型一律为空，绝不伪造。
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier, Models: []pluginapi.ModelInfo{}})
	case pluginabi.MethodModelForAuth:
		resp, errModels := r.modelsForAuth(raw)
		if errModels != nil {
			return nil, errModels
		}
		return okEnvelope(resp)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/copilot/login-info"},
			{Method: http.MethodGet, Path: "/plugins/copilot/summary"},
		}})
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
		return nil, newPluginCallError("unsupported_operation", "Copilot does not expose an independent exact token counter", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorHTTPRequest:
		return nil, newPluginCallError("unsupported_operation", "Copilot plugin does not expose arbitrary HTTP forwarding", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorCancel:
		var req pluginapi.CancelExecutionRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_cancel", "Copilot cancel request is invalid", http.StatusBadRequest, false)
		}
		if errCancel := r.cancelExecution(req); errCancel != nil {
			return nil, errCancel
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodExecutorCloseSession:
		var req pluginapi.CloseExecutionSessionRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_close", "Copilot close-session request is invalid", http.StatusBadRequest, false)
		}
		if errClose := r.closeExecutionSessions(req); errClose != nil {
			return nil, errClose
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodExecutorReadiness:
		var req pluginapi.ReadinessRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_readiness", "Copilot readiness request is invalid", http.StatusBadRequest, false)
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
				{Name: "account_type", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"individual", "business", "enterprise"}, Description: "Copilot subscription class selecting the API host: individual, business, or enterprise. Defaults to individual."},
				{Name: "enterprise_domain", Type: pluginapi.ConfigFieldTypeString, Description: "GitHub Enterprise Server bare hostname; rewrites github.com, api.github.com, and the Copilot API base to the GHES hosts."},
				{Name: "github_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Explicit GitHub base for the device-code login endpoints; defaults to https://github.com or the GHES host."},
				{Name: "github_api_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Explicit GitHub API base for token exchange, user, and quota endpoints; defaults to https://api.github.com or the GHES host."},
				{Name: "copilot_api_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Explicit Copilot API base for models and chat completions; defaults to the account_type or GHES host."},
				{Name: "oauth_client_id", Type: pluginapi.ConfigFieldTypeString, Description: "GitHub OAuth app client ID used by the device-code login flow."},
				{Name: "editor_version", Type: pluginapi.ConfigFieldTypeString, Description: "Editor version identity sent as the editor-version header, for example 1.110.1."},
				{Name: "editor_plugin_version", Type: pluginapi.ConfigFieldTypeString, Description: "Plugin identity sent as the editor-plugin-version header, for example copilot-chat/0.38.2."},
				{Name: "user_agent", Type: pluginapi.ConfigFieldTypeString, Description: "User-Agent identity sent to GitHub and Copilot endpoints."},
				{Name: "api_version", Type: pluginapi.ConfigFieldTypeString, Description: "GitHub API version sent as the x-github-api-version header."},
				{Name: "excluded_model_prefixes", Type: pluginapi.ConfigFieldTypeArray, Description: "Case-sensitive model ID prefixes excluded from the published account catalog and from execution."},
				{Name: "model_cache_ttl", Type: pluginapi.ConfigFieldTypeString, Description: "Per-credential live model catalog cache TTL, at most 10m."},
			},
			// 宿主请求日志对凭据交换端点的双向 body 脱敏；声明与宿主内置规则并集生效。
			SensitiveEndpoints: []pluginapi.SensitiveEndpoint{
				{Method: http.MethodPost, PathSuffix: "/login/device/code"},
				{Method: http.MethodPost, PathSuffix: "/login/oauth/access_token"},
				{Method: http.MethodGet, PathSuffix: "/copilot_internal/v2/token"},
			},
		},
		Capabilities: registrationCapabilities{
			ModelProvider: true, AuthProvider: true, Executor: true,
			ExecutionCanceller: true, ProviderReadiness: true, ExecutionSessionCloser: true,
			ExecutorModelScope:   pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats: []string{"chat-completions", "embeddings"}, ExecutorOutputFormats: []string{"chat-completions", "embeddings"},
			ManagementAPI: true,
		},
	}
}
