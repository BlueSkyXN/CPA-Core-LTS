package redisqueue

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueuePluginOutcomeComesFromRecordNotFinalStatus(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithResponseStatusHolder(context.Background())
		internallogging.SetResponseStatus(ctx, http.StatusBadGateway)
		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(ctx, coreusage.Record{
			Provider:    "codex",
			Model:       "gpt-image-2",
			APIKey:      "test-key",
			RequestedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		})
		payload := waitForSinglePayload(t, 2*time.Second)
		requireBoolField(t, payload, "failed", false)
		requireFailField(t, payload, http.StatusOK, "")
	})
}
