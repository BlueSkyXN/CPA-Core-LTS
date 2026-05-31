package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestSyncAuthFileCompactAttribute(t *testing.T) {
	auth := &coreauth.Auth{
		ID:         "codex.json",
		Provider:   "codex",
		Metadata:   map[string]any{"compact": "force_off"},
		Attributes: map[string]string{},
	}
	syncAuthFileCompactAttribute(auth, true)
	if got := auth.Attributes["compact_mode"]; got != "force_off" {
		t.Fatalf("compact_mode = %q, want force_off", got)
	}
	if got := auth.Attributes["compact_allowed"]; got != "false" {
		t.Fatalf("compact_allowed = %q, want false", got)
	}

	auth.Metadata["compact"] = "auto"
	syncAuthFileCompactAttribute(auth, false)
	if got := auth.Attributes["compact_allowed"]; got != "false" {
		t.Fatalf("auto with default deny compact_allowed = %q, want false", got)
	}

	nonCodex := &coreauth.Auth{
		ID:         "gemini.json",
		Provider:   "gemini-cli",
		Metadata:   map[string]any{"compact": "force_on"},
		Attributes: map[string]string{"compact_mode": "force_on", "compact_allowed": "true"},
	}
	syncAuthFileCompactAttribute(nonCodex, true)
	if _, ok := nonCodex.Attributes["compact_mode"]; ok {
		t.Fatalf("non-codex compact_mode should be removed, got %#v", nonCodex.Attributes)
	}
	if _, ok := nonCodex.Attributes["compact_allowed"]; ok {
		t.Fatalf("non-codex compact_allowed should be removed, got %#v", nonCodex.Attributes)
	}
}

func TestBuildAuthFileEntry_IncludesCodexCompact(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	entry := h.buildAuthFileEntry(&coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Attributes: map[string]string{
			"path":         "/tmp/codex.json",
			"compact_mode": "force_on",
		},
	})
	if got := entry["compact"]; got != "force_on" {
		t.Fatalf("entry[compact] = %v, want force_on", got)
	}

	nonCodex := h.buildAuthFileEntry(&coreauth.Auth{
		ID:       "claude.json",
		FileName: "claude.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":         "/tmp/claude.json",
			"compact_mode": "force_on",
		},
	})
	if _, ok := nonCodex["compact"]; ok {
		t.Fatalf("non-codex entry compact = %v, want absent", nonCodex["compact"])
	}
}

func TestPatchAuthFileFields_CompactCodex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	record := &coreauth.Auth{
		ID:         "codex.json",
		FileName:   "codex.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/tmp/codex.json"},
		Metadata:   map[string]any{"type": "codex"},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("register: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), CompactDefault: "deny"}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(`{"name":"codex.json","compact":"AUTO"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	updated, ok := manager.GetByID("codex.json")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if got := updated.Attributes["compact_mode"]; got != "auto" {
		t.Fatalf("compact_mode = %q, want auto", got)
	}
	if got := updated.Attributes["compact_allowed"]; got != "false" {
		t.Fatalf("compact_allowed = %q, want false", got)
	}
	if got, _ := updated.Metadata["compact"].(string); got != "auto" {
		raw, _ := json.Marshal(updated.Metadata)
		t.Fatalf("metadata compact = %q, metadata = %s", got, string(raw))
	}
}

func TestPatchAuthFileFields_CompactRejectsInvalidAndNonCodex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, record := range []*coreauth.Auth{
		{ID: "codex.json", FileName: "codex.json", Provider: "codex", Attributes: map[string]string{"path": "/tmp/codex.json"}, Metadata: map[string]any{"type": "codex"}},
		{ID: "gemini.json", FileName: "gemini.json", Provider: "gemini-cli", Attributes: map[string]string{"path": "/tmp/gemini.json"}, Metadata: map[string]any{"type": "gemini"}},
	} {
		if _, err := manager.Register(context.Background(), record); err != nil {
			t.Fatalf("register %s: %v", record.ID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid", body: `{"name":"codex.json","compact":"bad"}`},
		{name: "non-codex", body: `{"name":"gemini.json","compact":"force_on"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			h.PatchAuthFileFields(ctx)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}
