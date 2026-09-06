package management

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

func TestFlowControlStatusReferencesDoNotExposeTokens(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"private-caller-token"}
	manager := coreauth.NewManager(nil, nil, nil)
	defer manager.CloseFlowControl()
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "test-file", Label: "fixture", Provider: "codex", Metadata: map[string]any{"account_id": "private-account-id", "access_token": "private-oauth-token"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, "", manager)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	h.GetFlowControl(c)
	if recorder.Code != 200 {
		t.Fatal(recorder.Code)
	}
	body := recorder.Body.String()
	for _, secret := range []string{"private-caller-token", "private-account-id", "private-oauth-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("raw identity in status: %s", secret)
		}
	}
	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["schema-version"] != float64(3) || result["supported"] != true {
		t.Fatal(result)
	}
	keys := result["keys"].([]any)
	if keys[0].(map[string]any)["ref"] != coresession.CallerScope(cfg.APIKeys[0]) {
		t.Fatal("reference differs from runtime caller scope")
	}
}

func TestFlowV3OneAuthRecordAndReadOnlyExplain(t *testing.T) {
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	defer manager.CloseFlowControl()
	for _, id := range []string{"file-a", "file-b"} {
		_, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "secret"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(cfg, "", manager)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetFlowControl(c)
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data["accounts"].([]any)) != 2 || data["credentials"] != nil {
		t.Fatal("V3 must expose one account per Auth record, not an inferred group")
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/flow-control/explain?stage=attempt&key=anonymous&model=fixture", nil)
	h.ExplainFlowControl(c)
	if w.Code != 200 {
		t.Fatalf("explain: %d", w.Code)
	}
	if manager.FlowControlSnapshot().Admitted != 0 {
		t.Fatal("preview reserved a slot")
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/flow-control/explain?stage=wrong", nil)
	h.ExplainFlowControl(c)
	if w.Code != 400 {
		t.Fatal("invalid stage accepted")
	}
}

func TestFlowV3ManagementReadOnlyPreviewAndLimits(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	defer manager.CloseFlowControl()
	h := NewHandler(&config.Config{}, "", manager)
	router := gin.New()
	router.GET("/events", h.GetFlowControlEvents)
	router.GET("/details", h.GetFlowControlDetails)
	router.POST("/preview", h.PreviewFlowControl)
	router.POST("/migration", h.PreviewFlowControlMigration)
	cases := []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/events", "", 409},
		{"GET", "/details?limit=201", "", 400}, {"GET", "/details?offset=broken", "", 400},
		{"POST", "/preview", `{"targets":[{"stage":"attempt","model":"m1"}],"config":{"version":3,"enabled":true,"rules":[{"id":"s","stage":"attempt","scope":"custom","models":["m1","m2"],"max-concurrent":3}]}}`, 200},
		{"POST", "/preview", `{"targets":[{"stage":"attempt","model":"m*"}]}`, 400},
		{"POST", "/migration", `{"enabled":false,"rules":[]}`, 200},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if manager.FlowControlSummary().Admitted != 0 {
		t.Fatal("management query acquired slot")
	}
}
