package openai

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

const collaborationPlaintextTestTools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}]}]`
const collaborationPlaintextTestOutput = `[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"send_message","namespace":"collaboration","arguments":"{\"message\":\"task text\"}"}]`

func TestResponsesBoundaryRestoresPlaintextToolArguments(t *testing.T) {
	for _, client := range []string{"Codex Desktop/0.163.0", "zcode/1.0"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", client, stream), func(t *testing.T) {
				executor := &responsesMultiAgentCaptureExecutor{output: []byte(collaborationPlaintextTestOutput)}
				handler, model := newResponsesMultiAgentTestHandler(t, executor)
				router := gin.New()
				router.POST("/v1/responses", handler.Responses)
				body := fmt.Sprintf(`{"model":%q,"stream":%t,"input":"delegate","tools":%s}`, model, stream, collaborationPlaintextTestTools)
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				request.Header.Set("User-Agent", client)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status=%d: %s", recorder.Code, recorder.Body.String())
				}
				response := recorder.Body.Bytes()
				path := "output.0"
				if stream {
					for _, line := range bytes.Split(response, []byte("\n")) {
						if bytes.HasPrefix(line, []byte("data:")) && gjson.GetBytes(bytes.TrimSpace(line[5:]), "type").String() == "response.completed" {
							response = bytes.TrimSpace(line[5:])
							break
						}
					}
					path = "response.output.0"
				}
				item := gjson.GetBytes(response, path)
				if item.Get("arguments").String() != `{"message":"task text"}` || item.Get("call_id").String() != "call-1" {
					t.Fatalf("tool identity or content changed: %s", response)
				}
				marker := item.Get("encrypted_function_args")
				if want := strings.HasPrefix(client, "Codex Desktop/"); marker.Exists() != want || want && marker.Raw != "[]" {
					t.Fatalf("client=%s plaintext marker=%s", client, marker.Raw)
				}
			})
		}
	}
}

func TestResponsesWebsocketRestoresPlaintextToolArguments(t *testing.T) {
	executor := &responsesMultiAgentCaptureExecutor{output: []byte(collaborationPlaintextTestOutput)}
	handler, model := newResponsesMultiAgentTestHandler(t, executor)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", http.Header{"User-Agent": []string{"Codex Desktop/0.163.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < 3; turn++ {
		toolsField := ""
		input := `[]`
		if turn == 0 {
			toolsField = `,"tools":` + collaborationPlaintextTestTools
		} else {
			input = `[{"type":"function_call_output","call_id":"call-1","output":"ok"}]`
			if turn == 2 {
				toolsField = `,"tools":` + strings.ReplaceAll(collaborationPlaintextTestTools, `"encrypted":true`, `"encrypted":false`)
			}
		}
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":%s%s}`, model, input, toolsField)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
			t.Fatal(err)
		}
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(payload, "type").String() == "response.completed" {
				marker := gjson.GetBytes(payload, "response.output.0.encrypted_function_args")
				if want := turn < 2; marker.Exists() != want || want && marker.Raw != "[]" {
					t.Fatalf("turn %d: plaintext marker did not follow inherited/replaced tools: %s", turn, payload)
				}
				break
			}
		}
	}
}
