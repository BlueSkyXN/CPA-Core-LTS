package main

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

func codeBuddyModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{{
		ID:                         codeBuddyModel,
		Object:                     "model",
		OwnedBy:                    pluginIdentifier,
		DisplayName:                "CodeBuddy HY3 Preview Agent",
		SupportedGenerationMethods: []string{"chat"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
		UserDefined:                true,
	}}
}

func modelsForAuth(raw []byte) (pluginapi.ModelResponse, error) {
	var req rpcAuthModelRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ModelResponse{}, errDecode
	}
	if _, errAuth := parseStoredAuth(req.StorageJSON); errAuth != nil {
		return pluginapi.ModelResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), 400, false)
	}
	return pluginapi.ModelResponse{Provider: pluginIdentifier, Models: codeBuddyModels()}, nil
}
