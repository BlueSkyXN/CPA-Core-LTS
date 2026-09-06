package management

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

type flowReference struct {
	Ref      string   `json:"ref"`
	Label    string   `json:"label"`
	Provider string   `json:"provider,omitempty"`
	AuthIDs  []string `json:"auth-ids,omitempty"`
	Account  string   `json:"account,omitempty"`
	AuthKind string   `json:"auth-kind,omitempty"`
}

// GetFlowControl is read-only and uses the existing management middleware.
// Key/account references are opaque namespaces, never raw API/OAuth credentials.
// Config writes retain the standard config.yaml path and existing reload handling.
func (h *Handler) GetFlowControl(c *gin.Context) {
	h.mu.Lock()
	cfg := flowcontrol.Config{}
	home := false
	keys := []string(nil)
	if h.cfg != nil {
		cfg = h.cfg.FlowControl.Effective()
		home = h.cfg.Home.Enabled
		keys = append(keys, h.cfg.APIKeys...)
	}
	h.mu.Unlock()
	keyRefs := []flowReference{}
	seenKeys := map[string]bool{}
	for i, key := range keys {
		ref := coresession.CallerScope(key)
		if ref == "" || seenKeys[ref] {
			continue
		}
		seenKeys[ref] = true
		keyRefs = append(keyRefs, flowReference{Ref: ref, Label: fmt.Sprintf("API Key %d", i+1)})
	}
	accountRefs := []flowReference{}
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a == nil {
				continue
			}
			label := a.Label
			if label == "" {
				label = filepath.Base(a.FileName)
			}
			if label == "" || label == "." {
				label = a.ID
			}
			accountRefs = append(accountRefs, flowReference{Ref: coreauth.FlowAccountReference(a), Label: label, Provider: coreauth.FlowAccountProvider(a), AuthIDs: []string{a.ID}, AuthKind: a.AuthKind()})
		}
	}
	sort.Slice(accountRefs, func(i, j int) bool { return accountRefs[i].Ref < accountRefs[j].Ref })
	state := flowcontrol.Summary{}
	applied := cfg
	configError := false
	var updateFailure *coreauth.FlowControlUpdateFailure
	if h.authManager != nil {
		state = h.authManager.FlowControlSummary()
		applied = h.authManager.FlowControlPolicy()
		configError = h.authManager.FlowControlConfigurationError()
		updateFailure = h.authManager.FlowControlLastUpdateFailure()
	}
	models := []string{}
	modelOptions := []coreauth.FlowModelOption{}
	modelOptionsTruncated := false
	if h.authManager != nil {
		modelOptions, modelOptionsTruncated = h.authManager.FlowControlModelOptions()
	}
	for _, row := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
		if id, ok := row["id"].(string); ok {
			models = append(models, id)
		}
	}
	sort.Strings(models)

	c.JSON(http.StatusOK, gin.H{
		"schema-version": 3, "supported": !home && h.authManager != nil,
		"single-process": true, "home-supported": false,
		"stages":              []string{flowcontrol.Request, flowcontrol.Attempt},
		"scopes":              []string{"global", "key", "model", "key-model", "provider", "account", "account-model", "key-account", "key-account-model", "custom"},
		"configuration-error": configError, "configuration-failure": updateFailure, "configured-enabled": cfg.Enabled,
		"configured-policy": cfg,
		"queue":             applied.Queue, "state": state, "keys": keyRefs, "accounts": accountRefs, "policy": applied, "features": []string{"model-sets", "single-account", "joint-first-admission", "shared-summary", "paged-details", "draft-preview", "resolved-model-options", "last-good-policy"}, "models": models, "model-options": modelOptions, "model-options-truncated": modelOptionsTruncated,
		"events-supported": true, "events-enabled": applied.Observation.Realtime, "events-interval-ms": applied.Observation.IntervalMS, "explain-supported": true,
	})
}

// Stream snapshots use the same management authentication as GetFlowControl.
// Query strings never carry the management key.
func (h *Handler) GetFlowControlEvents(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "flow control unavailable"})
		return
	}
	h.authManager.ServeFlowControlEvents(c.Writer, c.Request)
}
func (h *Handler) ExplainFlowControl(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "flow control unavailable"})
		return
	}
	d := flowcontrol.Identity{Stage: c.Query("stage"), Key: c.Query("key"), Model: c.Query("model"), Provider: c.Query("provider"), Account: c.Query("account"), Credential: c.Query("credential"), AuthKind: c.Query("auth-kind")}
	if d.Stage == "" {
		d.Stage = flowcontrol.Attempt
	}
	if d.Stage != flowcontrol.Request && d.Stage != flowcontrol.Attempt {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stage must be request or attempt"})
		return
	}
	for _, v := range []string{d.Key, d.Model, d.Provider, d.Account, d.Credential, d.AuthKind} {
		if len(v) > 256 || strings.ContainsAny(v, "\r\n\x00") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid identity field"})
			return
		}
	}
	c.JSON(http.StatusOK, h.authManager.ExplainFlowControl(d))
}

// Details and preview are manual bounded reads. Polling summaries never calls
// model discovery or constructs per-request explanations.
func (h *Handler) GetFlowControlSummary(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "flow control unavailable"})
		return
	}
	c.JSON(200, gin.H{"schema-version": 3, "state": h.authManager.FlowControlSummary()})
}
func (h *Handler) GetFlowControlDetails(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "flow control unavailable"})
		return
	}
	offset, errOffset := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, errLimit := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if errOffset != nil || errLimit != nil || offset < 0 || offset > 10000 || limit < 1 || limit > 200 {
		c.JSON(400, gin.H{"error": "offset 0..10000; limit 1..200"})
		return
	}
	q := flowcontrol.DetailsOptions{Offset: offset, Limit: limit, Stage: c.Query("stage"), State: c.Query("state"), Key: c.Query("key"), Account: c.Query("account"), Model: c.Query("model")}
	if (q.Stage != "" && q.Stage != flowcontrol.Request && q.Stage != flowcontrol.Attempt) || (q.State != "" && q.State != "waiting" && q.State != "running" && q.State != "draining") {
		c.JSON(400, gin.H{"error": "invalid details stage/state"})
		return
	}
	for _, value := range []string{q.Key, q.Account, q.Model} {
		if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			c.JSON(400, gin.H{"error": "invalid details filter"})
			return
		}
	}
	c.JSON(200, h.authManager.FlowControlDetails(q))
}
func (h *Handler) PreviewFlowControl(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "flow control unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var body struct {
		Config  *flowcontrol.Config    `json:"config"`
		Targets []flowcontrol.Identity `json:"targets"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Targets) == 0 || len(body.Targets) > 24 {
		c.JSON(400, gin.H{"error": "provide 1..24 targets and an optional draft config"})
		return
	}
	rows, err := h.authManager.PreviewFlowControl(body.Config, body.Targets)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"schema-version": 3, "results": rows})
}
func (h *Handler) PreviewFlowControlMigration(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "flow control unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var cfg flowcontrol.Config
	if c.ShouldBindJSON(&cfg) != nil {
		c.JSON(400, gin.H{"error": "invalid flow-control config"})
		return
	}
	c.JSON(200, h.authManager.PreviewFlowMigration(cfg))
}
