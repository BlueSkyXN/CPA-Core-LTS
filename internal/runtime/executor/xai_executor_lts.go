package executor

import "github.com/MoeForge/moego/ojson"

func failClosedXAIToolChoice(body []byte) []byte {
	body, _ = ojson.SetBytes(body, "tool_choice", "none")
	return body
}
