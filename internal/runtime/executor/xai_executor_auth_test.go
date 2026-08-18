package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestXAIExecutorRefreshReplacesStaleBillingUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access-token",
			"refresh_token": "fresh-refresh-token",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	var fetchedAccessToken string
	exec.userIDFetcher = func(_ context.Context, accessToken, proxyURL string) (string, error) {
		fetchedAccessToken = accessToken
		if proxyURL != "" {
			t.Fatalf("proxy URL = %q, want empty", proxyURL)
		}
		return "fresh-billing-user", nil
	}
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{
			"access_token":   "stale-access-token",
			"refresh_token":  "stale-refresh-token",
			"token_endpoint": server.URL,
			"user_id":        "stale-billing-user",
		},
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if fetchedAccessToken != "fresh-access-token" {
		t.Fatalf("identity access token = %q, want fresh token", fetchedAccessToken)
	}
	if got := xaiMetadataString(refreshed.Metadata, "user_id"); got != "fresh-billing-user" {
		t.Fatalf("user_id = %q, want fresh-billing-user", got)
	}
}

func TestXAIExecutorRefreshKeepsExistingUserIDWhenEnrichmentFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access-token",
			"refresh_token": "fresh-refresh-token",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	exec.userIDFetcher = func(context.Context, string, string) (string, error) {
		return "", context.DeadlineExceeded
	}
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{
			"refresh_token":  "stale-refresh-token",
			"token_endpoint": server.URL,
			"user_id":        "existing-billing-user",
		},
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := xaiMetadataString(refreshed.Metadata, "user_id"); got != "existing-billing-user" {
		t.Fatalf("user_id = %q, want existing-billing-user", got)
	}
}
