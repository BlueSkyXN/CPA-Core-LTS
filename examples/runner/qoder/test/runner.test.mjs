import assert from "node:assert/strict";
import { PassThrough, Writable } from "node:stream";
import test from "node:test";

import { BoundedFrameWriter, ProtocolError, redactStderr } from "../dist/protocol.js";
import { toModelRecord, userMessage } from "../dist/qoder.js";
import { RunnerServer } from "../dist/server.js";

class FakeAdapter {
  events = [];
  cancelCount = 0;
  closeCount = 0;
  shutdownCount = 0;
  emit = null;
  params = null;

  async readiness(auth) {
    return { auth_ready: Boolean(auth), message: auth ? "configured" : "missing" };
  }

  async models() {
    return [{ id: "qfmodel", display_name: "Qwen3.8-Flash", is_default: true }];
  }

  async start(params, emit) {
    this.params = params;
    this.emit = emit;
    await emit(event(params, 1, "session.created", { native_session_id: "native-1" }));
    await emit(event(params, 2, "turn.started", {}));
    return { cancel: async () => {
      this.cancelCount += 1;
      await emit(event(params, 3, "turn.cancelled", { state: "cancelled" }));
    } };
  }

  async close() {
    this.closeCount += 1;
  }

  async shutdown() {
    this.shutdownCount += 1;
  }
}

function event(params, sequence, type, payload) {
  return {
    schema_version: 1,
    type,
    request_id: params.request_id,
    execution_session_id: params.execution_session_id,
    turn_id: params.turn_id,
    provider: "qoder",
    auth_id: params.auth_id,
    auth_index: params.auth_index,
    sequence,
    timestamp: new Date().toISOString(),
    payload,
  };
}

function request(id, method, params = {}) {
  return { protocol_version: 1, id, method, params };
}

function startParams() {
  return {
    request_id: "request-1",
    execution_session_id: "session-1",
    turn_id: "turn-1",
    provider: "qoder",
    auth_id: "auth-1",
    auth_index: "index-1",
    prompt: "reply OK",
    content: [{ type: "text", text: "User:" }, { type: "text", text: "reply OK" }],
    model: "qfmodel",
    auth: { mode: "pat", env_var: "CPA_QODER_PAT" },
    permission_policy: { default: "deny", rules: [] },
  };
}

function frames(output) {
  const chunks = [];
  for (;;) {
    const chunk = output.read();
    if (chunk === null) break;
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
}

test("protocol handshake, model IDs, event correlation, cancel, close and shutdown", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  const server = new RunnerServer(adapter, output, 16);

  assert.equal(await server.handle(request("h", "handshake")), true);
  assert.equal(await server.handle(request("r", "readiness", { auth: { mode: "local_cli", profile_id: "default" } })), true);
  assert.equal(await server.handle(request("m", "models", { auth: { mode: "local_cli" } })), true);
  assert.equal(await server.handle(request("s", "start", startParams())), true);
  assert.equal(await server.handle(request("c", "cancel", { request_id: "request-1", execution_session_id: "session-1" })), true);
  assert.equal(await server.handle(request("x", "close", { execution_session_id: "session-1" })), true);
  assert.equal(await server.handle(request("z", "shutdown")), false);

  const all = frames(output);
  const handshake = all.find((frame) => frame.id === "h");
  assert.equal(handshake.result.protocol_version, 1);
  assert.equal(handshake.result.sdk_version, "1.0.10");
  const models = all.find((frame) => frame.id === "m");
  assert.deepEqual(models.result.models.map((model) => model.id), ["qfmodel"]);
  const events = all.filter((frame) => frame.type === "event").map((frame) => frame.event);
  assert.deepEqual(events.map((entry) => entry.type), ["session.created", "turn.started", "turn.cancelled"]);
  assert.ok(events.every((entry) => entry.request_id === "request-1" && entry.auth_index === "index-1"));
  assert.equal(adapter.cancelCount, 1);
  assert.equal(adapter.closeCount, 1);
  assert.equal(adapter.shutdownCount, 1);
  await server.close();
});

test("version and permission policy fail closed", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  const server = new RunnerServer(adapter, output);
  await server.handle({ protocol_version: 2, id: "bad-version", method: "handshake" });
  const invalid = startParams();
  invalid.permission_policy = { default: "allow" };
  await server.handle(request("bad-policy", "start", invalid));
  const all = frames(output);
  assert.equal(all.find((frame) => frame.id === "bad-version").error.code, "protocol_version_mismatch");
  assert.equal(all.find((frame) => frame.id === "bad-policy").error.code, "invalid_permission_policy");
  assert.equal(adapter.params, null);
  await server.close();
});

test("prompt-only protocol v1 starts remain backward compatible", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  const server = new RunnerServer(adapter, output);
  const params = startParams();
  delete params.content;
  assert.equal(await server.handle(request("legacy", "start", params)), true);
  assert.deepEqual(userMessage(params).message.content, [{ type: "text", text: "reply OK" }]);
  await server.close();
});

test("structured image and fixed skill, tool, and MCP config cross the runner boundary", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  const server = new RunnerServer(adapter, output);
  const params = startParams();
  params.system_prompt = "Return exact probe markers.";
  params.content = [
    { type: "text", text: "User:" },
    { type: "image", source: { type: "base64", media_type: "image/png", data: "AA==" } },
  ];
  params.skills = ["cpa-probe"];
  params.setting_sources = ["project"];
  params.allowed_tools = ["Read", "mcp__cpa_probe__echo"];
  params.disallowed_tools = ["Bash"];
  params.mcp_servers = {
    cpa_probe: { type: "stdio", command: "/usr/bin/node", args: ["/tmp/mcp.mjs"] },
  };
  assert.equal(await server.handle(request("structured", "start", params)), true);
  assert.deepEqual(adapter.params.content, params.content);
  assert.deepEqual(adapter.params.mcp_servers, params.mcp_servers);
  const message = userMessage(params);
  assert.deepEqual(message.message.content, params.content);
  await server.close();
});

test("live model metadata preserves vision, reasoning, and context capabilities", () => {
  const model = toModelRecord({
    value: "qmodel_38max",
    modelId: "qmodel_38max",
    displayName: "Qwen3.8-Max",
    description: "test",
    isEnabled: true,
    isReasoning: true,
    isVl: true,
    maxInputTokens: 200000,
    maxOutputTokens: 32768,
    efforts: ["low", "high"],
    defaultEffort: "high",
    supportsDisabled: true,
    availableContextWindows: [128000, 200000],
    defaultContextWindow: 128000,
  });
  assert.equal(model.id, "qmodel_38max");
  assert.equal(model.is_vl, true);
  assert.equal(model.is_reasoning, true);
  assert.deepEqual(model.reasoning_efforts, ["low", "high"]);
  assert.deepEqual(model.available_context_windows, [128000, 200000]);
  assert.equal(model.default_context_window, 128000);
});

test("terminal during start does not leave a stale active session", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  adapter.start = async (params, emit) => {
    await emit(event(params, 1, "turn.started", {}));
    await emit(event(params, 2, "turn.completed", { state: "completed" }));
    return { cancel: async () => {} };
  };
  const server = new RunnerServer(adapter, output);
  assert.equal(await server.handle(request("first", "start", startParams())), true);
  const next = startParams();
  next.request_id = "request-2";
  next.turn_id = "turn-2";
  assert.equal(await server.handle(request("second", "start", next)), true);
  const all = frames(output);
  assert.equal(all.find((frame) => frame.id === "first").ok, true);
  assert.equal(all.find((frame) => frame.id === "second").ok, true);
  await server.close();
});

test("cancel requires matching request and execution session", async () => {
  const output = new PassThrough();
  const adapter = new FakeAdapter();
  const server = new RunnerServer(adapter, output);
  await server.handle(request("start", "start", startParams()));
  await server.handle(request("wrong", "cancel", { request_id: "request-1", execution_session_id: "other-session" }));
  await server.handle(request("right", "cancel", { request_id: "request-1", execution_session_id: "session-1" }));
  const all = frames(output);
  assert.equal(all.find((frame) => frame.id === "wrong").result.cancelled, false);
  assert.equal(all.find((frame) => frame.id === "right").result.cancelled, true);
  assert.equal(adapter.cancelCount, 1);
  await server.close();
});

test("bounded writer rejects oversized frames", async () => {
  const output = new PassThrough();
  const writer = new BoundedFrameWriter(output, 4, 1024, 32);
  await assert.rejects(
    writer.write({ protocol_version: 1, type: "response", id: "x", ok: true, result: { text: "x".repeat(100) } }),
    (error) => error instanceof ProtocolError && error.code === "frame_too_large",
  );
});

test("bounded writer rejects queue overflow under backpressure", async () => {
  class SlowWritable extends Writable {
    callbacks = [];
    constructor() {
      super({ highWaterMark: 1 });
    }
    _write(_chunk, _encoding, callback) {
      this.callbacks.push(callback);
    }
    release() {
      this.callbacks.shift()?.();
    }
  }
  const output = new SlowWritable();
  const writer = new BoundedFrameWriter(output, 1, 4096, 1024);
  const frame = { protocol_version: 1, type: "response", id: "x", ok: true };
  const first = writer.write(frame);
  await new Promise((resolve) => setImmediate(resolve));
  const second = writer.write({ ...frame, id: "y" });
  await assert.rejects(
    writer.write({ ...frame, id: "z" }),
    (error) => error instanceof ProtocolError && error.code === "queue_full",
  );
  output.release();
  await first;
  await new Promise((resolve) => setImmediate(resolve));
  output.release();
  await second;
});

test("stderr redaction removes direct and header secrets", () => {
  const secret = "pt-secret-value";
  const safe = redactStderr(`token=${secret} Authorization: Bearer ${secret}`, [secret]);
  assert.equal(safe.includes(secret), false);
  assert.match(safe, /REDACTED_SECRET/);
});
