package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 长期层凭据：github_token 是 GitHub OAuth/PAT 长期令牌；短时 Copilot token 只存内存。
type copilotAuth struct {
	Type        string `json:"type"`
	AuthMode    string `json:"auth_mode"`
	GitHubToken string `json:"github_token,omitempty"`
	Label       string `json:"label,omitempty"`
}

func parseStoredAuth(raw []byte) (copilotAuth, error) {
	var auth copilotAuth
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if errDecode := decoder.Decode(&auth); errDecode != nil {
		return copilotAuth{}, fmt.Errorf("decode Copilot auth: invalid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Type), pluginIdentifier) {
		return copilotAuth{}, fmt.Errorf("auth type is not Copilot")
	}
	auth.Type = pluginIdentifier
	auth.AuthMode = strings.ToLower(strings.TrimSpace(auth.AuthMode))
	auth.Label = strings.TrimSpace(auth.Label)
	switch auth.AuthMode {
	case "oauth", "github_token":
	default:
		return copilotAuth{}, fmt.Errorf("Copilot auth_mode must be oauth or github_token")
	}
	if strings.TrimSpace(auth.GitHubToken) == "" {
		return copilotAuth{}, fmt.Errorf("Copilot github_token is required")
	}
	if auth.GitHubToken != strings.TrimSpace(auth.GitHubToken) {
		return copilotAuth{}, fmt.Errorf("Copilot github_token must not contain surrounding whitespace")
	}
	auth.GitHubToken = strings.TrimSpace(auth.GitHubToken)
	if strings.ContainsAny(auth.GitHubToken, "\r\n\x00") || strings.ContainsAny(auth.Label, "\r\n\x00") {
		return copilotAuth{}, fmt.Errorf("Copilot auth contains invalid characters")
	}
	return auth, nil
}

func (auth copilotAuth) tokenSource() string {
	return auth.GitHubToken
}

func parseAuthRequest(raw []byte) (pluginapi.AuthParseResponse, error) {
	var req pluginapi.AuthParseRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthParseResponse{}, errDecode
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if errDecode := json.Unmarshal(req.RawJSON, &discriminator); errDecode != nil || !strings.EqualFold(strings.TrimSpace(discriminator.Type), pluginIdentifier) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	auth, errAuth := parseStoredAuth(req.RawJSON)
	if errAuth != nil {
		return pluginapi.AuthParseResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusBadRequest, false)
	}
	label := auth.Label
	if label == "" {
		label = "GitHub Copilot"
	}
	attributes := map[string]string{"auth_mode": auth.AuthMode, "multi_account": "supported"}
	metadata := map[string]any{"type": pluginIdentifier, "auth_mode": auth.AuthMode}
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
		Provider:    pluginIdentifier,
		FileName:    strings.TrimSpace(req.FileName),
		Label:       label,
		StorageJSON: bytes.Clone(req.RawJSON),
		Metadata:    metadata,
		Attributes:  attributes,
	}}, nil
}

// RefreshAuth 不改动长期凭据：GitHub token 的有效性由短时换票失败时再判定。
func refreshAuthRequest(raw []byte) (pluginapi.AuthRefreshResponse, error) {
	var req pluginapi.AuthRefreshRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthRefreshResponse{}, errDecode
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return pluginapi.AuthRefreshResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusBadRequest, false)
	}
	metadata := cloneAnyMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{"type": pluginIdentifier, "auth_mode": auth.AuthMode}
	}
	attributes := cloneStringMap(req.Attributes)
	if attributes == nil {
		attributes = map[string]string{"auth_mode": auth.AuthMode, "multi_account": "supported"}
	}
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		Provider: pluginIdentifier, ID: strings.TrimSpace(req.AuthID),
		StorageJSON: bytes.Clone(req.StorageJSON), Metadata: metadata, Attributes: attributes,
	}}, nil
}
