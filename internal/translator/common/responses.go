package common

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// SetResponsesToolCallIdentityDoc writes a resolved Responses tool name and namespace into doc.
func SetResponsesToolCallIdentityDoc(doc *ojson.Doc, name, namespace, itemPath string) {
	if doc == nil {
		return
	}
	namePath := "name"
	namespacePath := "namespace"
	if itemPath != "" {
		namePath = itemPath + ".name"
		namespacePath = itemPath + ".namespace"
	}
	_ = doc.Set(namePath, name)
	if namespace != "" {
		_ = doc.Set(namespacePath, namespace)
	} else {
		_ = doc.Delete(namespacePath)
	}
}

// SetResponsesToolCallIdentity writes a resolved Responses tool name and namespace.
func SetResponsesToolCallIdentity(item []byte, name, namespace, itemPath string) []byte {
	doc := ojson.NewDoc(item)
	SetResponsesToolCallIdentityDoc(doc, name, namespace, itemPath)
	return doc.Bytes()
}

// ExtractResponsesCallID extracts the tool call ID from a Responses API item,
// prioritizing dedicated tool call references over generic item IDs:
// call_id -> tool_call_id -> callId -> id (excluding fco_ output item IDs)
func ExtractResponsesCallID(node ojson.Result) string {
	if callID := strings.TrimSpace(node.Get("call_id").String()); callID != "" {
		return callID
	}
	if toolCallID := strings.TrimSpace(node.Get("tool_call_id").String()); toolCallID != "" {
		return toolCallID
	}
	if callId := strings.TrimSpace(node.Get("callId").String()); callId != "" {
		return callId
	}
	id := strings.TrimSpace(node.Get("id").String())
	if strings.HasPrefix(id, "fco_") {
		return ""
	}
	return id
}

// NormalizeResponsesToolCallOutputs scans a slice of Responses input items,
// pairs function_call_output / custom_tool_call_output with preceding pending tool calls,
// and assigns missing call_ids to tool outputs using a multi-pass matching strategy:
// 1. Exact explicit call ID match (reserving calls for outputs that explicitly reference them across the entire conversation).
// 2. Function name match for outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// 3. FIFO queue fallback for remaining outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// Outputs that already carry an explicit call ID that does not match any pending call
// are never rewritten or stolen.
func NormalizeResponsesToolCallOutputs(items []ojson.Result) []ojson.Result {
	if len(items) == 0 {
		return items
	}

	normalized := make([]ojson.Result, len(items))
	copy(normalized, items)

	explicitOutputCounts := make(map[string]int)
	for _, item := range items {
		typ := item.Get("type").String()
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			if id := ExtractResponsesCallID(item); id != "" {
				explicitOutputCounts[id]++
			}
		}
	}

	var pendingCallIDs []string
	pendingCallNames := make(map[string]string)

	i := 0
	for i < len(normalized) {
		item := normalized[i]
		itemType := item.Get("type").String()

		switch itemType {
		case "function_call", "custom_tool_call":
			callID := ExtractResponsesCallID(item)
			if callID != "" {
				pendingCallIDs = append(pendingCallIDs, callID)
				name := item.Get("name").String()
				pendingCallNames[callID] = name
			}
			i++

		case "function_call_output", "custom_tool_call_output":
			start := i
			for i < len(normalized) && (normalized[i].Get("type").String() == "function_call_output" || normalized[i].Get("type").String() == "custom_tool_call_output") {
				i++
			}
			outputs := normalized[start:i]

			if len(pendingCallIDs) > 0 {
				used := make([]bool, len(outputs))
				matchedForPending := make([]int, len(pendingCallIDs))
				for idx := range matchedForPending {
					matchedForPending[idx] = -1
				}

				// Pass 1: exact explicit call ID match
				for pendingIdx, pendingID := range pendingCallIDs {
					for outIdx, out := range outputs {
						if !used[outIdx] && ExtractResponsesCallID(out) == pendingID {
							used[outIdx] = true
							matchedForPending[pendingIdx] = outIdx
							explicitOutputCounts[pendingID]--
							break
						}
					}
				}

				// Pass 2: match by function name for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx, pendingID := range pendingCallIDs {
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					expectedName := pendingCallNames[pendingID]
					if expectedName != "" {
						for outIdx, out := range outputs {
							if !used[outIdx] && ExtractResponsesCallID(out) == "" && !isCodexDelegationToolOutput(out) {
								outName := strings.TrimSpace(out.Get("name").String())
								if outName != "" && outName == expectedName {
									used[outIdx] = true
									matchedForPending[pendingIdx] = outIdx
									break
								}
							}
						}
					}
				}

				// Pass 3: FIFO fallback for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx := range pendingCallIDs {
					pendingID := pendingCallIDs[pendingIdx]
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					for outIdx, out := range outputs {
						if !used[outIdx] && ExtractResponsesCallID(out) == "" && !isCodexDelegationToolOutput(out) {
							outName := strings.TrimSpace(out.Get("name").String())
							expectedName := pendingCallNames[pendingID]
							if outName == "" || expectedName == "" || outName == expectedName {
								used[outIdx] = true
								matchedForPending[pendingIdx] = outIdx
								break
							}
						}
					}
				}

				// Apply matched call_ids to outputs
				var remainingPending []string
				for pendingIdx, pendingID := range pendingCallIDs {
					outIdx := matchedForPending[pendingIdx]
					if outIdx < 0 {
						remainingPending = append(remainingPending, pendingID)
						continue
					}
					matchedOut := outputs[outIdx]
					if matchedOut.Get("call_id").String() != pendingID {
						raw := []byte(matchedOut.Raw)
						raw, _ = ojson.SetBytes(raw, "call_id", pendingID)
						normalized[start+outIdx] = util.ParseGJSONBytesNoCopy(raw)
					}
				}
				pendingCallIDs = remainingPending
			}

		default:
			i++
		}
	}

	return normalized
}

// SanitizeOrphanResponsesToolOutputsDoc rewrites standalone function_call_output
// and custom_tool_call_output items that carry no call_id into user message
// items. Codex Desktop Companion Page context is injected as function_call_output
// items without call_id, which strict Responses upstreams reject with HTTP 400
// ("responses function_call_output call_id is required").
func SanitizeOrphanResponsesToolOutputsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	return SanitizeOrphanResponsesToolOutputsInputDoc(doc.Get("input"), func() *ojson.Doc {
		return doc
	})
}

// SanitizeOrphanResponsesToolOutputsInputDoc pairs tool outputs missing call_id
// with preceding unmatched tool calls when possible, and rewrites remaining
// standalone function_call_output and custom_tool_call_output items without
// call_id into user message items in-place on the document returned by ensureDoc.
func SanitizeOrphanResponsesToolOutputsInputDoc(inputResult ojson.Result, ensureDoc func() *ojson.Doc) bool {
	if !inputResult.IsArray() || ensureDoc == nil {
		return false
	}
	inputItems := inputResult.Array()
	if len(inputItems) == 0 {
		return false
	}

	normalizedItems := NormalizeResponsesToolCallOutputs(inputItems)
	if responsesToolOutputAssignmentAmbiguous(inputItems) {
		normalizedItems = append(make([]ojson.Result, 0, len(inputItems)), inputItems...)
	}

	changed := false
	pendingMissingCallIDOutputType := ""
	callIDs := make(map[string]struct{})
	for _, item := range inputItems {
		if item.IsObject() {
			itemType := strings.TrimSpace(item.Get("type").String())
			if itemType == "function_call" || itemType == "custom_tool_call" {
				if callID := ExtractResponsesCallID(item); callID != "" {
					callIDs[callID] = struct{}{}
				}
			}
		}
	}
	for i := 0; i < len(inputItems); i++ {
		item := normalizedItems[i]
		if !item.IsObject() {
			pendingMissingCallIDOutputType = ""
			continue
		}
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "function_call" || itemType == "custom_tool_call" {
			if ExtractResponsesCallID(item) == "" {
				pendingMissingCallIDOutputType = itemType + "_output"
			} else {
				pendingMissingCallIDOutputType = ""
			}
			continue
		}
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			pendingMissingCallIDOutputType = ""
			continue
		}
		if isCodexDelegationToolOutput(item) {
			pendingMissingCallIDOutputType = ""
			continue
		}
		resolvedCallID := responsesToolOutputReferenceCallID(item)
		if resolvedCallID == "" {
			if effectiveCallID := ExtractResponsesCallID(item); effectiveCallID != "" {
				if _, matchedCall := callIDs[effectiveCallID]; matchedCall {
					resolvedCallID = effectiveCallID
				}
			}
		}
		if resolvedCallID != "" {
			pendingMissingCallIDOutputType = ""
			origCallID := strings.TrimSpace(inputItems[i].Get("call_id").String())
			if origCallID != resolvedCallID {
				itemPath := fmt.Sprintf("input.%d.call_id", i)
				if errSet := ensureDoc().Set(itemPath, resolvedCallID); errSet == nil {
					changed = true
				}
			}
			continue
		}
		if pendingMissingCallIDOutputType == itemType &&
			strings.TrimSpace(item.Get("name").String()) == "" &&
			strings.TrimSpace(item.Get("id").String()) == "" {
			pendingMissingCallIDOutputType = ""
			continue
		}
		pendingMissingCallIDOutputType = ""

		itemPath := fmt.Sprintf("input.%d", i)
		if setOrphanResponsesToolOutputMessageDoc(ensureDoc(), itemPath, item) {
			changed = true
		}
	}
	return changed
}

func responsesToolOutputReferenceCallID(item ojson.Result) string {
	for _, key := range []string{"call_id", "tool_call_id", "callId"} {
		if callID := strings.TrimSpace(item.Get(key).String()); callID != "" {
			return callID
		}
	}
	return ""
}

func responsesToolOutputAssignmentAmbiguous(items []ojson.Result) bool {
	missingPairableOutputs := 0
	explicitOutputs := make(map[string]int)
	for _, item := range items {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call_output", "custom_tool_call_output":
			if callID := ExtractResponsesCallID(item); callID != "" {
				explicitOutputs[callID]++
				continue
			}
			if responsesToolOutputPairingCandidate(item) {
				missingPairableOutputs++
			}
		}
	}
	if missingPairableOutputs == 0 {
		return false
	}

	unclaimedCalls := 0
	for _, item := range items {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			callID := ExtractResponsesCallID(item)
			if callID != "" && explicitOutputs[callID] == 0 {
				unclaimedCalls++
			}
		}
	}
	return missingPairableOutputs > 1 || unclaimedCalls > 1
}

func responsesToolOutputPairingCandidate(item ojson.Result) bool {
	return ExtractResponsesCallID(item) == "" && !isCodexDelegationToolOutput(item)
}

func isCodexDelegationToolOutput(item ojson.Result) bool {
	if strings.TrimSpace(item.Get("type").String()) != "function_call_output" {
		return false
	}
	if strings.TrimSpace(item.Get("namespace").String()) != "codex_app" {
		return false
	}
	switch strings.TrimSpace(item.Get("name").String()) {
	case "create_thread", "send_message_to_thread":
		return true
	default:
		return false
	}
}

func setOrphanResponsesToolOutputMessageDoc(targetDoc *ojson.Doc, itemPath string, item ojson.Result) bool {
	if targetDoc == nil {
		return false
	}
	messageDoc := ojson.NewDoc([]byte(`{"type":"message","role":"user","content":[]}`))
	content := buildOrphanResponsesToolOutputContent(item)
	for idx, partRaw := range content {
		if errSet := messageDoc.SetRawBytes(fmt.Sprintf("content.%d", idx), partRaw); errSet != nil {
			return false
		}
	}
	return targetDoc.SetRawBytes(itemPath, messageDoc.Bytes()) == nil
}

func buildOrphanResponsesToolOutputContent(item ojson.Result) [][]byte {
	name := strings.TrimSpace(item.Get("name").String())
	if name == "" {
		name = "tool"
	}
	label := []byte(`{"type":"input_text","text":""}`)
	label, _ = ojson.SetBytes(label, "text", "Tool output from "+name+":")
	parts := [][]byte{label}
	return append(parts, orphanResponsesToolOutputContentParts(item.Get("output"))...)
}

func orphanResponsesToolOutputContentParts(output ojson.Result) [][]byte {
	if !output.Exists() || output.Type == ojson.Null {
		return [][]byte{orphanResponsesTextPart("(empty)")}
	}
	if output.Type == ojson.String {
		text := strings.TrimSpace(output.String())
		if ojson.Valid(text) {
			parsed := ojson.Parse(text)
			if parsed.IsArray() {
				return orphanResponsesToolOutputArrayParts(parsed)
			}
		}
		return [][]byte{orphanResponsesTextPart(output.String())}
	}
	if output.IsArray() {
		return orphanResponsesToolOutputArrayParts(output)
	}
	if output.IsObject() {
		if part := orphanResponsesToolOutputObjectPart(output); part != nil {
			return [][]byte{part}
		}
	}
	return [][]byte{orphanResponsesTextPart(output.Raw)}
}

func orphanResponsesToolOutputArrayParts(output ojson.Result) [][]byte {
	items := output.Array()
	parts := make([][]byte, 0, len(items))
	for _, item := range items {
		switch {
		case item.Type == ojson.String:
			parts = append(parts, orphanResponsesTextPart(item.String()))
		case item.IsObject():
			if part := orphanResponsesToolOutputObjectPart(item); part != nil {
				parts = append(parts, part)
			}
		default:
			if raw := strings.TrimSpace(item.Raw); raw != "" {
				parts = append(parts, orphanResponsesTextPart(raw))
			}
		}
	}
	if len(parts) == 0 {
		return [][]byte{orphanResponsesTextPart("(empty)")}
	}
	return parts
}

func orphanResponsesToolOutputObjectPart(item ojson.Result) []byte {
	switch strings.TrimSpace(item.Get("type").String()) {
	case "text", "input_text", "output_text":
		return orphanResponsesTextPart(item.Get("text").String())
	case "image_url", "input_image", "output_image":
		if part := orphanResponsesImagePart(item); part != nil {
			return part
		}
	case "input_file":
		if part := orphanResponsesFilePart(item); part != nil {
			return part
		}
	}
	if raw := strings.TrimSpace(item.Raw); raw != "" {
		return orphanResponsesTextPart(raw)
	}
	return nil
}

func orphanResponsesImagePart(item ojson.Result) []byte {
	var urlValue ojson.Result
	detailValue := item.Get("detail")
	switch strings.TrimSpace(item.Get("type").String()) {
	case "image_url":
		urlValue = item.Get("image_url.url")
		detailValue = item.Get("image_url.detail")
	case "input_image", "output_image":
		urlValue = item.Get("image_url")
		if urlValue.IsObject() {
			urlValue = urlValue.Get("url")
		}
	}
	if urlValue.Type != ojson.String || strings.TrimSpace(urlValue.String()) == "" {
		return nil
	}

	part := []byte(`{"type":"input_image","image_url":""}`)
	part, _ = ojson.SetBytes(part, "image_url", urlValue.String())
	if detail := strings.TrimSpace(detailValue.String()); detail != "" {
		part, _ = ojson.SetBytes(part, "detail", detail)
	}
	return part
}

func orphanResponsesFilePart(item ojson.Result) []byte {
	for _, key := range []string{"file_id", "file_data", "file_url"} {
		if value := item.Get(key); value.Type == ojson.String && strings.TrimSpace(value.String()) != "" {
			return []byte(item.Raw)
		}
	}
	return nil
}

func orphanResponsesTextPart(text string) []byte {
	part := []byte(`{"type":"input_text","text":""}`)
	part, _ = ojson.SetBytes(part, "text", text)
	return part
}
