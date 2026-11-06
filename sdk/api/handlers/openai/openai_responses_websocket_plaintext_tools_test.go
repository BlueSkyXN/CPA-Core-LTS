package openai

import (
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"strings"
	"testing"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
)

func TestResponsesWebsocketPlaintextHistoryScopeAndLifetime(t *testing.T) {
	before := []byte(`{"tools":` + collaborationPlaintextTestTools + `}`)
	after := []byte(strings.ReplaceAll(string(before), `,"encrypted":true`, ""))
	confirmed := multiagentv2.CapturePlaintextToolProvenance(before, after)
	unproven := multiagentv2.CapturePlaintextToolProvenance(after, after)
	response := []byte(`{"output":` + collaborationPlaintextTestOutput + `}`)
	var history responsesWebsocketPlaintextHistory
	history.remember("auth-a", "first", confirmed)
	history.remember("auth-a", "second", unproven)
	history.remember("auth-b", "second", confirmed)
	assertMarker := func(authID, request, want string) {
		t.Helper()
		got := history.parent(authID, []byte(request)).Restore(response)
		if marker := ojson.GetBytes(got, "output.0.encrypted_function_args").Raw; marker != want {
			t.Fatalf("auth=%s request=%s: marker=%q want=%q", authID, request, marker, want)
		}
	}
	assertMarker("auth-a", `{"type":"response.create","previous_response_id":"first"}`, "[]")
	assertMarker("auth-a", `{"type":"response.create","previous_response_id":"second"}`, "")
	assertMarker("auth-b", `{"type":"response.create","previous_response_id":"second"}`, "[]")
	assertMarker("auth-b", `{"type":"response.create","previous_response_id":"first"}`, "")
	assertMarker("auth-a", `{"type":"response.create","previous_response_id":"unknown"}`, "")
	assertMarker("auth-b", `{"type":"response.create"}`, "")
	assertMarker("auth-a", `{"type":"response.append"}`, "")
	assertMarker("auth-b", `{"type":"response.append"}`, "[]")

	history.forgetAuth("auth-a")
	assertMarker("auth-a", `{"type":"response.create","previous_response_id":"first"}`, "")
	assertMarker("auth-b", `{"type":"response.append"}`, "[]")
	for i := 0; i < responsesWebsocketPlaintextHistoryLimit; i++ {
		history.remember("auth-b", fmt.Sprintf("new-%d", i), confirmed)
	}
	assertMarker("auth-b", `{"type":"response.create","previous_response_id":"second"}`, "")
	assertMarker("auth-b", `{"type":"response.append"}`, "[]")
}
