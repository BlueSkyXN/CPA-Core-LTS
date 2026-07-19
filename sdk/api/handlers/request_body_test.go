package handlers

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
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
