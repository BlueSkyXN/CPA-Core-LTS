package common

import (
	"strings"

	"github.com/tidwall/gjson"
)

// UnconvertedAgentMessageError prevents an unsupported delegated task from
// becoming an empty successful turn or an upstream credential failure.
type UnconvertedAgentMessageError struct{}

func (*UnconvertedAgentMessageError) Error() string {
	return "agent_message cannot be sent to this provider: send a standard user message; for Codex, enable client.codex.optimize-multi-agent-v2 and start a new plaintext delegation, or continue encrypted history on its native Codex route"
}

func (*UnconvertedAgentMessageError) StatusCode() int       { return 400 }
func (*UnconvertedAgentMessageError) IsRequestScoped() bool { return true }

// RejectUnconvertedAgentMessages runs after any permitted client adaptation.
// Native Codex routes must not call it: they retain their original history.
func RejectUnconvertedAgentMessages(payload []byte) error {
	for _, item := range gjson.GetBytes(payload, "input").Array() {
		if strings.TrimSpace(item.Get("type").String()) == "agent_message" {
			return &UnconvertedAgentMessageError{}
		}
	}
	return nil
}
