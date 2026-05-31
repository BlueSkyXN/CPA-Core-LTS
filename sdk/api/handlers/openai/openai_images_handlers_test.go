package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type imageCaptureExecutor struct {
	sourceFormat string
	model        string
	payload      []byte
}

func (e *imageCaptureExecutor) Identifier() string { return "openai-compatibility" }

func (e *imageCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.sourceFormat = opts.SourceFormat.String()
	e.model = req.Model
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`)}, nil
}

func (e *imageCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *imageCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *imageCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imageCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type geminiChatImageCaptureExecutor struct {
	sourceFormat string
	model        string
	payload      []byte
}

func (e *geminiChatImageCaptureExecutor) Identifier() string { return "gemini" }

func (e *geminiChatImageCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.sourceFormat = opts.SourceFormat.String()
	e.model = req.Model
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"created":1700000000,"choices":[{"message":{"role":"assistant","images":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,/9j/ABC="}}]}}]}`)}, nil
}

func (e *geminiChatImageCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *geminiChatImageCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *geminiChatImageCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *geminiChatImageCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func performImagesEndpointRequest(t *testing.T, endpointPath string, contentType string, body io.Reader, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(endpointPath, handler)

	req := httptest.NewRequest(http.MethodPost, endpointPath, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func assertUnsupportedImagesModelResponse(t *testing.T, resp *httptest.ResponseRecorder, model string) {
	t.Helper()

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}

	message := gjson.GetBytes(resp.Body.Bytes(), "error.message").String()
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + defaultImagesToolModel + ", a Gemini image model, or a configured openai-compatibility image model."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
	if errorType := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); errorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", errorType)
	}
}

func TestImagesModelValidationAllowsGPTImage2WithOptionalPrefix(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "codex/gpt-image-2"} {
		if !isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if isSupportedImagesModel("gpt-5.4-mini") {
		t.Fatal("expected gpt-5.4-mini to be rejected")
	}
}

func TestImagesModelValidationAllowsOpenAICompatImageModel(t *testing.T) {
	authID := "test-openai-image-validation"
	modelID := "image-alias-validation"
	registry.GetGlobalRegistry().RegisterClient(authID, "openai-compatibility", []*registry.ModelInfo{{ID: modelID, Type: registry.OpenAIImageModelType}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	if !isSupportedImagesModel(modelID) {
		t.Fatalf("expected configured openai-compatibility image model %s to be supported", modelID)
	}
}

func TestImagesModelValidationAllowsGeminiImageModels(t *testing.T) {
	for _, model := range []string{
		"gemini-3.1-flash-image",
		"antigravity/gemini-3.1-flash-image",
		"gemini-2.5-flash-image-preview",
		"imagen-3",
	} {
		if !isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	for _, model := range []string{"gemini-2.5-flash", "gemini-2.5-pro"} {
		if isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be rejected", model)
		}
	}
}

func TestBuildGeminiChatImagesRequest(t *testing.T) {
	req := buildGeminiChatImagesRequest("a red apple", "antigravity/gemini-3.1-flash-image")

	if got := gjson.GetBytes(req, "model").String(); got != "antigravity/gemini-3.1-flash-image" {
		t.Fatalf("model = %q, want antigravity/gemini-3.1-flash-image", got)
	}
	if got := gjson.GetBytes(req, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(req, "messages.0.content").String(); got != "a red apple" {
		t.Fatalf("messages.0.content = %q, want a red apple", got)
	}
	if got := gjson.GetBytes(req, "modalities.0").String(); got != "image" {
		t.Fatalf("modalities.0 = %q, want image", got)
	}
	if got := gjson.GetBytes(req, "modalities.1").String(); got != "text" {
		t.Fatalf("modalities.1 = %q, want text", got)
	}
}

func TestExtractImagesFromChatCompletions(t *testing.T) {
	resp := []byte(`{"created":1700000000,"choices":[{"message":{"role":"assistant","images":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,/9j/ABC="}}]}}]}`)

	results, createdAt, err := extractImagesFromChatCompletions(resp)
	if err != nil {
		t.Fatalf("extractImagesFromChatCompletions() error = %v", err)
	}
	if createdAt != 1700000000 {
		t.Fatalf("createdAt = %d, want 1700000000", createdAt)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Result != "/9j/ABC=" {
		t.Fatalf("result = %q, want /9j/ABC=", results[0].Result)
	}
	if results[0].OutputFormat != "jpeg" {
		t.Fatalf("output_format = %q, want jpeg", results[0].OutputFormat)
	}
}

func TestExtractImagesFromChatCompletionsNoImages(t *testing.T) {
	resp := []byte(`{"created":1700000000,"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)

	_, _, err := extractImagesFromChatCompletions(resp)
	if err == nil {
		t.Fatal("expected error for response with no images")
	}
}

func TestImagesGenerationsRoutesOpenAICompatImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-openai-image", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelID := "image-alias"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID, Type: registry.OpenAIImageModelType}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"image-alias","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("b64_json = %q, want AA==", got)
	}
	if executor.sourceFormat != openAIImagesHandlerType {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, openAIImagesHandlerType)
	}
	if executor.model != modelID {
		t.Fatalf("model = %q, want %q", executor.model, modelID)
	}
	if gjson.GetBytes(executor.payload, "stream").Exists() {
		t.Fatalf("expected non-streaming payload to remove stream flag: %s", string(executor.payload))
	}
}

func TestImagesGenerationsRoutesGeminiImageModelThroughChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &geminiChatImageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	modelID := "gemini-3.1-flash-image"
	auth := &coreauth.Auth{ID: "auth-gemini-image", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gemini-3.1-flash-image","prompt":"draw a red apple","response_format":"url"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "data:image/jpeg;base64,/9j/ABC=" {
		t.Fatalf("data.0.url = %q, want data:image/jpeg;base64,/9j/ABC=", got)
	}
	if executor.sourceFormat != "openai" {
		t.Fatalf("source format = %q, want openai", executor.sourceFormat)
	}
	if executor.model != modelID {
		t.Fatalf("model = %q, want %q", executor.model, modelID)
	}
	if got := gjson.GetBytes(executor.payload, "messages.0.content").String(); got != "draw a red apple" {
		t.Fatalf("messages.0.content = %q, want draw a red apple; payload=%s", got, string(executor.payload))
	}
	if got := gjson.GetBytes(executor.payload, "modalities.0").String(); got != "image" {
		t.Fatalf("modalities.0 = %q, want image; payload=%s", got, string(executor.payload))
	}
}

func TestImagesEditsJSONRoutesOpenAICompatImageModelBeforeCodexImageValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-openai-image-edit", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelID := "image-alias-edit"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID, Type: registry.OpenAIImageModelType}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"image-alias-edit","prompt":"edit this","image":"raw-image-ref","stream":false}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.sourceFormat != openAIImagesHandlerType {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, openAIImagesHandlerType)
	}
	if executor.model != modelID {
		t.Fatalf("model = %q, want %q", executor.model, modelID)
	}
	if got := gjson.GetBytes(executor.payload, "image").String(); got != "raw-image-ref" {
		t.Fatalf("image field = %q, want raw-image-ref; payload=%s", got, string(executor.payload))
	}
	if gjson.GetBytes(executor.payload, "stream").Exists() {
		t.Fatalf("expected non-streaming executor payload to remove stream flag: %s", string(executor.payload))
	}
}

func TestImagesGenerationsRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesEditsJSONRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesEditsMultipartRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-5.4-mini"); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	if err := writer.WriteField("prompt", "edit this"); err != nil {
		t.Fatalf("write prompt field: %v", err)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesGenerations_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesGenerations_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}
