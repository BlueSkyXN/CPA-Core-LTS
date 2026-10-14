package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newOAuthModelPolicyTestHandler(t *testing.T) (*Handler, *string) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("host: 0.0.0.0\nport: 8317\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	h := NewHandler(&config.Config{}, configPath, nil)
	return h, &configPath
}

func patchOAuthModelPolicyRequest(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-model-policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchOAuthModelPolicy(ctx)
	return rec
}

func TestOAuthModelPolicyPatchPutGetDelete(t *testing.T) {
	h, configPath := newOAuthModelPolicyTestHandler(t)

	rec := patchOAuthModelPolicyRequest(t, h, `{"provider":"Copilot","policy":{"allowlist":{"enabled":true,"models":[" GPT-* ","gpt-*"]},"denylist":{"enabled":false,"models":["*-preview"]}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body %s", rec.Code, rec.Body.String())
	}

	rawConfig, errRead := os.ReadFile(*configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !strings.Contains(string(rawConfig), "oauth-model-policy") {
		t.Fatalf("policy not persisted: %s", rawConfig)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/oauth-model-policy", nil)
	h.GetOAuthModelPolicy(getCtx)
	var got struct {
		Policy map[string]config.OAuthModelPolicyEntry `json:"oauth-model-policy"`
	}
	if errDecode := json.Unmarshal(getRec.Body.Bytes(), &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	entry, ok := got.Policy["copilot"]
	if !ok || entry.Allowlist == nil || !entry.Allowlist.Enabled || len(entry.Allowlist.Models) != 1 || entry.Allowlist.Models[0] != "gpt-*" {
		t.Fatalf("get policy = %#v", got.Policy)
	}

	// Empty policy removes the provider entry.
	rec = patchOAuthModelPolicyRequest(t, h, `{"provider":"copilot","policy":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove patch status = %d body %s", rec.Code, rec.Body.String())
	}
	if h.cfg.OAuthModelPolicy != nil {
		t.Fatalf("policy map should be empty after removal: %#v", h.cfg.OAuthModelPolicy)
	}

	// Patching a missing provider with an empty policy is a 404.
	rec = patchOAuthModelPolicyRequest(t, h, `{"provider":"copilot","policy":{}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing provider remove status = %d", rec.Code)
	}
}

func TestOAuthModelPolicyPatchRejectsInvalidEntry(t *testing.T) {
	h, _ := newOAuthModelPolicyTestHandler(t)
	rec := patchOAuthModelPolicyRequest(t, h, `{"provider":"codex","policy":{"denylist":{"enabled":true,"models":["bad\u0000id"]}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid entry status = %d body %s", rec.Code, rec.Body.String())
	}
	if h.cfg.OAuthModelPolicy != nil {
		t.Fatalf("invalid entry must not persist: %#v", h.cfg.OAuthModelPolicy)
	}
}

func TestOAuthModelPolicyPutRejectsInvalidMapping(t *testing.T) {
	h, _ := newOAuthModelPolicyTestHandler(t)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := `{"qoder":{"denylist":{"enabled":true,"models":["bad\nid"]}}}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/oauth-model-policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PutOAuthModelPolicy(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid put status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthModelPolicyPutItemsWrapper(t *testing.T) {
	h, _ := newOAuthModelPolicyTestHandler(t)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/oauth-model-policy", strings.NewReader(`{"items":{"codex":{"allowlist":{"enabled":true,"models":[]}}}}`))
	h.PutOAuthModelPolicy(ctx)
	list := h.cfg.OAuthModelPolicy["codex"].Allowlist
	if rec.Code != http.StatusOK || list == nil || !list.Enabled {
		t.Fatalf("wrapped PUT lost restriction: status=%d policy=%+v", rec.Code, h.cfg.OAuthModelPolicy)
	}
}
