package main

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

var codeBuddyModelDefinitions = []struct {
	ID          string
	DisplayName string
}{
	{ID: codeBuddyModel, DisplayName: "CodeBuddy HY3"},
	{ID: codeBuddyPreviewModel, DisplayName: "CodeBuddy HY3 Preview Agent"},
}

func codeBuddyModels() []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(codeBuddyModelDefinitions))
	for _, definition := range codeBuddyModelDefinitions {
		models = append(models, pluginapi.ModelInfo{
			ID:                         definition.ID,
			Object:                     "model",
			OwnedBy:                    pluginIdentifier,
			DisplayName:                definition.DisplayName,
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text"},
			UserDefined:                true,
		})
	}
	return models
}

func isCodeBuddyModel(model string) bool {
	for _, definition := range codeBuddyModelDefinitions {
		if model == definition.ID {
			return true
		}
	}
	return false
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
