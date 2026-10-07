package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Existing layout fixtures edit the latest document; conflict tests call ConfigV8 directly.
func (h *Handler) configV8WithCurrentRevisionForTest(t *testing.T) gin.HandlerFunc {
	return func(c *gin.Context) {
		t.Helper()
		if c.Request.Method != http.MethodGet {
			r := httptest.NewRecorder()
			read, _ := gin.CreateTestContext(r)
			read.Request = httptest.NewRequest(http.MethodGet, "/config.yaml", nil)
			h.GetConfigYAML(read)
			if r.Code != http.StatusOK || r.Header().Get("ETag") == "" {
				t.Fatal("could not read configuration revision")
			}
			c.Request.Header.Set("If-Match", r.Header().Get("ETag"))
		}
		h.ConfigV8(c)
	}
}

func newRevisionTestRouter(t *testing.T, raw string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	r := gin.New()
	r.GET("/v0/config.yaml", h.GetConfigYAML)
	r.PUT("/v0/config.yaml", h.PutConfigYAML)
	r.PUT("/v0/request-retry", h.PutRequestRetry)
	r.GET("/v8/config.yaml", h.ConfigV8)
	r.PUT("/v8/config.yaml", h.ConfigV8)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r.Handle(method, "/v8/config/*path", h.ConfigV8)
	}
	return r, path
}

func revisionRequest(r http.Handler, method, url, body, revision string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, url, strings.NewReader(body))
	if revision != "" {
		request.Header.Set("If-Match", revision)
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	return response
}

func TestConfigRevisionRequiresExactSnapshot(t *testing.T) {
	raw := "port: 8317\nrequest-retry: 3\n"
	r, path := newRevisionTestRouter(t, raw)
	view := revisionRequest(r, http.MethodGet, "/v8/config.yaml", "", "")
	revision := view.Header().Get("ETag")
	if revision == "" || revision != revisionRequest(r, http.MethodGet, "/v0/config.yaml", "", "").Header().Get("ETag") {
		t.Fatal("raw and canonical views must identify the same persisted document")
	}
	if disk, _ := os.ReadFile(path); string(disk) != raw {
		t.Fatal("GET migrated the document")
	}
	for _, tc := range []struct {
		revision string
		want     int
	}{
		{"", http.StatusPreconditionRequired},
		{"*", http.StatusPreconditionFailed},
		{"W/" + revision, http.StatusPreconditionFailed},
		{revision + ", " + revision, http.StatusPreconditionFailed},
		{`"stale"`, http.StatusPreconditionFailed},
	} {
		response := revisionRequest(r, http.MethodPut, "/v8/config/routing/retry/request-retry", "9", tc.revision)
		if response.Code != tc.want {
			t.Fatalf("revision %q: status=%d want=%d", tc.revision, response.Code, tc.want)
		}
		if disk, _ := os.ReadFile(path); string(disk) != raw {
			t.Fatal("rejected save changed the document")
		}
	}
	duplicate := httptest.NewRequest(http.MethodPut, "/v8/config/routing/retry/request-retry", strings.NewReader("9"))
	duplicate.Header.Add("If-Match", revision)
	duplicate.Header.Add("If-Match", revision)
	duplicateResponse := httptest.NewRecorder()
	r.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusPreconditionFailed {
		t.Fatal("duplicate revision headers must be rejected")
	}
	response := revisionRequest(r, http.MethodPut, "/v8/config/routing/retry/request-retry", "9", revision)
	if response.Code != http.StatusOK {
		t.Fatalf("current revision rejected: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") != "" {
		t.Fatal("successful save must not return the old document revision")
	}
	response = revisionRequest(r, http.MethodPut, "/v8/config.yaml", view.Body.String(), revision)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale YAML status=%d", response.Code)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil || cfg.RequestRetry != 9 {
		t.Fatal("stale YAML overwrote the current value", err)
	}
}

func TestConfigRevisionConcurrentGroupWritesHaveOneWinner(t *testing.T) {
	r, _ := newRevisionTestRouter(t, "server: {port: 8317}\napi-keys: {codex: [{name: shared, keys: [{api-key: synthetic}]}]}\n")
	path := "/v8/config/api-keys/codex"
	revision := revisionRequest(r, http.MethodGet, path, "", "").Header().Get("ETag")
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, body := range []string{`[{"name":"first","keys":[{"api-key":"synthetic"}]}]`, `[{"name":"second","keys":[{"api-key":"synthetic"}]}]`} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- revisionRequest(r, http.MethodPut, path, body, revision).Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("concurrent results=%v", counts)
	}
}

func TestConfigRevisionDetectsLegacyAndExternalWrites(t *testing.T) {
	for _, writer := range []string{"legacy", "external"} {
		t.Run(writer, func(t *testing.T) {
			r, path := newRevisionTestRouter(t, "port: 8317\nrequest-retry: 3\n")
			view := revisionRequest(r, http.MethodGet, "/v0/config.yaml", "", "")
			if writer == "legacy" {
				if response := revisionRequest(r, http.MethodPut, "/v0/request-retry", `{"value":9}`, ""); response.Code != 200 {
					t.Fatal(response.Code)
				}
			} else if err := os.WriteFile(path, []byte("port: 8317\nrequest-retry: 9\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			for _, endpoint := range []string{"/v0/config.yaml", "/v8/config.yaml"} {
				response := revisionRequest(r, http.MethodPut, endpoint, view.Body.String(), view.Header().Get("ETag"))
				if response.Code != http.StatusPreconditionFailed {
					t.Fatalf("stale write status=%d", response.Code)
				}
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("stale save changed disk")
			}
		})
	}
}

func TestConfigRevisionChecksPatchDeleteAndMissingFamily(t *testing.T) {
	r, _ := newRevisionTestRouter(t, "server: {port: 8317}\n")
	missing := revisionRequest(r, http.MethodGet, "/v8/config/api-keys/codex", "", "")
	revision := missing.Header().Get("ETag")
	if missing.Code != 404 || revision == "" {
		t.Fatal("missing family must still expose a document revision")
	}
	if response := revisionRequest(r, http.MethodPut, "/v8/config/api-keys/codex", `[]`, revision); response.Code != 200 {
		t.Fatal(response.Code)
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		if response := revisionRequest(r, method, "/v8/config/api-keys/codex", `[]`, revision); response.Code != http.StatusPreconditionFailed {
			t.Fatalf("stale %s status=%d", method, response.Code)
		}
	}
}
