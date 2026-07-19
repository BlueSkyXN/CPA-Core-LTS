package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexClientMetadataCredentialScopePrefersSelectedAuthID(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Metadata: map[string]any{"account_id": "account-1"},
	}
	if got := codexClientMetadataCredentialScope(auth); got != "codex:auth:auth-1" {
		t.Fatalf("credential scope = %q, want selected auth identity", got)
	}
}

func TestCodexClientMetadataCredentialScopeFallsBackToAccount(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "account-1"}}
	if got := codexClientMetadataCredentialScope(auth); got != "codex:account:account-1" {
		t.Fatalf("credential scope = %q, want account fallback", got)
	}
}

func TestCodexIncomingTurnMetadataFallsBackToGinHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("X-Codex-Turn-Metadata", `{"request_kind":"turn","thread_id":"gin-thread"}`)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	got := codexIncomingTurnMetadata(ctx, http.Header{})
	if got != `{"request_kind":"turn","thread_id":"gin-thread"}` {
		t.Fatalf("incoming turn metadata = %q, want Gin header fallback", got)
	}
}

func TestCodexIncomingTurnMetadataPrefersProvidedHeaders(t *testing.T) {
	provided := http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn","thread_id":"provided-thread"}`}}
	if got := codexIncomingTurnMetadata(context.Background(), provided); got != `{"request_kind":"turn","thread_id":"provided-thread"}` {
		t.Fatalf("incoming turn metadata = %q, want provided header", got)
	}
}
