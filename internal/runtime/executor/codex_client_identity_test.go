package executor

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorFinalizesLunaClientIdentityVersion(t *testing.T) {
	capturedHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"plan_type": "pro",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator": "Codex Desktop",
		"User-Agent": "codex_cli_rs/0.115.0 (Mac OS 14.2.0; arm64) iTerm.app",
		"Version":    "0.115.0-alpha.27",
	})

	_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"hello","store":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case headers := <-capturedHeaders:
		const wantUA = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
		if got := headers.Get("User-Agent"); got != wantUA {
			t.Fatalf("User-Agent = %q, want %q", got, wantUA)
		}
		if got := headers.Get("Originator"); got != "codex-tui" {
			t.Fatalf("Originator = %q, want codex-tui", got)
		}
		if got := headers.Get("Version"); got != "0.144.0" {
			t.Fatalf("Version = %q, want 0.144.0", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Codex HTTP request")
	}
}

func TestCodexWebsocketsExecutorFinalizesClientIdentity(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders <- r.Header.Clone()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if !bytes.Contains(payload, []byte(`"model":"gpt-5.4"`)) {
			t.Errorf("upstream payload missing model: %s", payload)
			return
		}
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"plan_type": "pro",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator": "Codex Desktop",
		"User-Agent": "codex_cli_rs/0.144.1 (Mac OS 14.2.0; arm64) iTerm.app",
		"Version":    "0.115.0-alpha.27",
	})

	_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","store":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case headers := <-capturedHeaders:
		if got := headers.Get("User-Agent"); got != "codex_cli_rs/0.144.1 (Mac OS 14.2.0; arm64) iTerm.app" {
			t.Fatalf("User-Agent = %q, want preserved official CLI UA", got)
		}
		if got := headers.Get("Originator"); got != "codex_cli_rs" {
			t.Fatalf("Originator = %q, want codex_cli_rs", got)
		}
		if got := headers.Get("Version"); got != "0.144.1" {
			t.Fatalf("Version = %q, want 0.144.1", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Codex websocket request")
	}
}

func TestFinalizeCodexClientIdentityHeaders(t *testing.T) {
	tests := []struct {
		name           string
		auth           *cliproxyauth.Auth
		originator     string
		userAgent      string
		version        string
		wantOriginator string
		wantUserAgent  string
		wantVersion    string
		wantSession    bool
	}{
		{
			name:           "pairs official CLI and raises old version",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "Codex Desktop",
			userAgent:      "codex_cli_rs/0.144.1 (Mac OS 14.2.0; arm64) iTerm.app",
			version:        "0.115.0-alpha.27",
			wantOriginator: "codex_cli_rs",
			wantUserAgent:  "codex_cli_rs/0.144.1 (Mac OS 14.2.0; arm64) iTerm.app",
			wantVersion:    "0.144.1",
			wantSession:    true,
		},
		{
			name:           "keeps omitted version omitted",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "codex_cli_rs",
			userAgent:      "codex-tui/0.144.0 (Linux 6.8; x86_64) xterm",
			wantOriginator: "codex-tui",
			wantUserAgent:  "codex-tui/0.144.0 (Linux 6.8; x86_64) xterm",
		},
		{
			name:           "preserves higher explicit version",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "codex-tui",
			userAgent:      "codex-tui/0.144.0 (Linux 6.8; x86_64) xterm",
			version:        "0.145.0-beta.1",
			wantOriginator: "codex-tui",
			wantUserAgent:  "codex-tui/0.144.0 (Linux 6.8; x86_64) xterm",
			wantVersion:    "0.145.0-beta.1",
		},
		{
			name:           "restores originator override from trailer",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "cccc",
			userAgent:      "cccc/0.144.1 (Linux 6.8; x86_64) xterm (codex-tui; 0.144.1)",
			version:        "invalid",
			wantOriginator: "codex-tui",
			wantUserAgent:  "codex-tui/0.144.1 (Linux 6.8; x86_64) xterm (codex-tui; 0.144.1)",
			wantVersion:    "0.144.1",
		},
		{
			name:           "preserves Codex family identity",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "codex_cli_rs",
			userAgent:      "Codex Desktop/1.2.3 (Mac OS 14.2.0; arm64)",
			version:        "1.0.0",
			wantOriginator: "Codex Desktop",
			wantUserAgent:  "Codex Desktop/1.2.3 (Mac OS 14.2.0; arm64)",
			wantVersion:    "1.2.3",
			wantSession:    true,
		},
		{
			name:           "falls back from third party identity",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "opencode",
			userAgent:      "third-party/9.9.9",
			version:        "0.1.0",
			wantOriginator: codexOriginator,
			wantUserAgent:  codexUserAgent,
			wantVersion:    "0.135.0",
			wantSession:    true,
		},
		{
			name:           "rejects control characters in Codex family identity",
			auth:           &cliproxyauth.Auth{Provider: "codex"},
			originator:     "Codex Desktop",
			userAgent:      "Codex \x01evil/9.9.9",
			version:        "0.1.0",
			wantOriginator: codexOriginator,
			wantUserAgent:  codexUserAgent,
			wantVersion:    "0.135.0",
			wantSession:    true,
		},
		{
			name: "does not rewrite API key requests",
			auth: &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{
				"api_key": "sk-test",
			}},
			originator:     "custom-origin",
			userAgent:      "custom-client/1.0.0",
			version:        "0.1.0",
			wantOriginator: "custom-origin",
			wantUserAgent:  "custom-client/1.0.0",
			wantVersion:    "0.1.0",
		},
		{
			name:          "does not inject a missing originator",
			auth:          &cliproxyauth.Auth{Provider: "codex"},
			userAgent:     "third-party/1.0.0",
			version:       "0.1.0",
			wantUserAgent: "third-party/1.0.0",
			wantVersion:   "0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			if tt.originator != "" {
				headers.Set("Originator", tt.originator)
			}
			if tt.userAgent != "" {
				headers.Set("User-Agent", tt.userAgent)
			}
			if tt.version != "" {
				headers.Set("Version", tt.version)
			}

			finalizeCodexClientIdentityHeaders(headers, tt.auth)

			if got := headers.Get("Originator"); got != tt.wantOriginator {
				t.Fatalf("Originator = %q, want %q", got, tt.wantOriginator)
			}
			if got := headers.Get("User-Agent"); got != tt.wantUserAgent {
				t.Fatalf("User-Agent = %q, want %q", got, tt.wantUserAgent)
			}
			if got := headers.Get("Version"); got != tt.wantVersion {
				t.Fatalf("Version = %q, want %q", got, tt.wantVersion)
			}
			if gotSession := codexSessionHeaderValue(headers) != ""; gotSession != tt.wantSession {
				t.Fatalf("Session_id present = %v, want %v", gotSession, tt.wantSession)
			}
		})
	}
}

func TestCodexDirectImageOAuthFinalizesClientIdentity(t *testing.T) {
	capturedHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"plan_type": "pro",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator": "Codex Desktop",
		"User-Agent": "third-party/9.9.9",
		"Version":    "0.115.0-alpha.27",
	})

	exec := NewCodexExecutor(&config.Config{})
	_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "codex/gpt-image-1.5",
		Payload: []byte(`{"model":"codex/gpt-image-1.5","prompt":"hello","n":1,"size":"1024x1024"}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case headers := <-capturedHeaders:
		if got := headers.Get("User-Agent"); got != codexUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, codexUserAgent)
		}
		if got := headers.Get("Originator"); got != codexOriginator {
			t.Fatalf("Originator = %q, want %q", got, codexOriginator)
		}
		if got := headers.Get("Version"); got != "0.135.0" {
			t.Fatalf("Version = %q, want 0.135.0", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for direct image request")
	}
}
