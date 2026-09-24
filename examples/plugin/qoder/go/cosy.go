package main

// COSY 目录签名参考 Sliverkiss/cpa-plugin 的 MIT 实现；见 ../THIRD_PARTY_NOTICES.md。
// 只用于模型元数据读取，不引入旧 agent 推理模板或厂商运行时。
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const qoderCatalogPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

func cosyUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func qoderEncode(raw []byte) string {
	encoded := base64.StdEncoding.EncodeToString(raw)
	n := len(encoded) / 3
	encoded = encoded[len(encoded)-n:] + encoded[n:len(encoded)-n] + encoded[:n]
	const normal = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	const custom = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!$"
	result := []byte(encoded)
	for i, c := range result {
		result[i] = custom[strings.IndexByte(normal, c)]
	}
	return string(result)
}

func qoderCatalogHeaders(endpoint string, user map[string]any, uid string, state qoderTokenState) (http.Header, error) {
	return qoderSignedHeaders(endpoint, user, uid, state, "", "")
}

func qoderSignedHeaders(endpoint string, user map[string]any, uid string, state qoderTokenState, encodedBody, modelID string) (http.Header, error) {
	machine, err := cosyUUID()
	if err != nil {
		return nil, err
	}
	id, err := cosyUUID()
	if err != nil {
		return nil, err
	}
	key := []byte(strings.ReplaceAll(id, "-", "")[:16])
	block, _ := pem.Decode([]byte(qoderCatalogPublicKey))
	if block == nil {
		return nil, fmt.Errorf("invalid catalog public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("invalid catalog key type")
	}
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, public, key)
	if err != nil {
		return nil, err
	}
	userType := stringValueFromMap(user, "user_type", "userType")
	identity, _ := json.Marshal(map[string]string{
		"name": stringValueFromMap(user, "name", "username"), "aid": uid, "uid": uid, "yx_uid": "",
		"organization_id": stringValueFromMap(user, "organization_id"), "organization_name": stringValueFromMap(user, "organization_name"),
		"user_type": userType, "security_oauth_token": state.Token, "refresh_token": state.RefreshToken,
	})
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(identity)%aes.BlockSize
	for i := 0; i < pad; i++ {
		identity = append(identity, byte(pad))
	}
	encrypted := make([]byte, len(identity))
	cipher.NewCBCEncrypter(c, key).CryptBlocks(encrypted, identity)
	payload, _ := json.Marshal(map[string]string{"cosyVersion": "0.1.43", "ideVersion": "", "info": base64.StdEncoding.EncodeToString(encrypted), "requestId": id, "version": "v1"})
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	date := strconv.FormatInt(time.Now().Unix(), 10)
	cosyKey, payload64 := base64.StdEncoding.EncodeToString(wrapped), base64.StdEncoding.EncodeToString(payload)
	// 签名的 body 必须与实际发送的字节完全一致；GET 使用空串。
	sum := md5.Sum([]byte(strings.Join([]string{payload64, cosyKey, date, encodedBody, strings.TrimPrefix(u.Path, "/algo")}, "\n")))
	h := make(http.Header)
	for name, value := range map[string]string{
		"Authorization": "Bearer COSY." + payload64 + "." + hex.EncodeToString(sum[:]),
		"Cosy-Key":      cosyKey, "Cosy-Date": date, "Cosy-User": uid,
		"Cosy-Machineid": machine, "Cosy-Machinetoken": base64.RawURLEncoding.EncodeToString([]byte(machine + id)[:50]),
		"Cosy-Machinetype": strings.ReplaceAll(machine, "-", "")[:18], "Cosy-Clienttype": "5",
		"Cosy-Version": "0.1.43", "Cosy-Data-Policy": "AGREE", "Cosy-Clientip": "169.254.198.161",
		"Login-Version": "v2", "Accept": "application/json", "Accept-Encoding": "identity",
		"Content-Type": "application/json", "User-Agent": "Go-http-client/2.0",
	} {
		h.Set(name, value)
	}
	if modelID != "" {
		h.Set("Accept", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Model-Key", modelID)
		h.Set("X-Model-Source", "system")
	}
	return h, nil
}
