#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    void* ptr;
    size_t len;
} cliproxy_buffer;

typedef struct {
    uint32_t abi_version;
    int (*call)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
    void (*free_buffer)(void*, size_t);
    void (*shutdown)(void);
} cliproxy_plugin_api;

static char marker_path[4096];

#ifdef LIFETIME_EMBEDS_GO
int lifetime_go_payload(void);
#endif

static void mark(char phase) {
    FILE* file = fopen(marker_path, "a");
    if (file != NULL) {
        fputc(phase, file);
        fclose(file);
    }
}

static int plugin_call(const char* method, const uint8_t* request, size_t len, cliproxy_buffer* response) {
    response->ptr = NULL;
    response->len = 0;
    return 0;
}

static void plugin_free(void* ptr, size_t len) {
    free(ptr);
}

static void plugin_shutdown(void) {
    mark('S');
}

__attribute__((destructor)) static void plugin_unmapped(void) {
    mark('D');
}

int cliproxy_plugin_init(const void* host, cliproxy_plugin_api* plugin) {
#ifdef LIFETIME_EMBEDS_GO
    if (lifetime_go_payload() != 42) {
        return 1;
    }
#endif
    const char* path = getenv("CPA_PLUGIN_LIFETIME_MARKER");
    if (path == NULL || strlen(path) >= sizeof(marker_path)) {
        return 1;
    }
    strcpy(marker_path, path);
    plugin->abi_version = 1;
    plugin->call = plugin_call;
    plugin->free_buffer = plugin_free;
    plugin->shutdown = plugin_shutdown;
    return 0;
}
