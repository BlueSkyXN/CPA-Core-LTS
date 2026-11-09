package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// copilotQuotaSnapshots 是 copilot_internal/user 的 quota_snapshots 原始形状；
// 字段语义以字节级透传为准，这里只读取归一化所需的数值。
type copilotQuotaSnapshots map[string]copilotQuotaSnapshot

type copilotQuotaSnapshot struct {
	Entitlement      float64 `json:"entitlement"`
	QuotaRemaining   float64 `json:"quota_remaining"`
	Remaining        float64 `json:"remaining"`
	PercentRemaining float64 `json:"percent_remaining"`
	Unlimited        bool    `json:"unlimited"`
}

// copilotQuotaGroupOrder 固定展示顺序，避免 map 遍历导致 UI 顺序抖动。
var copilotQuotaGroupOrder = []string{"chat", "completions", "premium_interactions"}

var copilotQuotaGroupLabels = map[string]string{
	"chat":                 "Chat",
	"completions":          "Completions",
	"premium_interactions": "Premium interactions",
}

// fetchQuota 实现 quota.fetch：长 token 直查 copilot_internal/user（复用 summary
// 的缓存与错误语义），把 quota_snapshots 归一化为 QuotaFetchResponse。
func (r *pluginRuntime) fetchQuota(raw []byte) (pluginapi.QuotaFetchResponse, error) {
	var req rpcQuotaFetchRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.QuotaFetchResponse{}, newPluginCallError("invalid_request", "Copilot quota request is invalid", http.StatusBadRequest, false)
	}
	if strings.TrimSpace(req.Provider) != "" && !strings.EqualFold(strings.TrimSpace(req.Provider), pluginIdentifier) {
		return pluginapi.QuotaFetchResponse{}, newPluginCallError("unsupported_provider", "Copilot quota does not cover this provider", http.StatusBadRequest, false)
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return pluginapi.QuotaFetchResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusBadRequest, false)
	}
	cfg := r.loadedConfig()
	// 与 handleSummary 共用 summaryCache：管理端点与 quota.fetch 打同一上游。
	cacheKey := copilotSummaryCacheKey(auth, cfg.GitHubAPIEndpoint, strings.TrimSpace(req.AuthIndex))
	r.mu.Lock()
	epoch := r.generation
	cached, okCached := r.summaryCache[cacheKey]
	r.mu.Unlock()
	if okCached && time.Since(cached.FetchedAt) < copilotSummaryCacheTTL {
		return copilotQuotaResponseFromSummary(cached.Summary), nil
	}
	summary := r.copilotSummary(auth, req.HostCallbackID, cfg)
	if summary.Account.Status != "available" {
		return pluginapi.QuotaFetchResponse{}, newPluginCallError("copilot_quota_unavailable", "Copilot quota upstream is unavailable: "+summary.Account.Status, http.StatusBadGateway, true)
	}
	r.mu.Lock()
	// configure() 会整体重置 summaryCache；旧配置代次的在途结果不再写回新缓存。
	if r.generation == epoch {
		if r.summaryCache == nil {
			r.summaryCache = make(map[string]copilotSummaryCacheEntry)
		}
		r.summaryCache[cacheKey] = copilotSummaryCacheEntry{FetchedAt: time.Now(), Summary: cloneCopilotSummary(summary)}
	}
	r.mu.Unlock()
	return copilotQuotaResponseFromSummary(summary), nil
}

// copilotQuotaResponseFromSummary 把 summary 转成归一化 quota：
// copilot_plan → subscription；quota_snapshots 每组一个 bucket，unlimited 满格；
// entitlement/quota_remaining 数值进 summary 指标行（Panel 侧通用渲染）。
func copilotQuotaResponseFromSummary(summary copilotSummary) pluginapi.QuotaFetchResponse {
	response := pluginapi.QuotaFetchResponse{}
	if plan := strings.TrimSpace(summary.Account.CopilotPlan); plan != "" {
		response.Subscription = &pluginapi.QuotaSubscription{Plan: plan}
	}
	if summary.Account.QuotaResetDate != "" {
		response.Summary = append(response.Summary, pluginapi.QuotaMetric{
			Key: "quota_reset_date", Label: "Quota reset date", Value: 0,
		})
	}
	snapshots := parseCopilotQuotaSnapshots(summary.QuotaSnapshots)
	if len(snapshots) == 0 {
		return response
	}
	group := pluginapi.QuotaGroup{DisplayName: "Copilot usage"}
	for _, key := range copilotQuotaGroupOrder {
		snapshot, ok := snapshots[key]
		if !ok {
			continue
		}
		remainingFraction := snapshot.PercentRemaining / 100
		if remainingFraction < 0 {
			remainingFraction = 0
		}
		if remainingFraction > 1 {
			remainingFraction = 1
		}
		if snapshot.Unlimited {
			remainingFraction = 1
		}
		group.Buckets = append(group.Buckets, pluginapi.QuotaBucket{
			Window:            key,
			RemainingFraction: remainingFraction,
			Description:       copilotQuotaBucketDescription(key, snapshot),
		})
		response.Summary = append(response.Summary, pluginapi.QuotaMetric{
			Key: key + "_remaining", Label: copilotQuotaGroupLabel(key) + " remaining",
			Value: copilotSnapshotRemaining(snapshot), Unit: "requests",
		})
	}
	// 未知 quota_id 也要展示，避免 GitHub 新增分组时静默丢失。
	for key, snapshot := range snapshots {
		if _, known := copilotQuotaGroupLabels[key]; known {
			continue
		}
		remainingFraction := snapshot.PercentRemaining / 100
		if remainingFraction < 0 {
			remainingFraction = 0
		}
		if remainingFraction > 1 {
			remainingFraction = 1
		}
		if snapshot.Unlimited {
			remainingFraction = 1
		}
		group.Buckets = append(group.Buckets, pluginapi.QuotaBucket{
			Window:            key,
			RemainingFraction: remainingFraction,
			Description:       copilotQuotaBucketDescription(key, snapshot),
		})
	}
	if len(group.Buckets) > 0 {
		response.Groups = append(response.Groups, group)
	}
	return response
}

func copilotQuotaGroupLabel(key string) string {
	if label, ok := copilotQuotaGroupLabels[key]; ok {
		return label
	}
	return key
}

// copilotQuotaBucketDescription 输出剩余/总量；unlimited 组只标注 unlimited。
func copilotQuotaBucketDescription(key string, snapshot copilotQuotaSnapshot) string {
	if snapshot.Unlimited {
		return copilotQuotaGroupLabel(key) + ": unlimited"
	}
	remaining := copilotSnapshotRemaining(snapshot)
	if snapshot.Entitlement > 0 {
		return copilotQuotaGroupLabel(key) + ": " +
			trimQuotaFloat(remaining) + " / " + trimQuotaFloat(snapshot.Entitlement)
	}
	return copilotQuotaGroupLabel(key) + ": " + trimQuotaFloat(remaining)
}

func copilotSnapshotRemaining(snapshot copilotQuotaSnapshot) float64 {
	if snapshot.QuotaRemaining > 0 {
		return snapshot.QuotaRemaining
	}
	return snapshot.Remaining
}

func trimQuotaFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func parseCopilotQuotaSnapshots(raw json.RawMessage) copilotQuotaSnapshots {
	if len(raw) == 0 || bytesEqualJSONNull(raw) {
		return nil
	}
	var snapshots copilotQuotaSnapshots
	if errDecode := json.Unmarshal(raw, &snapshots); errDecode != nil || len(snapshots) == 0 {
		return nil
	}
	return snapshots
}
