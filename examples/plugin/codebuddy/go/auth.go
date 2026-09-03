package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type codeBuddyAuth struct {
	Type     string `json:"type"`
	AuthMode string `json:"auth_mode"`
	PAT      string `json:"pat,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Label    string `json:"label,omitempty"`
}

func parseStoredAuth(raw []byte) (codeBuddyAuth, error) {
	var auth codeBuddyAuth
	if errDecode := json.Unmarshal(raw, &auth); errDecode != nil {
		return codeBuddyAuth{}, fmt.Errorf("decode CodeBuddy auth: invalid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Type), pluginIdentifier) {
		return codeBuddyAuth{}, fmt.Errorf("auth type is not CodeBuddy")
	}
	auth.AuthMode = strings.ToLower(strings.TrimSpace(auth.AuthMode))
	if auth.AuthMode != "pat" && auth.AuthMode != "api_key" {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy auth_mode must be pat or api_key")
	}
	auth.PAT = strings.TrimSpace(auth.PAT)
	auth.APIKey = strings.TrimSpace(auth.APIKey)
	auth.Label = strings.TrimSpace(auth.Label)
	if strings.ContainsAny(auth.PAT, "\r\n\x00") || strings.ContainsAny(auth.APIKey, "\r\n\x00") || strings.ContainsAny(auth.Label, "\r\n\x00") {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy auth contains invalid characters")
	}
	if auth.PAT != "" && auth.APIKey != "" && auth.PAT != auth.APIKey {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy pat and api_key must match when both are present")
	}
	if auth.AuthMode == "pat" && auth.PAT == "" {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy pat is required")
	}
	if auth.AuthMode == "api_key" && auth.APIKey == "" {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy api_key is required")
	}
	auth.Type = pluginIdentifier
	if auth.PAT == "" {
		auth.PAT = auth.APIKey
	}
	if auth.APIKey == "" {
		auth.APIKey = auth.PAT
	}
	return auth, nil
}

func parseAuthRequest(raw []byte) (pluginapi.AuthParseResponse, error) {
	var req pluginapi.AuthParseRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthParseResponse{}, errDecode
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if errDecode := json.Unmarshal(req.RawJSON, &discriminator); errDecode != nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(discriminator.Type), pluginIdentifier) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	auth, errAuth := parseStoredAuth(req.RawJSON)
	if errAuth != nil {
		return pluginapi.AuthParseResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), 400, false)
	}
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider:    pluginIdentifier,
			FileName:    strings.TrimSpace(req.FileName),
			Label:       authLabelForDisplay(req.RawJSON),
			StorageJSON: bytes.Clone(req.RawJSON),
			Metadata: map[string]any{
				"type":      pluginIdentifier,
				"auth_mode": auth.AuthMode,
			},
		},
	}, nil
}

func authLabelForDisplay(raw []byte) string {
	auth, errAuth := parseStoredAuth(raw)
	if errAuth == nil && auth.Label != "" {
		return auth.Label
	}
	if errAuth == nil && auth.AuthMode == "pat" {
		return "CodeBuddy PAT"
	}
	return "CodeBuddy API Key"
}

func refreshAuthRequest(raw []byte) (pluginapi.AuthRefreshResponse, error) {
	var req pluginapi.AuthRefreshRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthRefreshResponse{}, errDecode
	}
	if _, errAuth := parseStoredAuth(req.StorageJSON); errAuth != nil {
		return pluginapi.AuthRefreshResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), 400, false)
	}
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		Provider:    pluginIdentifier,
		ID:          strings.TrimSpace(req.AuthID),
		StorageJSON: bytes.Clone(req.StorageJSON),
		Metadata:    cloneAnyMap(req.Metadata),
		Attributes:  cloneStringMap(req.Attributes),
	}}, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
