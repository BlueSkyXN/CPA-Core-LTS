package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestResponsesControlErrorsAreReturnedBeforeExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/responses/compact", h.Compact)
	for _, tt := range []struct{ path, body, message string }{
		{"/v1/responses", `{"model":"gpt-6-astra","truncation":"auto","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "automatic truncation"},
		{"/v1/responses/compact", `{"model":"gpt-6-astra","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "standalone"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), tt.message) {
			t.Fatalf("%s: status %d body %s", tt.path, recorder.Code, recorder.Body.String())
		}
	}
}
