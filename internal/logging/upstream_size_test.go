package logging

import (
	"context"
	"testing"
)

func TestUpstreamRequestSizeHolderIsPerAttempt(t *testing.T) {
	ctx := WithFreshUpstreamRequestSizeHolder(context.Background())
	SetUpstreamRequestSize(ctx, UpstreamRequestSize{Transport: "http", RequestBytes: 10})
	if got, ok := GetUpstreamRequestSize(ctx); !ok || got.RequestBytes != 10 {
		t.Fatalf("GetUpstreamRequestSize() = %+v %v, want stored size", got, ok)
	}
	retry := WithFreshUpstreamRequestSizeHolder(ctx)
	if _, ok := GetUpstreamRequestSize(retry); ok {
		t.Fatal("fresh holder must not inherit the previous attempt")
	}
	SetUpstreamRequestSize(context.Background(), UpstreamRequestSize{RequestBytes: 1})
	if _, ok := GetUpstreamRequestSize(context.Background()); ok {
		t.Fatal("context without holder must not report a size")
	}
}

func TestUpstreamRequestSizeLogFields(t *testing.T) {
	for _, tc := range []struct {
		size UpstreamRequestSize
		want string
	}{
		{UpstreamRequestSize{}, ""},
		{UpstreamRequestSize{Transport: "http", RequestBytes: 1050000, WireBytes: 1050000, ResponseBytes: 91}, "transport=http req_bytes=1050000 resp_bytes=91"},
		{UpstreamRequestSize{Transport: "http", WireBytes: 210000, Encoding: "zstd"}, "transport=http wire_bytes=210000 encoding=zstd"},
		{UpstreamRequestSize{Transport: "websocket", RequestBytes: 900, WireBytes: 900}, "transport=websocket req_bytes=900"},
	} {
		if got := tc.size.LogFields(); got != tc.want {
			t.Fatalf("LogFields(%+v) = %q, want %q", tc.size, got, tc.want)
		}
	}
}
