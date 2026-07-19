package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexmetadata"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func prepareCodexOutboundMetadata(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, userPayload, rawJSON []byte, incomingHeaders http.Header) ([]byte, codexIdentityConfuseState, error) {
	effective := (config.CodexClientMetadataConfig{}).Effective()
	if cfg != nil {
		effective = cfg.Codex.ClientMetadata.Effective()
	}
	normalizedBody, metadataState, err := codexmetadata.NormalizeRequest(rawJSON, codexIncomingTurnMetadata(ctx, incomingHeaders), codexmetadata.Policy{
		Mode:            effective.Mode,
		WorkspacePolicy: effective.WorkspacePolicy,
		Scope:           codexClientMetadataCredentialScope(auth),
	})
	if err != nil {
		return nil, codexIdentityConfuseState{}, invalidCodexClientMetadataError()
	}
	state := codexIdentityConfuseState{clientMetadata: metadataState}
	if metadataState.CanonicalPresent {
		return normalizedBody, state, nil
	}

	legacyBody, legacyState := applyCodexIdentityConfuseBody(cfg, auth, userPayload, normalizedBody)
	legacyState.clientMetadata = metadataState
	return legacyBody, legacyState, nil
}

func codexIncomingTurnMetadata(ctx context.Context, provided http.Header) string {
	if value := strings.TrimSpace(headerValueCaseInsensitive(provided, "X-Codex-Turn-Metadata")); value != "" {
		return value
	}
	if ctx == nil {
		return ""
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return strings.TrimSpace(headerValueCaseInsensitive(ginCtx.Request.Header, "X-Codex-Turn-Metadata"))
	}
	return ""
}

func applyCodexOutboundMetadataHeaders(headers http.Header, state *codexIdentityConfuseState) {
	if state != nil && state.clientMetadata.CanonicalPresent {
		state.clientMetadata.ApplyHeaders(headers)
		return
	}
	applyCodexIdentityConfuseHeaders(headers, state)
}

func codexClientMetadataCredentialScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "codex:anonymous"
	}
	if authID := strings.TrimSpace(auth.ID); authID != "" {
		return "codex:auth:" + authID
	}
	if auth.Metadata != nil {
		if accountID, ok := auth.Metadata["account_id"].(string); ok {
			if accountID = strings.TrimSpace(accountID); accountID != "" {
				return "codex:account:" + accountID
			}
		}
	}
	if auth.Attributes != nil {
		if accountID := strings.TrimSpace(auth.Attributes["account_id"]); accountID != "" {
			return "codex:account:" + accountID
		}
	}
	return "codex:anonymous"
}

func invalidCodexClientMetadataError() statusErr {
	return statusErr{
		code: http.StatusBadRequest,
		msg:  `{"error":{"message":"invalid Codex client_metadata","type":"invalid_request_error","code":"invalid_client_metadata"}}`,
	}
}
