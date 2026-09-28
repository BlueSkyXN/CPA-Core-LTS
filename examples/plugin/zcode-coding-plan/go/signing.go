package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func splitKey(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return "", "", problem(400, "invalid_key", "API key must have two non-empty parts")
	}
	return parts[0], parts[1], nil
}
func derive(secret, info string) []byte {
	k, err := hkdf.Key(sha256.New, []byte(secret), []byte("WD_CLIENT_SIGN_KDF_SALT"), info, 32)
	if err != nil {
		panic("HKDF failed")
	}
	return k
}
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b)
}
func handshakePayload(apiKey, ts, nonce string) []byte {
	id, secret, _ := splitKey(apiKey)
	key := derive(secret, "getSignKey_hmac")
	defer clear(key)
	h := hmac.New(sha256.New, key)
	h.Write([]byte("get_sign_key\n" + id + "\n" + ts + "\n" + nonce))
	return encode(struct {
		Key   string `json:"apiKey"`
		Nonce string `json:"nonce"`
		Sig   string `json:"sig"`
		TS    string `json:"ts"`
	}{apiKey, nonce, base64.StdEncoding.EncodeToString(h.Sum(nil)), ts})
}
func decryptKey(apiKey, text string) (ed25519.PrivateKey, error) {
	id, secret, err := splitKey(apiKey)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(data) < 29 {
		return nil, problem(502, "signing_failed", "Invalid signing key response")
	}
	key := derive(secret, "ed25519_priv")
	defer clear(key)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, data[:12], data[12:], []byte(id))
	if err != nil {
		return nil, problem(502, "signing_failed", "Invalid signing key response")
	}
	defer clear(plain)
	der, err := base64.StdEncoding.DecodeString(string(plain))
	if err != nil {
		return nil, problem(502, "signing_failed", "Invalid signing key encoding")
	}
	defer clear(der)
	value, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, problem(502, "signing_failed", "Invalid signing key")
	}
	private, ok := value.(ed25519.PrivateKey)
	if !ok {
		return nil, problem(502, "signing_failed", "Invalid key type")
	}
	return private, nil
}
func signMessage(key ed25519.PrivateKey, id, session, ts, nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(id+"\n"+ts+"\n3.14.3\n"+session+"\n"+nonce)))
}
func pow(ctx context.Context, id, session, ts string) (string, error) {
	h := sha256.Sum256([]byte(id + "\nzcode\n" + session + "\n" + ts))
	seed := hex.EncodeToString(h[:])[:32]
	for counter := uint64(0); counter < 0xffffffff; counter++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate := randomHex(12) + fmt.Sprintf("%08x", counter)
		hash := sha256.Sum256([]byte(seed + "\n" + candidate))
		if hash[0] == 0 {
			return candidate, nil
		}
	}
	return "", problem(502, "signing_failed", "Proof of work exhausted")
}

type handshakeFlight struct {
	done chan struct{}
	key  ed25519.PrivateKey
	err  error
}
type signer struct {
	mu               sync.Mutex
	key              ed25519.PrivateKey
	flight           *handshakeFlight
	apiKey, endpoint string
	closed           bool
}

func (s *signer) close() { s.mu.Lock(); s.closed = true; clear(s.key); s.key = nil; s.mu.Unlock() }
func (s *signer) keyFor(ctx context.Context, handshake func() (ed25519.PrivateKey, error)) (ed25519.PrivateKey, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, problem(503, "unavailable", "Signer closed")
	}
	if s.key != nil {
		key := append(ed25519.PrivateKey(nil), s.key...)
		s.mu.Unlock()
		return key, nil
	}
	flight := s.flight
	if flight == nil {
		flight = &handshakeFlight{done: make(chan struct{})}
		s.flight = flight
		// 发起者同步等待宿主回调，避免 Execute 返回后 callback context 失效。
		s.mu.Unlock()
		key, err := handshake()
		s.mu.Lock()
		if s.closed {
			clear(key)
			err = problem(503, "unavailable", "Signer closed")
		} else if err == nil {
			s.key = key
		}
		flight.key = append(ed25519.PrivateKey(nil), key...)
		flight.err = err
		s.flight = nil
		close(flight.done)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return append(ed25519.PrivateKey(nil), flight.key...), flight.err
	}
}
func (s *signer) handshake(r *pluginRuntime, e *execution) (ed25519.PrivateKey, error) {
	u, _ := url.Parse(s.endpoint)
	u.Path = "/api/paas/c1f3a7e2/v2/client"
	u.RawQuery = ""
	raw, err := r.caller.Call("host.http.do_stream", httpRequest{CallbackID: e.req.CallbackID, Method: "POST", URL: u.String(), Headers: http.Header{"Authorization": []string{s.apiKey}, "Content-Type": []string{"application/json"}}, Body: handshakePayload(s.apiKey, fmt.Sprint(time.Now().UnixMilli()), randomHex(16)), DisableRedirects: true})
	if err != nil {
		return nil, problem(502, "signing_failed", "Signing handshake failed; no model request was sent")
	}
	var response httpStream
	if json.Unmarshal(raw, &response) != nil || response.StreamID == "" {
		return nil, problem(502, "signing_failed", "Invalid signing handshake response")
	}
	if err = r.bind(e, response.StreamID); err != nil {
		return nil, err
	}
	defer r.releaseStream(e, response.StreamID)
	if response.StatusCode != 200 {
		return nil, problem(502, "signing_failed", "Signing handshake rejected")
	}
	body, err := r.readAll(e, response.StreamID, 65536)
	if err != nil {
		return nil, problem(502, "signing_failed", "Invalid signing handshake response")
	}
	var result struct {
		Code int `json:"code"`
		Data struct {
			Cipher string `json:"privateCipher"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code != 200 {
		return nil, problem(502, "signing_failed", "Signing handshake rejected")
	}
	return decryptKey(s.apiKey, result.Data.Cipher)
}
func (s *signer) headers(r *pluginRuntime, e *execution, session string) (http.Header, error) {
	ctx := e.ctx
	key, err := s.keyFor(ctx, func() (ed25519.PrivateKey, error) { return s.handshake(r, e) })
	if err != nil {
		return nil, err
	}
	defer clear(key)
	id, _, _ := splitKey(s.apiKey)
	ts, nonce := fmt.Sprint(time.Now().UnixMilli()), randomHex(16)
	proof, err := pow(ctx, id, session, ts)
	if err != nil {
		return nil, err
	}
	return http.Header{"X-Session-Id": []string{session}, "X-Client-Ts": []string{ts}, "X-Client-Version": []string{"3.14.3"}, "X-Client-Nonce": []string{nonce}, "X-App-Id": []string{"zcode"}, "X-Client-Pow": []string{proof}, "X-Client-Sig": []string{signMessage(key, id, session, ts, nonce)}}, nil
}
func identityHeaders(c *config) http.Header {
	return http.Header{
		"Anthropic-Version": []string{"2023-06-01"}, "Anthropic-Beta": []string{"mid-conversation-system-2026-04-07"}, "Content-Type": []string{"application/json"},
		"User-Agent": []string{"ZCode/3.14.3 ai-sdk/provider-utils/4.0.27 runtime/node.js/24"}, "Http-Referer": []string{"https://zcode.z.ai"},
		"X-Zcode-App-Version": []string{"3.14.3"}, "X-Title": []string{"Z Code@cli"}, "X-Zcode-Agent": []string{"glm"}, "X-Release-Channel": []string{"production"},
		"X-Platform": []string{c.Identity.Platform}, "X-Os-Category": []string{c.Identity.Category}, "X-Os-Version": []string{c.Identity.Version}, "X-Client-Language": []string{c.Identity.Language}, "X-Client-Timezone": []string{c.Identity.Timezone}, "X-Zcode-Session-Type": []string{"main"},
		"Accept-Language": []string{"*"}, "Sec-Fetch-Mode": []string{"cors"}, "Authorization": []string{"Bearer " + c.APIKey}, "X-Api-Key": []string{c.APIKey},
	}
}
