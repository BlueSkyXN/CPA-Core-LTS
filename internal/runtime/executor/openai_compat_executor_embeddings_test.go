package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatExecutor_EmbeddingsEndpointPassthrough(t *testing.T) {
	const responseModel = "text-embedding-3-small"
	var gotPath, gotAuth, gotModel, gotInput string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotModel, _ = jsonGetString(body, "model")
		gotInput, _ = jsonGetString(body, "input")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"` + responseModel + `","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer server.Close()

	alias := "openai-compat-embeddings-test"
	capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
	})

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL,
			"api_key":      "test-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	payload := []byte(`{"model":"alias-prefix/text-embedding-3-small","input":"hello world"}`)
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "text-embedding-3-small",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("embeddings"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotPath != "/embeddings" {
		t.Fatalf("upstream path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if gotInput != "hello world" {
		t.Fatalf("input was not forwarded verbatim: %q", gotInput)
	}
	if gotModel != "text-embedding-3-small" {
		t.Fatalf("model was not normalized to the resolved name: %q", gotModel)
	}

	record := capture.await(t)
	if record.Model != responseModel {
		t.Fatalf("recorded Model = %q, want %q", record.Model, responseModel)
	}
}

func jsonGetString(body []byte, key string) (string, error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode request body: %w", err)
	}
	value, _ := parsed[key].(string)
	return value, nil
}
