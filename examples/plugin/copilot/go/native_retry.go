package main

import "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

// 空流最多在同一执行内补试一次；usage 在尝试间累计，在最终成功或失败时交给 Core。
// decoder 只在看到正文、推理或工具片段时设置 sawResult，这些片段发出后不重放。
func (r *pluginRuntime) consumeNativeAttempts(req rpcExecutorRequest, exec *nativeExecution, upstream hostHTTPStreamResponse, emit func([]byte) error) (*nativeDecoder, error) {
	var previousUsage pluginapi.AgentUsageV1
	for attempt := 0; ; attempt++ {
		decoder := newNativeDecoder(req.RequestID, req.Model, emit)
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
	add := func(a, b *int64) *int64 {
		if a == nil {
			return b
		}
		if b == nil {
			return a
		}
		value := *a + *b
		return &value
	}
	current.InputTokens = add(previous.InputTokens, current.InputTokens)
	current.OutputTokens = add(previous.OutputTokens, current.OutputTokens)
	current.TotalTokens = add(previous.TotalTokens, current.TotalTokens)
	current.CacheReadTokens = add(previous.CacheReadTokens, current.CacheReadTokens)
	current.CacheCreationTokens = add(previous.CacheCreationTokens, current.CacheCreationTokens)
	current.ReasoningTokens = add(previous.ReasoningTokens, current.ReasoningTokens)
	if chatUsage(current) != nil {
		current.Provenance = "provider_reported_unverified"
	}
	return current
}
