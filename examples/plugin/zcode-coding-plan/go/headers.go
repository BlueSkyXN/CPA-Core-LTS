package main

import (
	"net/http"
	"strings"
)

func modelHeaders(c *config, incoming http.Header) http.Header {
	headers := identityHeaders(c)
	betas := []string{headers.Get("Anthropic-Beta")}
	seen := map[string]bool{betas[0]: true}
	for _, value := range incoming.Values("Anthropic-Beta") {
		for _, beta := range strings.Split(value, ",") {
			beta = strings.TrimSpace(beta)
			switch beta {
			case "mid-conversation-system-2026-04-07", "mid-conversation-system-clear-at-2026-08-21",
				"mid-conversation-output-config-2026-07-01", "per-turn-control-2026-07-01",
				"mid-conversation-tool-changes-2026-07-01", "inline-tools-2026-09-15":
				if !seen[beta] {
					betas = append(betas, beta)
					seen[beta] = true
				}
			}
		}
	}
	headers.Set("Anthropic-Beta", strings.Join(betas, ","))
	return headers
}
