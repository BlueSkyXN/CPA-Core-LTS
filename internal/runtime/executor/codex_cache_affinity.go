package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	authpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	session "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexCacheSource string

const (
	cacheExplicit  codexCacheSource = "explicit"
	cacheProtocol  codexCacheSource = "protocol"
	cacheExecution codexCacheSource = "execution"
	cacheDerived   codexCacheSource = "derived"
	cacheCaller    codexCacheSource = "caller"
	cacheIdentity  codexCacheSource = "identity"
	cachePrefix    codexCacheSource = "prefix"
	cacheTemporary codexCacheSource = "temporary"
)

type codexCacheBase struct {
	key         string
	source      codexCacheSource
	originalPCK string
}

func codexAffinityStrategy(cfg *config.Config) string {
	if cfg == nil {
		return "client-aware"
	}
	return cfg.Codex.CacheAffinity.EffectiveStrategy()
}
func codexAffinityEnabled(cfg *config.Config, auth *authpkg.Auth) bool {
	return auth != nil && auth.AuthKind() == authpkg.AuthKindOAuth && codexAffinityStrategy(cfg) != "legacy"
}
func codexAffinityHeaders(ctx context.Context, headers http.Header) http.Header {
	if headers != nil {
		return headers
	}
	if ctx != nil {
		if g, ok := ctx.Value("gin").(*gin.Context); ok && g != nil && g.Request != nil {
			return g.Request.Header
		}
	}
	return nil
}
func affinityMetadata(req execpkg.Request, key string) string {
	s, _ := req.Metadata[key].(string)
	return strings.TrimSpace(s)
}
func codexAffinityCaller(ctx context.Context, req execpkg.Request) string {
	if s := affinityMetadata(req, execpkg.CallerScopeMetadataKey); s != "" {
		return s
	}
	return session.CallerScope(helps.APIKeyFromContext(ctx))
}
func affinityDigest(parts ...string) string {
	// JSON framing avoids delimiter collisions in opaque identifiers.
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func affinityUUID(parts ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(affinityDigest(append([]string{"cpa:codex:cache-affinity:v1"}, parts...)...))).String()
}

// oauthCacheBase keeps established mappings, but records their origin before
// automatic fields can be confused with an explicit client/config choice.
func oauthCacheBase(ctx context.Context, from translator.Format, req execpkg.Request, body []byte, headers http.Header) (codexCacheBase, []byte, error) {
	b := codexCacheBase{originalPCK: strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())}
	b.key = b.originalPCK
	if b.key == "" {
		b.key = strings.TrimSpace(gjson.GetBytes(req.Payload, "prompt_cache_key").String())
	}
	if b.key != "" {
		b.source = cacheExplicit
	}
	if b.key == "" && sourceFormatEqual(from, translator.FormatClaude) {
		cached, ok, err := helps.ClaudeCodePromptCache(ctx, gjson.GetBytes(body, "model").String(), req.Payload, headers)
		if err != nil {
			return b, nil, err
		}
		if ok {
			b.key, b.source = cached.ID, cacheProtocol
		}
	}
	if b.key == "" {
		b.key = helps.ProviderSessionUUID("codex", req.Metadata)
		if b.key != "" {
			b.source = cacheDerived
			if affinityMetadata(req, execpkg.ExecutionSessionMetadataKey) != "" {
				b.source = cacheExecution
			}
		}
	}
	if b.key == "" && sourceFormatEqual(from, translator.FormatOpenAI) {
		if key := strings.TrimSpace(helps.APIKeyFromContext(ctx)); key != "" {
			b.key, b.source = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+key)).String(), cacheCaller
		}
	}
	if b.key != "" {
		body = helps.SetStringIfDifferent(body, "prompt_cache_key", b.key)
	}
	return b, body, nil
}

// Only fields whose documented meaning is a conversation are accepted. In
// particular canonical_session_id may contain request/user/LCP identities.
func codexAffinityIdentity(headers http.Header, payload []byte) (kind, id, parent string) {
	for _, h := range []string{"X-Session-Id", "X-Thread-Id", "Thread-Id", "X-Conversation-Id", "Conversation-Id"} {
		if v := session.NormalizeExplicitID(headerValueCaseInsensitive(headers, h)); v != "" {
			kind, id = strings.ToLower(h), v
			break
		}
	}
	if id == "" {
		for _, p := range []string{"session_id", "sessionId", "thread_id", "threadId", "conversation_id", "conversation.id", "metadata.session_id", "metadata.thread_id", "metadata.conversation_id"} {
			v := gjson.GetBytes(payload, p)
			if v.Type == gjson.String {
				if s := session.NormalizeExplicitID(v.String()); s != "" {
					kind, id = p, s
					break
				}
			}
		}
	}
	// A parent label or subagent UA is not evidence of ancestry.
	if id != "" {
		for _, p := range []string{"parent_session_id", "parent_thread_id", "parent_conversation_id", "forked_from_thread_id", "metadata.parent_session_id"} {
			value := gjson.GetBytes(payload, p)
			if value.Type == gjson.String {
				if normalized := session.NormalizeExplicitID(value.String()); normalized != "" {
					parent = normalized
					break
				}
			}
		}
	}
	return
}

func codexNativeAffinity(headers http.Header, state codexIdentityConfuseState, explicitPCK string) bool {
	ua := strings.ToLower(headerValueCaseInsensitive(headers, "User-Agent"))
	origin := strings.ToLower(headerValueCaseInsensitive(headers, "Originator"))
	return strings.HasPrefix(ua, "codex") && strings.HasPrefix(origin, "codex") && state.clientMetadata.CanonicalPresent && state.clientMetadata.HasSessionID && codexSessionHeaderValue(headers) != "" && explicitPCK != ""
}

// adaptOAuthCache runs after replay resolution and metadata validation, before
// random/model header shaping. It never changes execution or replay identity.
func (e *CodexExecutor) adaptOAuthCache(ctx context.Context, auth *authpkg.Auth, req execpkg.Request, from translator.Format, endpoint string, original, body []byte, incoming http.Header, base codexCacheBase, state *codexIdentityConfuseState) ([]byte, http.Header) {
	incoming = codexAffinityHeaders(ctx, incoming)
	headers := make(http.Header)
	strategy := codexAffinityStrategy(e.cfg)
	native := codexSessionHeaderValue(incoming)
	pck := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	key, source := base.key, base.source
	if state.promptCacheKey != "" {
		key, pck = state.promptCacheKey, state.promptCacheKey
	}
	if native != "" {
		key, source = native, cacheExplicit
	}
	if state.clientMetadata.HasSessionID && native == "" && base.source != cacheExplicit {
		key, source = state.clientMetadata.SessionID, cacheProtocol
	}
	explicit := base.source == cacheExplicit || native != ""
	// Native root forks deliberately route on pck while metadata names the child.
	originalPCK := strings.TrimSpace(gjson.GetBytes(original, "prompt_cache_key").String())
	if state.clientMetadata.CanonicalPresent && native != "" && native == originalPCK && codexUnambiguousCacheRoute(incoming, original, native) {
		state.cacheRoute = native
	}
	caller := codexAffinityCaller(ctx, req)
	kind, id, parent := codexAffinityIdentity(incoming, original)
	identity := ""
	if caller != "" && id != "" {
		identity = affinityDigest(caller, kind, id)
	}
	stable := ""
	if identity != "" {
		stable = affinityUUID(caller, kind, id)
	}
	reliable := identity != "" || source == cacheProtocol || source == cacheExecution
	if source == cacheProtocol || source == cacheExecution {
		identity = affinityDigest(caller, string(source), key)
	}
	if key == "" && stable != "" {
		key, source = stable, cacheIdentity
	}
	// Keep pck for established mappings in stable-id. In client-aware, derived
	// and caller-wide fallbacks are seeds only, never an explicit cache intent.
	seed := ""
	if strategy == "client-aware" && caller != "" && (base.source == cacheDerived || base.source == cacheCaller) {
		seed = base.key
		if base.originalPCK == "" {
			body, _ = sjson.DeleteBytes(body, "prompt_cache_key")
			pck = ""
		}
		if !explicit {
			key, source = stable, cacheIdentity
		}
	}
	if key == "" {
		requestID := affinityMetadata(req, execpkg.RequestIDMetadataKey)
		if requestID != "" {
			key = affinityUUID(caller, "request", requestID)
		} else {
			key = uuid.NewString()
		}
		source = cacheTemporary
	}
	state.cacheSource = source
	state.cachePCK = pck
	if strategy == "client-aware" && !codexNativeAffinity(incoming, *state, originalPCK) && !explicit && caller != "" && auth.ID != "" && endpoint != "" && gjson.GetBytes(body, "model").String() != "" {
		snap := codexAffinitySnapshotOf(body)
		scope := affinityDigest(caller, endpoint, auth.ID, gjson.GetBytes(body, "model").String())
		parentIdentity := ""
		if parent != "" {
			parentIdentity = affinityDigest(caller, kind, parent)
		}
		requestID := affinityMetadata(req, execpkg.RequestIDMetadataKey)
		if requestID != "" {
			requestID = affinityDigest(caller, requestID)
		}
		decision := e.affinityStore().decide(ctx, scope, identity, parentIdentity, requestID, key, seed, reliable, snap)
		decision.generation = e.affinityGeneration
		key = decision.group
		state.affinity = decision
		if decision.adopted {
			state.cacheSource = cachePrefix
		}
	}
	// stable-id also freezes logical decisions, allowing a hot strategy change
	// to finish the request using its original automatic affinity.
	if state.affinity == nil && !explicit {
		requestID := affinityMetadata(req, execpkg.RequestIDMetadataKey)
		if requestID != "" {
			bindingIdentity := identity
			if caller == "" {
				bindingIdentity = ""
			}
			d := e.affinityStore().decide(ctx, affinityDigest(caller, endpoint, auth.ID), bindingIdentity, "", affinityDigest(caller, requestID), key, "", bindingIdentity != "", codexAffinitySnapshot{})
			d.generation = e.affinityGeneration
			state.affinity = d
			key = d.group
		}
	}
	body, state.cachePCK = state.affinity.freezeFields(body, pck)
	setCodexSessionHeaderCasePreserved(headers, "Session-Id", key)
	return body, headers
}

func codexAffinityRequestMetadata(req execpkg.Request, opts execpkg.Options) execpkg.Request {
	m := make(map[string]any, len(req.Metadata)+3)
	for k, v := range req.Metadata {
		m[k] = v
	}
	for _, k := range []string{execpkg.RequestIDMetadataKey, execpkg.CallerScopeMetadataKey, execpkg.ExecutionSessionMetadataKey} {
		if v, ok := opts.Metadata[k]; ok {
			m[k] = v
		}
	}
	req.Metadata = m
	return req
}

func (s *codexIdentityConfuseState) verifyAffinityHeader(headers http.Header) {
	if s.affinity != nil && s.affinity.group != codexSessionHeaderValue(headers) {
		s.affinity.close()
		s.affinity = nil
	}
}

func codexUnambiguousCacheRoute(headers http.Header, body []byte, key string) bool {
	count := 0
	valid := true
	gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
		if k.String() == "prompt_cache_key" {
			count++
			if v.Type != gjson.String || strings.TrimSpace(v.String()) != key {
				valid = false
			}
		}
		return true
	})
	if count != 1 || !valid {
		return false
	}
	for name, values := range headers {
		if !codexSessionHeaderKey(name) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != key {
				return false
			}
		}
	}
	return true
}

// legacy 只对新逻辑请求恢复旧流程；策略切换前的在途请求仍使用冻结字段。
func (e *CodexExecutor) hasFrozenAffinity(ctx context.Context, auth *authpkg.Auth, req execpkg.Request) bool {
	if auth == nil || auth.AuthKind() != authpkg.AuthKindOAuth || e.affinity == nil {
		return false
	}
	id := affinityMetadata(req, execpkg.RequestIDMetadataKey)
	if id == "" {
		return false
	}
	caller := codexAffinityCaller(ctx, req)
	key := affinityDigest("logical-request", affinityDigest(caller, id))
	s := e.affinityStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(s.now())
	return s.requests[key] != nil
}
