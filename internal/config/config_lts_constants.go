package config

const (
	CodexClientMetadataModeOff                                                   = "off"
	CodexClientMetadataModeRepair                                                = "repair"
	CodexClientMetadataModeStrict                                                = "strict"
	CodexClientMetadataWorkspacePolicyPassthrough                                = "passthrough"
	CodexClientMetadataWorkspacePolicyRedact                                     = "redact"
	CodexClientMetadataWorkspacePolicyDrop                                       = "drop"
	CodexModelFallbackTriggerUsageLimit                                          = "usage-limit"
	CodexModelFallbackTriggerCapacity                                            = "capacity"
	CodexModelFallbackReasoningContinuitySameModelOnly                           = "same-model-only"
	CodexModelFallbackReasoningContinuityContextReset                            = "context-reset"
	CodexRateLimitContinuityDefaultObservationWindowSeconds                      = 30
	CodexRateLimitContinuityDefaultEstablishedSuccessThreshold                   = 2
	CodexRateLimitContinuityDefaultEstablishedSessionTTLSeconds                  = 3600
	CodexAbnormalReasoningRetryActionRetry                                       = "retry"
	CodexAbnormalReasoningRetryActionObserveOnly                                 = "observe-only"
	CodexAbnormalReasoningRetryActionDisabled                                    = "disabled"
	CodexAbnormalReasoningRetryExhaustedBehaviorError                            = "error"
	CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough                      = "pass-through"
	CodexAbnormalReasoningRetryClientUsageAggregationDeliveredOnly               = "delivered-only"
	CodexAbnormalReasoningRetryClientUsageAggregationSum                         = "sum"
	CodexAbnormalReasoningRetryClientUsageAggregationSumWithDeliveredTotal       = "sum-with-delivered-total"
	CodexAbnormalReasoningRetryDeliveryPolicyBestNonSpecial                      = "best-non-special"
	CodexAbnormalReasoningRetryDeliveryPolicyFirstNonSpecial                     = "first-non-special"
	CodexAbnormalReasoningRetryDeliveryPolicyMaxOutput                           = "max-output"
	CodexAbnormalReasoningRetryDeliveryPolicyLatest                              = "latest"
	CodexAbnormalReasoningRetryFallbackPolicyBestSpecial                         = "best-special"
	CodexAbnormalReasoningRetryFallbackPolicyMaxOutputSpecial                    = "max-output-special"
	CodexAbnormalReasoningRetryFallbackPolicyLatestSpecial                       = "latest-special"
	CodexAbnormalReasoningHedgedRetryModeSpeed                                   = "speed"
	CodexAbnormalReasoningHedgedRetryModeQuality                                 = "quality"
	CodexAbnormalReasoningRetryDefaultStreamBufferMaxBytes                 int64 = 16 << 20
)
