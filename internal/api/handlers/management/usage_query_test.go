package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func TestUsageQueryHTTPContract(t *testing.T) {
	h := &Handler{usageStats: usage.NewRequestStatistics()}
	for _, test := range []struct {
		body   string
		status int
	}{{`{}`, 200}, {`{"limit":201}`, 400}, {`{"modules":["invalid"]}`, 400}, {`{"unknown":true}`, 400}, {`{} {}`, 400}, {`{"bound":"invalid"}`, 400}, {`{"from_ms":2,"to_ms":1}`, 400}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/usage/query/details", strings.NewReader(test.body))
		h.QueryUsageDetails(c)
		if w.Code != test.status {
			t.Fatalf("status = %d, expected %d", w.Code, test.status)
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/usage/query/capabilities", nil)
	h.GetUsageQueryCapabilities(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("capability contract")
	}
}
