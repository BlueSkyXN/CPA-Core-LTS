import type { Readable, Writable } from "node:stream";

import {
  BoundedFrameWriter,
  DEFAULT_MAX_FRAME_BYTES,
  ProtocolError,
  safeError,
} from "./protocol.js";
import {
  QODER_SDK_VERSION,
  RUNNER_NAME,
  RUNNER_PROTOCOL_VERSION,
  RUNNER_VERSION,
  type ActiveTurn,
  type AgentEventV1,
  type CancelParams,
  type CloseParams,
  type ModelsParams,
  type QoderAdapter,
  type QoderTransport,
  type ReadinessParams,
  type RunnerRequest,
  type RunnerResponse,
  type StartParams,
} from "./types.js";

export class RunnerServer {
  private readonly writer: BoundedFrameWriter;
  private readonly activeByRequest = new Map<string, ActiveTurn>();
  private readonly activeRequestBySession = new Map<string, string>();
  private accepting = true;

  constructor(
    private readonly adapter: QoderAdapter,
    output: Writable,
    queueFrames?: number,
  ) {
    this.writer = new BoundedFrameWriter(output, queueFrames);
  }

  async handle(request: RunnerRequest): Promise<boolean> {
    let keepRunning = true;
    try {
      validateRequest(request);
      const result = await this.dispatch(request);
      await this.respond(request.id, true, result);
      if (request.method === "shutdown") {
        keepRunning = false;
        this.accepting = false;
      }
    } catch (error) {
      const safe = safeError(error);
      await this.respond(request?.id || "unknown", false, undefined, safe);
    }
    return keepRunning;
  }

  async close(): Promise<void> {
    this.accepting = false;
    await this.adapter.shutdown();
    this.writer.close();
  }

  private async dispatch(request: RunnerRequest): Promise<unknown> {
    switch (request.method) {
      case "handshake": {
        const transport: QoderTransport = this.adapter.transport ?? "sdk_cli";
        return {
          runner: RUNNER_NAME,
          runner_version: RUNNER_VERSION,
          protocol_version: RUNNER_PROTOCOL_VERSION,
          sdk_version: transport === "direct_openai" ? "not_applicable" : QODER_SDK_VERSION,
          transport,
          node_version: process.version,
          capabilities: [
            ...(transport === "direct_openai"
              ? ["readiness", "models", "events", "cancel", "close", "direct_openai", "client_tools", "structured_input", "image_input"]
              : ["readiness", "models", "sessions", "events", "cancel", "close", "structured_input", "image_input", "fixed_permissions", "fixed_skills", "fixed_mcp"]),
          ],
        };
      }
      case "readiness": {
        const params = asObject<ReadinessParams>(request.params);
        const state = await this.adapter.readiness(params.auth);
        return {
          ready: state.auth_ready,
          checks: [
            { level: "runner_installed", state: "ready", version: RUNNER_VERSION },
            { level: "protocol_ready", state: "ready", version: String(RUNNER_PROTOCOL_VERSION) },
            { level: "auth_ready", state: state.auth_ready ? "ready" : "not_ready", message: state.message },
          ],
        };
      }
      case "models":
        return { models: await this.adapter.models(asObject<ModelsParams>(request.params)) };
      case "start": {
        if (!this.accepting) throw new ProtocolError("runner_quiescing", "Qoder runner is shutting down", true);
        const params = asObject<StartParams>(request.params);
        validateStart(params);
        if (this.activeRequestBySession.has(params.execution_session_id)) {
          throw new ProtocolError("turn_conflict", "Qoder execution session already has an active turn", true);
        }
        this.activeRequestBySession.set(params.execution_session_id, params.request_id);
        let turn: ActiveTurn;
        try {
          turn = await this.adapter.start(params, async (event) => this.emitEvent(event));
        } catch (error) {
          this.releaseSession(params.execution_session_id, params.request_id);
          throw error;
        }
        if (this.activeRequestBySession.get(params.execution_session_id) === params.request_id) {
          this.activeByRequest.set(params.request_id, turn);
        }
        return { accepted: true, request_id: params.request_id, execution_session_id: params.execution_session_id, turn_id: params.turn_id };
      }
      case "cancel": {
        const params = asObject<CancelParams>(request.params);
        const requestID = required(params.request_id, "request_id");
        const sessionID = required(params.execution_session_id, "execution_session_id");
        const turn = this.activeRequestBySession.get(sessionID) === requestID
          ? this.activeByRequest.get(requestID)
          : undefined;
        if (turn) await turn.cancel();
        return { cancelled: Boolean(turn) };
      }
      case "close": {
        const params = asObject<CloseParams>(request.params);
        const sessionID = required(params.execution_session_id, "execution_session_id");
        await this.adapter.close(sessionID);
        this.releaseSession(sessionID);
        return { closed: true };
      }
      case "shutdown":
        this.accepting = false;
        await this.adapter.shutdown();
        this.activeByRequest.clear();
        this.activeRequestBySession.clear();
        return { shutdown: true };
    }
  }

  private async emitEvent(event: AgentEventV1): Promise<void> {
    validateEvent(event);
    await this.writer.write({
      protocol_version: RUNNER_PROTOCOL_VERSION,
      type: "event",
      request_id: event.request_id,
      event,
    });
    if (isTerminal(event.type)) this.releaseSession(event.execution_session_id, event.request_id);
  }

  private releaseSession(sessionID: string, requestID?: string): void {
    const activeRequestID = this.activeRequestBySession.get(sessionID);
    if (activeRequestID && (!requestID || activeRequestID === requestID)) {
      this.activeRequestBySession.delete(sessionID);
      this.activeByRequest.delete(activeRequestID);
    }
  }

  async rejectFrame(id: string, error: ProtocolError): Promise<void> {
    await this.respond(id || "unknown", false, undefined, error);
  }

  private async respond(id: string, ok: boolean, result?: unknown, error?: ProtocolError): Promise<void> {
    const response: RunnerResponse = {
      protocol_version: RUNNER_PROTOCOL_VERSION,
      type: "response",
      id,
      ok,
      result,
      error: error ? { code: error.code, message: error.message, retryable: error.retryable } : undefined,
    };
    await this.writer.write(response);
  }
}

export async function runJSONLServer(
  server: RunnerServer,
  input: Readable,
  maxFrameBytes = DEFAULT_MAX_FRAME_BYTES,
): Promise<void> {
  let buffer = Buffer.alloc(0);
  let chain = Promise.resolve(true);
  let running = true;

  const rejectOversizedFrame = (line: Buffer) => {
    if (!running) return;
    const requestID = requestIDFromOversizedFrame(line);
    chain = chain.then(async (keepRunning) => {
      if (!keepRunning) return false;
      await server.rejectFrame(
        requestID,
        new ProtocolError("frame_too_large", "runner input frame exceeds the configured limit"),
      );
      return false;
    });
    running = false;
  };

  const processLine = (line: Buffer) => {
    if (!running || line.length === 0) return;
    if (line.length > maxFrameBytes) {
      rejectOversizedFrame(line);
      return;
    }
    let request: RunnerRequest;
    try {
      request = JSON.parse(line.toString("utf8")) as RunnerRequest;
    } catch {
      request = { protocol_version: RUNNER_PROTOCOL_VERSION, id: "invalid-json", method: "handshake", params: {} };
      Object.defineProperty(request, "__invalid", { value: true });
    }
    chain = chain.then(async (keepRunning) => {
      if (!keepRunning) return false;
      if ((request as RunnerRequest & { __invalid?: boolean }).__invalid) {
        await server.handle({ protocol_version: 0, id: "invalid-json", method: "handshake" });
        return true;
      }
      const next = await server.handle(request);
      if (!next) {
        running = false;
        if ("destroy" in input && typeof input.destroy === "function") input.destroy();
      }
      return next;
    });
  };

  try {
    for await (const chunk of input) {
      if (!running) break;
      buffer = Buffer.concat([buffer, Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)]);
      if (buffer.length > maxFrameBytes && !buffer.includes(0x0a)) {
        rejectOversizedFrame(buffer);
        buffer = Buffer.alloc(0);
        break;
      }
      for (;;) {
        const newline = buffer.indexOf(0x0a);
        if (newline < 0) break;
        const line = buffer.subarray(0, newline);
        buffer = buffer.subarray(newline + 1);
        processLine(line);
        if (!running) {
          buffer = Buffer.alloc(0);
          break;
        }
      }
      // Do not wait for another IPC chunk after a rejected, newline-terminated
      // oversized frame; a long-lived stdin pipe may otherwise never close.
      if (!running) break;
    }
  } catch (error) {
    if (running) throw error;
  }
  if (running && buffer.length > 0) processLine(buffer);
  await chain;
  await server.close();
}

function requestIDFromOversizedFrame(line: Buffer): string {
  const prefix = line.subarray(0, Math.min(line.length, 64 * 1024)).toString("utf8");
  let depth = 0;
  for (let index = 0; index < prefix.length; index += 1) {
    const char = prefix[index];
    if (char === '"') {
      const keyToken = readJSONStringToken(prefix, index);
      if (!keyToken) return "oversized";
      index = keyToken.end - 1;
      if (depth !== 1) continue;
      let cursor = skipJSONWhitespace(prefix, keyToken.end);
      if (prefix[cursor] !== ":") continue;
      let key: unknown;
      try {
        key = JSON.parse(keyToken.raw);
      } catch {
        return "oversized";
      }
      if (key !== "id") continue;
      cursor = skipJSONWhitespace(prefix, cursor + 1);
      const valueToken = readJSONStringToken(prefix, cursor);
      if (!valueToken) return "oversized";
      try {
        const value: unknown = JSON.parse(valueToken.raw);
        if (typeof value === "string" && value.trim() !== "") return value;
      } catch {
        return "oversized";
      }
      return "oversized";
    }
    if (char === "{" || char === "[") depth += 1;
    else if (char === "}" || char === "]") depth = Math.max(0, depth - 1);
  }
  return "oversized";
}

function readJSONStringToken(text: string, start: number): { raw: string; end: number } | undefined {
  if (text[start] !== '"') return undefined;
  let escaped = false;
  for (let index = start + 1; index < text.length; index += 1) {
    const char = text[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (char === "\\") {
      escaped = true;
      continue;
    }
    if (char === '"') return { raw: text.slice(start, index + 1), end: index + 1 };
  }
  return undefined;
}

function skipJSONWhitespace(text: string, start: number): number {
  let index = start;
  while (index < text.length && /\s/.test(text[index])) index += 1;
  return index;
}

function validateRequest(request: RunnerRequest): void {
  if (request === null || typeof request !== "object" || Array.isArray(request)) {
    throw new ProtocolError("invalid_request", "runner request must be a JSON object");
  }
  if (request.protocol_version !== RUNNER_PROTOCOL_VERSION) {
    throw new ProtocolError("protocol_version_mismatch", `runner protocol ${request.protocol_version} is unsupported`);
  }
  required(request.id, "id");
  if (!["handshake", "readiness", "models", "start", "cancel", "close", "shutdown"].includes(request.method)) {
    throw new ProtocolError("unknown_method", "runner method is unsupported");
  }
}

function validateStart(params: StartParams): void {
  required(params.request_id, "request_id");
  required(params.execution_session_id, "execution_session_id");
  required(params.turn_id, "turn_id");
  required(params.prompt, "prompt");
  required(params.model, "model");
  if (params.provider !== "qoder") throw new ProtocolError("invalid_provider", "runner provider must be qoder");
  if (!params.permission_policy || !["deny", "cancel_turn"].includes(params.permission_policy.default)) {
    throw new ProtocolError("invalid_permission_policy", "fixed permission policy must default to deny or cancel_turn");
  }
  if (params.content !== undefined && (!Array.isArray(params.content) || params.content.length === 0)) {
    throw new ProtocolError("invalid_content", "structured Qoder content must be a non-empty array when supplied");
  }
  for (const block of params.content ?? []) {
    if (!isRecord(block)) throw new ProtocolError("invalid_content", "Qoder content block must be an object");
    if (block.type === "text") {
      required(typeof block.text === "string" ? block.text : undefined, "content text");
      continue;
    }
    if (block.type !== "image" || !isRecord(block.source)) {
      throw new ProtocolError("invalid_content", "Qoder content block type is unsupported");
    }
    if (block.source.type === "base64") {
      required(typeof block.source.media_type === "string" ? block.source.media_type : undefined, "image media_type");
      required(typeof block.source.data === "string" ? block.source.data : undefined, "image data");
    } else if (block.source.type === "url") {
      required(typeof block.source.url === "string" ? block.source.url : undefined, "image url");
    } else {
      throw new ProtocolError("invalid_content", "Qoder image source type is unsupported");
    }
  }
  validateStringList(params.skills, "skills", 64);
  validateStringList(params.allowed_tools, "allowed_tools", 256);
  validateStringList(params.disallowed_tools, "disallowed_tools", 256);
  validateStringList(params.setting_sources, "setting_sources", 3);
  for (const source of params.setting_sources ?? []) {
    if (!['user', 'project', 'local'].includes(source)) {
      throw new ProtocolError("invalid_configuration", "Qoder setting source is unsupported");
    }
  }
  if (params.mcp_servers !== undefined) {
    if (!isRecord(params.mcp_servers) || Object.keys(params.mcp_servers).length > 32) {
      throw new ProtocolError("invalid_configuration", "fixed Qoder MCP server config is invalid");
    }
    for (const [name, server] of Object.entries(params.mcp_servers)) {
      if (!/^[A-Za-z0-9_-]{1,64}$/.test(name) || !isRecord(server)) {
        throw new ProtocolError("invalid_configuration", "fixed Qoder MCP server entry is invalid");
      }
      const type = server.type ?? "stdio";
      if (type === "stdio") required("command" in server && typeof server.command === "string" ? server.command : undefined, "MCP command");
      else if (type === "sse" || type === "http") required("url" in server && typeof server.url === "string" ? server.url : undefined, "MCP URL");
      else throw new ProtocolError("invalid_configuration", "fixed Qoder MCP transport is unsupported");
    }
  }
}

function validateEvent(event: AgentEventV1): void {
  if (event.schema_version !== 1 || event.provider !== "qoder" || !event.request_id || event.sequence < 1) {
    throw new ProtocolError("invalid_event", "Qoder adapter emitted an invalid AgentEventV1");
  }
}

function asObject<T>(value: unknown): T {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ProtocolError("invalid_params", "runner params must be an object");
  }
  return value as T;
}

function required(value: string | undefined, name: string): string {
  const result = String(value ?? "").trim();
  if (!result) throw new ProtocolError("invalid_params", `${name} is required`);
  return result;
}

function validateStringList(values: unknown, name: string, maxItems: number): void {
  if (values === undefined) return;
  if (!Array.isArray(values) || values.length > maxItems || values.some((value) => typeof value !== "string" || value.trim() === "")) {
    throw new ProtocolError("invalid_configuration", `${name} is invalid`);
  }
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isTerminal(type: AgentEventV1["type"]): boolean {
  return type === "turn.completed" || type === "turn.failed" || type === "turn.cancelled" || type === "session.closed";
}
