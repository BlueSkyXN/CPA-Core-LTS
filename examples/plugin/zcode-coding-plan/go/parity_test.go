package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestNodeCryptoParity(t *testing.T) {
	node := os.Getenv("CP_NODE")
	if node == "" {
		t.Skip("Set CP_NODE to Node 24.14.0 for cross-language parity")
	}
	raw, err := os.ReadFile("../testdata/crypto-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		APIKey    string   `json:"api_key"`
		Timestamp string   `json:"timestamp"`
		Nonce     string   `json:"nonce"`
		Session   string   `json:"session_id"`
		Seed      string   `json:"seed_hex"`
		Public    string   `json:"public_hex"`
		Scopes    []string `json:"scopes"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("vector")
	}
	seed, _ := hex.DecodeString(v.Seed)
	key := ed25519.NewKeyFromSeed(seed)
	if hex.EncodeToString(key.Public().(ed25519.PublicKey)) != v.Public {
		t.Fatal("RFC public key mismatch")
	}
	var hs any
	json.Unmarshal(handshakePayload(v.APIKey, v.Timestamp, v.Nonce), &hs)
	expected := map[string]any{"hmac_key": hex.EncodeToString(derive("synthetic-secret", "getSignKey_hmac")), "ed_key": hex.EncodeToString(derive("synthetic-secret", "ed25519_priv")), "handshake": hs, "signature": signMessage(key, "synthetic-key", v.Session, v.Timestamp, v.Nonce), "session": sessionID(v.Scopes[0], v.Scopes[1], v.Scopes[2], v.Scopes[3])}
	out, err := exec.Command(node, "../testdata/crypto-reference.mjs").Output()
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if json.Unmarshal(out, &actual) != nil {
		t.Fatal("Node vector output")
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("Go/Node crypto parity mismatch")
	}
}
