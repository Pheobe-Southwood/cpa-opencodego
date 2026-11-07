// Command plugin is the read-only OpenCode Go quota viewer for CLIProxyAPI.
//
// The plugin registers no executor and no model: it only adds one sidebar page
// to the management centre. The page lists the OpenCode Go keys the host
// already routes (read from the host configuration), queries the upstream
// /usage endpoint for each key and renders the rolling, weekly and monthly
// windows. Nothing is written anywhere, so the plugin cannot change how the
// host routes requests.
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
	"fmt"
	"net/http"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/plugin"
)

// version is stamped with -ldflags at build time.
var version = "dev"

var runtime *plugin.Runtime

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, instance *C.cliproxy_plugin_api) C.int {
	if instance == nil {
		return 1
	}
	C.store_host_api(host)
	runtime = plugin.New()
	runtime.SetVersion(version)
	abi.HostCall = hostCall

	instance.abi_version = C.uint32_t(pluginabi.ABIVersion)
	instance.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	instance.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	instance.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, abi.Fail("invalid_method", "method is required", http.StatusBadRequest))
		return 1
	}
	var payload []byte
	if request != nil && requestLen > 0 {
		payload = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	writeResponse(response, dispatch(C.GoString(method), payload))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	if runtime != nil {
		runtime.Shutdown()
	}
}

// dispatch isolates a panicking plugin call so a bug cannot fuse the plugin in
// the host: a panic becomes an error envelope instead.
func dispatch(method string, payload []byte) (raw []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			raw = abi.Fail("plugin_panic", fmt.Sprintf("插件内部错误: %v", recovered), http.StatusInternalServerError)
		}
	}()
	if runtime == nil {
		return abi.Fail("not_initialized", "插件尚未初始化", http.StatusInternalServerError)
	}
	return runtime.Dispatch(method, payload)
}

// hostCall performs a host callback such as host.log.
func hostCall(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}

	var response C.cliproxy_buffer
	status := C.call_host_api(cMethod, request, C.size_t(len(payload)), &response)
	if response.ptr == nil {
		if status != 0 {
			return nil, fmt.Errorf("host callback %s failed with status %d", method, int(status))
		}
		return nil, fmt.Errorf("host callback %s returned no payload", method)
	}
	defer C.free_host_buffer(response.ptr, response.len)
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	pointer := C.CBytes(raw)
	if pointer == nil {
		return
	}
	response.ptr = pointer
	response.len = C.size_t(len(raw))
}
