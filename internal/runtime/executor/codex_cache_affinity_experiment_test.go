package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/gorilla/websocket"
	authpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Isolated factorial constructor. Override H and K only AFTER the production
// helper and metadata/header shaping, so C cannot accidentally become D.
func codexAffinityExperimentRequest(ctx context.Context, auth *authpkg.Auth, endpoint, group, run string, payload []byte) (*http.Request, error) {
	e := NewCodexExecutor(affinityTestConfig("legacy"))
	incoming := http.Header{"X-Session-Id": {"synthetic-experiment"}, "User-Agent": {"synthetic-client"}}
	r, body, state, err := e.cacheHelper(ctx, translator.FormatOpenAIResponse, endpoint, auth, execpkg.Request{Model: gjson.GetBytes(payload, "model").String(), Payload: payload}, payload, payload, incoming)
	if err != nil {
		return nil, err
	}
	token, _ := codexCreds(auth)
	applyCodexHeaders(r, auth, token, true, e.cfg, incoming)
	applyFinalCodexClientHeaders(r.Header, resolveCodexModelHeaderProfile(gjson.GetBytes(payload, "model").String()), auth)
	applyCodexOutboundMetadataHeaders(r.Header, &state)
	switch group {
	case "B":
		r.Header.Set("Session-Id", affinityUUID("experiment", run, "B"))
		body, _ = sjson.DeleteBytes(body, "prompt_cache_key")
	case "C":
		body, _ = sjson.SetBytes(body, "prompt_cache_key", affinityUUID("experiment", run, "C"))
	case "D":
		key := affinityUUID("experiment", run, "D")
		r.Header.Set("Session-Id", key)
		body, _ = sjson.SetBytes(body, "prompt_cache_key", key)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return r, nil
}
func TestCodexCacheAffinityExperimentWireIndependence(t *testing.T) {
	server, captured := affinityTestServer(t, false)
	defer server.Close()
	for _, group := range []string{"A", "B", "C", "D"} {
		var firstH, firstK string
		for i := 0; i < 2; i++ {
			req, err := codexAffinityExperimentRequest(context.Background(), affinityTestAuth(server.URL, "oauth"), server.URL+"/responses", group, "synthetic-run", []byte(`{"model":"gpt-5.6-sol","input":"synthetic"}`))
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			wire := <-captured
			h, k := wire.headers.Get("Session-Id"), gjson.GetBytes(wire.body, "prompt_cache_key").String()
			if h == "" {
				t.Fatal("missing H")
			}
			if (group == "A" || group == "B") && k != "" {
				t.Fatal("unexpected K")
			}
			if (group == "C" || group == "D") && k == "" {
				t.Fatal("missing K")
			}
			if i == 1 {
				if (group == "B" || group == "D") && h != firstH {
					t.Fatal("fixed H changed")
				}
				if (group == "A" || group == "C") && h == firstH {
					t.Fatal("varying H fixed")
				}
				if k != firstK {
					t.Fatal("fixed K changed")
				}
			}
			if group == "C" && h == k {
				t.Fatal("C coupled into D")
			}
			firstH, firstK = h, k
		}
	}
}

func codexAffinityExperimentWSHeaders(headers http.Header) http.Header {
	out := headers.Clone()
	// gorilla owns the Upgrade/Connection handshake headers.
	out.Del("Connection")
	out.Set("OpenAI-Beta", codexResponsesWebsocketBetaHeaderValue)
	return out
}
func TestCodexCacheAffinityExperimentWebsocketWireIndependence(t *testing.T) {
	server, captured := affinityTestServer(t, true)
	defer server.Close()
	for _, group := range []string{"A", "B", "C", "D"} {
		var previous string
		for i := 0; i < 2; i++ {
			req, err := codexAffinityExperimentRequest(context.Background(), affinityTestAuth(server.URL, "oauth"), server.URL+"/responses", group, "synthetic-ws-run", []byte(`{"model":"gpt-5.6-sol","input":"synthetic"}`))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(req.Body)
			req.Body.Close()
			url, _ := buildCodexResponsesWebsocketURL(req.URL.String())
			conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), url, codexAffinityExperimentWSHeaders(req.Header))
			if err != nil {
				t.Fatal(err)
			}
			if err = conn.WriteMessage(websocket.TextMessage, buildCodexWebsocketRequestBody(body)); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			_, _, err = conn.ReadMessage()
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			wire := <-captured
			h, k := wire.headers.Get("Session-Id"), gjson.GetBytes(wire.body, "prompt_cache_key").String()
			if group == "C" && (h == k || k == "") {
				t.Fatal("WS C coupled to D")
			}
			if (group == "A" || group == "B") && k != "" {
				t.Fatal("unexpected WS pck")
			}
			if group == "D" && (h != k || h == "") {
				t.Fatal("WS D fields differ")
			}
			if i > 0 {
				if (group == "B" || group == "D") && previous != h {
					t.Fatal("fixed WS H changed")
				}
				if (group == "A" || group == "C") && previous == h {
					t.Fatal("varying WS H fixed")
				}
			}
			previous = h
		}
	}
}
