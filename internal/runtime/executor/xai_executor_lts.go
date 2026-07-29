package executor

import "github.com/tidwall/sjson"

func failClosedXAIToolChoice(body []byte) []byte {
	body, _ = sjson.SetBytes(body, "tool_choice", "none")
	return body
}
