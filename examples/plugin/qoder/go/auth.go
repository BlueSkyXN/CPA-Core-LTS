package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type qoderAuth struct {
	Type        string `json:"type"`
	AuthMode    string `json:"auth_mode"`
	AccountID   string `json:"account_id,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	ConfigDir   string `json:"config_dir,omitempty"`
}

func parseStoredAuth(raw []byte) (qoderAuth, error) {
	var auth qoderAuth
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if errDecode := decoder.Decode(&auth); errDecode != nil {
		return qoderAuth{}, fmt.Errorf("decode Qoder auth: invalid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Type), pluginIdentifier) {
		return qoderAuth{}, fmt.Errorf("auth type is not Qoder")
	}
	auth.Type = pluginIdentifier
	auth.AuthMode = strings.TrimSpace(auth.AuthMode)
	auth.AccountID = strings.TrimSpace(auth.AccountID)
	auth.ProfileID = strings.TrimSpace(auth.ProfileID)
	auth.ConfigDir = strings.TrimSpace(auth.ConfigDir)
	switch auth.AuthMode {
	case "pat":
		if auth.AccessToken == "" || strings.TrimSpace(auth.AccessToken) != auth.AccessToken {
			return qoderAuth{}, fmt.Errorf("Qoder access_token is required and must not contain surrounding whitespace")
		}
		if strings.ContainsAny(auth.AccessToken, "\r\n\x00") {
			return qoderAuth{}, fmt.Errorf("Qoder access_token contains invalid characters")
		}
		auth.ProfileID = ""
		auth.ConfigDir = ""
	case "local_cli":
		if auth.ProfileID == "" {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli profile_id is required")
		}
		if auth.ConfigDir == "" || !filepath.IsAbs(auth.ConfigDir) || strings.ContainsRune(auth.ConfigDir, '\x00') {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli config_dir must be an absolute path so profiles remain isolated")
		}
		auth.AccessToken = ""
		auth.AccountID = ""
	default:
		return qoderAuth{}, fmt.Errorf("Qoder auth_mode must be pat or local_cli")
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
	if errDecode := json.Unmarshal(req.RawJSON, &discriminator); errDecode != nil || !strings.EqualFold(strings.TrimSpace(discriminator.Type), pluginIdentifier) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	auth, errAuth := parseStoredAuth(req.RawJSON)
	if errAuth != nil {
		return pluginapi.AuthParseResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), 400, false)
	}
	label := "Qoder PAT"
	attributes := map[string]string{"auth_mode": auth.AuthMode, "multi_account": "supported"}
	if auth.AuthMode == "local_cli" {
		label = "Qoder Local CLI " + auth.ProfileID
		attributes["profile_id"] = auth.ProfileID
		attributes["multi_account"] = "profile_isolation_required"
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
		Provider:    pluginIdentifier,
		FileName:    strings.TrimSpace(req.FileName),
		Label:       label,
		StorageJSON: bytes.Clone(req.RawJSON),
		Metadata:    map[string]any{"type": pluginIdentifier, "auth_mode": auth.AuthMode},
		Attributes:  attributes,
	}}, nil
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
		Provider: pluginIdentifier, ID: strings.TrimSpace(req.AuthID), StorageJSON: bytes.Clone(req.StorageJSON),
		Metadata: cloneAnyMap(req.Metadata), Attributes: cloneStringMap(req.Attributes),
	}}, nil
}
