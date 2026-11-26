package responses

import (
	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// responsesToolMediaTracker isolates LTS per-group tool call/output pairing,
// message deferral, and per-output tool media relay ordering so the main
// Responses -> Chat Completions request switch stays concise at upstream seams.
type responsesToolMediaTracker struct {
	callCounts            map[string]int
	explicitOutputCounts  map[string]int
	duplicateOutputIDs    map[string]struct{}
	pendingToolCalls      []interface{}
	pendingToolCallIDs    []string
	awaitingToolOutputs   map[string]bool
	matchedOutputsPending int
	outputCounts          map[string]int
	deferredMessages      [][]byte
	pendingToolMedia      [][][]byte
}

func newResponsesToolMediaTracker(rawInputArray []ojson.Result) (*responsesToolMediaTracker, []ojson.Result) {
	explicitOutputCounts := make(map[string]int)
	missingIDOutputsCount := 0
	for _, item := range rawInputArray {
		itemType := item.Get("type").String()
		if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
			id := translatorcommon.ExtractResponsesCallID(item)
			if id != "" {
				explicitOutputCounts[id]++
			} else {
				missingIDOutputsCount++
			}
		}
	}

	callCounts := make(map[string]int)
	unclaimedCalls := make(map[string]bool)
	for _, item := range rawInputArray {
		itemType := item.Get("type").String()
		if itemType == "function_call" || itemType == "custom_tool_call" {
			id := translatorcommon.ExtractResponsesCallID(item)
			if id == "" {
				continue
			}
			callCounts[id]++
			if explicitOutputCounts[id] == 0 {
				unclaimedCalls[id] = true
			}
		}
	}
	inputItems := translatorcommon.NormalizeResponsesToolCallOutputs(rawInputArray)
	if missingIDOutputsCount > 1 || (missingIDOutputsCount > 0 && len(unclaimedCalls) > 1) {
		for idx, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
				if idx < len(rawInputArray) && translatorcommon.ExtractResponsesCallID(rawInputArray[idx]) == "" {
					rawDoc := ojson.NewDocString(item.Raw)
					_ = rawDoc.Delete("call_id")
					_ = rawDoc.Delete("tool_call_id")
					_ = rawDoc.Delete("callId")
					inputItems[idx] = util.ParseGJSONBytesNoCopy(rawDoc.Bytes())
				}
			}
		}
	}

	return &responsesToolMediaTracker{
		callCounts:           callCounts,
		explicitOutputCounts: explicitOutputCounts,
		duplicateOutputIDs:   make(map[string]struct{}),
		pendingToolCalls:     make([]interface{}, 0),
		pendingToolCallIDs:   make([]string, 0),
		awaitingToolOutputs:  make(map[string]bool),
		outputCounts:         make(map[string]int),
		deferredMessages:     make([][]byte, 0),
	}, inputItems
}

func (t *responsesToolMediaTracker) hasPendingCalls() bool {
	return len(t.pendingToolCalls) > 0
}

func (t *responsesToolMediaTracker) pendingCalls() []interface{} {
	return t.pendingToolCalls
}

func (t *responsesToolMediaTracker) recordCall(toolCall interface{}, callID string) {
	t.pendingToolCalls = append(t.pendingToolCalls, toolCall)
	if callID != "" {
		t.pendingToolCallIDs = append(t.pendingToolCallIDs, callID)
	}
}

func (t *responsesToolMediaTracker) finishPendingCallGroup() {
	// Defer interleaved messages only for a fully paired, unambiguous group.
	// An incomplete group elsewhere in the history does not affect this one.
	completeGroup := len(t.pendingToolCalls) == len(t.pendingToolCallIDs)
	for _, id := range t.pendingToolCallIDs {
		if t.callCounts[id] != 1 || t.explicitOutputCounts[id] != 1 || t.outputCounts[id] != 0 {
			completeGroup = false
			break
		}
	}
	if completeGroup {
		t.matchedOutputsPending += len(t.pendingToolCallIDs)
	}
	for _, id := range t.pendingToolCallIDs {
		t.awaitingToolOutputs[id] = completeGroup
	}
	t.pendingToolCalls = t.pendingToolCalls[:0]
	t.pendingToolCallIDs = t.pendingToolCallIDs[:0]
}

func (t *responsesToolMediaTracker) appendRegularMessage(message []byte, appendMessage func([]byte), messageCount func() int) int {
	// Keep tool-call adjacency strict for providers that require
	// assistant(tool_calls) -> tool(tool_call_id) with no message in between.
	// Messages arriving while calls are still buffered are deferred too,
	// so one assistant turn of parallel calls stays in a single message.
	if len(t.pendingToolCalls) > 0 || t.matchedOutputsPending > 0 {
		t.deferredMessages = append(t.deferredMessages, message)
		return -1
	}
	appendMessage(message)
	return messageCount() - 1
}

func (t *responsesToolMediaTracker) flushDeferredMessages(appendMessage func([]byte)) {
	for _, message := range t.deferredMessages {
		appendMessage(message)
	}
	t.deferredMessages = t.deferredMessages[:0]
}

func (t *responsesToolMediaTracker) flushPendingToolMedia(appendRegularMessage func([]byte) int) {
	for _, parts := range t.pendingToolMedia {
		userDoc := ojson.NewDocString(`{"role":"user","content":[]}`)
		translatorcommon.SetRawArrayItemsDoc(userDoc, "content", parts)
		appendRegularMessage(userDoc.Bytes())
	}
	t.pendingToolMedia = nil
}

func (t *responsesToolMediaTracker) flushBeforeItem(itemType string, appendRegularMessage func([]byte) int) {
	if itemType != "function_call_output" && itemType != "custom_tool_call_output" && t.matchedOutputsPending == 0 {
		t.flushPendingToolMedia(appendRegularMessage)
	}
}

func (t *responsesToolMediaTracker) handleToolOutput(
	itemType string,
	item ojson.Result,
	appendMessage func([]byte),
	appendRegularMessage func([]byte) int,
) error {
	setContent := setFunctionCallOutputContentDoc
	if itemType == "custom_tool_call_output" {
		setContent = setCustomToolCallOutputContentDoc
	}
	callID := translatorcommon.ExtractResponsesCallID(item)
	if callID != "" {
		t.outputCounts[callID]++
		if t.outputCounts[callID] > 1 {
			t.duplicateOutputIDs[callID] = struct{}{}
		}
	}
	var errOut error
	completeGroup, awaiting := t.awaitingToolOutputs[callID]
	if !awaiting {
		if t.matchedOutputsPending == 0 {
			t.flushPendingToolMedia(appendRegularMessage)
		}
		// Orphan outputs (empty call_id or no matching assistant
		// tool_calls, e.g. Codex send_message_to_thread cards) must
		// not become tool messages. Emit as user text instead.
		errOut = appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setContent, appendRegularMessage)
	} else {
		toolMessageDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)
		_ = toolMessageDoc.Set("tool_call_id", callID)
		delete(t.awaitingToolOutputs, callID)
		if completeGroup {
			t.matchedOutputsPending--
		}
		if output := item.Get("output"); output.Exists() {
			fileParts, errToolOut := setContent(toolMessageDoc, output, true)
			if errToolOut != nil {
				errOut = errToolOut
			}
			if len(fileParts) > 0 {
				t.pendingToolMedia = append(t.pendingToolMedia, fileParts)
			}
		}
		appendMessage(toolMessageDoc.Bytes())
	}
	if len(t.awaitingToolOutputs) == 0 || (completeGroup && t.matchedOutputsPending == 0) {
		t.flushPendingToolMedia(appendRegularMessage)
		t.flushDeferredMessages(appendMessage)
	}
	return errOut
}

func (t *responsesToolMediaTracker) extraAmbiguousIDs() []string {
	if t == nil || len(t.duplicateOutputIDs) == 0 {
		return nil
	}
	extraAmbiguous := make([]string, 0, len(t.duplicateOutputIDs))
	for id := range t.duplicateOutputIDs {
		extraAmbiguous = append(extraAmbiguous, id)
	}
	return extraAmbiguous
}

func relayChatToolOutputImages(structuredContent ojson.Result, relayImages bool) ([][]byte, [][]byte) {
	contentItems := make([][]byte, 0, len(structuredContent.Array()))
	relayItems := make([][]byte, 0, 2)
	for _, item := range structuredContent.Array() {
		if itemType := item.Get("type").String(); relayImages && (itemType == "image_url" || itemType == "input_image") {
			if relayPart := chatToolOutputContentPart(item); relayPart != nil {
				relayItems = append(relayItems, relayPart)
				continue
			}
		}
		contentItems = append(contentItems, chatToolOutputContentPart(item))
	}
	if len(relayItems) > 0 {
		contentItems = append(contentItems, []byte(`{"type":"text","text":"[Tool images are included in a following user message.]"}`))
		notice := []byte(`{"type":"text","text":"Images returned by the preceding tool call(s):"}`)
		relayItems = append([][]byte{notice}, relayItems...)
	}
	return contentItems, relayItems
}
