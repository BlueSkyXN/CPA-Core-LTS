package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func newCodexOpenAIImageTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": serverURL,
			"api_key":  "codex-token",
		},
	}
}

func codexOpenAIImageTestOptions(path string, stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Stream:       stream,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: path,
		},
	}
}

func TestPublishCodexImageToolUsagePreservesResponseTierPrecedence(t *testing.T) {
	tests := []struct {
		name              string
		responseTier      string
		wantEffectiveTier string
	}{
		{name: "recognized response overrides outbound", responseTier: "default", wantEffectiveTier: "standard"},
		{name: "unknown response blocks outbound fallback", responseTier: "flex", wantEffectiveTier: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginName := "codex-image-effective-tier-" + strings.ReplaceAll(tt.name, " ", "-")
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin(pluginName, recorder)
			t.Cleanup(func() { usage.RegisterNamedPlugin(pluginName, noopUsagePlugin{}) })

			parentModel := "gpt-effective-parent-" + strings.ReplaceAll(tt.name, " ", "-")
			imageModel := "gpt-effective-image-" + strings.ReplaceAll(tt.name, " ", "-")
			body := []byte(`{"service_tier":"priority","tools":[{"type":"image_generation","model":"` + imageModel + `"}]}`)
			completed := []byte(`{"type":"response.completed","response":{"service_tier":"` + tt.responseTier + `","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3},"tool_usage":{"image_gen":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}}}`)

			reporter := helps.NewUsageReporter(context.Background(), "codex", parentModel, nil)
			reporter.SetOutboundServiceTier(body)
			parentDetail, ok := helps.ParseCodexUsage(completed)
			if !ok {
				t.Fatalf("ParseCodexUsage() ok = false; payload=%s", completed)
			}
			reporter.Publish(context.Background(), parentDetail)
			publishCodexImageToolUsage(context.Background(), reporter, body, completed)

			parentRecord := recorder.waitForRecord(t, func(record usage.Record) bool { return record.Model == parentModel })
			imageRecord := recorder.waitForRecord(t, func(record usage.Record) bool { return record.Model == imageModel })
			for model, record := range map[string]usage.Record{
				parentModel: parentRecord,
				imageModel:  imageRecord,
			} {
				if record.ResponseServiceTier != tt.responseTier {
					t.Errorf("record[%s].ResponseServiceTier = %q, want %q", model, record.ResponseServiceTier, tt.responseTier)
				}
				if record.EffectiveServiceTier != tt.wantEffectiveTier {
					t.Errorf("record[%s].EffectiveServiceTier = %q, want %q", model, record.EffectiveServiceTier, tt.wantEffectiveTier)
				}
			}
		})
	}
}

func TestCodexExecutorDirectOpenAIImageGenerationUsesImagesEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotUA string
	var gotVersion string
	var gotTurnMetadata string
	var gotClientRequestID string
	var gotOriginator string
	var gotBody []byte
	upstreamBody := []byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}],"usage":{"total_tokens":100,"input_tokens":50,"output_tokens":50}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		gotVersion = r.Header.Get("Version")
		gotTurnMetadata = r.Header.Get("X-Codex-Turn-Metadata")
		gotClientRequestID = r.Header.Get("X-Client-Request-Id")
		gotOriginator = r.Header.Get("Originator")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "downstream-client/9.9",
		"Version":               "0.135.0",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "client-request-1",
		"Originator":            "Codex Desktop",
	})
	executor := NewCodexExecutor(&config.Config{})
	resp, errExecute := executor.Execute(ctx, newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "codex/gpt-image-1.5",
		Payload: []byte(`{"model":"codex/gpt-image-1.5","prompt":"A cute baby sea otter","n":1,"size":"1024x1024","quality":"high","background":"opaque","output_format":"jpeg","output_compression":70,"moderation":"low","extra":{"preserve":true},"stream":false}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", gotPath)
	}
	if gotAuth != "Bearer codex-token" {
		t.Fatalf("Authorization = %q, want Bearer codex-token", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", gotAccept)
	}
	if gotUA != codexUserAgent {
		t.Fatalf("User-Agent = %q, want codex default %q", gotUA, codexUserAgent)
	}
	if gotVersion != "0.135.0" {
		t.Fatalf("Version = %q, want %q", gotVersion, "0.135.0")
	}
	if gotTurnMetadata != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want %q", gotTurnMetadata, `{"turn_id":"turn-1"}`)
	}
	if gotClientRequestID != "client-request-1" {
		t.Fatalf("X-Client-Request-Id = %q, want %q", gotClientRequestID, "client-request-1")
	}
	if gotOriginator != "Codex Desktop" {
		t.Fatalf("Originator = %q, want %q", gotOriginator, "Codex Desktop")
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-1.5" {
		t.Fatalf("model = %q, want gpt-image-1.5; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "extra.preserve").Bool(); !got {
		t.Fatalf("extra.preserve missing from body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "output_compression").Int(); got != 70 {
		t.Fatalf("output_compression = %d, want 70; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream should be removed for non-stream execution: %s", string(gotBody))
	}
	if !bytes.Equal(resp.Payload, upstreamBody) {
		t.Fatalf("payload = %s, want %s", string(resp.Payload), string(upstreamBody))
	}
}

func TestCodexExecutorDirectOpenAIImageRepairsCanonicalMetadata(t *testing.T) {
	var gotBody []byte
	var gotTurnMetadata string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTurnMetadata = r.Header.Get("X-Codex-Turn-Metadata")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	canonical := `{"installation_id":"install-image-1","session_id":"thread-image-1","thread_id":"thread-image-1","turn_id":"turn-image-1","window_id":"thread-image-1:1","request_kind":"turn","workspaces":{"/Users/private/image-project":{"associated_remote_urls":{"origin":"https://user:secret@example.com/org/repo.git"}}}}`
	payload, errMarshal := json.Marshal(map[string]any{
		"model":  "gpt-image-1.5",
		"prompt": "A private workspace image",
		"client_metadata": map[string]any{
			"x-codex-turn-metadata": canonical,
			"thread_id":             "wrong-thread",
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	auth := newCodexOpenAIImageTestAuth(server.URL)
	auth.ID = "image-auth-1"
	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode:            config.CodexClientMetadataModeRepair,
		WorkspacePolicy: config.CodexClientMetadataWorkspacePolicyDrop,
	}}})
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1.5",
		Payload: payload,
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	metadata := gjson.GetBytes(gotBody, "client_metadata.x-codex-turn-metadata").String()
	if strings.Contains(metadata, `"workspaces"`) || strings.Contains(metadata, "secret@example.com") {
		t.Fatalf("direct image request retained workspace metadata: %s", metadata)
	}
	if got := gjson.GetBytes(gotBody, "client_metadata.thread_id").String(); got != "thread-image-1" {
		t.Fatalf("client_metadata.thread_id = %q, want thread-image-1", got)
	}
	if gotTurnMetadata != metadata {
		t.Fatalf("X-Codex-Turn-Metadata does not match canonical image metadata: header=%s body=%s", gotTurnMetadata, metadata)
	}
}

func TestPrepareCodexOpenAIImageBodyPreservesClientMetadata(t *testing.T) {
	canonical := `{"installation_id":"install-image-2","session_id":"thread-image-2","thread_id":"thread-image-2","window_id":"thread-image-2:1","request_kind":"turn"}`
	payload, errMarshal := json.Marshal(map[string]any{
		"model":  "gpt-image-legacy",
		"prompt": "image",
		"client_metadata": map[string]any{
			"x-codex-turn-metadata": canonical,
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	executor := NewCodexExecutor(&config.Config{})
	body, err := executor.prepareCodexOpenAIImageBody(codexBuildImagesResponsesRequest("image", nil, nil), cliproxyexecutor.Request{
		Model:   "gpt-image-legacy",
		Payload: payload,
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false), codexOpenAIImagesMainModel)
	if err != nil {
		t.Fatalf("prepareCodexOpenAIImageBody() error = %v", err)
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String(); got != canonical {
		t.Fatalf("canonical client metadata = %q, want %q; body=%s", got, canonical, body)
	}
}

func TestCodexExecutorOpenAIImageSDKHeaderOnlyCanonicalAcrossPaths(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		stream bool
		direct bool
	}{
		{name: "direct non-stream", model: "gpt-image-1.5", direct: true},
		{name: "direct stream", model: "gpt-image-1.5", direct: true, stream: true},
		{name: "translated non-stream", model: "gpt-image-legacy"},
		{name: "translated stream", model: "gpt-image-legacy", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTurnMetadata string
			var gotSessionID string
			var gotBody []byte
			readErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTurnMetadata = r.Header.Get("X-Codex-Turn-Metadata")
				gotSessionID = codexSessionHeaderValue(r.Header)
				var errRead error
				gotBody, errRead = io.ReadAll(r.Body)
				readErr <- errRead
				if tt.direct && !tt.stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"created_at\":111,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"AAA\",\"output_format\":\"png\"}]}}\n\n"))
			}))
			defer server.Close()

			canonical := `{"request_kind":"turn","session_id":"image-sdk-session","thread_id":"image-sdk-session"}`
			payload := []byte(`{"model":"` + tt.model + `","prompt":"image"}`)
			opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, tt.stream)
			opts.Headers = http.Header{"X-Codex-Turn-Metadata": {canonical}}
			executor := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: tt.model, Payload: payload}
			if tt.stream {
				stream, err := executor.ExecuteStream(context.Background(), newCodexOpenAIImageTestAuth(server.URL), req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), req, opts); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if err := <-readErr; err != nil {
				t.Fatalf("read upstream body: %v", err)
			}
			if got := gjson.Get(gotTurnMetadata, "session_id").String(); got != "image-sdk-session" {
				t.Fatalf("X-Codex-Turn-Metadata session_id = %q; header=%s", got, gotTurnMetadata)
			}
			if gotSessionID != "image-sdk-session" {
				t.Fatalf("Session_id = %q, want image-sdk-session", gotSessionID)
			}
			if bodyCanonical := gjson.GetBytes(gotBody, "client_metadata.x-codex-turn-metadata").String(); bodyCanonical != gotTurnMetadata {
				t.Fatalf("body/header canonical mismatch: body=%s header=%s request=%s", bodyCanonical, gotTurnMetadata, gotBody)
			}
		})
	}
}

func TestCodexExecutorOpenAIImageOffModePreservesSDKHeaderOnlyCanonical(t *testing.T) {
	var gotTurnMetadata string
	var gotSessionID string
	var gotBody []byte
	readErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTurnMetadata = r.Header.Get("X-Codex-Turn-Metadata")
		gotSessionID = codexSessionHeaderValue(r.Header)
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		readErr <- errRead
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	canonical := `{"request_kind":"turn","session_id":"image-off-sdk-session","thread_id":"image-off-sdk-session"}`
	payload := []byte(`{"model":"gpt-image-1.5","prompt":"image"}`)
	opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, false)
	opts.Headers = http.Header{"X-Codex-Turn-Metadata": {canonical}}
	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{Mode: config.CodexClientMetadataModeOff}}})
	if _, err := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{Model: "gpt-image-1.5", Payload: payload}, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if err := <-readErr; err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if gotTurnMetadata != canonical {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want original %q", gotTurnMetadata, canonical)
	}
	if gotSessionID != "image-off-sdk-session" {
		t.Fatalf("Session_id = %q, want image-off-sdk-session", gotSessionID)
	}
	if gjson.GetBytes(gotBody, "client_metadata").Exists() {
		t.Fatalf("off mode rebuilt body canonical metadata: %s", gotBody)
	}
}

func TestPrepareCodexOpenAIImageBodyMergesMetadataBeforePayloadRules(t *testing.T) {
	canonical := `{"request_kind":"turn","session_id":"image-merge-session"}`
	encodedCanonical, errMarshal := json.Marshal(canonical)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	source := []byte(`{"model":"gpt-image-legacy","prompt":"image","client_metadata":{"x-codex-turn-metadata":` + string(encodedCanonical) + `,"client_field":"client"}}`)
	translated := codexBuildImagesResponsesRequest("image", nil, nil)
	translated, _ = sjson.SetBytes(translated, "client_metadata.transport_marker", true)
	modelRule := []config.PayloadModelRule{{Name: "*", Protocol: "codex"}}

	tests := []struct {
		name      string
		payload   config.PayloadConfig
		wantValue string
		wantField bool
	}{
		{name: "default preserves client", payload: config.PayloadConfig{Default: []config.PayloadRule{{Models: modelRule, Params: map[string]any{"client_metadata.client_field": "default"}}}}, wantValue: "client", wantField: true},
		{name: "override wins", payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: modelRule, Params: map[string]any{"client_metadata.client_field": "override"}}}}, wantValue: "override", wantField: true},
		{name: "filter wins", payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: modelRule, Params: []string{"client_metadata.client_field"}}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewCodexExecutor(&config.Config{Payload: tt.payload})
			body, err := executor.prepareCodexOpenAIImageBody(translated, cliproxyexecutor.Request{Model: "gpt-image-legacy", Payload: source}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false), codexOpenAIImagesMainModel)
			if err != nil {
				t.Fatalf("prepareCodexOpenAIImageBody() error = %v", err)
			}
			if !gjson.GetBytes(body, "client_metadata.transport_marker").Bool() {
				t.Fatalf("translated metadata was replaced instead of merged: %s", body)
			}
			field := gjson.GetBytes(body, "client_metadata.client_field")
			if field.Exists() != tt.wantField || (tt.wantField && field.String() != tt.wantValue) {
				t.Fatalf("client_field = %s exists=%t, want %q exists=%t; body=%s", field.Raw, field.Exists(), tt.wantValue, tt.wantField, body)
			}
		})
	}
}

func TestCodexExecutorOpenAIImageRejectsDuplicateMetadataBeforeTransformInOffMode(t *testing.T) {
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{Mode: config.CodexClientMetadataModeOff}}})
	body := []byte(`{"model":"gpt-image-legacy","prompt":"image","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"one\"}"},"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"two\"}"}}`)
	_, err := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{Model: "gpt-image-legacy", Payload: body}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if err == nil {
		t.Fatal("Execute() accepted duplicate client_metadata before image transform")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %T %v, want 400", err, err)
	}
	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("error = %T %v, want request-scoped", err, err)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestCodexExecutorOpenAIImageOffModeDoesNotPrevalidateMalformedCanonical(t *testing.T) {
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{Mode: config.CodexClientMetadataModeOff}}})
	body := []byte(`{"model":"gpt-image-1.5","prompt":"image","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\""}}`)
	_, err := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{Model: "gpt-image-1.5", Payload: body}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if err != nil {
		t.Fatalf("Execute() off-mode malformed canonical error = %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestCodexExecutorDirectOpenAIImageGenerationStreamsImagesEndpoint(t *testing.T) {
	var gotPath string
	var gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"AA==\",\"partial_image_index\":0}\n\n"))
		_, _ = w.Write([]byte("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"BB==\",\"usage\":{\"total_tokens\":10,\"input_tokens\":4,\"output_tokens\":6}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	stream, errStream := executor.ExecuteStream(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"A cute baby sea otter","partial_images":2}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, true))
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var combined bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if gotPath != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", gotPath)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", gotAccept)
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream flag missing from upstream body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "partial_images").Int(); got != 2 {
		t.Fatalf("partial_images = %d, want 2; body=%s", got, string(gotBody))
	}
	out := combined.String()
	if !strings.Contains(out, "event: image_generation.partial_image") || !strings.Contains(out, "event: image_generation.completed") {
		t.Fatalf("stream output missing image events: %q", out)
	}
}

func TestCodexExecutorDirectOpenAIImageEditUsesImagesEditEndpointForJSON(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}],"usage":{"total_tokens":10}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, errExecute := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"Replace the background","images":[{"file_id":"file-abc123"}],"mask":{"file_id":"file-mask123"},"size":"1024x1024","quality":"high","output_format":"png","output_compression":100,"stream":false}`),
	}, codexOpenAIImageTestOptions(codexImagesEditsPath, false))
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/edits" {
		t.Fatalf("path = %q, want /images/edits", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-2" {
		t.Fatalf("model = %q, want gpt-image-2; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "images.0.file_id").String(); got != "file-abc123" {
		t.Fatalf("images.0.file_id = %q, want file-abc123; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "mask.file_id").String(); got != "file-mask123" {
		t.Fatalf("mask.file_id = %q, want file-mask123; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream should be removed for non-stream execution: %s", string(gotBody))
	}
}

func TestCodexExecutorDirectOpenAIImageEditUsesImagesEditEndpointForMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "codex/gpt-image-1.5"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "Create a lovely gift basket"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	if errWrite := writer.WriteField("output_format", "webp"); errWrite != nil {
		t.Fatalf("write output_format field: %v", errWrite)
	}
	if errWrite := writer.WriteField("n", "2"); errWrite != nil {
		t.Fatalf("write n field: %v", errWrite)
	}
	if errWrite := writer.WriteField("stream", "false"); errWrite != nil {
		t.Fatalf("write stream field: %v", errWrite)
	}
	imagePart, errCreate := writer.CreateFormFile("image[]", "source.png")
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := imagePart.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image data: %v", errWrite)
	}
	maskPart, errCreateMask := writer.CreateFormFile("mask", "mask.png")
	if errCreateMask != nil {
		t.Fatalf("create mask field: %v", errCreateMask)
	}
	if _, errWrite := maskPart.Write([]byte("mask-data")); errWrite != nil {
		t.Fatalf("write mask data: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	var gotPath string
	var gotContentType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	opts := codexOpenAIImageTestOptions(codexImagesEditsPath, false)
	opts.Headers = http.Header{"Content-Type": []string{writer.FormDataContentType()}}
	executor := NewCodexExecutor(&config.Config{})
	_, errExecute := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "codex/gpt-image-1.5",
		Payload: body.Bytes(),
	}, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/edits" {
		t.Fatalf("path = %q, want /images/edits", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if !json.Valid(gotBody) {
		t.Fatalf("body is not valid JSON: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-1.5" {
		t.Fatalf("model = %q, want gpt-image-1.5; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "prompt").String(); got != "Create a lovely gift basket" {
		t.Fatalf("prompt = %q", got)
	}
	if got := gjson.GetBytes(gotBody, "output_format").String(); got != "webp" {
		t.Fatalf("output_format = %q, want webp; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "n").Int(); got != 2 {
		t.Fatalf("n = %d, want 2; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream should be removed for non-stream execution: %s", string(gotBody))
	}
	imageURL := gjson.GetBytes(gotBody, "images.0.image_url").String()
	if !strings.Contains(imageURL, ";base64,cG5nLWRhdGE=") {
		t.Fatalf("images.0.image_url = %q, want png-data data URL; body=%s", imageURL, string(gotBody))
	}
	maskURL := gjson.GetBytes(gotBody, "mask.image_url").String()
	if !strings.Contains(maskURL, ";base64,bWFzay1kYXRh") {
		t.Fatalf("mask.image_url = %q, want mask-data data URL; body=%s", maskURL, string(gotBody))
	}
}
