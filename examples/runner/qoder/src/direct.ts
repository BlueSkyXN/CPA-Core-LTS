import { ProtocolError } from "./protocol.js";
import type {
  ActiveTurn,
  AgentEventType,
  AgentEventV1,
  AuthSpec,
  ModelRecord,
  ModelsParams,
  QoderAdapter,
  QoderTransport,
  StartParams,
} from "./types.js";

const DEFAULT_REQUEST_TIMEOUT_MS = 10 * 60 * 1000;
const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;
const MAX_ERROR_BYTES = 8 * 1024;

export type DirectTokenMode = "auto" | "bearer" | "pat_exchange";

type DirectOptions = {
  endpoint: string;
  modelsEndpoint?: string;
  authEndpoint?: string;
  tokenMode?: DirectTokenMode;
  openAPIEndpoint?: string;
  openAPIUserAgent?: string;
  models: ModelRecord[];
  requestTimeoutMs?: number;
  fetchImpl?: typeof fetch;
};

type TokenState = {
  source: string;
  accessToken: string;
  refreshToken?: string;
  expiresAt: number;
  patExchange: boolean;
};

type DirectSession = {
  executionSessionID: string;
  nativeSessionID: string;
  current?: DirectTurn;
  closed: boolean;
};

type DirectTurn = {
  session: DirectSession;
  params: StartParams;
  emit: (event: AgentEventV1) => Promise<void>;
  controller: AbortController;
  sequence: number;
  canceled: boolean;
  terminal: boolean;
  finishReason?: string;
  toolCalls: Map<number, { id?: string; name?: string }>;
};

type DirectChunk = {
  choices?: unknown;
  usage?: unknown;
};

export class DirectOpenAIAdapter implements QoderAdapter {
  readonly transport: QoderTransport = "direct_openai";
  private readonly sessions = new Map<string, DirectSession>();
  private readonly tokenManager: QoderTokenManager;
  private readonly fetchImpl: typeof fetch;
  private readonly requestTimeoutMs: number;
  private readonly modelsConfig: ModelRecord[];
  private discoveredModels?: ModelRecord[];
  private readonly authEndpoint?: string;
  private readonly tokenMode: DirectTokenMode;
  private readonly openAPIUserAgent: string;

  constructor(private readonly options: DirectOptions) {
    const openAPIEndpoint = options.openAPIEndpoint?.replace(/\/+$/, "");
    const legacyAuthEndpoint = options.authEndpoint?.replace(/\/+$/, "");
    if (openAPIEndpoint && legacyAuthEndpoint && openAPIEndpoint !== legacyAuthEndpoint) {
      throw new ProtocolError("direct_auth_config", "openapi endpoint and direct auth endpoint must match");
    }
    const authEndpoint = openAPIEndpoint || legacyAuthEndpoint;
    this.authEndpoint = authEndpoint;
    this.tokenMode = options.tokenMode ?? "auto";
    this.openAPIUserAgent = options.openAPIUserAgent?.trim() || "qoder/1.1.40";
    if (!isSupportedEndpoint(options.endpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "direct endpoint must use HTTPS or loopback HTTP");
    }
    if (options.modelsEndpoint && !isSupportedEndpoint(options.modelsEndpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "direct models endpoint must use HTTPS or loopback HTTP");
    }
    if (options.authEndpoint && !isSupportedEndpoint(options.authEndpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "direct auth endpoint must use HTTPS or loopback HTTP");
    }
    if (options.openAPIEndpoint && !isSupportedEndpoint(options.openAPIEndpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "openapi endpoint must use HTTPS or loopback HTTP");
    }
    if (this.tokenMode === "pat_exchange" && !authEndpoint) {
      throw new ProtocolError("direct_auth_config", "direct PAT exchange requires an auth endpoint");
    }
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.requestTimeoutMs = Math.max(1000, options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS);
    this.modelsConfig = validateModelRecords(options.models);
    this.tokenManager = new QoderTokenManager(
      authEndpoint,
      this.tokenMode,
      this.openAPIUserAgent,
      this.fetchImpl,
      this.requestTimeoutMs,
    );
  }

  async readiness(auth?: AuthSpec): Promise<{ auth_ready: boolean; message: string }> {
    if (!isSupportedEndpoint(this.options.endpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "direct endpoint is invalid");
    }
    if (!auth) return { auth_ready: false, message: "selected Qoder direct auth was not supplied" };
    if (auth.mode === "local_cli") {
      return { auth_ready: false, message: "direct_openai requires a PAT or bearer access-token auth" };
    }
    const source = process.env[auth.env_var] ?? "";
    if (!source) return { auth_ready: false, message: "configured Qoder direct token source is unavailable" };
    const requiresExchange = auth.mode === "pat" && (this.tokenMode === "pat_exchange" || this.tokenMode === "auto" && source.startsWith("pt-"));
    if (requiresExchange && !this.authEndpoint) return { auth_ready: false, message: "Qoder OpenAPI endpoint is not configured" };
    return { auth_ready: true, message: "Qoder direct OpenAI transport is configured; remote acceptance is checked on execution" };
  }

  async models(params: ModelsParams, signal?: AbortSignal): Promise<ModelRecord[]> {
    const configured = parseModelJSON(params.models_json);
    if (configured.length > 0) {
      this.discoveredModels = cloneModels(configured);
      return cloneModels(configured);
    }
    if (this.modelsConfig.length > 0) {
      this.discoveredModels = cloneModels(this.modelsConfig);
      return cloneModels(this.modelsConfig);
    }
    const endpoint = params.models_endpoint || this.options.modelsEndpoint;
    if (!endpoint) {
      throw new ProtocolError("models_unavailable", "direct_openai requires direct_models or direct_models_endpoint", true);
    }
    if (!isSupportedEndpoint(endpoint)) {
      throw new ProtocolError("direct_endpoint_invalid", "direct models endpoint must use HTTPS or loopback HTTP");
    }
    const auth = params.auth;
    const response = await this.tokenManager.fetchWithAuth(auth, async (token) => {
      return this.fetchWithTimeout(endpoint, {
        method: "GET",
        signal,
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${token}`,
        },
      });
    }, signal);
    if (!response.ok) throw await directHTTPError(response, "direct model discovery");
    const raw = await readResponseText(response, MAX_RESPONSE_BYTES);
    const models = parseModelResponse(raw);
    this.discoveredModels = cloneModels(models);
    return models;
  }

  async start(params: StartParams, emit: (event: AgentEventV1) => Promise<void>): Promise<ActiveTurn> {
    if (!params.chat_request || !isRecord(params.chat_request)) {
      throw new ProtocolError("direct_request_missing", "direct_openai requires the original Chat request");
    }
    // The event protocol projects exactly one assistant choice. Reject n>1
    // before creating a session rather than merging independent completions.
    if (params.chat_request.n != null && params.chat_request.n !== 1) {
      throw new ProtocolError("invalid_request", "Qoder direct transport supports only n=1");
    }
    const existing = this.sessions.get(params.execution_session_id);
    const session = existing ?? {
      executionSessionID: params.execution_session_id,
      nativeSessionID: `direct-${params.execution_session_id}`,
      closed: false,
    };
    if (session.closed) throw new ProtocolError("session_closed", "Qoder direct execution session is closed");
    if (session.current) throw new ProtocolError("turn_conflict", "Qoder direct execution session already has an active turn", true);
    this.sessions.set(params.execution_session_id, session);

    const turn: DirectTurn = {
      session,
      params,
      emit,
      controller: new AbortController(),
      sequence: 0,
      canceled: false,
      terminal: false,
      toolCalls: new Map(),
    };
    session.current = turn;
    if (!existing) await this.emit(turn, "session.created", { native_session_id: session.nativeSessionID, transport: this.transport });
    await this.emit(turn, "turn.started", { transport: this.transport });
    void this.runTurn(turn);
    return {
      cancel: async () => {
        if (session.current !== turn || turn.terminal) return;
        turn.canceled = true;
        turn.controller.abort();
      },
    };
  }

  async close(executionSessionID: string): Promise<void> {
    const session = this.sessions.get(executionSessionID);
    if (!session) return;
    session.closed = true;
    this.sessions.delete(executionSessionID);
    const current = session.current;
    if (!current) return;
    current.canceled = true;
    current.controller.abort();
    session.current = undefined;
    await this.emitTerminal(current, "session.closed", "session_closed", "session_closed", "Qoder direct execution session was closed");
  }

  async shutdown(): Promise<void> {
    await Promise.all([...this.sessions.keys()].map((id) => this.close(id)));
  }

  private async runTurn(turn: DirectTurn): Promise<void> {
    let timedOut = false;
    const turnTimer = setTimeout(() => {
      timedOut = true;
      turn.controller.abort();
    }, this.requestTimeoutMs);
    try {
      await this.ensureModel(turn.params, turn.controller.signal);
      if (turn.canceled || turn.session.closed || turn.controller.signal.aborted) {
        if (timedOut) throw new ProtocolError("direct_timeout", "Qoder direct turn exceeded the configured timeout", true);
        await this.emitTerminal(turn, "turn.cancelled", "cancelled", "request_cancelled", "Qoder direct turn was cancelled");
        return;
      }
      const request = buildDirectRequest(turn.params);
      const rawBody = JSON.stringify(request);
      if (Buffer.byteLength(rawBody) > MAX_RESPONSE_BYTES) {
        throw new ProtocolError("direct_request_too_large", "Qoder direct request exceeds the bounded body limit");
      }
      const response = await this.tokenManager.fetchWithAuth(turn.params.auth, async (token) => {
        return this.fetchWithTimeout(this.options.endpoint, {
          method: "POST",
          headers: {
            Accept: "text/event-stream",
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
            "X-Request-ID": turn.params.request_id,
            "X-Session-ID": turn.params.execution_session_id,
          },
          body: rawBody,
          signal: turn.controller.signal,
        });
      }, turn.controller.signal);
      if (!response.ok) throw await directHTTPError(response, "Qoder direct inference");
      const contentType = response.headers.get("content-type")?.toLowerCase() ?? "";
      if (contentType.includes("application/json") && !contentType.includes("text/event-stream")) {
        await this.consumeJSONResponse(turn, await readResponseText(response, MAX_RESPONSE_BYTES));
      } else {
        await this.consumeSSE(turn, response);
      }
      if (turn.terminal) return;
      if (turn.canceled || turn.session.closed) {
        await this.emitTerminal(turn, "turn.cancelled", "cancelled", "request_cancelled", "Qoder direct turn was cancelled");
      } else if (timedOut) {
        await this.emitTerminal(turn, "turn.failed", "failed", "direct_timeout", "Qoder direct turn exceeded the configured timeout", true);
      } else {
        throw new ProtocolError("stream_truncated", "Qoder direct stream ended before [DONE]", true);
      }
    } catch (error) {
      if (turn.terminal) return;
      if (turn.canceled || turn.session.closed) {
        await this.emitTerminal(turn, "turn.cancelled", "cancelled", "request_cancelled", "Qoder direct turn was cancelled");
      } else if (timedOut) {
        await this.emitTerminal(turn, "turn.failed", "failed", "direct_timeout", "Qoder direct turn exceeded the configured timeout", true);
      } else {
        const safe = error instanceof ProtocolError
          ? error
          : new ProtocolError("connection_lifecycle", "Qoder direct connection failed", true);
        await this.emitTerminal(turn, "turn.failed", "failed", safe.code, safe.message, safe.retryable);
      }
    } finally {
      clearTimeout(turnTimer);
      if (turn.session.current === turn) turn.session.current = undefined;
    }
  }

  private async ensureModel(params: StartParams, signal: AbortSignal): Promise<void> {
    const catalog = this.discoveredModels ?? (this.modelsConfig.length > 0 ? this.modelsConfig : await this.models({ auth: params.auth }, signal));
    if (!catalog.some((model) => model.id === params.model)) {
      throw new ProtocolError("unsupported_model", "Qoder direct model must match an exact catalog ID");
    }
  }

  private async consumeSSE(turn: DirectTurn, response: Response): Promise<void> {
    if (!response.body) throw new ProtocolError("stream_truncated", "Qoder direct response has no body", true);
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let bytes = 0;
    let doneReceived = false;
    try {
      while (!doneReceived) {
        if (turn.canceled || turn.session.closed) {
          await reader.cancel();
          return;
        }
        const next = await reader.read();
        if (next.done) break;
        bytes += next.value.byteLength;
        if (bytes > MAX_RESPONSE_BYTES) throw new ProtocolError("stream_too_large", "Qoder direct response exceeds the bounded limit");
        buffer += decoder.decode(next.value, { stream: true });
        const lines = buffer.split(/\r?\n/);
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          if (!line.startsWith("data:")) continue;
          const result = await this.consumeDataLine(turn, line.slice(5).trim());
          if (result) {
            doneReceived = true;
            break;
          }
        }
      }
      if (!doneReceived && buffer.trim().startsWith("data:")) {
        doneReceived = await this.consumeDataLine(turn, buffer.trim().slice(5).trim());
      }
    } finally {
      await reader.cancel().catch(() => undefined);
      reader.releaseLock();
    }
    if (!doneReceived && !turn.canceled && !turn.session.closed) {
      throw new ProtocolError("stream_truncated", "Qoder direct stream ended before [DONE]", true);
    }
  }

  private async consumeJSONResponse(turn: DirectTurn, raw: string): Promise<void> {
    let value: unknown;
    try {
      value = JSON.parse(raw);
    } catch {
      throw new ProtocolError("invalid_upstream_response", "Qoder direct response was not valid JSON");
    }
    if (isRecord(value) && isRecord(value.error)) {
      throw new ProtocolError("direct_upstream_error", "Qoder direct response reported an upstream error", true);
    }
    await this.consumeChunk(turn, isRecord(value) ? value : {});
    if (!turn.canceled && !turn.session.closed) {
      await this.emitTerminal(turn, "turn.completed", "completed");
    }
  }

  private async consumeDataLine(turn: DirectTurn, data: string): Promise<boolean> {
    if (!data) return false;
    if (data === "[DONE]") {
      if (!turn.canceled && !turn.session.closed) {
        await this.emitTerminal(turn, "turn.completed", "completed");
      }
      return true;
    }
    let value: unknown;
    try {
      value = JSON.parse(data);
    } catch {
      throw new ProtocolError("invalid_upstream_response", "Qoder direct SSE data was not valid JSON");
    }
    if (!isRecord(value)) return false;
    if (isRecord(value.error)) {
      throw new ProtocolError("direct_upstream_error", "Qoder direct stream reported an upstream error", true);
    }
    let chunk: Record<string, unknown> = value;
    if (typeof value.body === "string") {
      if (value.body.trim() === "[DONE]") return this.consumeDataLine(turn, "[DONE]");
      try {
        const nested = JSON.parse(value.body);
        if (isRecord(nested)) chunk = nested;
      } catch {
        throw new ProtocolError("invalid_upstream_response", "Qoder direct nested SSE body was not valid JSON");
      }
    }
    if (isRecord(chunk.error)) {
      throw new ProtocolError("direct_upstream_error", "Qoder direct stream reported an upstream error", true);
    }
    await this.consumeChunk(turn, chunk);
    return false;
  }

  private async consumeChunk(turn: DirectTurn, chunk: DirectChunk): Promise<void> {
    const usage = isRecord(chunk.usage) ? chunk.usage : isRecord((chunk as Record<string, unknown>).raw_usage) ? (chunk as Record<string, any>).raw_usage : undefined;
    if (usage) {
      const input = numberOrUndefined(usage.input_tokens ?? usage.prompt_tokens);
      const output = numberOrUndefined(usage.output_tokens ?? usage.completion_tokens);
      const total = numberOrUndefined(usage.total_tokens) ?? (input !== undefined && output !== undefined ? input + output : undefined);
      if (input !== undefined || output !== undefined || total !== undefined) {
        await this.emit(turn, "usage.updated", {
          input_tokens: input,
          output_tokens: output,
          total_tokens: total,
          provenance: "provider_reported_unverified",
        });
      }
    }
    if (!Array.isArray(chunk.choices)) return;
    for (const rawChoice of chunk.choices) {
      if (!isRecord(rawChoice)) continue;
      if (rawChoice.index !== undefined && rawChoice.index !== 0) {
        throw new ProtocolError("invalid_upstream_response", "Qoder direct transport received multiple choices");
      }
      if (typeof rawChoice.finish_reason === "string" && rawChoice.finish_reason.trim()) {
        turn.finishReason = rawChoice.finish_reason;
      }
      const delta = isRecord(rawChoice.delta) ? rawChoice.delta : isRecord(rawChoice.message) ? rawChoice.message : {};
      const content = typeof delta.content === "string" ? delta.content : "";
      const reasoning = typeof delta.reasoning_content === "string"
        ? delta.reasoning_content
        : typeof delta.reasoning === "string" ? delta.reasoning : "";
      if (content) {
        await this.emit(turn, "message.delta", { text: content });
      }
      if (reasoning) {
        await this.emit(turn, "reasoning.delta", { text: reasoning });
      }
      if (Array.isArray(delta.tool_calls)) await this.consumeToolCalls(turn, delta.tool_calls);
    }
  }

  private async consumeToolCalls(turn: DirectTurn, calls: unknown[]): Promise<void> {
    for (const rawCall of calls) {
      if (!isRecord(rawCall)) continue;
      const index = numberOrUndefined(rawCall.index) ?? turn.toolCalls.size;
      const fn = isRecord(rawCall.function) ? rawCall.function : {};
      const previous = turn.toolCalls.get(index);
      if (!previous) {
        const entry = { id: stringOrUndefined(rawCall.id), name: stringOrUndefined(fn.name) };
        turn.toolCalls.set(index, entry);
        await this.emit(turn, "tool.started", { index, tool_call_id: entry.id, name: entry.name });
      }
      // IDs/names may arrive after the first delta. Forward their first value
      // exactly once; otherwise OpenAI stream clients concatenate repeated names.
      const newID = previous && !previous.id ? stringOrUndefined(rawCall.id) : undefined;
      const newName = previous && !previous.name ? stringOrUndefined(fn.name) : undefined;
      if (previous) {
        if (newID) previous.id = newID;
        if (newName) previous.name = newName;
      }
      // Fragments can end inside a JSON string. Trimming changes tool arguments.
      const argumentsPart = typeof fn.arguments === "string" ? fn.arguments : undefined;
      if (argumentsPart || newID || newName) await this.emit(turn, "tool.updated", {
        index, partial_json: argumentsPart ?? "", tool_call_id: newID, name: newName,
      });
    }
  }

  private async emit(turn: DirectTurn, type: AgentEventType, payload?: unknown): Promise<void> {
    if (turn.terminal && !isTerminal(type)) return;
    turn.sequence += 1;
    await turn.emit({
      schema_version: 1,
      type,
      request_id: turn.params.request_id,
      execution_session_id: turn.params.execution_session_id,
      turn_id: turn.params.turn_id,
      provider: "qoder",
      auth_id: turn.params.auth_id,
      auth_index: turn.params.auth_index,
      sequence: turn.sequence,
      timestamp: new Date().toISOString(),
      payload,
    });
  }

  private async emitTerminal(
    turn: DirectTurn,
    type: "turn.completed" | "turn.failed" | "turn.cancelled" | "session.closed",
    state: string,
    code?: string,
    message?: string,
    retryable = false,
  ): Promise<void> {
    if (turn.terminal) return;
    if (type === "turn.completed") {
      for (const [index] of turn.toolCalls) await this.emit(turn, "tool.completed", { index });
      turn.terminal = true;
      if (turn.session.current === turn) turn.session.current = undefined;
      await this.emit(turn, type, { state: "completed", finish_reason: turn.finishReason });
      return;
    }
    turn.terminal = true;
    if (turn.session.current === turn) turn.session.current = undefined;
    await this.emit(turn, type, { state, code, message, retryable });
  }

  private async fetchWithTimeout(url: string, init: RequestInit): Promise<Response> {
    const controller = new AbortController();
    const signal = init.signal;
    const onAbort = () => controller.abort(signal?.reason);
    const timer = setTimeout(() => controller.abort(), this.requestTimeoutMs);
    const cleanup = () => {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
    try {
      if (signal?.aborted) onAbort();
      controller.signal.throwIfAborted();
      const response = await this.fetchImpl(url, { ...init, signal: controller.signal });
      if (controller.signal.aborted) {
        await response.body?.cancel().catch(() => undefined);
        controller.signal.throwIfAborted();
      }
      if (!response.body) {
        cleanup();
        return response;
      }
      const reader = response.body.getReader();
      // fetch resolves at headers. Keep cancellation and the deadline connected
      // until the body is consumed or explicitly canceled by its owner.
      const body = new ReadableStream<Uint8Array>({
        async pull(output) {
          try {
            const next = await reader.read();
            if (next.done) {
              cleanup();
              reader.releaseLock();
              output.close();
            } else {
              output.enqueue(next.value);
            }
          } catch (error) {
            cleanup();
            reader.releaseLock();
            output.error(error);
          }
        },
        async cancel(reason) {
          cleanup();
          try { await reader.cancel(reason); } finally { reader.releaseLock(); }
        },
      });
      return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers });
    } catch (error) {
      cleanup();
      throw error;
    }
  }
}

export class QoderTokenManager {
  private state?: TokenState;

  constructor(
    private readonly authEndpoint: string | undefined,
    private readonly tokenMode: DirectTokenMode,
    private readonly openAPIUserAgent: string,
    private readonly fetchImpl: typeof fetch,
    private readonly requestTimeoutMs: number,
  ) {}

  async fetchWithAuth(
    auth: AuthSpec,
    request: (token: string) => Promise<Response>,
    signal?: AbortSignal,
  ): Promise<Response> {
    if (signal?.aborted) throw new ProtocolError("request_cancelled", "Qoder direct request was cancelled");
    const state = await this.ensure(auth, signal);
    if (signal?.aborted) throw new ProtocolError("request_cancelled", "Qoder direct request was cancelled");
    let response = await request(state.accessToken);
    if ((response.status === 401 || response.status === 403) && state.patExchange) {
      await response.body?.cancel().catch(() => undefined);
      const refreshed = await this.refreshOrExchange(state, signal);
      response = await request(refreshed.accessToken);
    }
    return response;
  }

  async getAccessToken(
    auth: AuthSpec,
    reason: "initial" | "unauthorized" = "initial",
    signal?: AbortSignal,
  ): Promise<string> {
    const state = await this.ensure(auth, signal);
    if (reason === "unauthorized" && state.patExchange) {
      return (await this.refreshOrExchange(state, signal)).accessToken;
    }
    return state.accessToken;
  }

  private async ensure(auth: AuthSpec, signal?: AbortSignal): Promise<TokenState> {
    if (auth.mode === "local_cli") throw new ProtocolError("direct_auth_invalid", "direct_openai requires PAT or bearer token auth");
    const source = String(process.env[auth.env_var] ?? "");
    if (!source) throw new ProtocolError("auth_not_configured", "Qoder direct token environment source is not configured");
    const patExchange = auth.mode === "pat" && (this.tokenMode === "pat_exchange" || this.tokenMode === "auto" && source.startsWith("pt-"));
    if (!patExchange) {
      this.state = { source, accessToken: source, expiresAt: Number.POSITIVE_INFINITY, patExchange: false };
      return this.state;
    }
    if (!this.authEndpoint) throw new ProtocolError("direct_auth_config", "Qoder OpenAPI endpoint is not configured");
    if (this.state?.source === source && this.state.patExchange === patExchange && this.state.expiresAt > Date.now() + 30_000) return this.state;
    this.state = await this.exchange(source, signal);
    return this.state;
  }

  private async refreshOrExchange(state: TokenState, signal?: AbortSignal): Promise<TokenState> {
    if (state.refreshToken && this.authEndpoint) {
      try {
        this.state = await this.refresh(state.refreshToken, signal);
        this.state.source = state.source;
        this.state.patExchange = true;
        return this.state;
      } catch (error) {
        if (signal?.aborted) throw error;
        // A short-lived refresh token can expire before the long-lived PAT.
      }
    }
    this.state = await this.exchange(state.source, signal);
    return this.state;
  }

  private async exchange(pat: string, signal?: AbortSignal): Promise<TokenState> {
    return this.tokenRequest("/api/v1/jobToken/exchange", { personal_token: pat }, pat, signal);
  }

  private async refresh(refreshToken: string, signal?: AbortSignal): Promise<TokenState> {
    return this.tokenRequest("/api/v1/jobToken/refresh", { refresh_token: refreshToken }, "", signal);
  }

  private async tokenRequest(path: string, body: Record<string, string>, source: string, signal?: AbortSignal): Promise<TokenState> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.requestTimeoutMs);
    const onAbort = () => controller.abort();
    signal?.addEventListener("abort", onAbort, { once: true });
    try {
      if (signal?.aborted) controller.abort(signal.reason);
      controller.signal.throwIfAborted();
      const response = await this.fetchImpl(`${this.authEndpoint}${path}`, {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "User-Agent": this.openAPIUserAgent,
        },
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      if (!response.ok) {
        await response.body?.cancel().catch(() => undefined);
        throw new ProtocolError("direct_auth_failed", `Qoder direct auth endpoint returned HTTP ${response.status}`, response.status >= 500);
      }
      const raw = await readResponseText(response, MAX_ERROR_BYTES);
      let value: unknown;
      try {
        value = JSON.parse(raw);
      } catch {
        throw new ProtocolError("direct_auth_failed", "Qoder direct auth response was not valid JSON");
      }
      const record = isRecord(value) && isRecord(value.data) ? value.data : value;
      const token = isRecord(record) ? stringOrUndefined(record.token ?? record.access_token) : undefined;
      if (!isRecord(record) || !token) {
        throw new ProtocolError("direct_auth_failed", "Qoder direct auth response did not contain an access token");
      }
      return {
        source,
        accessToken: token,
        refreshToken: stringOrUndefined(record.refresh_token),
        expiresAt: expiryFromTokenRecord(record),
        patExchange: true,
      };
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
    }
  }
}

function buildDirectRequest(params: StartParams): Record<string, unknown> {
  const original = params.chat_request;
  if (!original || !isRecord(original)) throw new ProtocolError("direct_request_missing", "direct_openai requires a JSON Chat request");
  const messages = original.messages;
  if (!Array.isArray(messages) || messages.length === 0) throw new ProtocolError("invalid_request", "direct_openai requires a non-empty messages array");
  const request: Record<string, unknown> = { ...original, model: params.model, stream: true };
  const existingOptions = isRecord(original.stream_options) ? { ...original.stream_options } : {};
  request.stream_options = { ...existingOptions, include_usage: true };
  const metadata = isRecord(original.metadata) ? { ...original.metadata } : {};
  const context = isRecord(metadata.context) ? { ...metadata.context } : {};
  metadata.context = {
    ...context,
    request_id: params.request_id,
    request_set_id: params.request_id,
    session_id: params.execution_session_id,
    task_id: "common",
    client_type: "cpa-qoder-direct",
  };
  request.metadata = metadata;
  return request;
}

async function directHTTPError(response: Response, operation: string): Promise<ProtocolError> {
  const status = response.status;
  await readResponseText(response, MAX_ERROR_BYTES).catch(() => "");
  const suffix = ` (HTTP ${status})`;
  if (status === 401 || status === 403) return new ProtocolError("auth_expired", `${operation} authentication was rejected${suffix}`);
  if (status === 429) return new ProtocolError("quota_or_rate_limit", `${operation} was rate limited${suffix}`, true);
  if (status >= 500) return new ProtocolError("direct_upstream_error", `${operation} failed upstream${suffix}`, true);
  return new ProtocolError("direct_invalid_request", `${operation} was rejected${suffix}`);
}

async function readResponseText(response: Response, maxBytes: number): Promise<string> {
  const reader = response.body?.getReader();
  if (!reader) return "";
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const next = await reader.read();
      if (next.done) break;
      total += next.value.byteLength;
      if (total > maxBytes) throw new ProtocolError("response_too_large", "Qoder direct response exceeds the bounded limit");
      chunks.push(next.value);
    }
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
  const merged = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(merged);
}

function parseModelJSON(raw: string | undefined): ModelRecord[] {
  if (!raw) return [];
  try {
    const value = JSON.parse(raw);
    if (Array.isArray(value)) return validateModelRecords(value);
    if (isRecord(value) && Array.isArray(value.models)) return validateModelRecords(value.models);
  } catch {
    throw new ProtocolError("direct_models_invalid", "direct_models JSON is invalid");
  }
  throw new ProtocolError("direct_models_invalid", "direct_models JSON must be an array or an object with models");
}

function parseModelResponse(raw: string): ModelRecord[] {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new ProtocolError("models_schema_invalid", "direct model response was not valid JSON", true);
  }
  const records = isRecord(value) && Array.isArray(value.data)
    ? value.data
    : isRecord(value) && Array.isArray(value.models) ? value.models : Array.isArray(value) ? value : undefined;
  if (!records) throw new ProtocolError("models_schema_invalid", "direct model response has no model array", true);
  const models = validateModelRecords(records);
  if (models.length === 0) throw new ProtocolError("models_unavailable", "direct model response contained no enabled model IDs", true);
  return models;
}

function validateModelRecords(values: unknown[]): ModelRecord[] {
  const seen = new Set<string>();
  const models: ModelRecord[] = [];
  for (const value of values) {
    if (typeof value === "string") {
      const id = value.trim();
      if (id && !seen.has(id)) {
        seen.add(id);
        models.push({ id, display_name: id });
      }
      continue;
    }
    if (!isRecord(value)) throw new ProtocolError("direct_models_invalid", "direct model record is invalid");
    const id = String(value.id ?? value.model ?? value.value ?? "").trim();
    if (!id || id.length > 512 || seen.has(id)) continue;
    const display = String(value.display_name ?? value.displayName ?? value.name ?? id).trim() || id;
    seen.add(id);
    models.push({
      id,
      display_name: display,
      description: stringOrUndefined(value.description),
      source: stringOrUndefined(value.source),
      is_default: booleanOrUndefined(value.is_default ?? value.isDefault),
      is_enabled: value.is_enabled === false || value.isEnabled === false ? false : true,
      is_reasoning: booleanOrUndefined(value.is_reasoning ?? value.isReasoning),
      is_vl: booleanOrUndefined(value.is_vl ?? value.isVl),
      max_input_tokens: numberOrUndefined(value.max_input_tokens ?? value.maxInputTokens),
      max_output_tokens: numberOrUndefined(value.max_output_tokens ?? value.maxOutputTokens),
      reasoning_efforts: stringArrayOrUndefined(value.reasoning_efforts ?? value.efforts),
      default_reasoning_effort: stringOrUndefined(value.default_reasoning_effort ?? value.defaultEffort),
      supports_disabled: booleanOrUndefined(value.supports_disabled ?? value.supportsDisabled),
      available_context_windows: numberArrayOrUndefined(value.available_context_windows ?? value.availableContextWindows),
      default_context_window: numberOrUndefined(value.default_context_window ?? value.defaultContextWindow),
    });
  }
  return models.filter((model) => model.is_enabled !== false).map(({ is_enabled: _enabled, ...model }) => model);
}

function cloneModels(models: ModelRecord[]): ModelRecord[] {
  return models.map((model) => ({ ...model, reasoning_efforts: model.reasoning_efforts ? [...model.reasoning_efforts] : undefined, available_context_windows: model.available_context_windows ? [...model.available_context_windows] : undefined }));
}

function expiryFromTokenRecord(record: Record<string, unknown>): number {
  const expiresAt = typeof record.expires_at === "string" ? Date.parse(record.expires_at) : NaN;
  if (Number.isFinite(expiresAt)) return expiresAt;
  const expiresIn = numberOrUndefined(record.expires_in);
  if (expiresIn !== undefined && expiresIn > 0 && expiresIn <= 7 * 24 * 60 * 60) {
    return Date.now() + Math.max(60_000, expiresIn * 1000);
  }
  return Date.now() + Math.max(60_000, expiresIn ?? 86_400_000);
}

export function isSupportedEndpoint(raw: string): boolean {
  try {
    const url = new URL(raw);
    if (url.username || url.password || url.search || url.hash || !url.hostname) return false;
    if (url.protocol === "https:") return true;
    if (url.protocol !== "http:") return false;
    return url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "::1";
  } catch {
    return false;
  }
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isTerminal(type: AgentEventType): boolean {
  return type === "turn.completed" || type === "turn.failed" || type === "turn.cancelled" || type === "session.closed";
}

function numberOrUndefined(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function stringOrUndefined(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function booleanOrUndefined(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function stringArrayOrUndefined(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  return value.filter((item): item is string => typeof item === "string" && item.trim() !== "").map((item) => item.trim());
}

function numberArrayOrUndefined(value: unknown): number[] | undefined {
  if (!Array.isArray(value)) return undefined;
  return value.filter((item): item is number => typeof item === "number" && Number.isFinite(item));
}
