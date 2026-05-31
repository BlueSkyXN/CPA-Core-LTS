package signature

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testGPTReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestInspectGPTReasoningSignatureAcceptsTransportShape(t *testing.T) {
	info, err := InspectGPTReasoningSignature(testGPTReasoningSignature())
	if err != nil {
		t.Fatalf("InspectGPTReasoningSignature returned error: %v", err)
	}
	if info.CiphertextLen != 16 {
		t.Fatalf("ciphertext length = %d, want 16", info.CiphertextLen)
	}
}

func TestInspectGPTReasoningSignatureRejectsUnicodeEllipsis(t *testing.T) {
	sig := testGPTReasoningSignature()
	polluted := sig[:20] + string(rune(0x2026)) + sig[20:]

	_, err := InspectGPTReasoningSignature(polluted)
	if err == nil {
		t.Fatal("expected invalid GPT reasoning signature")
	}
	if !strings.Contains(err.Error(), "non-base64url character U+2026") {
		t.Fatalf("error = %q, want U+2026 base64url detail", err.Error())
	}
}

func TestInspectGPTReasoningSignatureRejectsWrongPrefix(t *testing.T) {
	_, err := InspectGPTReasoningSignature("not-a-gpt-signature")
	if err == nil {
		t.Fatal("expected invalid GPT reasoning signature")
	}
	if !strings.Contains(err.Error(), "expected gAAAA prefix") {
		t.Fatalf("error = %q, want prefix detail", err.Error())
	}
}
