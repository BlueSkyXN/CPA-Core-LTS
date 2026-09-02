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
	Type      string `json:"type"`
	AuthMode  string `json:"auth_mode"`
	Transport string `json:"transport,omitempty"`
	PAT       string `json:"pat,omitempty"`
	Label     string `json:"label,omitempty"`
	// AccessToken is the legacy source used by published Qoder auth files.
	// New files should use PAT, but the provider must keep reading this field.
	AccessToken string `json:"access_token,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
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
	auth.AuthMode = strings.ToLower(strings.TrimSpace(auth.AuthMode))
	auth.Transport = strings.ToLower(strings.TrimSpace(auth.Transport))
	auth.Label = strings.TrimSpace(auth.Label)
	auth.AccountID = strings.TrimSpace(auth.AccountID)
	auth.ProfileID = strings.TrimSpace(auth.ProfileID)
	auth.ConfigDir = strings.TrimSpace(auth.ConfigDir)
	if auth.Transport != "" && auth.Transport != "sdk_cli" && auth.Transport != "direct_openai" {
		return qoderAuth{}, fmt.Errorf("Qoder transport must be sdk_cli or direct_openai")
	}
	if strings.ContainsAny(auth.PAT, "\r\n\x00") || strings.ContainsAny(auth.AccessToken, "\r\n\x00") || strings.ContainsAny(auth.Label, "\r\n\x00") || strings.ContainsAny(auth.AccountID, "\r\n\x00") || strings.ContainsAny(auth.ProfileID, "\r\n\x00") || strings.ContainsAny(auth.ConfigDir, "\r\n\x00") {
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
	case "local_cli":
		if auth.Transport == "direct_openai" {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli auth cannot use direct_openai transport")
		}
		if auth.PAT != "" {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli cannot include the new pat field")
		}
		if auth.ProfileID == "" {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli profile_id is required")
		}
		if auth.ConfigDir == "" || !filepath.IsAbs(auth.ConfigDir) || strings.ContainsRune(auth.ConfigDir, '\x00') {
			return qoderAuth{}, fmt.Errorf("Qoder local_cli config_dir must be an absolute path so profiles remain isolated")
		}
		// The released parser ignored legacy token/account fields on local_cli
		// profiles. Keep accepting that stored shape, but never forward them.
		auth.AccessToken = ""
		auth.AccountID = ""
	default:
		return qoderAuth{}, fmt.Errorf("Qoder auth_mode must be pat or local_cli")
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
	if auth.Transport != "" {
		attributes["transport"] = auth.Transport
		metadata["transport"] = auth.Transport
	}
	if auth.AuthMode == "local_cli" {
		label = "Qoder Local CLI " + auth.ProfileID
		attributes["profile_id"] = auth.ProfileID
		attributes["multi_account"] = "profile_isolation_required"
		metadata["profile_id"] = auth.ProfileID
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
