package helps

import (
	"bytes"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

var responseAccessProgramsMarker = []byte(`"access_programs"`)

func extractResponseCyberProgram(payload []byte) string {
	if !bytes.Contains(payload, responseAccessProgramsMarker) {
		return ""
	}
	payload = bytes.TrimSpace(payload)
	payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
	if !ojson.ValidBytes(payload) {
		return ""
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.IsObject() || (root.Get("error").Exists() && root.Get("error").Type != ojson.Null) {
		return ""
	}
	if event := root.Get("type").String(); event != "" {
		switch event {
		case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed":
			root = root.Get("response")
		default:
			return ""
		}
	} else if response := root.Get("response"); response.IsObject() {
		root = response
	}
	programs := root.Get("access_programs")
	if !programs.Exists() {
		return ""
	}
	// The Responses API explicitly reports null for standard safeguards;
	// an absent field, including on legacy responses, is not that evidence.
	if programs.Type == ojson.Null {
		return "standard"
	}
	cyber := programs.Get("cyber")
	if !programs.IsObject() || cyber.Type != ojson.String || cyber.String() == "" {
		return "unknown"
	}
	program := usage.CanonicalResponseCyberProgram(cyber.String())
	if program == "" {
		return "unknown"
	}
	return program
}

func (r *UsageReporter) observeResponseCyberProgram(payload []byte) {
	if program := extractResponseCyberProgram(payload); program != "" {
		r.responseModelMu.Lock()
		r.responseCyberProgram = program
		r.responseModelMu.Unlock()
	}
}

func (r *UsageReporter) snapshotResponseCyberProgram() string {
	r.responseModelMu.RLock()
	defer r.responseModelMu.RUnlock()
	return r.responseCyberProgram
}
