package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type qoderAuth struct {
	Type      string `json:"type"`
	AuthMode  string `json:"auth_mode"`
	Transport string `json:"transport,omitempty"`
	PAT       string `json:"pat,omitempty"`
	Label     string `json:"label,omitempty"`
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
	auth.AuthMode = strings.ToLower(strings.TrimSpace(auth.AuthMode))
	auth.Transport = strings.ToLower(strings.TrimSpace(auth.Transport))
	auth.PAT = strings.TrimSpace(auth.PAT)
	auth.Label = strings.TrimSpace(auth.Label)
	if auth.Transport != "" && auth.Transport != "sdk_cli" && auth.Transport != "direct_openai" {
		return qoderAuth{}, fmt.Errorf("Qoder transport must be sdk_cli or direct_openai")
	}
	if auth.AuthMode != "pat" {
		return qoderAuth{}, fmt.Errorf("Qoder auth_mode must be pat")
	}
	if auth.PAT == "" || strings.TrimSpace(auth.PAT) != auth.PAT {
		return qoderAuth{}, fmt.Errorf("Qoder pat is required and must not contain surrounding whitespace")
	}
	if !strings.HasPrefix(auth.PAT, "pt-") {
		return qoderAuth{}, fmt.Errorf("Qoder pat must use the pt- prefix")
	}
	if strings.ContainsAny(auth.PAT, "\r\n\x00") || strings.ContainsAny(auth.Label, "\r\n\x00") {
		return qoderAuth{}, fmt.Errorf("Qoder auth contains invalid characters")
	}
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &fields); errDecode == nil {
		for _, legacy := range []string{"access_token", "account_id", "profile_id", "config_dir"} {
			if _, exists := fields[legacy]; exists {
				return qoderAuth{}, fmt.Errorf("Qoder %s is no longer supported; use pat", legacy)
			}
		}
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
	label := auth.Label
	if label == "" {
		label = "Qoder PAT"
	}
	attributes := map[string]string{"auth_mode": auth.AuthMode, "multi_account": "supported"}
	metadata := map[string]any{"type": pluginIdentifier, "auth_mode": auth.AuthMode}
	if auth.Transport != "" {
		attributes["transport"] = auth.Transport
		metadata["transport"] = auth.Transport
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
		Provider:    pluginIdentifier,
		FileName:    strings.TrimSpace(req.FileName),
		Label:       label,
		StorageJSON: bytes.Clone(req.RawJSON),
		Metadata:    metadata,
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
