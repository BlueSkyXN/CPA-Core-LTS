package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesRecentRequestsBuckets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "runtime-only-auth-1",
		Provider: "codex",
		Prefix:   "team",
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	ginCtx.Request = req

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := payload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", payload)
	}
	if len(filesRaw) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(filesRaw))
	}

	fileEntry, ok := filesRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected file entry object, got %#v", filesRaw[0])
	}

	if _, ok := fileEntry["success"].(float64); !ok {
		t.Fatalf("expected success number, got %#v", fileEntry["success"])
	}
	if _, ok := fileEntry["failed"].(float64); !ok {
		t.Fatalf("expected failed number, got %#v", fileEntry["failed"])
	}
	if got, _ := fileEntry["prefix"].(string); got != "team" {
		t.Fatalf("prefix = %q, want team", got)
	}

	recentRaw, ok := fileEntry["recent_requests"].([]any)
	if !ok {
		t.Fatalf("expected recent_requests array, got %#v", fileEntry["recent_requests"])
	}
	if len(recentRaw) != 20 {
		t.Fatalf("expected 20 recent_requests buckets, got %d", len(recentRaw))
	}
	for idx, item := range recentRaw {
		bucket, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected bucket object at %d, got %#v", idx, item)
		}
		if _, ok := bucket["time"].(string); !ok {
			t.Fatalf("expected bucket time string at %d, got %#v", idx, bucket["time"])
		}
		if _, ok := bucket["success"].(float64); !ok {
			t.Fatalf("expected bucket success number at %d, got %#v", idx, bucket["success"])
		}
		if _, ok := bucket["failed"].(float64); !ok {
			t.Fatalf("expected bucket failed number at %d, got %#v", idx, bucket["failed"])
		}
	}
}

func TestBuildAuthFromFileDataHydratesPrefix(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, nil)
	path := filepath.Join(dir, "prefix.json")
	auth, err := h.buildAuthFromFileData(path, []byte(`{"type":"claude","prefix":" /team/ "}`))
	if err != nil {
		t.Fatalf("buildAuthFromFileData: %v", err)
	}
	if auth == nil || auth.Prefix != "team" {
		t.Fatalf("auth = %#v, want hydrated prefix team", auth)
	}
}

func TestListAuthFilesPrunesOnlyExplicitlyExpiredAvailability(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{
			ID:             "expired-auth",
			Provider:       "claude",
			Status:         coreauth.StatusError,
			StatusMessage:  "transient upstream error",
			Unavailable:    true,
			NextRetryAfter: past,
			LastError:      &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"},
			Attributes:     map[string]string{"runtime_only": "true"},
		},
		{
			ID:             "future-auth",
			Provider:       "claude",
			Status:         coreauth.StatusError,
			StatusMessage:  "quota exhausted",
			Unavailable:    true,
			NextRetryAfter: future,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future},
			LastError:      &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			ModelStates: map[string]*coreauth.ModelState{
				"expired-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: past, LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}},
			},
			Attributes: map[string]string{"runtime_only": "true"},
		},
		{
			ID:            "zero-auth",
			Provider:      "claude",
			Status:        coreauth.StatusError,
			StatusMessage: "quota exhausted without recovery time",
			Unavailable:   true,
			Quota:         coreauth.QuotaState{Exceeded: true, Reason: "quota"},
			LastError:     &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			ModelStates: map[string]*coreauth.ModelState{
				"expired-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: past, LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}},
			},
			Attributes: map[string]string{"runtime_only": "true"},
		},
		{
			ID:             "cloudflare-auth",
			Provider:       "claude",
			Status:         coreauth.StatusError,
			StatusMessage:  "cloudflare challenge",
			Unavailable:    true,
			NextRetryAfter: past,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: past},
			LastError:      &coreauth.Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"},
			ModelStates: map[string]*coreauth.ModelState{
				"expired-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: past, LastError: &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}},
			},
			Attributes: map[string]string{"runtime_only": "true"},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byID := make(map[string]map[string]any, len(payload.Files))
	for _, entry := range payload.Files {
		id, _ := entry["id"].(string)
		byID[id] = entry
	}
	expired := byID["expired-auth"]
	if expired["status"] != string(coreauth.StatusActive) || expired["unavailable"] != false || expired["status_message"] != "" {
		t.Fatalf("expired entry = %#v, want active without warning", expired)
	}
	if _, ok := expired["next_retry_after"]; ok {
		t.Fatalf("expired entry retained next_retry_after: %#v", expired)
	}
	futureEntry := byID["future-auth"]
	if futureEntry["status"] != string(coreauth.StatusError) || futureEntry["unavailable"] != true {
		t.Fatalf("future entry = %#v, want preserved error", futureEntry)
	}
	if _, ok := futureEntry["next_retry_after"]; !ok {
		t.Fatalf("future entry lost next_retry_after: %#v", futureEntry)
	}
	zero := byID["zero-auth"]
	if zero["status"] != string(coreauth.StatusError) || zero["unavailable"] != true || zero["status_message"] != "quota exhausted without recovery time" {
		t.Fatalf("zero-deadline entry = %#v, want preserved warning", zero)
	}
	if _, ok := zero["next_retry_after"]; ok {
		t.Fatalf("zero-deadline entry unexpectedly gained next_retry_after: %#v", zero)
	}
	cloudflare := byID["cloudflare-auth"]
	if cloudflare["status"] != string(coreauth.StatusError) || cloudflare["unavailable"] != true || cloudflare["status_message"] != "cloudflare challenge" {
		t.Fatalf("cloudflare entry = %#v, want preserved challenge", cloudflare)
	}
}
