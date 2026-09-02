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
	if errDecode := json.Unmarshal(rawResponse, &env); errDecode != nil {
		return nil, fmt.Errorf("decode host callback %s", method)
	}
	if !env.OK {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
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
		r.quiesceAndWait()
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
		return nil, newPluginCallError("unsupported_auth_flow", "CodeBuddy G1 accepts preconfigured PAT/API keys only", http.StatusNotImplemented, false)
	case pluginabi.MethodAuthRefresh:
		resp, errRefresh := refreshAuthRequest(raw)
		if errRefresh != nil {
			return nil, errRefresh
		}
		return okEnvelope(resp)
	case pluginabi.MethodModelStatic:
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier})
	case pluginabi.MethodModelForAuth:
		resp, errModels := globalRuntime.modelsForAuth(raw)
		if errModels != nil {
			return nil, errModels
		}
		return okEnvelope(resp)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{Routes: []managementRoute{{
			Method: http.MethodGet,
			Path:   "/plugins/codebuddy/summary",
		}}})
	case pluginabi.MethodManagementHandle:
		resp, errManagement := globalRuntime.handleManagement(raw)
		if errManagement != nil {
			return nil, errManagement
		}
		return okEnvelope(resp)
	case pluginabi.MethodExecutorExecute:
		return nil, newPluginCallError("stream_required", "CodeBuddy G1 supports streaming requests only", http.StatusBadRequest, false)
	case pluginabi.MethodExecutorExecuteStream:
		resp, errStream := r.executeStream(raw)
		if errStream != nil {
			return nil, errStream
		}
		return okEnvelope(resp)
	case pluginabi.MethodExecutorCountTokens:
		return nil, newPluginCallError("unsupported_operation", "CodeBuddy G1 does not expose a token counting endpoint", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorHTTPRequest:
		return nil, newPluginCallError("unsupported_operation", "CodeBuddy G1 does not expose arbitrary HTTP forwarding", http.StatusNotImplemented, false)
	case pluginabi.MethodExecutorCancel:
		var req pluginapi.CancelExecutionRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_cancel", "CodeBuddy cancel request is invalid", http.StatusBadRequest, false)
		}
		r.cancel(strings.TrimSpace(req.RequestID))
		return okEnvelope(struct{}{})
	case pluginabi.MethodExecutorReadiness:
		var req pluginapi.ReadinessRequest
		if errDecode := decodeRequest(raw, &req); errDecode != nil {
			return nil, newPluginCallError("invalid_readiness", "CodeBuddy readiness request is invalid", http.StatusBadRequest, false)
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
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "BlueSkyXN",
			GitHubRepository: "https://github.com/BlueSkyXN/CPA-Core-LTS",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "CodeBuddy Chat Completions endpoint. HTTPS is required except for loopback test fixtures."},
				{Name: "user_agent", Type: pluginapi.ConfigFieldTypeString, Description: "Non-secret User-Agent sent to CodeBuddy."},
				{Name: "catalog_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "CodeBuddy model catalog endpoint used for selected-PAT discovery."},
				{Name: "catalog_user_agent", Type: pluginapi.ConfigFieldTypeString, Description: "Client User-Agent required by the CodeBuddy catalog service."},
				{Name: "billing_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Optional CodeBuddy billing meter endpoint."},
				{Name: "account_endpoint", Type: pluginapi.ConfigFieldTypeString, Description: "Optional CodeBuddy account endpoint; failure falls back to the auth label and fingerprint."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelProvider:          true,
			AuthProvider:           true,
			Executor:               true,
			ExecutionCanceller:     true,
			ProviderReadiness:      true,
			ExecutionSessionCloser: false,
			ExecutorModelScope:     pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:   []string{"chat-completions"},
			ExecutorOutputFormats:  []string{"chat-completions"},
			ManagementAPI:          true,
		},
	}
}
