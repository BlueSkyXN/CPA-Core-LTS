package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type qoderAuth struct {
	Type     string `json:"type"`
	AuthMode string `json:"auth_mode"`
	PAT      string `json:"pat,omitempty"`
	Label    string `json:"label,omitempty"`
	// AccessToken is the legacy source used by published Qoder auth files.
	// New files should use PAT, but the provider must keep reading this field.
	AccessToken string `json:"access_token,omitempty"`
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
	auth.Label = strings.TrimSpace(auth.Label)
	var compat struct {
		Transport string `json:"transport"`
	}
	if errCompat := json.Unmarshal(raw, &compat); errCompat != nil {
		return qoderAuth{}, fmt.Errorf("decode Qoder auth transport: transport must be a string when present")
	}
	transport := strings.ToLower(strings.TrimSpace(compat.Transport))
	if transport == "sdk_cli" {
		return qoderAuth{}, fmt.Errorf("Qoder no longer supports the sdk_cli transport; use a PAT with the native direct transport")
	}
	if transport != "" && transport != "direct_openai" {
		return qoderAuth{}, fmt.Errorf("Qoder transport is not supported: %s", transport)
	}
	// transport is accepted as a no-op compatibility field; execution is always native direct.
	if strings.ContainsAny(auth.PAT, "\r\n\x00") || strings.ContainsAny(auth.AccessToken, "\r\n\x00") || strings.ContainsAny(auth.Label, "\r\n\x00") {
		return qoderAuth{}, fmt.Errorf("Qoder auth contains invalid characters")
	}
	if strings.TrimSpace(auth.PAT) != auth.PAT || strings.TrimSpace(auth.AccessToken) != auth.AccessToken {
		return qoderAuth{}, fmt.Errorf("Qoder pat or access_token must not contain surrounding whitespace")
	}
	if auth.PAT != "" && auth.AccessToken != "" && auth.PAT != auth.AccessToken {
		return qoderAuth{}, fmt.Errorf("Qoder pat and access_token must match when both are present")
	}
	switch auth.AuthMode {
	case "pat":
		if auth.PAT != "" && !strings.HasPrefix(auth.PAT, "pt-") {
			return qoderAuth{}, fmt.Errorf("Qoder pat must use the pt- prefix")
		}
		if auth.PAT == "" {
			auth.PAT = auth.AccessToken
		}
		if auth.PAT == "" || strings.TrimSpace(auth.PAT) != auth.PAT {
			return qoderAuth{}, fmt.Errorf("Qoder pat or access_token is required and must not contain surrounding whitespace")
		}
		// New `pat` files are validated as PATs. Legacy access_token files are
		// intentionally accepted as opaque token sources for compatibility.
		auth.AccessToken = auth.PAT
	default:
		return qoderAuth{}, fmt.Errorf("Qoder only supports PAT authentication")
	}
	return auth, nil
}

func (auth qoderAuth) tokenSource() string {
	if auth.PAT != "" {
		return auth.PAT
	}
	return auth.AccessToken
}

func (auth qoderAuth) isPAT() bool {
	return auth.AuthMode == "pat" && strings.HasPrefix(auth.tokenSource(), "pt-")
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
