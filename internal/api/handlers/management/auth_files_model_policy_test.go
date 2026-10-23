package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPatchAuthFileFieldsModelPolicyRoundTrip(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "policy.json"
	filePath := filepath.Join(authDir, fileName)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	body := `{"name":"policy.json","model_policy":{"allowlist":{"enabled":true,"models":["gpt-5-codex","gpt-4.1"]},"denylist":{"enabled":false,"models":["*-preview"]}}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("failed to read updated auth file: %v", errRead)
	}
	if !strings.Contains(string(raw), `"gpt-5-codex"`) {
		t.Fatalf("model_policy not persisted: %s", raw)
	}

	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatal("auth record missing after patch")
	}
	if !coreauth.EvaluateModelPolicy(nil, updated, "codex", "gpt-5-codex").Allowed || coreauth.EvaluateModelPolicy(nil, updated, "codex", "other").Allowed {
		t.Fatal("runtime model policy does not match the persisted allowlist")
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"name":"policy.json","model_policy.allowlist.enabled":false}`, http.StatusOK},
		{`{"name":"policy.json","model_policy.allowlist.enabled":"false"}`, http.StatusBadRequest},
		{`{"name":"policy.json","model_policy.allowlist":{"enabled":"bad"}}`, http.StatusBadRequest},
		{`{"name":"policy.json","model_policy.allowlist.enabled":true,"model_policy.allowlist.models":[]}`, http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(tc.body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchAuthFileFields(ctx)
		if rec.Code != tc.status {
			t.Fatalf("nested patch status=%d, want %d: %s", rec.Code, tc.status, rec.Body.String())
		}
	}
	updated, _ = manager.GetByID(fileName)
	if coreauth.EvaluateModelPolicy(nil, updated, "codex", "gpt-5-codex").Allowed {
		t.Fatal("enabled empty allowlist did not take effect")
	}
}

func TestPatchAuthFileFieldsRejectsInvalidModelPolicy(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "badpolicy.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	body := `{"name":"badpolicy.json","model_policy":{"allowlist":{"enabled":"true"}}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("failed to read auth file: %v", errRead)
	}
	if strings.Contains(string(raw), "model_policy") {
		t.Fatalf("invalid policy must not persist: %s", raw)
	}
}
