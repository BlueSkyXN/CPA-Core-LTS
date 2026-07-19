package gemini

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

type failingRequestBody struct{}

func (failingRequestBody) Read([]byte) (int, error) {
	return 0, errors.New("request body must not be read")
}

func TestGeminiHandlerUnknownMethodReturnsNotFound(t *testing.T) {
	tests := []struct {
		name string
		body io.Reader
	}{
		{name: "valid body", body: strings.NewReader(`{}`)},
		{name: "unreadable body", body: failingRequestBody{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "action", Value: "gemini-2.5-pro:unknownMethod"}}
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:unknownMethod", tt.body)

			NewGeminiAPIHandler(&handlers.BaseAPIHandler{}).GeminiHandler(ctx)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
			}
			if body := recorder.Body.String(); !strings.Contains(body, "invalid_request_error") || !strings.Contains(body, "not found") {
				t.Fatalf("body = %s, want stable not-found error", body)
			}
		})
	}
}
