package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newCodexCompressionTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := config.ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.CodexKey = []config.CodexKey{{APIKey: "test-key", BaseURL: "https://example.com", RequestCompression: "none"}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	if err := config.SaveConfigPreserveComments(h.configFilePath, cfg); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/codex-api-key", h.GetCodexKeys)
	router.PUT("/codex-api-key", h.PutCodexKeys)
	router.PATCH("/codex-api-key", h.PatchCodexKey)
	return h, router
}

func codexCompressionTestRequest(router *gin.Engine, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/codex-api-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func assertCodexCompressionReadback(t *testing.T, h *Handler, router *gin.Engine, want string, priority int) {
	t.Helper()
	rec := codexCompressionTestRequest(router, http.MethodGet, "")
	var result struct {
		Entries []config.CodexKey `json:"codex-api-key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(result.Entries) != 1 {
		t.Fatalf("GET status=%d entries=%d", rec.Code, len(result.Entries))
	}
	for _, optional := range []bool{false, true} {
		loaded, err := config.LoadConfigOptional(h.configFilePath, optional)
		if err != nil {
			t.Fatalf("load saved config (optional=%t): %v", optional, err)
		}
		if len(loaded.CodexKey) != 1 {
			t.Fatalf("saved entries=%d, want 1", len(loaded.CodexKey))
		}
		for _, entry := range []config.CodexKey{result.Entries[0], loaded.CodexKey[0]} {
			if entry.RequestCompression != want || entry.Priority != priority {
				t.Fatalf("compression=%q priority=%d, want %q/%d", entry.RequestCompression, entry.Priority, want, priority)
			}
		}
	}
}

func TestCodexKeyCompressionPutAndPatch(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		for _, mode := range []struct{ raw, want string }{{"", ""}, {" NONE ", "none"}, {" ZSTD ", "zstd"}} {
			t.Run(method+"/"+mode.want, func(t *testing.T) {
				h, router := newCodexCompressionTestHandler(t)
				reloads := make(chan config.CodexKey, 2)
				h.SetConfigReloadHook(func(_ context.Context, snapshot *config.Config) {
					reloads <- snapshot.CodexKey[0]
				})
				assertReload := func(priority int) {
					t.Helper()
					select {
					case entry := <-reloads:
						if entry.RequestCompression != mode.want || entry.Priority != priority {
							t.Fatalf("reload compression=%q priority=%d, want %q/%d", entry.RequestCompression, entry.Priority, mode.want, priority)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("config reload hook not called")
					}
				}
				value := map[string]any{"api-key": "test-key", "base-url": "https://example.com", "priority": 7, "request-compression": mode.raw}
				var payload any = []any{value}
				if method == http.MethodPatch {
					payload = map[string]any{"index": 0, "value": value}
				}
				body, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				rec := codexCompressionTestRequest(router, method, string(body))
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d, want 200: %s", rec.Code, rec.Body.String())
				}
				assertCodexCompressionReadback(t, h, router, mode.want, 7)
				assertReload(7)
				// PATCH 省略该字段时，保留已归一化的值。
				rec = codexCompressionTestRequest(router, http.MethodPatch, `{"index":0,"value":{"priority":8}}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("omitted-field PATCH status=%d", rec.Code)
				}
				assertCodexCompressionReadback(t, h, router, mode.want, 8)
				assertReload(8)
			})
		}
	}
}

func TestCodexKeyCompressionRejectsInvalidWritesAtomically(t *testing.T) {
	for _, tc := range []struct{ name, method, body string }{
		{"PUT invalid second entry", http.MethodPut, `[{"api-key":"test-key","base-url":"https://example.com","priority":7,"request-compression":"zstd"},{"api-key":"second-key","base-url":"https://example.com","request-compression":"gzip"}]`},
		{"PATCH invalid with priority", http.MethodPatch, `{"index":0,"value":{"priority":7,"request-compression":"gzip"}}`},
		{"PATCH invalid with deletion", http.MethodPatch, `{"index":0,"value":{"base-url":"","request-compression":"auto"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, router := newCodexCompressionTestHandler(t)
			before, err := os.ReadFile(h.configFilePath)
			if err != nil {
				t.Fatal(err)
			}
			entries := append([]config.CodexKey(nil), h.cfg.CodexKey...)
			rec := codexCompressionTestRequest(router, tc.method, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", rec.Code)
			}
			after, err := os.ReadFile(h.configFilePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !reflect.DeepEqual(entries, h.cfg.CodexKey) || h.reloadGeneration != 0 {
				t.Fatal("invalid compression changed config or scheduled a reload")
			}
			assertCodexCompressionReadback(t, h, router, "none", 0)
		})
	}
}
