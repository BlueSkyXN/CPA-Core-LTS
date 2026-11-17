# 2026-11-17 upstream 分层同步审查

本批 Core 冻结范围：`43806c865da7d47f9252316e95e315ad9f885b84..7df56074b252384d878ec4fca840552c6fb5236e`。按 first-parent 分为三个同步层，逐层记录冲突与验证结果。

## Stage 1：JSON/正则库与提交前 WebSocket failover

- LTS 起点：`11195efd5da6e7076b2e991c1f42e146314cecb7`。
- 上游终点：`508f8708d0239f7369b7a094e0effa1011c36bfd`。
- 合入方式：`git merge --no-ff --log`，保留上游祖先。
- 普通上游变化：使用 moego JSON/regexp，完整 response.create 在尚未向下游提交输出时支持一次换凭据重试。
- 依赖遵循本层 upstream：moego alpha.3 由 router-for-me/moego v0.1.3 提供；三个受影响独立示例模块采用同一 replacement。

### 冲突和 protected delta

共处理 25 个冲突文件。仓库 AGENTS 和三语言商业中立 README 保留 LTS 内容；Go import 冲突取所需依赖并集，去除被替换的标准库 import。示例模块保留有效校验和，并接入同一 moego 版本。

WebSocket 主流程采用上游的尝试循环，同时在每次尝试中恢复 LTS plaintext tool provenance、完整上下文重置门、增量输入凭据绑定、丢失连接时的历史清理和 socket cancellation cause；最终失败保留请求历史回滚和下一轮 transcript replay。auth 选择保留已有 LTS 排除集合，并合入上游 Home 排除列表。

| 保护面 | 结论 | 依据 |
|---|---|---|
| 完整 usage、import/export、usage queue | preserved | usage/management/queue 包测试通过；统计实现及 schema 未删除。 |
| Management、auth/config、Panel asset source | preserved | 既有调用和默认面板源保留，相关变更为序列化 import；Management 包通过。 |
| Flow、auth identity、fallback、stream commitment | adapted | 保留 LTS retry 排除、context reset、provenance 和失败回滚；auth 与 OpenAI handler 全包通过。 |
| 公共 SDK/plugin JSON 类型 | preserved | moego RawMessage、Number、Marshaler 等为标准库别名；pluginhost 与独立插件示例编译测试通过。 |
| AGENTS、商业中立 README | preserved | 相对 LTS 起点无改动。 |

### 下游补丁逐项状态复核

逐项沿用 ledger 的有效状态，本层不删除补丁、不推进退休。`upstream-equivalent` 行均为既有 `removable` 项，不是本轮新宣告等价。下列回归名称用于追溯；实际运行范围见验证小节，未把文件存在当成行为通过。

| 补丁 | 结论 | 本层复核依据 |
|---|---|---|
| `reasoning-effort-policy` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestReasoningEffortConfigV8RoundTrip`。 |
| `responses-effort-summary-independence` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConvertOpenAIResponsesRequestToAntigravity_ReasoningSummaries`。 |
| `codex-model-fallback` | patch-still-required | 在新 WebSocket 尝试循环中保留完整上下文重置门与错误历史回滚。 回归：`TestLoadConfigOptionalCodexModelFallback`。 |
| `codex-rate-limit-continuity` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestLoadConfigOptionalCodexRateLimitContinuity`。 |
| `codex-interactions-service-tier-response` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestConvertInteractionsRequestToCodexNormalizesFastServiceTier`。 |
| `plugin-configured-enable-default` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestParseConfigBytes_PluginInstanceEmptyRawYAML`。 |
| `codex-gpt56-ultra-level` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestParseLevelSuffixUltra`。 |
| `codex-model-header-provider-snapshot` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestResolveCodexModelHeaderProfileUsesCodexProviderRegardlessOfRegistrationOrder`。 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexExecutorFinalizesLunaClientIdentityVersion`。 |
| `codex-client-metadata-privacy` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestNormalizeRequestRepairSanitizesWorkspaceAndRegeneratesProjections`。 |
| `codex-model-not-found-request-scope` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestManager_MarkResult_CodexModelNotFoundDoesNotCooldownAuth`。 |
| `codex-websocket-reader-generation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexWebsocketHandshakeFailureReleasesExecutionSession`。 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexExecutorDropsUnsupportedReasoningSummaryForSpark`。 |
| `xai-explicit-tool-choice-none` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestXAIExecutorPrepareDoesNotInjectUnauthorizedXSearch`。 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestKimiClaudeAuthUsesRequestLocalBaseURL`。 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestShouldCaptureRequestBody`。 |
| `api-request-body-size-limit` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestReadRequestBodyRejectsIdentityBodyOverLimit`。 |
| `object-store-auth-path-containment` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestObjectTokenStoreRejectsAuthPathsOutsideAuthDir`。 |
| `auth-token-private-file-permissions` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestSaveTokenToFileUsesPrivatePermissions`。 |
| `auth-store-list-error-propagation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestGitTokenStoreListReturnsAuthFileErrors`。 |
| `auth-store-metadata-hydration` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestNormalizeAuthPrefix`。 |
| `panel-release-token-isolation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestFetchLatestAssetUsesDedicatedPanelToken`。 |
| `gemini-unknown-method-not-found` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestGeminiHandlerUnknownMethodReturnsNotFound`。 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestManager_CountTokensGenericNotFoundRecordsFailureWithoutCooldown`。 |
| `management-logs-bounded-response` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestGetLogsNoLimitUsesBoundedDefault`。 |
| `transient-eof-cooldown` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestResultErrorFromErrorClassifiesEOFAsTransient`。 |
| `expired-availability-pruning` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestModelAvailabilityExpiredRequiresPastResettableDeadline`。 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesCallsSplitByAssistantText`。 |
| `codex-desktop-tool-overlay` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestRegistryDefinitionsAndFingerprint`。 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | 每次新尝试保留 plaintext provenance；forward 仍携带 plaintextTools。 回归：`TestRewriteCodexMultiAgentV2MessageEncryptionNamespacesAndToolNames`。 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | 保留按凭据清理 plaintext history；其余相关改动为序列化 import。 回归：`TestXAIExecutorPrepareResponsesRequestRecordsPlaintextMultiAgentProvenance`。 |
| `provider-runtime-state-isolation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexExecutorAbnormalReasoningRetry_NonBufferingStreamOverReconstructionCapDoesNotFailAfterDelta`。 |
| `auth-provider-generation-fence` | patch-still-required | 保留原有 generation fence，仅并入尝试排除集合初始化。 回归：`TestManager_ProviderTransitionRejectsStaleExecutionGeneration`。 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestHandlerRewritesLiveCallAndSchedulesOAuth`。 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCountXAIInputTokensExcludesRequestStructure`。 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestXAIWebsocketTerminalReadInvalidationCancelsSaturatedActiveChannel`。 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestManager_MarkResult_XaiBadCredentialsMarksModelUnauthorized`。 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCustomMagicHeaders_Claude`。 |
| `astra-native-responses-controls` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexAstraControlsSurviveNativeTranslation`。 |
| `astra-async-guidance-hotfix` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestAstraGuidanceRefreshOnlyChangesKnownSentence`。 |
| `local-flow-control` | patch-still-required | LTS 排除集合与上游 Home 排除集合取并集；不绕过 Flow ownership。 回归：`TestFlowRejectedConfigKeepsLastGoodManagerPolicyAndRequests`。 |
| `openai-claude-cache-write-accounting` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestConvertCodexResponseToClaude_StreamPreservesCacheWriteUsage`。 |
| `plugin-stream-history-capability-negotiation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestStreamChunkHistoryPolicyByCapability`。 |
| `wsrelay-terminal-frame-preservation` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestSession_Dispatch_TerminalDeliveredWhenBufferFullAndSessionClosing`。 |
| `codex-dotted-collaboration-tool-restoration` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestOptimizeCodexMultiAgentV2RequestSkipsDotPrefixConflict`。 |
| `session-dispatch-context-after-selection` | patch-still-required | 保留原有选择后上下文发布顺序，新增的 Home 排除列表不改变归属。 回归：`TestManagerSessionDispatchIdentity`。 |
| `antigravity-compaction-completion-validation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestAntigravityCompactionRequiresCompleteSummary`。 |
| `gemini-explicit-text-signature-carriers` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConvertGeminiResponseToOpenAIResponses_TrailingCarrierDirectionSurvivesStrippedIDs`。 |
| `antigravity-pool-settings-cache-key` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestAntigravityTransportKeySeparatesPoolSettingsAcrossReload`。 |
| `claude-missing-tool-schema-default` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestConvertClaudeRequestToOpenAI_ToolWithoutInputSchemaDefaultsParameters`。 |
| `amp-session-extraction-priority` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestExtractSessionID_AmpThreadId`。 |
| `antigravity-response-json-schema-dialect-preservation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConvertGeminiRequestToAntigravity_PreservesResponseJsonSchemaDialect`。 |
| `management-auth-refresh-status-redaction` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestRefreshAuthFiles_AllAndSpecific`。 |
| `responses-stream-error-flat-compatibility` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestBuildOpenAIResponsesStreamErrorChunk`。 |
| `responses-tool-output-item-id-disambiguation` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestExtractResponsesCallID`。 |
| `bounded-forced-auth-refresh` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestManager_ForceRefreshAll_WorkersBounded`。 |
| `antigravity-empty-text-stream-block-lifetime` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestConvertAntigravityResponseToClaudeStream_EmptyTextPartKeepsThinkingBlockOpen`。 |
| `responses-same-chunk-reasoning-order` | upstream-equivalent | 既有 removable 状态保留，补丁实现和回归不删除。 回归：`TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ChunkWithContentAndReasoningContent`。 |
| `usage-base-url-credential-redaction` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestSafeBaseURL`。 |
| `claude-legacy-credential-identity-preservation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestManagerLogin_PreservesMatchingLegacyClaudeCredentialIdentity`。 |
| `codex-compat-unknown-signature-rejection` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConvertClaudeRequestToCodexWithCompat_UnknownThinkingSignatures`。 |
| `text-only-relayed-tool-image-guard` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestOpenAICompatExecutorToolResultContentByInputModalities`。 |
| `gemini-server-tool-signature-validation` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestSanitizeGeminiRequestThoughtSignatures_PreservesToolCallAndResponseSignatures`。 |
| `model-list-response-boundary` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestWriteModelListResponsePreservesHeadersAndWriteOutcome`。 |
| `codex-outbound-strategy` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestCodexOutboundDefaultsToFollow`。 |
| `management-proxy-url-probe` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestProxyURLProbe`。 |
| `credential-change-unauthorized-recovery` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCredentialChangeClearsUnauthorizedDeadlineButPreservesQuota`。 |
| `management-auth-sync-error-redaction` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestPatchAuthFileStatusHookErrorReturns500`。 |
| `claude-setup-token-metadata-lock` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestClaudeExecutorPrepareRequestAuthIsRaceFreeOnSharedCredential`。 |
| `daybreak-blue-catalog-guard` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestModelsCatalogRefreshMergesGuardModels`。 |
| `daybreak-blue-access-program-errors` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestIsRequestFaultStructuredIdentifiers`。 |
| `v8-plugin-config-opaque-yaml` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConfigV8OpaquePluginJSONBoundary`。 |
| `translation-failure-request-scoped` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestCodexTranslationFailureDoesNotRotateOrCooldown`。 |
| `openai-compat-image-status-errors` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestOpenAICompatExecutor_ImageNon2xxReturnsStatusError`。 |
| `v8-turn-credential-ambiguity` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConfigV8JSONTURNSecretsRejectAmbiguousRestore`。 |
| `interceptor-path-override-validation` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestValidateRequestPathOverride`。 |
| `apply-patch-final-snapshot-consistency` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestApplyPatchResponsesBridgeRejectsSnapshotExtendingCompletedStream`。 |
| `terminal-unauthorized-same-credential-update` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestManager_Update_SameCredentialsKeepsTerminalUnauthorized`。 |
| `usage-queue-record-outcome` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestUsageQueuePluginOutcomeComesFromRecordNotFinalStatus`。 |
| `v8-yaml-replace-document-comments` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestConfigV8YAMLReplaceKeepsDocumentComments`。 |
| `codex-empty-incomplete-failure-usage` | patch-still-required | 登记的实现文件本层未修改，退休条件未由本层变化满足。 回归：`TestCodexEmptyIncompletePreservesFailureUsage`。 |
| `openai-compat-complete-sse-usage` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestOpenAICompatExecutorChatCompletionUsageObservesCompleteFrames`。 |
| `usage-request-size-fields` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestRequestSizeCanonical`。 |
| `codex-permanent-refresh-failure` | patch-still-required | 涉及文件的既有 LTS 行为保留；本层依赖/import 迁移未满足该项退休条件。 回归：`TestRefreshTokenErrorClassification`。 |

### 验证

- 已通过：OpenAI handler、auth conductor、Management、usage、Redis queue、pluginhost、Gemini Responses translator 的完整包测试。
- 已通过：不限制测试名的 `go test ./internal/translator/openai/openai/responses`。
- 已通过：`scripts/check-lts-contract.sh`（含 registry validator）、server build、`git diff --check`。
- 已通过：claude-web-search-router、codex-service-tier、realtime-openai-go 三个独立模块的 `go test ./...`。
- 全仓 `go test ./...` 和跨平台检查交由本层 PR CI 回读；此记录生成时尚未运行完成。
- 本层不部署 HF Space，不执行真实账号登录或真实模型请求。


## Stage 2：payload.replace 与请求/流式错误边界

- 直接下层：Stage 1 `8f153189922e6117843dc5e1252aacc968a9f4c3`。
- upstream 范围：`508f8708d0239f7369b7a094e0effa1011c36bfd..4ed882f14d1e672ae5015c1a1814d06e3e72651f`。
- 合入方式：merge commit，保留上游祖先；处理 12 个文本冲突。

新增 `requests.payload.replace`，按有序规则替换指定 user 消息文字，支持空 replacement 删除。执行顺序为 override → replace → filter。保留 LTS 的 model scope、auth kind 和 request path 条件；图片、工具和 thinking 不作为替换对象。

### Protected delta review

- config 示例仅补入 replace；现有 LTS 默认、兼容项和注释保留。YAML empty string 与替换顺序按上游 parser 处理。
- Claude finalizer 传入选中 auth；剥离缓存选项后才执行用户规则，最终请求再应用 LTS effort policy，避免后续 Doc 序列化覆盖结果。
- 新 policy/bio 错误与 LTS access-program 错误取并集；Codex 状态保留 model fallback reason、rate-limit class 和 credential scope。对原 JSON object 更新分类字段，保留嵌套 body.error。
- SSE legacy error chunk 保留顶层 code/message；新的 response.failed 事件采用上游结构。failure usage、replay cache 清理与日志路径保留。
- Claude refresh_token_expired 进入 terminal sign-in 路径；Codex permanent refresh backoff 和 Claude generic invalid_grant 重试策略保留。
- xAI transport retry 测试经 LTS deferred logging 的快照接口断言两次发送使用同一 final frame；不更换生产日志容器。
- 本层不退休 Stage 1 列出的 84 个有效 ledger 项；涉及的 reasoning-effort-policy、payload execution scope、stream error compatibility、permanent refresh failure 等继续保留原有回归。

### 验证

- `go test -json ./...`：113 个有测试的包全部通过。
- payload 各规则种类（含 replace）的 auth/path 条件、wrapped WS error、xAI retry、Claude/Codex refresh 定向测试通过。
- `scripts/check-lts-contract.sh`（含 registry）、server build、`git diff --check` 通过。
- Stage 1 PR #371 的 9 项云检查已全绿；Stage 2 云检查由本层 PR 创建后回读。


## Stage 3：有界 plugin continuity、Claude Thread 与刷新退避

- 直接下层：Stage 2；初始 merge parent 为 `57c776424fa7c4f7c6996c06186c2226b4898701`，后续承接该层的 WebSocket 夹具修复。
- upstream 范围：`4ed882f14d1e672ae5015c1a1814d06e3e72651f..7df56074b252384d878ec4fca840552c6fb5236e`。
- 6 个冲突文件，保留 merge ancestry。
- moego 跟随上游升级到 alpha.4；11 个引用根模块的独立插件示例同步模块图，去除过时 replacement。

### Protected delta review

新增可选 SchedulerContinuity 能力及 `scheduler.continuity` ABI 方法，不修改既有方法签名或 schema version。仅在已启用该能力的 Codex scheduler 上工作；Home 与强制 upstream WebSocket 不进入此等待路径。插件返回 ready/wait/disabled；host 将轮询间隔限制在 10ms–5s，默认 500ms，等待预算默认 30s、最多 120s，同一请求可缩短但不可延长预算。候选使用脱敏属性，不携带凭据文档。

- LTS 的真实执行循环位于 `conductor_lts.go`。continuity 门移植到该循环，保留 Flow admission、异常推理 retry/hedge、usage accumulator、模型回退和 Home 分支；不以 upstream Execute 全体覆盖 LTS wrapper。
- 同一请求绑定一个 continuity session；预算终止同时阻止外层模型回退和 stream bootstrap 重新派发。SSE/WS 仅在真实 continuity 等待期间发送新增 keepalive；首次输出后的重试边界保持。
- RequestID 和 TraceID 各自保留；WebSocket turn 使用独立 session，并保留 LTS plaintext tool provenance、reset gate、上下文 cause、历史回滚。
- continuity 候选同时遵守 LTS ExcludeAuthIDs 与 Home 排除集合。provider 更换时清除新增 quota/non-quota 冷却分类，防止继承上个 provider 的等待资格。
- Claude Thread owner 与 replay error 采用上游语义；重复 Home cooldown 的流式返回亦按 pinned thread 处理。
- bootstrap 二次选择无凭据时，保留前一次真实 429，避免改写成泛化 503。
- plugin 自行声明正值 RetryAfter 时仍保留原 header；内部模型 cooldown 接受上游 10s 下限。相应回归同时验证 header=7s 和内部 cooldown 下限，不把二者混成一个值。
- LTS modelFallbackReason、credentialRetryAfter、generation fence、统计归属与默认 Panel release source 保留；此前 ledger 项不退休。

### 验证

- 全仓 113 个有测试的包通过；auth、OpenAI handler、pluginhost 的 race 全包通过。
- 新增 LTS continuity 集成回归：流式/非流式终止后不进入模型回退、排除凭据不进入候选、provider 更换清除新增冷却分类；最终改动后相关 continuity race 测试通过。
- 11 个独立插件示例模块 `go test ./...` 通过。
- LTS contract/registry、server build、diff check 通过；最终 SHA 的跨平台与全量检查继续由 PR CI 回读。
- Stage 2 云观察位暴露 ManyToManyFullDuplexMultiplexing 在首份列表到齐前启动断言；采用现有 waitServersVisible 等待完整拓扑，再读取快照断言数量。生产 wstunnel 未变，定向 race 50 次及全包通过。
