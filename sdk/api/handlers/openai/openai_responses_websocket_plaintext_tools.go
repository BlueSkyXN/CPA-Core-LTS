package openai

import (
	"strings"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/tidwall/gjson"
)

const responsesWebsocketPlaintextHistoryLimit = 16

type responsesWebsocketPlaintextSnapshot struct {
	authID     string
	responseID string
	proof      multiagentv2.PlaintextToolProvenance
}

// 只保留工具来源，不保存任务正文；容量与 duplex 的响应设置窗口一致。
type responsesWebsocketPlaintextHistory []responsesWebsocketPlaintextSnapshot

func (h responsesWebsocketPlaintextHistory) parent(authID string, payload []byte) multiagentv2.PlaintextToolProvenance {
	authID = strings.TrimSpace(authID)
	parentID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
	if authID == "" || (parentID == "" && gjson.GetBytes(payload, "type").String() != wsRequestTypeAppend) {
		return multiagentv2.PlaintextToolProvenance{}
	}
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].authID == authID && (parentID == "" || h[i].responseID == parentID) {
			return h[i].proof
		}
	}
	return multiagentv2.PlaintextToolProvenance{}
}

func (h *responsesWebsocketPlaintextHistory) remember(authID, responseID string, proof multiagentv2.PlaintextToolProvenance) {
	authID, responseID = strings.TrimSpace(authID), strings.TrimSpace(responseID)
	if authID == "" || responseID == "" {
		return
	}
	for i, entry := range *h {
		if entry.authID == authID && entry.responseID == responseID {
			copy((*h)[i:], (*h)[i+1:])
			*h = (*h)[:len(*h)-1]
			break
		}
	}
	if len(*h) == responsesWebsocketPlaintextHistoryLimit {
		copy(*h, (*h)[1:])
		*h = (*h)[:len(*h)-1]
	}
	*h = append(*h, responsesWebsocketPlaintextSnapshot{authID: authID, responseID: responseID, proof: proof})
}

func (h *responsesWebsocketPlaintextHistory) forgetAuth(authID string) {
	kept := (*h)[:0]
	for _, entry := range *h {
		if entry.authID != authID {
			kept = append(kept, entry)
		}
	}
	clear((*h)[len(kept):])
	*h = kept
}
