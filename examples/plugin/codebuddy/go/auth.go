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
	APIKey   string `json:"api_key"`
}

func parseStoredAuth(raw []byte) (codeBuddyAuth, error) {
	var auth codeBuddyAuth
	if errDecode := json.Unmarshal(raw, &auth); errDecode != nil {
		return codeBuddyAuth{}, fmt.Errorf("decode CodeBuddy auth: invalid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Type), pluginIdentifier) {
		return codeBuddyAuth{}, fmt.Errorf("auth type is not CodeBuddy")
	}
	if strings.TrimSpace(auth.AuthMode) != "api_key" {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy auth_mode must be api_key")
	}
	auth.APIKey = strings.TrimSpace(auth.APIKey)
	if auth.APIKey == "" {
		return codeBuddyAuth{}, fmt.Errorf("CodeBuddy api_key is required")
	}
	auth.Type = pluginIdentifier
	auth.AuthMode = "api_key"
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
	if _, errAuth := parseStoredAuth(req.RawJSON); errAuth != nil {
		return pluginapi.AuthParseResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), 400, false)
	}
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider:    pluginIdentifier,
			FileName:    strings.TrimSpace(req.FileName),
			Label:       "CodeBuddy API Key",
			StorageJSON: bytes.Clone(req.RawJSON),
			Metadata: map[string]any{
				"type":      pluginIdentifier,
				"auth_mode": "api_key",
			},
		},
	}, nil
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
