//go:build codex_cache_live

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// Explicit build tag AND opt-in are required. This test spends upstream quota;
// normal go test ./... never compiles or executes it.
func TestCodexCacheAffinityLiveExperiment(t *testing.T) {
	if os.Getenv("CPA_AFFINITY_LIVE_AUTHORIZED") != "yes" {
		t.Skip("live quota experiment not authorized")
	}
	endpoint := os.Getenv("CPA_AFFINITY_ENDPOINT")
	token := os.Getenv("CPA_AFFINITY_TOKEN")
	model := os.Getenv("CPA_AFFINITY_MODEL")
	if endpoint == "" || token == "" || model == "" {
		t.Fatal("endpoint, token and model environment variables are required")
	}
	ws := os.Getenv("CPA_AFFINITY_TRANSPORT") == "ws"
	auth := affinityTestAuth("", "oauth")
	auth.Metadata["access_token"] = token
	if account := os.Getenv("CPA_AFFINITY_ACCOUNT"); account != "" {
		auth.Metadata["account_id"] = account
	}
	payload, _ := json.Marshal(map[string]any{"model": model, "stream": true, "store": false, "instructions": "Read the synthetic reference. Reply only OK.", "input": []any{map[string]any{"role": "user", "content": strings.Repeat("Synthetic reference: stable headers and exact content prefixes are measured independently.\n", 600)}}})
	run := uuid.NewString()
	// Two warm-up rounds, followed by four measured rounds, rotating order.
	groups := []string{"A", "B", "C", "D"}
	previousH := map[string]string{}
	previousK := map[string]string{}
	for round := 0; round < 6; round++ {
		for n := 0; n < 4; n++ {
			group := groups[(n+round)%4]
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			req, err := codexAffinityExperimentRequest(ctx, auth, endpoint, group, run, payload)
			if err != nil {
				cancel()
				t.Fatal("constructor failed")
			}
			bodyReader, _ := req.GetBody()
			body := new(bytes.Buffer)
			_, _ = body.ReadFrom(bodyReader)
			bodyReader.Close()
			h, k := req.Header.Get("Session-Id"), gjson.GetBytes(body.Bytes(), "prompt_cache_key").String()
			if round > 0 {
				if (group == "A" || group == "C") && h == previousH[group] {
					cancel()
					t.Fatal("varying final H became fixed")
				}
				if (group == "B" || group == "D") && h != previousH[group] {
					cancel()
					t.Fatal("fixed final H changed")
				}
				if k != previousK[group] {
					cancel()
					t.Fatal("final K changed")
				}
			}
			previousH[group], previousK[group] = h, k
			start := time.Now()
			var completed []byte
			if ws {
				url, err := buildCodexResponsesWebsocketURL(endpoint)
				if err != nil {
					cancel()
					t.Fatal("invalid WS endpoint")
				}
				conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, codexAffinityExperimentWSHeaders(req.Header))
				if err != nil {
					cancel()
					t.Fatal("WS handshake failed")
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
				if conn.WriteMessage(websocket.TextMessage, buildCodexWebsocketRequestBody(body.Bytes())) != nil {
					conn.Close()
					cancel()
					t.Fatal("WS write failed")
				}
				for {
					_, b, err := conn.ReadMessage()
					if err != nil {
						break
					}
					b = normalizeCodexWebsocketCompletion(b)
					if gjson.GetBytes(b, "type").String() == "response.completed" {
						completed = b
						break
					}
				}
				conn.Close()
			} else {
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					cancel()
					t.Fatal("HTTP experiment request failed")
				}
				if response.StatusCode != http.StatusOK {
					response.Body.Close()
					cancel()
					t.Fatalf("upstream HTTP status %d", response.StatusCode)
				}
				scan := bufio.NewScanner(response.Body)
				scan.Buffer(make([]byte, 64<<10), 2<<20)
				for scan.Scan() {
					line := scan.Bytes()
					if bytes.HasPrefix(line, []byte("data:")) {
						b := bytes.TrimSpace(line[5:])
						if gjson.GetBytes(b, "type").String() == "response.completed" {
							completed = bytes.Clone(b)
							break
						}
					}
				}
				response.Body.Close()
			}
			cancel()
			if len(completed) == 0 {
				t.Fatal("no completed response; inspect private gateway diagnostics")
			}
			usage := gjson.GetBytes(completed, "response.usage")
			actualModel := gjson.GetBytes(completed, "response.model").String()
			record := map[string]any{"group": group, "round": round, "warmup": round < 2, "ws": ws, "requested_model": model, "reported_model": actualModel, "latency_ms": time.Since(start).Milliseconds(), "input_tokens": usage.Get("input_tokens").Int(), "cached_tokens": usage.Get("input_tokens_details.cached_tokens").Int(), "output_tokens": usage.Get("output_tokens").Int()}
			b, _ := json.Marshal(record)
			t.Log(string(b))
		}
	}
}
