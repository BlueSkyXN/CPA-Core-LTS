package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func (h *BaseAPIHandler) resolveAuthorizedAutoModel(ctx context.Context) string {
	models := registry.GetGlobalRegistry()
	available := models.GetAvailableModelInfos()
	ids := make([]string, 0, len(available))
	for _, model := range available {
		ids = append(ids, model.ID)
	}
	visible := h.AuthManager.ResourceModelVisibility(ctx, ids)
	var best *registry.ModelInfo
	for _, model := range available {
		if visible != nil && !visible(model.ID) {
			continue
		}
		if (best == nil || model.Created > best.Created) && models.GetModelCount(model.ID) > 0 {
			best = model
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

// filterResourceModelList runs after response interceptors so renamed and
// injected catalog entries are checked against the caller's registered routes.
func (h *BaseAPIHandler) filterResourceModelList(ctx context.Context, sourceFormat string, body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	lists := make(map[string][]json.RawMessage, 2)
	routingIDs := make(map[string]string)
	var ids []string
	for _, field := range []string{"data", "models"} {
		if raw, exists := envelope[field]; exists {
			var rows []json.RawMessage
			if err := json.Unmarshal(raw, &rows); err != nil {
				return nil, err
			}
			lists[field] = rows
			for _, row := range rows {
				id := resourceCatalogModelID(row)
				routeID := id
				if sourceFormat == "claude" {
					routeID = claudemodels.ResolveClaudeModelIDPrefix(id)
					if routeID != id && h.AuthManager.CheckResourceModelAccess(ctx, id) != nil {
						continue
					}
				}
				routingIDs[id] = routeID
				ids = append(ids, routeID)
			}
		}
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("model catalog has no recognized model list")
	}
	visible := h.AuthManager.ResourceModelVisibility(ctx, ids)
	if visible == nil {
		return body, nil
	}
	for field, rows := range lists {
		filtered := make([]json.RawMessage, 0, len(rows))
		for _, row := range rows {
			if routeID, ok := routingIDs[resourceCatalogModelID(row)]; ok && visible(routeID) {
				filtered = append(filtered, row)
			}
		}
		if sourceFormat == "claude" && field == "data" {
			var firstID, lastID string
			if len(filtered) > 0 {
				firstID = resourceCatalogModelID(filtered[0])
				lastID = resourceCatalogModelID(filtered[len(filtered)-1])
			}
			envelope["first_id"], _ = json.Marshal(firstID)
			envelope["last_id"], _ = json.Marshal(lastID)
		}
		encoded, err := json.Marshal(filtered)
		if err != nil {
			return nil, err
		}
		envelope[field] = encoded
	}
	return json.Marshal(envelope)
}

func resourceCatalogModelID(row json.RawMessage) string {
	var model struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if json.Unmarshal(row, &model) != nil {
		return ""
	}
	if model.ID != "" {
		return model.ID
	}
	if model.Slug != "" {
		return model.Slug
	}
	return strings.TrimPrefix(model.Name, "models/")
}
