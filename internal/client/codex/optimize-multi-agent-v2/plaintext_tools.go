package multiagentv2

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type plaintextToolKey struct {
	namespace string
	name      string
}

// PlaintextToolProvenance is an immutable, request-local record of collaboration
// schemas whose encryption marker was removed. It contains no message contents.
type PlaintextToolProvenance struct {
	tools    map[plaintextToolKey]bool
	prepared bool
}

type initialPlaintextToolProvenanceKey struct{}

// WithInitialPlaintextToolProvenance carries the handler's proof before the
// executor sees tools whose encryption markers have already been removed.
func WithInitialPlaintextToolProvenance(ctx context.Context, proof PlaintextToolProvenance) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, initialPlaintextToolProvenanceKey{}, proof)
}

// InitialPlaintextToolProvenance applies only to the initial executor request,
// never to later creates read directly from a duplex connection.
func InitialPlaintextToolProvenance(ctx context.Context) (PlaintextToolProvenance, bool) {
	if ctx == nil {
		return PlaintextToolProvenance{}, false
	}
	proof, ok := ctx.Value(initialPlaintextToolProvenanceKey{}).(PlaintextToolProvenance)
	return proof, ok
}

// PreparePlaintextTools captures proof before namespace optimization. A disabled
// or unrelated client returns an ineligible snapshot that cannot inherit proof.
func PreparePlaintextTools(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) ([]byte, PlaintextToolProvenance) {
	if !codexMultiAgentV2Enabled(ctx, headers, cfg) {
		return payload, PlaintextToolProvenance{}
	}
	updated := rewriteCodexMultiAgentMessageEncryption(payload)
	return updated, CapturePlaintextToolProvenance(payload, updated)
}

// CapturePlaintextToolProvenance pairs the original and prepared client tool
// declarations. Unproven duplicate declarations revoke the identity rather than
// allowing a same-name tool to inherit another declaration's plaintext marker.
func CapturePlaintextToolProvenance(before, after []byte) PlaintextToolProvenance {
	proof := PlaintextToolProvenance{tools: make(map[plaintextToolKey]bool), prepared: true}
	for _, path := range codexMultiAgentMessageToolPaths(before) {
		separator := strings.LastIndex(path, ".tools.")
		if separator < 0 {
			continue
		}
		oldTool := gjson.GetBytes(before, path)
		newTool := gjson.GetBytes(after, path)
		key := plaintextToolKey{
			namespace: strings.TrimSpace(gjson.GetBytes(before, path[:separator]+".name").String()),
			name:      strings.TrimSpace(oldTool.Get("name").String()),
		}
		confirmed := oldTool.Get("parameters.properties.message.encrypted").Type == gjson.True &&
			strings.TrimSpace(gjson.GetBytes(after, path[:separator]+".name").String()) == key.namespace &&
			newTool.Get("type").String() == "function" && newTool.Get("name").String() == oldTool.Get("name").String() &&
			!newTool.Get("parameters.properties.message.encrypted").Exists()
		if previous, seen := proof.tools[key]; seen {
			confirmed = confirmed && previous
		}
		proof.tools[key] = confirmed
	}
	return proof
}

// InheritTurn follows WebSocket tool inheritance without sharing mutable state.
// Explicit top-level tools replace prior proof; inline declarations revoke only
// the identities they redefine. An ineligible current turn inherits nothing.
func (p PlaintextToolProvenance) InheritTurn(previous PlaintextToolProvenance, incoming []byte) PlaintextToolProvenance {
	if !p.prepared || gjson.GetBytes(incoming, "tools").Exists() || len(previous.tools) == 0 {
		return p
	}
	explicit := make(map[plaintextToolKey]bool)
	var declarations func(gjson.Result, string)
	declarations = func(tools gjson.Result, namespace string) {
		for _, tool := range tools.Array() {
			name := strings.TrimSpace(tool.Get("name").String())
			if tool.Get("type").String() == "namespace" {
				declarations(tool.Get("tools"), name)
			} else if namespace != "" && name != "" {
				explicit[plaintextToolKey{namespace: namespace, name: name}] = true
			}
		}
	}
	for _, item := range gjson.GetBytes(incoming, "input").Array() {
		if kind := item.Get("type").String(); kind == "additional_tools" || kind == "tool_search_output" {
			declarations(item.Get("tools"), "")
		}
	}
	next := PlaintextToolProvenance{tools: make(map[plaintextToolKey]bool), prepared: true}
	for key, confirmed := range previous.tools {
		if !explicit[key] {
			next.tools[key] = confirmed
		}
	}
	for key, confirmed := range p.tools {
		if _, inherited := next.tools[key]; !inherited {
			next.tools[key] = confirmed
		}
	}
	return next
}

// Restore marks the matching plaintext function arguments after provider names
// have been restored to the client namespace. Existing encryption information,
// arguments, call IDs and unrelated tools are left intact.
func (p PlaintextToolProvenance) Restore(payload []byte) []byte {
	if len(p.tools) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	updated := payload
	restore := func(path string) {
		item := gjson.GetBytes(updated, path)
		if item.Get("type").String() != "function_call" || item.Get("encrypted_function_args").Exists() {
			return
		}
		key := plaintextToolKey{namespace: strings.TrimSpace(item.Get("namespace").String()), name: strings.TrimSpace(item.Get("name").String())}
		if !p.tools[key] {
			return
		}
		if result, err := sjson.SetRawBytes(updated, path+".encrypted_function_args", []byte(`[]`)); err == nil {
			updated = result
		}
	}
	restore("item")
	for _, path := range []string{"output", "response.output"} {
		for index := range gjson.GetBytes(updated, path).Array() {
			restore(fmt.Sprintf("%s.%d", path, index))
		}
	}
	return updated
}
