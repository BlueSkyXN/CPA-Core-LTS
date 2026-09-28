package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*,const char*,const uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*,size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*,uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*,size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*,uint8_t*,size_t,cliproxy_buffer*);
extern void cliproxyPluginFree(void*,size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* host_api;
static void set_host(const cliproxy_host_api* host) {host_api=host;}
static int invoke_host(const char* method,const uint8_t* request,size_t size,cliproxy_buffer* response) {
 if(!host_api||!host_api->call)return 1;
 return host_api->call(host_api->host_ctx,method,request,size,response);
}
static void release_host(void* ptr,size_t size) {if(host_api&&host_api->free_buffer&&ptr)host_api->free_buffer(ptr,size);}
*/
import "C"
import (
	"encoding/json"
	"unsafe"
)

var runtimeInstance = newRuntime(cgoCaller{})

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 {
		return 1
	}
	C.set_host(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) (result C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeResponse(response, envelope(nil, problem(500, "plugin_error", "Plugin execution failed")))
			result = 1
		}
	}()
	if method == nil || size > maxBody*2 || size > 0 && request == nil {
		writeResponse(response, envelope(nil, problem(400, "invalid_request", "Invalid ABI request")))
		return 1
	}
	var raw []byte
	if size > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(size))
	}
	value, err := runtimeInstance.dispatch(C.GoString(method), raw)
	writeResponse(response, envelope(value, err))
	if err != nil {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { runtimeInstance.stop() }
func writeResponse(out *C.cliproxy_buffer, raw []byte) {
	if out == nil || len(raw) == 0 {
		return
	}
	out.ptr = C.CBytes(raw)
	out.len = C.size_t(len(raw))
}

type cgoCaller struct{}

func (cgoCaller) Call(method string, payload any) (json.RawMessage, error) {
	raw := encode(payload)
	m := C.CString(method)
	defer C.free(unsafe.Pointer(m))
	p := C.CBytes(raw)
	defer C.free(p)
	var out C.cliproxy_buffer
	status := C.invoke_host(m, (*C.uint8_t)(p), C.size_t(len(raw)), &out)
	if out.ptr != nil {
		defer C.release_host(out.ptr, out.len)
	}
	if out.ptr == nil || out.len > maxBody*3 {
		return nil, problem(502, "host_callback_failed", "Host callback failed")
	}
	result := C.GoBytes(out.ptr, C.int(out.len))
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if status != 0 || json.Unmarshal(result, &env) != nil || !env.OK {
		return nil, problem(502, "host_callback_failed", "Host callback failed")
	}
	return env.Result, nil
}
