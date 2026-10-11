package helps

import (
	"context"
	"testing"
)

func TestNewChatGPTHTTP2TransportHonorsAcceptEncodingMode(t *testing.T) {
	if newChatGPTHTTP2Transport(context.Background()).DisableCompression {
		t.Fatal("default transport must keep automatic gzip (legacy behavior)")
	}
	if !newChatGPTHTTP2Transport(WithoutAutoAcceptEncoding(context.Background())).DisableCompression {
		t.Fatal("marked context must disable automatic Accept-Encoding")
	}
	if AutoAcceptEncodingDisabled(nil) { //nolint:staticcheck // nil context is part of the contract
		t.Fatal("nil context must report the legacy default")
	}
}
