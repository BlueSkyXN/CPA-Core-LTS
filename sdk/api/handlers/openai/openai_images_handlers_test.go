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
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
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
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + defaultImagesToolModel + " or a configured openai-compatibility image model."
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

func TestCollectImagesFromResponsesStreamCompleted(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.completed
data: {"type":"response.completed","response":{"created_at":123,"output":[{"type":"image_generation_call","result":"image-data","output_format":"png","revised_prompt":"refined"}],"tool_usage":{"image_gen":{"total_tokens":7}}}}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if errMsg != nil {
		t.Fatalf("collectImagesFromResponsesStream() error = %v", errMsg.Error)
	}
	if got := gjson.GetBytes(out, "created").Int(); got != 123 {
		t.Fatalf("created = %d, want 123", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "image-data" {
		t.Fatalf("data.0.b64_json = %q, want image-data", got)
	}
	if got := gjson.GetBytes(out, "data.0.revised_prompt").String(); got != "refined" {
		t.Fatalf("data.0.revised_prompt = %q, want refined", got)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 7 {
		t.Fatalf("usage.total_tokens = %d, want 7", got)
	}
}

func TestCollectImagesFromResponsesStreamMissingCompleted(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp-1"}}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=missing_response_completed")
	requireImagesStreamErrorContains(t, errMsg, "saw_response_completed=false")
	requireImagesStreamErrorContains(t, errMsg, "saw_first_event=true")
	requireImagesStreamErrorContains(t, errMsg, `last_event_type="response.created"`)
	requireImagesStreamErrorContains(t, errMsg, `last_data_type="response.created"`)
	requireImagesStreamErrorContains(t, errMsg, "event_count=1")
	requireImagesStreamErrorContains(t, errMsg, "data_count=1")
	requireImagesStreamErrorContains(t, errMsg, "chunk_count=1")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="data_channel_closed"`)
	requireImagesStreamErrorContains(t, errMsg, "cause=upstream_stream_closed")
}

func TestCollectImagesFromResponsesStreamUpstreamClosedWithoutPayload(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=upstream_stream_closed")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="upstream_stream_closed"`)
	requireImagesStreamErrorContains(t, errMsg, "saw_response_completed=false")
}

func TestCollectImagesFromResponsesStreamPreservesAddonOnWrappedErrors(t *testing.T) {
	tests := []struct {
		name       string
		msg        *interfaces.ErrorMessage
		wantStatus int
	}{
		{
			name: "scanner error",
			msg: &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New("scanner read failed"),
				Addon: http.Header{
					"Retry-After":  {"30"},
					"X-Request-Id": {"req-1", "req-2"},
				},
			},
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "closed without error",
			msg: &interfaces.ErrorMessage{
				StatusCode: http.StatusUnauthorized,
				Addon: http.Header{
					"Retry-After":  {"60"},
					"X-Request-Id": {"req-3"},
				},
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- tt.msg
			close(errs)

			_, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

			if errMsg == nil {
				t.Fatal("errMsg = nil, want wrapped error")
			}
			if errMsg.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", errMsg.StatusCode, tt.wantStatus)
			}
			if got := errMsg.Addon.Get("Retry-After"); got != tt.msg.Addon.Get("Retry-After") {
				t.Fatalf("Retry-After = %q, want %q", got, tt.msg.Addon.Get("Retry-After"))
			}
			if got, want := errMsg.Addon.Values("X-Request-Id"), tt.msg.Addon.Values("X-Request-Id"); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("X-Request-Id = %#v, want %#v", got, want)
			}

			tt.msg.Addon.Set("Retry-After", "mutated")
			if got := errMsg.Addon.Get("Retry-After"); got == "mutated" {
				t.Fatal("wrapped Addon shares source header map")
			}
		})
	}
}

func TestImagesResponsesStreamStatusCode(t *testing.T) {
	tests := []struct {
		name           string
		classification string
		upstreamStatus int
		want           int
	}{
		{
			name:           "preserves upstream client error",
			classification: "scanner_error",
			upstreamStatus: http.StatusTooManyRequests,
			want:           http.StatusTooManyRequests,
		},
		{
			name:           "preserves upstream server error",
			classification: "upstream_stream_closed",
			upstreamStatus: http.StatusInternalServerError,
			want:           http.StatusInternalServerError,
		},
		{
			name:           "scanner fallback without upstream status",
			classification: "scanner_error",
			upstreamStatus: 0,
			want:           http.StatusBadGateway,
		},
		{
			name:           "scanner ignores upstream success status",
			classification: "scanner_error",
			upstreamStatus: http.StatusOK,
			want:           http.StatusBadGateway,
		},
		{
			name:           "timeout fallback without upstream status",
			classification: "context_timeout",
			upstreamStatus: 0,
			want:           http.StatusGatewayTimeout,
		},
		{
			name:           "preserves upstream request timeout",
			classification: "context_timeout",
			upstreamStatus: http.StatusRequestTimeout,
			want:           http.StatusRequestTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imagesResponsesStreamStatusCode(tt.classification, tt.upstreamStatus); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCollectImagesFromResponsesStreamPreservesUpstream408Timeout(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestTimeout,
		Error:      errors.New("upstream request timeout"),
	}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusRequestTimeout, "classification=context_timeout")
	requireImagesStreamErrorContains(t, errMsg, "cause=request_timeout")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="scanner_error"`)
}

func TestCollectImagesFromResponsesStreamScannerError(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("scanner read failed")}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusInternalServerError, "classification=scanner_error")
	requireImagesStreamErrorContains(t, errMsg, "cause=scanner_error")
	requireImagesStreamErrorContains(t, errMsg, `scanner_error_type="scanner_error"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="scanner_error"`)
}

func TestCollectImagesFromResponsesStreamContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)

	out, errMsg := collectImagesFromResponsesStream(ctx, data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusGatewayTimeout, "classification=context_timeout")
	requireImagesStreamErrorContains(t, errMsg, "cause=context_deadline_exceeded")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="context_timeout"`)
}

func TestCollectImagesFromResponsesStreamContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)

	out, errMsg := collectImagesFromResponsesStream(ctx, data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusRequestTimeout, "classification=context_canceled")
	requireImagesStreamErrorContains(t, errMsg, "cause=context_canceled")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="context_canceled"`)
}

func TestCollectImagesFromResponsesStreamHTTP2Reset(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusInternalServerError,
		Error:      errors.New("stream error: stream ID 15; INTERNAL_ERROR; received from peer"),
	}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusInternalServerError, "classification=h2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, "cause=http2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, `scanner_error_type="http2_stream_reset"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="h2_stream_reset"`)
	requireImagesStreamErrorNotContains(t, errMsg, "stream ID 15")
}

func TestCollectImagesFromResponsesStreamHTTP2ResetRSTStream(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("http2: RST_STREAM closed stream"),
	}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=h2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, "cause=http2_stream_reset")
	requireImagesStreamErrorNotContains(t, errMsg, "RST_STREAM")
}

func TestCollectImagesFromResponsesStreamUpstreamErrorEvent(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: error
data: {"type":"error","message":"safe upstream error"}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=upstream_error_event")
	requireImagesStreamErrorContains(t, errMsg, "saw_error_event=true")
	requireImagesStreamErrorContains(t, errMsg, `last_event_type="error"`)
	requireImagesStreamErrorContains(t, errMsg, `last_data_type="error"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="upstream_error_event"`)
	requireImagesStreamErrorNotContains(t, errMsg, "safe upstream error")
}

func TestCollectImagesFromResponsesStreamErrorSummaryDoesNotLeakSensitiveData(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp-1"}}

`)
	close(data)
	close(errs)

	_, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	for _, forbidden := range []string{
		"Authorization",
		"Cookie",
		"API key",
		"api_key",
		"prompt",
		"base64",
		"b64_json",
		`"response":{"id":"resp-1"}`,
		"data:",
		"event: response.created",
	} {
		requireImagesStreamErrorNotContains(t, errMsg, forbidden)
	}
}

func requireImagesStreamError(t *testing.T, errMsg *interfaces.ErrorMessage, status int, contains string) {
	t.Helper()
	if errMsg == nil {
		t.Fatalf("errMsg = nil, want status %d containing %q", status, contains)
	}
	if errMsg.StatusCode != status {
		t.Fatalf("status = %d, want %d: %v", errMsg.StatusCode, status, errMsg.Error)
	}
	requireImagesStreamErrorContains(t, errMsg, contains)
}

func requireImagesStreamErrorContains(t *testing.T, errMsg *interfaces.ErrorMessage, contains string) {
	t.Helper()
	if errMsg == nil || errMsg.Error == nil {
		t.Fatalf("errMsg/error is nil, want containing %q", contains)
	}
	if !strings.Contains(errMsg.Error.Error(), contains) {
		t.Fatalf("error = %q, want containing %q", errMsg.Error.Error(), contains)
	}
}

func requireImagesStreamErrorNotContains(t *testing.T, errMsg *interfaces.ErrorMessage, contains string) {
	t.Helper()
	if errMsg == nil || errMsg.Error == nil {
		return
	}
	if strings.Contains(errMsg.Error.Error(), contains) {
		t.Fatalf("error = %q, want not containing %q", errMsg.Error.Error(), contains)
	}
}
