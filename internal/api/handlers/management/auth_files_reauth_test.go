package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesFlagsCredentialsNeedingReauth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	runtimeOnly := map[string]string{"runtime_only": "true"}
	// Access token still valid: the credential stays active, only the refresh was rejected.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "active", Provider: "codex", Status: coreauth.StatusActive, RefreshFailures: 1,
		Attributes: runtimeOnly,
	})
	// Access token expired: the refresh path already wrote its own status message.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "expired", Provider: "codex", Status: coreauth.StatusError, Unavailable: true,
		StatusMessage: "invalid grant (retrying)", RefreshFailures: 2,
		NextRetryAfter: time.Now().Add(time.Hour), Attributes: runtimeOnly,
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "healthy", Provider: "codex", Status: coreauth.StatusActive, Attributes: runtimeOnly,
	})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	byID := make(map[string]map[string]any, len(payload.Files))
	for _, file := range payload.Files {
		id, _ := file["id"].(string)
		byID[id] = file
	}

	active := byID["active"]
	if active["reauth_required"] != true || active["status_message"] != authFileReauthRequiredMessage {
		t.Fatalf("active entry = %+v, want reauth_required with the re-login message", active)
	}
	if active["status"] != string(coreauth.StatusActive) || active["unavailable"] != false {
		t.Fatalf("active entry = %+v, want it to stay active and available", active)
	}
	expired := byID["expired"]
	if expired["reauth_required"] != true || expired["status_message"] != "invalid grant (retrying)" {
		t.Fatalf("expired entry = %+v, want reauth_required keeping the refresh status message", expired)
	}
	healthy := byID["healthy"]
	if _, ok := healthy["reauth_required"]; ok || healthy["status_message"] != "" {
		t.Fatalf("healthy entry = %+v, want no reauth flag or message", healthy)
	}
}
