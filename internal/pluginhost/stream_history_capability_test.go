package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStreamHistoryRequiresExplicitOptOut(t *testing.T) {
	for _, omit := range []bool{false, true} {
		var got pluginapi.StreamChunkInterceptRequest
		plugin := pluginapi.Plugin{SchemaVersion: 5, Capabilities: pluginapi.Capabilities{
			StreamChunkHistoryOmitted: omit,
			StreamChunkInterceptor: responseInterceptorFunc{interceptStreamChunk: func(_ context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
				got = req
				return pluginapi.StreamChunkInterceptResponse{Body: req.Body}, nil
			}},
		}}
		h := newHostWithRecords(capabilityRecord{id: "test", plugin: plugin})
		for _, index := range []int{pluginapi.StreamChunkHeaderInitIndex, 0} {
			h.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{ChunkIndex: index, Body: []byte("now"), HistoryChunks: [][]byte{[]byte("before")}})
			want := 1
			if omit && index != pluginapi.StreamChunkHeaderInitIndex {
				want = 0
			}
			if len(got.HistoryChunks) != want {
				t.Fatalf("omit=%v,index=%d,history=%q", omit, index, got.HistoryChunks)
			}
		}
		wire, err := json.Marshal(rpcCapabilitiesFromPlugin(plugin))
		if err != nil {
			t.Fatal(err)
		}
		var decoded rpcCapabilities
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.StreamChunkHistoryOmitted != omit {
			t.Fatalf("capability lost in RPC: %s", wire)
		}
	}
}
