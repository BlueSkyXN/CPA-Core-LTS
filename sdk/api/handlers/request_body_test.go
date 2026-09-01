package handlers

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestReadRequestBodyRejectsIdentityBodyOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(bytes.Repeat([]byte("x"), int(defaultMaxRequestBodyBytes)+1)))

	_, err := ReadRequestBody(c)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBody error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestDecodeZstdRequestBodyRejectsDecodedBodyOverLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 65)
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err = encoder.Write(payload); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}

	_, err = decodeZstdRequestBodyWithLimit(compressed.Bytes(), 64)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("decodeZstdRequestBodyWithLimit error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestBaseAPIHandlerReadRequestBodyAllowsConfiguredLargerBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(defaultMaxRequestBodyBytes)+1)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: int64(len(body))}, nil)
	c := newRequestBodyTestContext(body, "")

	got, err := handler.ReadRequestBody(c)
	if err != nil {
		t.Fatalf("ReadRequestBody error = %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("ReadRequestBody length = %d, want %d", len(got), len(body))
	}
}

func TestBaseAPIHandlerReadRequestBodyUsesHotReloadedLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 65)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 64}, nil)

	_, err := handler.ReadRequestBody(newRequestBodyTestContext(body, ""))
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBody error = %v, want ErrRequestBodyTooLarge", err)
	}

	handler.UpdateClients(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 128})
	got, err := handler.ReadRequestBody(newRequestBodyTestContext(body, ""))
	if err != nil {
		t.Fatalf("ReadRequestBody after UpdateClients error = %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("ReadRequestBody length = %d, want %d", len(got), len(body))
	}
}

func TestBaseAPIHandlerReadRequestBodyAppliesConfiguredLimitAfterZstdDecode(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 65)
	compressed := compressRequestBodyForTest(t, payload)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 64}, nil)

	_, err := handler.ReadRequestBody(newRequestBodyTestContext(compressed, "zstd"))
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBody error = %v, want ErrRequestBodyTooLarge", err)
	}

	handler.UpdateClients(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 128})
	got, err := handler.ReadRequestBody(newRequestBodyTestContext(compressed, "zstd"))
	if err != nil {
		t.Fatalf("ReadRequestBody after UpdateClients error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("ReadRequestBody = %q, want %q", got, payload)
	}
}

func TestReadRequestBodyKeepsValidJSONFallbackForBadEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Encoding", "zstd")

	got, err := ReadRequestBody(c)
	if err != nil {
		t.Fatalf("ReadRequestBody error = %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("ReadRequestBody = %q, want raw JSON fallback %q", got, body)
	}
}

func newRequestBodyTestContext(body []byte, encoding string) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	if encoding != "" {
		c.Request.Header.Set("Content-Encoding", encoding)
	}
	return c
}

func compressRequestBodyForTest(t *testing.T, payload []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err = encoder.Write(payload); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return compressed.Bytes()
}
