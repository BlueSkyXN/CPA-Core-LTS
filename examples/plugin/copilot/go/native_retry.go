package main

import "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

// 空流最多在同一执行内补试一次；usage 在尝试间累计，在最终成功或失败时交给 Core。
// decoder 只在看到正文、推理或工具片段时设置 sawResult，这些片段发出后不重放。
func (r *pluginRuntime) consumeNativeAttempts(req rpcExecutorRequest, exec *nativeExecution, upstream hostHTTPStreamResponse, emit func([]byte) error) (*nativeDecoder, error) {
	var previousUsage pluginapi.AgentUsageV1
	for attempt := 0; ; attempt++ {
		decoder := newNativeDecoder(req.RequestID, req.Model, emit)
		if emit != nil {
			decoder.beforeTool = func(index int, tool nativeToolIdentity) { r.trackNativeTool(exec, index, tool) }
			decoder.toolsFinished = func(count int) { r.finishNativeTools(exec, count) }
		}
		err := r.consumeNative(exec, upstream, decoder)
		if err == nil {
			err = decoder.resultError()
		}
		callErr, _ := err.(*pluginCallError)
		if attempt != 0 || decoder.sawResult || exec.isCanceled() || callErr == nil || callErr.code != "empty_upstream_response" {
			decoder.projection.usage = addCopilotUsage(previousUsage, decoder.projection.usage)
			return decoder, err
		}
		previousUsage = addCopilotUsage(previousUsage, decoder.projection.usage)
		exec.closeUpstream(r.caller, false)
		upstream, err = exec.open()
		if err != nil {
			decoder.projection.usage = previousUsage
			return decoder, err
		}
	}
}

func addCopilotUsage(previous, current pluginapi.AgentUsageV1) pluginapi.AgentUsageV1 {
	if chatUsage(previous) == nil {
		return current
	}
	if chatUsage(current) == nil {
		return previous
	}

	// Each attempt contributes its known counts, even if it never reported a total.
	previous = copilotUsageLowerBounds(previous)
	current = copilotUsageLowerBounds(current)
	current.InputTokens = addReportedTokens(previous.InputTokens, current.InputTokens)
	current.OutputTokens = addReportedTokens(previous.OutputTokens, current.OutputTokens)
	current.TotalTokens = addReportedTokens(previous.TotalTokens, current.TotalTokens)
	current.CacheReadTokens = addReportedTokens(previous.CacheReadTokens, current.CacheReadTokens)
	current.CacheCreationTokens = addReportedTokens(previous.CacheCreationTokens, current.CacheCreationTokens)
	current.ReasoningTokens = addReportedTokens(previous.ReasoningTokens, current.ReasoningTokens)
	return current
}

// The input and output buckets contain their respective details. Use only
// disjoint known buckets to form a lower bound for a missing parent counter.
func copilotUsageLowerBounds(usage pluginapi.AgentUsageV1) pluginapi.AgentUsageV1 {
	if usage.InputTokens == nil {
		usage.InputTokens = addReportedTokens(usage.CacheReadTokens, usage.CacheCreationTokens)
	}
	if usage.OutputTokens == nil {
		usage.OutputTokens = usage.ReasoningTokens
	}
	if usage.TotalTokens == nil {
		usage.TotalTokens = addReportedTokens(usage.InputTokens, usage.OutputTokens)
	}
	return usage
}

func addReportedTokens(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}
