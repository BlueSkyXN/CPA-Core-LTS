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

// 这里的 API key 是账号文件里的上游凭据，不是客户端访问 CPA 时使用的 key。
// 当前插件只接受 id.secret 形式；签名私钥要另向上游握手获取，不能把 secret 当成私钥。
func splitKey(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return "", "", problem(400, "invalid_key", "API key must have two non-empty parts")
	}
	return parts[0], parts[1], nil
}

// 同一个 secret 派生两把不同用途的密钥：证明有权握手，以及解密上游返回的私钥。
// salt 和 info 是协议约定，不能互换；它们与 ZCode 本地 credentials.json 的加密无关。
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

// 这里的 sig 是申请签名私钥时使用的 HMAC 凭证，不是模型请求的 X-Client-Sig。
// 握手的 ts/nonce 与后续模型请求的 ts/nonce 各自生成，不共用一套值。
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

// privateCipher 的线格式是 base64(12 字节 IV || 密文 || 16 字节认证标签)，AAD 为 id。
// 解出的文本还需 base64 解码才是 PKCS8 私钥；任一认证/解析失败都不能继续发模型请求。
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

// X-Client-Sig 绑定这五段身份参数，不包含聊天正文或 device_id。
// 换行、顺序和版本均是签名消息的一部分；版本必须与 headers 的 X-Client-Version 一致。
func signMessage(key ed25519.PrivateKey, id, session, ts, nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(id+"\n"+ts+"\n3.14.3\n"+session+"\n"+nonce)))
}

// PoW 是独立于签名的小计算题：答案须满足同一 id/session/ts 下的哈希前 8 位为零。
// 它不是私钥；同一题可以有多个有效答案，无需复现另一客户端找到的那个值。
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

// 缓存的是可重复使用的签名私钥，不是某次请求的签名头；同一 signer 的并发首次请求共享一次握手。
// 返回副本供调用方清零，避免破坏缓存；当前没有按时间过期或被上游拒绝后自动刷新的机制。
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

// 握手向当前上游申请私钥，不调用模型，也不从本机 ZCode 提取旧签名。
// 该请求携带完整上游凭据，禁止跟随重定向；失败时不降级为无签名模型请求。
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

// session 由调用方按本次会话提供，并与请求体中的 session_id 保持一致。
// 私钥可以复用；每次模型请求重新生成 ts 和签名 nonce。
// PoW 与签名共用 id/session/ts；PoW 候选串独立生成，不使用签名 nonce。
// 输出头必须携带本次签名和 PoW 实际使用的参数。
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
