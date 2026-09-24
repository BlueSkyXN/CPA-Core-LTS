package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func decodeTestQoder(t *testing.T, encoded string) map[string]any {
	t.Helper()
	const normal = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	const custom = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!$"
	decoded := []byte(encoded)
	for i, c := range decoded {
		index := strings.IndexByte(custom, c)
		if index < 0 {
			t.Fatalf("invalid encoded byte")
		}
		decoded[i] = normal[index]
	}
	n := len(decoded) / 3
	reordered := append(append([]byte{}, decoded[len(decoded)-n:]...), decoded[n:len(decoded)-n]...)
	reordered = append(reordered, decoded[:n]...)
	raw, err := base64.StdEncoding.DecodeString(string(reordered))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNativeCosyTextInferenceSignsEncodedBody(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var chats int
			r, host := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v1/jobToken/exchange":
					exchangeFixture(w)
				case "/api/v1/userinfo":
					if request.Header.Get("Authorization") != "Bearer jt-fixture" {
						t.Error("userinfo used the wrong credential")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"fixture-user","name":"Fixture","user_type":"personal_professional_trial"}`)
				case qoderCosyInferencePath:
					chats++
					if request.URL.Query().Get("Encode") != "1" || request.URL.Query().Get("AgentId") != "agent_common" || request.Header.Get("X-Model-Key") != "qmodel_38max" {
						t.Error("COSY route or model identity is invalid")
					}
					encoded, _ := io.ReadAll(request.Body)
					parts := strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer COSY."), ".")
					if len(parts) != 2 {
						t.Fatal("missing COSY authorization")
					}
					sum := md5.Sum([]byte(strings.Join([]string{parts[0], request.Header.Get("Cosy-Key"), request.Header.Get("Cosy-Date"), string(encoded),
						strings.TrimPrefix(request.URL.Path, "/algo")}, "\n")))
					if parts[1] != hex.EncodeToString(sum[:]) {
						t.Error("COSY POST signature does not match the transmitted body")
					}
					body := decodeTestQoder(t, string(encoded))
					messages := body["messages"].([]any)
					if len(messages) != 1 || messages[0].(map[string]any)["content"] != "Reply with OK." || body["stream"] != true ||
						body["model_config"].(map[string]any)["key"] != "qmodel_38max" || len(body["tools"].([]any)) != 0 {
						t.Error("COSY request changed the text message, model, or tool policy")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"OK.\\\"},\\\"finish_reason\\\":\\\"stop\\\"}]}\"}\n\n")
					fmt.Fprint(w, "data: {\"body\":\"[DONE]\"}\n\n")
				default:
					t.Errorf("unexpected route: %s", request.URL.Path)
				}
			})
			r.config.DirectEndpoint = strings.TrimSuffix(r.config.DirectEndpoint, "/model/v1/chat/completions") + qoderCosyInferencePath
			req := nativeReq("cosy", stream)
			req.Payload = []byte(`{"messages":[{"role":"user","content":"Reply with OK."}],"max_tokens":64}`)
			if stream {
				response, err := r.executeNativeStream(req)
				if err != nil || response.Headers.Get("Content-Type") != "text/event-stream" {
					t.Fatalf("stream start: %#v, %v", response, err)
				}
				closed := waitNativeClose(t, host)
				if closed.Error != "" || closed.ErrorCode != "" {
					t.Fatalf("stream close: %#v", closed)
				}
				host.mu.Lock()
				joined := string(bytes.Join(host.emitted, []byte("\n")))
				host.mu.Unlock()
				if !strings.Contains(joined, "OK.") || !strings.Contains(joined, `"finish_reason":"stop"`) {
					t.Fatalf("stream did not finish with text: %q", joined)
				}
			} else {
				response, err := r.executeNative(req)
				if err != nil || !strings.Contains(string(response.Payload), "OK.") {
					t.Fatalf("non-stream result: %#v, %v", response, err)
				}
			}
			if chats != 1 {
				t.Fatalf("inference calls = %d", chats)
			}
		})
	}
}

func TestNativeCosyRejectsUnverifiedInputs(t *testing.T) {
	model := pluginapi.ModelInfo{ID: "qfmodel", DisplayName: "Qwen3.8-Flash"}
	for _, payload := range []string{
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`,
	} {
		req := nativeReq("unsupported", false).ExecutorRequest
		req.Model, req.Payload = "qfmodel", []byte(payload)
		if _, err := nativeCosyRequestPayload(req, model, nil); err == nil {
			t.Fatal("unsupported input was accepted")
		}
	}
}

func TestNativeCosyCatalogAdvertisesTextOnly(t *testing.T) {
	r, _ := nativeFixture(t, func(w http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected upstream request: %s", request.URL.Path)
	})
	r.config.DirectEndpoint = strings.TrimSuffix(r.config.DirectEndpoint, "/model/v1/chat/completions") + qoderCosyInferencePath
	r.config.DirectModels = []directModelConfig{{ID: "qfmodel", DisplayName: "Qwen3.8-Flash", IsVL: true}}
	raw, _ := json.Marshal(rpcAuthModelRequest{AuthModelRequest: pluginapi.AuthModelRequest{
		StorageJSON: []byte(`{"type":"qoder","auth_mode":"pat","pat":"pt-fixture"}`),
	}})
	result, err := r.modelsForAuth(raw)
	if err != nil || len(result.Models) != 1 || len(result.Models[0].SupportedInputModalities) != 1 || result.Models[0].SupportedInputModalities[0] != "text" {
		t.Fatalf("COSY advertised unsupported input: %#v, %v", result.Models, err)
	}
}
