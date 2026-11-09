package pluginhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type zcodeSearchReplay struct {
	scope   string
	ttl     time.Duration
	limit   int
	history map[string]string
}

func (r *zcodeSearchReplay) restoreRequest(ctx context.Context, payload []byte) []byte {
	restored := r.restoreItems(ctx, payload, "input")
	if r.scope == "" {
		return restored
	}
	for i, item := range ojson.GetBytes(payload, "input").Array() {
		id := item.Get("id").String()
		if item.Get("type").String() == "web_search_call" && id != "" && !item.Get("results").Exists() {
			if r.history == nil {
				r.history = make(map[string]string)
			}
			r.history[id] = ojson.GetBytes(restored, fmt.Sprintf("input.%d.results", i)).Raw
		}
	}
	return restored
}

// 新结果可能淘汰已恢复的历史，辅助计数不能继续沿用生成开始时的缓存快照。
func (r zcodeSearchReplay) historyUnchanged(ctx context.Context) bool {
	for id, original := range r.history {
		current, found, err := cache.GetClaudeWebSearchResultRequired(ctx, r.scope, id, r.ttl)
		if err != nil || (found && string(current) != original) || (!found && original != "") {
			return false
		}
	}
	return true
}

func (a *executorAdapter) zcodeSearchReplayScope(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options, info *registry.ModelInfo) zcodeSearchReplay {
	if a.provider != "zcode-coding-plan" || cfg == nil || !cfg.Client.Codex.WebSearchResultCache.Enabled ||
		auth == nil || strings.TrimSpace(auth.ID) == "" || info == nil || info.NativeCapabilities == nil ||
		info.NativeCapabilities.WebSearchReplay != registry.NativeWebSearchReplayBigModel {
		return zcodeSearchReplay{}
	}
	session := cache.ResolveReasoningReplaySessionKey(cache.CodexReasoningReplaySessionInput{
		Context: ctx, SourceFormat: tr.FormatOpenAIResponse.String(), RequestPayload: req.Payload, Headers: opts.Headers,
		OptionExecutionSession: executorExecutionSessionID(opts.Metadata), RequestExecutionSession: executorExecutionSessionID(req.Metadata),
	})
	if session == "" {
		return zcodeSearchReplay{}
	}
	caller := executorMetadataID(opts.Metadata, coreexecutor.CallerScopeMetadataKey)
	if caller == "" {
		caller = executorMetadataID(req.Metadata, coreexecutor.CallerScopeMetadataKey)
	}
	// 同时保留 API key 隔离，即使调用方显式指定了相同会话或 caller_scope。
	identity, _ := json.Marshal([]string{a.pluginID, a.provider, auth.ID, req.Model, caller, helps.APIKeyFromContext(ctx), session})
	sum := sha256.Sum256(identity)
	c := cfg.Client.Codex.WebSearchResultCache
	return zcodeSearchReplay{scope: "zcode-codex-search-v1:" + hex.EncodeToString(sum[:]), ttl: c.EffectiveTTL(), limit: c.EffectiveLimit()}
}

func (r zcodeSearchReplay) recordItem(ctx context.Context, item ojson.Result) {
	results := item.Get("results")
	if r.scope == "" || item.Get("type").String() != "web_search_call" || item.Get("id").String() == "" ||
		(!results.IsArray() && !results.IsObject()) || len(results.Raw) > zcodeContextMaxBytes {
		return
	}
	cache.CacheClaudeWebSearchResultBestEffort(ctx, r.scope, item.Get("id").String(), []byte(results.Raw), r.ttl, r.limit)
}

func (r zcodeSearchReplay) recordResponse(ctx context.Context, response []byte) {
	if r.scope == "" {
		return
	}
	for _, item := range ojson.GetBytes(response, "output").Array() {
		r.recordItem(ctx, item)
	}
}

func (r zcodeSearchReplay) recordFrame(ctx context.Context, frame []byte) {
	if r.scope == "" {
		return
	}
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if ojson.GetBytes(data, "type").String() == "response.output_item.done" {
			r.recordItem(ctx, ojson.GetBytes(data, "item"))
		}
	}
}

func (r zcodeSearchReplay) restoreItems(ctx context.Context, payload []byte, path string) []byte {
	if r.scope == "" {
		return payload
	}
	for i, item := range ojson.GetBytes(payload, path).Array() {
		if item.Get("type").String() != "web_search_call" || item.Get("results").Exists() || item.Get("id").String() == "" {
			continue
		}
		results, found, err := cache.GetClaudeWebSearchResultRequired(ctx, r.scope, item.Get("id").String(), r.ttl)
		// Home 失败或缓存未命中沿用缺失结果，不读取本地旧副本。
		if err != nil || !found || !json.Valid(results) {
			continue
		}
		if updated, err := ojson.SetRawBytes(payload, fmt.Sprintf("%s.%d.results", path, i), results); err == nil {
			payload = updated
		}
	}
	return payload
}

func (r zcodeSearchReplay) countOutput(ctx context.Context, output []byte) []byte {
	wrapped, err := ojson.SetRawBytes([]byte(`{}`), "input", output)
	if err != nil {
		return nil
	}
	for i, item := range util.ParseGJSONBytesNoCopy(output).Array() {
		if item.Get("type").String() == "web_search_call" {
			wrapped, _ = ojson.DeleteBytes(wrapped, fmt.Sprintf("input.%d.results", i))
		}
	}
	wrapped = r.restoreItems(ctx, wrapped, "input")
	return []byte(ojson.GetBytes(wrapped, "input").Raw)
}
