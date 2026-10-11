package usage

import "testing"

func TestRequestSizeCanonical(t *testing.T) {
	tests := []struct {
		name string
		in   RequestSize
		want RequestSize
	}{
		{name: "empty stays unversioned", in: RequestSize{Version: 7}, want: RequestSize{}},
		{
			name: "measured fields set version",
			in: RequestSize{
				ClientRequestBytes:       10,
				ClientRequestEncoding:    " ZSTD ",
				UpstreamTransport:        "HTTP",
				UpstreamRequestBytes:     20,
				UpstreamRequestWireBytes: 8,
				UpstreamRequestEncoding:  "zstd",
				UpstreamResponseBytes:    30,
			},
			want: RequestSize{
				Version:                  RequestSizeVersionV1,
				ClientRequestBytes:       10,
				ClientRequestEncoding:    "zstd",
				UpstreamTransport:        UpstreamTransportHTTP,
				UpstreamRequestBytes:     20,
				UpstreamRequestWireBytes: 8,
				UpstreamRequestEncoding:  "zstd",
				UpstreamResponseBytes:    30,
			},
		},
		{
			name: "negative counts and orphan encodings are cleared",
			in: RequestSize{
				ClientRequestBytes:      -1,
				ClientRequestEncoding:   "gzip",
				UpstreamTransport:       "grpc",
				UpstreamRequestBytes:    -5,
				UpstreamRequestEncoding: "zstd",
				UpstreamResponseBytes:   -2,
			},
			want: RequestSize{},
		},
		{
			name: "transport alone is a measurement",
			in:   RequestSize{UpstreamTransport: "websocket"},
			want: RequestSize{Version: RequestSizeVersionV1, UpstreamTransport: UpstreamTransportWebsocket},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Canonical(); got != tt.want {
				t.Fatalf("Canonical() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCanonicalContentEncoding(t *testing.T) {
	for in, want := range map[string]string{
		"":            "",
		"identity":    "",
		"Gzip":        "gzip",
		"gzip, zstd":  "gzip, zstd",
		"zstd\r\nx":   "",
		"br;q=1":      "",
		"x-custom_v2": "x-custom_v2",
	} {
		if got := CanonicalContentEncoding(in); got != want {
			t.Fatalf("CanonicalContentEncoding(%q) = %q, want %q", in, got, want)
		}
	}
}
