package cmd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gemini"
)

func TestPerformGeminiCLISetupUsesBackendProjectID(t *testing.T) {
	client := &http.Client{Transport: geminiCLILoginTestTransport{}}
	storage := &gemini.GeminiTokenStorage{}

	if err := performGeminiCLISetup(context.Background(), client, storage, "frontend-project"); err != nil {
		t.Fatalf("performGeminiCLISetup() error = %v", err)
	}

	if got := storage.ProjectID; got != "backend-project" {
		t.Fatalf("storage.ProjectID = %q, want backend-project", got)
	}
}

type geminiCLILoginTestTransport struct{}

func (geminiCLILoginTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch {
	case strings.HasSuffix(req.URL.Path, "/v1internal:loadCodeAssist"):
		body = `{"allowedTiers":[{"id":"PRO","isDefault":true}]}`
	case strings.HasSuffix(req.URL.Path, "/v1internal:onboardUser"):
		body = `{"done":true,"response":{"cloudaicompanionProject":{"id":"backend-project"}}}`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}
