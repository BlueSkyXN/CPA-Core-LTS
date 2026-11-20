package amp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestFallbackHandler_RequestToolCasing_RewritesStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-amp-tool-casing", "codex", []*registry.ModelInfo{
		{ID: "test/gpt-tool-casing", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-amp-tool-casing")

	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return nil }, nil, nil)
	handler := func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = c.Writer.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"glob\",\"id\":\"toolu_01\",\"input\":{}}}\n\n"))
	}

	r := gin.New()
	r.POST("/messages", fallback.WrapHandler(handler))

	reqBody := []byte(`{"model":"test/gpt-tool-casing","tools":[{"name":"Glob","input_schema":{"type":"object"}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"name":"Glob"`)) {
		t.Fatalf("expected streaming response to restore glob->Glob, got %s", w.Body.String())
	}
}

func TestFallbackHandler_ModelMapping_PreservesThinkingSuffixAndRewritesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-amp-fallback", "codex", []*registry.ModelInfo{
		{ID: "test/gpt-5.2", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-amp-fallback")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gpt-5.2", To: "test/gpt-5.2"},
	})

	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return nil }, mapper, nil)

	handler := func(c *gin.Context) {
		var req struct {
			Model string `json:"model"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"model":      req.Model,
			"seen_model": req.Model,
		})
	}

	r := gin.New()
	r.POST("/chat/completions", fallback.WrapHandler(handler))

	reqBody := []byte(`{"model":"gpt-5.2(xhigh)"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var resp struct {
		Model     string `json:"model"`
		SeenModel string `json:"seen_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if resp.Model != "gpt-5.2(xhigh)" {
		t.Errorf("Expected response model gpt-5.2(xhigh), got %s", resp.Model)
	}
	if resp.SeenModel != "test/gpt-5.2(xhigh)" {
		t.Errorf("Expected handler to see test/gpt-5.2(xhigh), got %s", resp.SeenModel)
	}
}

func TestFallbackHandlerRespectsStandaloneAutoOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		cfg       *config.Config
		wantLocal bool
	}{
		{name: "unowned auto can use Amp credits", cfg: &config.Config{}, wantLocal: false},
		{
			name: "global auto selection stays local",
			cfg: &config.Config{Routing: config.RoutingConfig{
				AutoModel: config.AutoModelConfig{Enabled: true, Candidates: []string{"free"}},
			}},
			wantLocal: true,
		},
		{
			name: "explicit auto alias stays local even when unavailable",
			cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {{Name: "real", Alias: "auto"}},
			}},
			wantLocal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyCalls := 0
			amp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls++
				_, _ = w.Write([]byte("amp"))
			}))
			defer amp.Close()
			target, err := url.Parse(amp.URL)
			if err != nil {
				t.Fatal(err)
			}

			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(tt.cfg)
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy {
				return httputil.NewSingleHostReverseProxy(target)
			}, nil, nil)
			fallback.baseHandler = base

			localCalls := 0
			r := gin.New()
			r.POST("/chat/completions", fallback.WrapHandler(func(c *gin.Context) {
				localCalls++
				c.JSON(http.StatusOK, gin.H{"model": "auto"})
			}))

			req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewBufferString(`{"model":"auto","messages":[]}`))
			req.Header.Set("Content-Type", "application/json")
			server := httptest.NewServer(r)
			defer server.Close()
			req.URL, err = url.Parse(server.URL + "/chat/completions")
			if err != nil {
				t.Fatal(err)
			}
			req.RequestURI = ""
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()

			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if tt.wantLocal && (localCalls != 1 || proxyCalls != 0) {
				t.Fatalf("local calls = %d, Amp calls = %d; want local only", localCalls, proxyCalls)
			}
			if !tt.wantLocal && (localCalls != 0 || proxyCalls != 1) {
				t.Fatalf("local calls = %d, Amp calls = %d; want Amp only", localCalls, proxyCalls)
			}
		})
	}
}
