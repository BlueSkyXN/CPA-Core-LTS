import { spawn } from "node:child_process";
import { lstatSync, readFileSync, writeFileSync } from "node:fs";
import { access, constants as fsConstants } from "node:fs/promises";

import {
  accessTokenFromEnv,
  jobToken,
  ProcessTransport,
  qodercliAuth,
  query,
  type CanUseTool,
  type ModelInfo,
  type Query,
  type SDKMessage,
  type SDKUserMessage,
  type SpawnedProcess,
  type SpawnOptions,
} from "@qoder-ai/qoder-agent-sdk";

import { isSupportedEndpoint, QoderTokenManager } from "./direct.js";
import { ProtocolError, redactStderr, safeError } from "./protocol.js";
import type {
  ActiveTurn,
  AgentEventType,
  AgentEventV1,
  AuthSpec,
  Correlation,
  FixedPermissionPolicy,
  ModelRecord,
  ModelsParams,
  QoderAdapter,
  QoderTransport,
  StartParams,
} from "./types.js";

type TurnState = {
  params: StartParams;
  emit: (event: AgentEventV1) => Promise<void>;
  sequence: number;
  started: boolean;
  sawPartial: boolean;
  canceled: boolean;
  terminal: boolean;
  permissionFailure?: "permission_denied" | "permission_unsupported";
  toolIndexes: Set<number>;
};

type QoderSDKOptions = {
  openAPIEndpoint?: string;
  openAPIUserAgent?: string;
  requestTimeoutMs?: number;
  fetchImpl?: typeof fetch;
};

type SDKAuthentication = {
  auth: ReturnType<typeof accessTokenFromEnv> | ReturnType<typeof qodercliAuth> | ReturnType<typeof jobToken>;
  spawnQoderCLIProcess?: (options: SpawnOptions) => SpawnedProcess;
};

const SDK_AUTH_PAYLOAD_ENV = "QODER_SDK_AUTH_PAYLOAD_FILE";
const MAX_SDK_AUTH_PAYLOAD_BYTES = 4 * 1024;

class PromptQueue implements AsyncIterable<SDKUserMessage> {
  private values: SDKUserMessage[] = [];
  private waiters: Array<(result: IteratorResult<SDKUserMessage>) => void> = [];
  private ended = false;

  push(value: SDKUserMessage): void {
    if (this.ended) throw new ProtocolError("session_closed", "Qoder session is closed");
    const waiter = this.waiters.shift();
    if (waiter) waiter({ value, done: false });
    else this.values.push(value);
  }

  close(): void {
    this.ended = true;
    for (const waiter of this.waiters.splice(0)) waiter({ value: undefined, done: true });
  }

  [Symbol.asyncIterator](): AsyncIterator<SDKUserMessage> {
    return {
      next: async () => {
        const value = this.values.shift();
        if (value) return { value, done: false };
        if (this.ended) return { value: undefined, done: true };
        return new Promise<IteratorResult<SDKUserMessage>>((resolve) => this.waiters.push(resolve));
      },
    };
  }
}

class SDKSession {
  readonly input = new PromptQueue();
  readonly query: Query;
  current?: TurnState;
  nativeSessionID = "";
  model: string;
  readonly configurationKey: string;
  readonly systemPrompt: string;
  initialized = false;
  authExpired = false;
  closed = false;
  pump: Promise<void>;

  constructor(
    readonly executionSessionID: string,
    params: StartParams,
    cliPath: string,
    cwd: string,
    stderr: (chunk: string) => void,
    canUseTool: CanUseTool,
    onAuthExpired: () => void,
    authentication: SDKAuthentication,
  ) {
    this.model = params.model;
    this.systemPrompt = params.system_prompt?.trim() || "";
    this.configurationKey = sessionConfigurationKey(params);
    this.query = query({
      prompt: this.input,
      options: {
        auth: authentication.auth,
        transport: ProcessTransport.default,
        pathToQoderCLIExecutable: cliPath,
        cwd,
        model: params.model,
        tools: { type: "preset", preset: "qodercli" },
        skills: params.skills,
        allowedTools: params.allowed_tools,
        disallowedTools: params.disallowed_tools,
        mcpServers: params.mcp_servers,
        strictMcpConfig: Boolean(params.mcp_servers && Object.keys(params.mcp_servers).length > 0),
        permissionMode: "dontAsk",
        settingSources: params.setting_sources && params.setting_sources.length > 0 ? params.setting_sources : undefined,
        systemPrompt: this.systemPrompt || undefined,
        includePartialMessages: true,
        canUseTool,
        stderr,
        spawnQoderCLIProcess: authentication.spawnQoderCLIProcess,
        onAuthExpired,
      },
    });
    this.pump = Promise.resolve();
  }
}

export class QoderSDKAdapter implements QoderAdapter {
  readonly transport: QoderTransport = "sdk_cli";
  private readonly sessions = new Map<string, SDKSession>();
  private readonly modelCache = new Map<string, { expires: number; models: ModelRecord[] }>();
  private readonly tokenManager?: QoderTokenManager;

  constructor(
    private readonly cliPath: string,
    private readonly cwd: string,
    options: QoderSDKOptions = {},
  ) {
    if (!cliPath) throw new ProtocolError("cli_path_required", "an explicit Qoder CLI path is required");
    const endpoint = options.openAPIEndpoint?.trim().replace(/\/+$/, "");
    if (endpoint && !isSupportedEndpoint(endpoint)) {
      throw new ProtocolError("sdk_auth_config", "Qoder SDK OpenAPI endpoint must use HTTPS or loopback HTTP");
    }
    if (endpoint) {
      this.tokenManager = new QoderTokenManager(
        endpoint,
        "pat_exchange",
        options.openAPIUserAgent?.trim() || "qoder/1.1.40",
        options.fetchImpl ?? fetch,
        Math.max(1000, options.requestTimeoutMs ?? 30_000),
      );
    }
  }

  async readiness(auth?: AuthSpec): Promise<{ auth_ready: boolean; message: string }> {
    try {
      await access(this.cliPath, fsConstants.X_OK);
    } catch {
      throw new ProtocolError("cli_unavailable", "configured Qoder CLI is not executable");
    }
    if (!auth) return { auth_ready: false, message: "selected Qoder auth was not supplied" };
    if (auth.mode === "pat" || auth.mode === "access_token") {
      if (!auth.env_var || !process.env[auth.env_var]) {
        return { auth_ready: false, message: "configured Qoder token environment source is unavailable" };
      }
      if (auth.mode === "pat" && !this.tokenManager) {
        return { auth_ready: false, message: "Qoder OpenAPI endpoint is required for SDK PAT exchange" };
      }
      const source = auth.mode === "pat" ? "PAT" : "legacy access token";
      return { auth_ready: true, message: `Qoder ${source} source is configured; remote acceptance is checked on execution` };
    }
    return { auth_ready: true, message: "Qoder local CLI profile reuse is configured; remote acceptance is checked on execution" };
  }

  async models(params: ModelsParams): Promise<ModelRecord[]> {
    const cacheKey = params.auth.mode === "local_cli"
      ? `local:${params.auth.profile_id ?? "default"}`
      : `${params.auth.mode}:${params.auth.env_var}`;
    const cached = this.modelCache.get(cacheKey);
    if (cached && cached.expires > Date.now()) return cached.models.map((model) => ({ ...model }));

    const input = new PromptQueue();
    const secrets = this.authSecrets(params.auth);
    const stderr = (chunk: string) => this.writeSafeStderr(chunk, secrets);
    const authentication = this.sdkAuthentication(params.auth, stderr);
    const q = query({
      prompt: input,
      options: {
        auth: authentication.auth,
        transport: ProcessTransport.default,
        pathToQoderCLIExecutable: this.cliPath,
        cwd: this.cwd,
        tools: [],
        permissionMode: "dontAsk",
        settingSources: undefined,
        stderr,
        spawnQoderCLIProcess: authentication.spawnQoderCLIProcess,
      },
    });
    try {
      await q.initializationResult();
      const discovered = await q.getAvailableModels({ fetchStrategy: "live" });
      const models = discovered.map(toModelRecord).filter((model) => model.id !== "");
      const ttl = Math.max(0, Math.min(params.cache_ttl_ms ?? 60_000, 10 * 60_000));
      this.modelCache.set(cacheKey, { expires: Date.now() + ttl, models });
      return models.map((model) => ({ ...model }));
    } catch (error) {
      throw safeError(error);
    } finally {
      input.close();
      await q.close().catch(() => undefined);
    }
  }

  async start(params: StartParams, emit: (event: AgentEventV1) => Promise<void>): Promise<ActiveTurn> {
    if (!params.prompt.trim()) throw new ProtocolError("prompt_required", "Qoder prompt is required");
    if (!params.model.trim()) throw new ProtocolError("model_required", "canonical Qoder model ID is required");
    let session = this.sessions.get(params.execution_session_id);
    if (session?.closed) {
      this.sessions.delete(params.execution_session_id);
      session = undefined;
    }
    if (session?.current) throw new ProtocolError("turn_conflict", "Qoder execution session already has an active turn", true);

    const state: TurnState = {
      params,
      emit,
      sequence: 0,
      started: false,
      sawPartial: false,
      canceled: false,
      terminal: false,
      toolIndexes: new Set<number>(),
    };
    if (!session) {
      const secrets = this.authSecrets(params.auth);
      let created: SDKSession;
      const canUseTool: CanUseTool = async (toolName, input, options) => {
        const active = created.current;
        if (!active) return { behavior: "deny", interrupt: true, message: "No active Qoder turn" };
        return this.resolvePermission(active, toolName, input, options.toolUseID);
      };
      const authentication = this.sdkAuthentication(
        params.auth,
        (chunk) => this.writeSafeStderr(chunk, secrets),
        () => {
          created.authExpired = true;
        },
      );
      created = new SDKSession(
        params.execution_session_id,
        params,
        this.cliPath,
        this.cwd,
        (chunk) => this.writeSafeStderr(chunk, secrets),
        canUseTool,
        () => {
          created.authExpired = true;
        },
        authentication,
      );
      session = created;
      session.current = state;
      session.pump = this.pumpSession(session);
      this.sessions.set(params.execution_session_id, session);
    } else {
      if (session.configurationKey !== sessionConfigurationKey(params)) {
        throw new ProtocolError("session_configuration_changed", "Qoder skill, tool, or MCP configuration changed within an execution session");
      }
      const nextSystemPrompt = params.system_prompt?.trim() || "";
      if (nextSystemPrompt && nextSystemPrompt !== session.systemPrompt) {
        throw new ProtocolError("session_configuration_changed", "Qoder system prompt changed within an execution session");
      }
      if (session.model !== params.model) {
        try {
          await session.query.setModel(params.model);
        } catch (error) {
          throw safeError(error);
        }
        session.model = params.model;
      }
      session.current = state;
      await this.emit(state, "turn.started", {});
      state.started = true;
    }

    session.input.push(userMessage(params));
    const selected = session;
    return {
      cancel: async () => {
        const current = selected.current;
        if (!current || current.params.request_id !== params.request_id) return;
        current.canceled = true;
        await selected.query.interrupt().catch((error) => {
          throw safeError(error);
        });
      },
    };
  }

  async close(executionSessionID: string): Promise<void> {
    const session = this.sessions.get(executionSessionID);
    if (!session) return;
    this.sessions.delete(executionSessionID);
    session.closed = true;
    session.input.close();
    const current = session.current;
    session.current = undefined;
    if (current) {
      await this.emitTerminal(current, "session.closed", "session_closed", "session_closed", "Qoder execution session was closed");
    }
    await session.query.close().catch(() => undefined);
    await session.pump.catch(() => undefined);
  }

  async shutdown(): Promise<void> {
    await Promise.all([...this.sessions.keys()].map((id) => this.close(id)));
  }

  private sdkAuthentication(
    auth: AuthSpec,
    stderr: (chunk: string) => void,
    onAuthExpired?: () => void,
  ): SDKAuthentication {
    if (auth.mode === "local_cli") return { auth: qodercliAuth() };
    if (auth.mode === "access_token") return { auth: accessTokenFromEnv(auth.env_var) };
    if (!this.tokenManager) {
      throw new ProtocolError("sdk_auth_config", "Qoder OpenAPI endpoint is required for SDK PAT exchange");
    }
    const tokenManager = this.tokenManager;
    return {
      auth: jobToken(async (request, options) => ({
        token: await tokenManager.getAccessToken(auth, request.reason, options.signal),
      })),
      spawnQoderCLIProcess: createQoderJobTokenSpawner(stderr, onAuthExpired),
    };
  }

  private async pumpSession(session: SDKSession): Promise<void> {
    try {
      for await (const message of session.query) await this.handleMessage(session, message);
      if (session.current) {
        const current = session.current;
        session.current = undefined;
        if (session.authExpired) {
          await this.emitTerminal(current, "turn.failed", "failed", "auth_expired", "Qoder authentication was rejected");
        } else {
          await this.emitTerminal(current, "turn.failed", "runner_lost", "runner_lost", "Qoder runtime closed unexpectedly", true);
        }
      }
    } catch (error) {
      const current = session.current;
      if (current) {
        session.current = undefined;
        const safe = safeError(error);
        const state = safe.code === "auth_expired" || session.authExpired ? "failed" : "runner_lost";
        const code = session.authExpired ? "auth_expired" : safe.code;
        const message = session.authExpired ? "Qoder authentication was rejected" : safe.message;
        await this.emitTerminal(current, "turn.failed", state, code, message, safe.retryable);
      }
    } finally {
      session.closed = true;
      this.sessions.delete(session.executionSessionID);
    }
  }

  private async handleMessage(session: SDKSession, message: SDKMessage): Promise<void> {
    const current = session.current;
    if (!current) return;
    if (message.type === "system" && message.subtype === "init") {
      session.nativeSessionID = message.session_id;
      if (!session.initialized) {
        session.initialized = true;
        await this.emit(current, "session.created", {
          native_session_id: message.session_id,
          qodercli_version: message.qodercli_version,
          protocol_version: message.protocol_version ?? "legacy",
        });
        await this.emit(current, "turn.started", {});
        current.started = true;
      }
      return;
    }
    if (!current.started) {
      await this.emit(current, "turn.started", {});
      current.started = true;
    }
    if (message.type === "stream_event") {
      current.sawPartial = true;
      await this.handleStreamEvent(current, message.event);
      return;
    }
    if (message.type === "assistant") {
      if (message.error === "authentication_failed") {
        session.current = undefined;
        await this.emitTerminal(current, "turn.failed", "failed", "auth_expired", "Qoder authentication was rejected");
        return;
      }
      if (!current.sawPartial) {
        for (const block of message.message.content) await this.handleContentBlock(current, block);
      }
      return;
    }
    if (message.type === "system" && message.subtype === "permission_denied") {
      current.permissionFailure = "permission_denied";
      await this.emit(current, "warning", { code: "permission_denied", tool_name: message.tool_name });
      return;
    }
    if (message.type === "result") {
      session.current = undefined;
      const input = numberOrUndefined(message.usage?.input_tokens);
      const output = numberOrUndefined(message.usage?.output_tokens);
      if (input !== undefined || output !== undefined) {
        await this.emit(current, "usage.updated", {
          input_tokens: input,
          output_tokens: output,
          total_tokens: input !== undefined && output !== undefined ? input + output : undefined,
          provenance: "provider_reported_unverified",
        });
      }
      if (current.canceled) {
        await this.emitTerminal(current, "turn.cancelled", "cancelled", "request_cancelled", "Qoder turn was cancelled");
      } else if (current.permissionFailure) {
        await this.emitTerminal(current, "turn.failed", current.permissionFailure, current.permissionFailure, "Qoder tool permission was denied");
      } else if (message.subtype === "success" && !message.is_error) {
        if (!current.sawPartial && message.result) await this.emit(current, "message.delta", { text: message.result });
        await this.emitTerminal(current, "turn.completed", "completed");
      } else {
        await this.emitTerminal(current, "turn.failed", "failed", String(message.error_code ?? message.subtype), "Qoder turn failed");
      }
    }
  }

  private async handleStreamEvent(current: TurnState, event: Record<string, unknown>): Promise<void> {
    const eventType = String(event.type ?? "");
    const delta = isRecord(event.delta) ? event.delta : {};
    if (eventType === "content_block_delta") {
      const deltaType = String(delta.type ?? "");
      if (deltaType === "text_delta" && typeof delta.text === "string") {
        await this.emit(current, "message.delta", { text: delta.text });
      } else if (deltaType === "thinking_delta" || deltaType === "reasoning_delta") {
        const text = typeof delta.thinking === "string" ? delta.thinking : typeof delta.text === "string" ? delta.text : "";
        if (text) await this.emit(current, "reasoning.delta", { text });
      } else if (deltaType === "input_json_delta") {
        await this.emit(current, "tool.updated", { index: event.index, partial_json: delta.partial_json ?? "" });
      }
      return;
    }
    if (eventType === "content_block_start" && isRecord(event.content_block)) {
      const block = event.content_block;
      if (block.type === "tool_use") {
        if (typeof event.index === "number") current.toolIndexes.add(event.index);
        await this.emit(current, "tool.started", { tool_call_id: block.id, name: block.name, input: block.input });
      }
      return;
    }
    if (eventType === "content_block_stop") {
      if (typeof event.index === "number" && current.toolIndexes.delete(event.index)) {
        await this.emit(current, "tool.completed", { index: event.index });
      }
    }
  }

  private async handleContentBlock(current: TurnState, block: Record<string, unknown>): Promise<void> {
    if (block.type === "text" && typeof block.text === "string") {
      await this.emit(current, "message.delta", { text: block.text });
    } else if ((block.type === "thinking" || block.type === "reasoning") && typeof block.text === "string") {
      await this.emit(current, "reasoning.delta", { text: block.text });
    } else if (block.type === "tool_use") {
      await this.emit(current, "tool.started", { tool_call_id: block.id, name: block.name, input: block.input });
      await this.emit(current, "tool.completed", { tool_call_id: block.id });
    } else if (block.type === "tool_result") {
      await this.emit(current, "tool.completed", { tool_call_id: block.tool_use_id, content: block.content, is_error: block.is_error });
    }
  }

  private async resolvePermission(
    current: TurnState,
    toolName: string,
    input: Record<string, unknown>,
    permissionID: string,
  ): Promise<{ behavior: "allow"; updatedInput?: Record<string, unknown> } | { behavior: "deny"; message: string; interrupt?: boolean }> {
    const rule = current.params.permission_policy.rules?.find((candidate) => candidate.tool_name === toolName);
    const behavior = rule?.behavior ?? current.params.permission_policy.default;
    await this.emit(current, "permission.required", {
      permission_id: permissionID,
      tool_name: toolName,
      options: [
        { id: "allow", label: "Allow" },
        { id: "deny", label: "Deny" },
      ],
    });
    if (behavior === "allow") return { behavior: "allow" };
    if (behavior === "allow_with_modified_input") {
      if (!rule?.modified_input) {
        current.permissionFailure = "permission_unsupported";
        return { behavior: "deny", interrupt: true, message: "Fixed permission rule has no modified input" };
      }
      return { behavior: "allow", updatedInput: rule.modified_input };
    }
    current.permissionFailure = "permission_denied";
    return { behavior: "deny", interrupt: behavior === "cancel_turn", message: "Denied by CPA fixed Qoder permission policy" };
  }

  private async emit(state: TurnState, type: AgentEventType, payload?: unknown): Promise<void> {
    if (state.terminal && !isTerminalEvent(type)) return;
    state.sequence += 1;
    await state.emit({
      schema_version: 1,
      type,
      request_id: state.params.request_id,
      execution_session_id: state.params.execution_session_id,
      turn_id: state.params.turn_id,
      provider: "qoder",
      auth_id: state.params.auth_id,
      auth_index: state.params.auth_index,
      sequence: state.sequence,
      timestamp: new Date().toISOString(),
      payload,
    });
  }

  private async emitTerminal(
    state: TurnState,
    type: "turn.completed" | "turn.failed" | "turn.cancelled" | "session.closed",
    terminalState: string,
    code?: string,
    message?: string,
    retryable = false,
  ): Promise<void> {
    if (state.terminal) return;
    state.terminal = true;
    await this.emit(state, type, { state: terminalState, code, message, retryable });
  }

  private authSecrets(auth: AuthSpec): string[] {
    return auth.mode === "local_cli" ? [] : [process.env[auth.env_var] ?? ""];
  }

  private writeSafeStderr(chunk: string, secrets: string[]): void {
    const safe = redactStderr(chunk, secrets).trim();
    if (safe) process.stderr.write(`[qodercli] ${safe}\n`);
  }
}

function createQoderJobTokenSpawner(
  stderr: (chunk: string) => void,
  onAuthExpired?: () => void,
): (options: SpawnOptions) => SpawnedProcess {
  let authExpiredFired = false;
  return (options) => {
    patchQoderSDKJobTokenPayload(options.env[SDK_AUTH_PAYLOAD_ENV]);
    const child = spawn(options.command, options.args, {
      cwd: options.cwd,
      env: options.env,
      signal: options.signal,
      stdio: ["pipe", "pipe", "pipe"],
    });
    child.stderr.on("data", (chunk: Buffer | string) => stderr(String(chunk)));
    child.once("exit", (code) => {
      if (code === 41 && !authExpiredFired) {
        authExpiredFired = true;
        onAuthExpired?.();
      }
    });
    return child;
  };
}

export function patchQoderSDKJobTokenPayload(payloadPath: string | undefined): void {
  if (!payloadPath) {
    throw new ProtocolError("sdk_auth_payload_incompatible", "Qoder SDK did not create a host job-token payload");
  }
  let raw: string;
  try {
    const stat = lstatSync(payloadPath);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > MAX_SDK_AUTH_PAYLOAD_BYTES) {
      throw new Error("invalid payload file");
    }
    raw = readFileSync(payloadPath, "utf8");
  } catch {
    throw new ProtocolError("sdk_auth_payload_incompatible", "Qoder SDK host job-token payload is unavailable");
  }
  let payload: Record<string, unknown>;
  try {
    const value = JSON.parse(raw);
    if (!isRecord(value)) throw new Error("object required");
    payload = value;
  } catch {
    throw new ProtocolError("sdk_auth_payload_incompatible", "Qoder SDK host job-token payload is invalid");
  }
  if (payload.type === "jobToken" && payload.jobTokenProvider === "host") return;
  if (payload.type !== "jobToken" || payload.hostTokenCallback !== true) {
    throw new ProtocolError("sdk_auth_payload_incompatible", "Qoder SDK host job-token payload shape is unsupported");
  }
  try {
    writeFileSync(payloadPath, JSON.stringify({ type: "jobToken", jobTokenProvider: "host" }), {
      encoding: "utf8",
      mode: 0o600,
    });
  } catch {
    throw new ProtocolError("sdk_auth_payload_incompatible", "Qoder SDK host job-token payload could not be adapted");
  }
}

export function userMessage(params: StartParams): SDKUserMessage {
  const content = Array.isArray(params.content) && params.content.length > 0
    ? params.content
    : [{ type: "text" as const, text: params.prompt }];
  return {
    type: "user",
    message: { role: "user", content },
    parent_tool_use_id: null,
  };
}

export function toModelRecord(model: ModelInfo): ModelRecord {
  return {
    id: String(model.value || model.modelId || "").trim(),
    display_name: String(model.displayName || model.value || model.modelId || "").trim(),
    description: model.description,
    source: model.source,
    is_default: model.isDefault,
    is_enabled: model.isEnabled,
    is_reasoning: model.isReasoning,
    is_vl: model.isVl,
    max_input_tokens: model.maxInputTokens,
    max_output_tokens: model.maxOutputTokens,
    reasoning_efforts: model.efforts,
    default_reasoning_effort: model.defaultEffort,
    supports_disabled: model.supportsDisabled,
    available_context_windows: model.availableContextWindows,
    default_context_window: model.defaultContextWindow,
  };
}

function sessionConfigurationKey(params: StartParams): string {
  return JSON.stringify({
    skills: params.skills ?? [],
    setting_sources: params.setting_sources ?? [],
    allowed_tools: params.allowed_tools ?? [],
    disallowed_tools: params.disallowed_tools ?? [],
    mcp_servers: params.mcp_servers ?? {},
  });
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function numberOrUndefined(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function isTerminalEvent(type: AgentEventType): boolean {
  return type === "turn.completed" || type === "turn.failed" || type === "turn.cancelled" || type === "session.closed";
}
