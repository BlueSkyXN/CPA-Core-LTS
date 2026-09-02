import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough, Writable } from "node:stream";
import test from "node:test";

import { BoundedFrameWriter, ProtocolError, redactStderr } from "../dist/protocol.js";
import { DirectOpenAIAdapter, QoderTokenManager } from "../dist/direct.js";
import { patchQoderSDKJobTokenPayload, toModelRecord, userMessage } from "../dist/qoder.js";
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
  assert.equal(await server.handle(request("r", "readiness", { auth: { mode: "pat", env_var: "CPA_QODER_PAT" } })), true);
  assert.equal(await server.handle(request("m", "models", { auth: { mode: "pat", env_var: "CPA_QODER_PAT" } })), true);
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

test("SDK PAT auth adapts the one-shot host job-token payload without storing a token", async () => {
  const root = await mkdtemp(join(tmpdir(), "qoder-sdk-auth-test-"));
  const payloadPath = join(root, "payload.json");
  try {
    await writeFile(payloadPath, JSON.stringify({ type: "jobToken", hostTokenCallback: true }), { mode: 0o600 });
    patchQoderSDKJobTokenPayload(payloadPath);
    assert.deepEqual(JSON.parse(await readFile(payloadPath, "utf8")), {
      type: "jobToken",
      jobTokenProvider: "host",
    });
    assert.equal((await stat(payloadPath)).mode & 0o777, 0o600);

    await writeFile(payloadPath, JSON.stringify({ type: "accessToken", accessToken: "must-not-be-read" }), { mode: 0o600 });
    assert.throws(
      () => patchQoderSDKJobTokenPayload(payloadPath),
      (error) => error instanceof ProtocolError && error.code === "sdk_auth_payload_incompatible",
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("shared Qoder token manager exchanges PAT and refreshes an unauthorized SDK job token", async () => {
  const envVar = "CPA_QODER_SDK_TOKEN_MANAGER_TEST";
  const previous = process.env[envVar];
  process.env[envVar] = "pt-fixture";
  const paths = [];
  const manager = new QoderTokenManager(
    "https://openapi.example.test",
    "pat_exchange",
    "qoder/test",
    async (url, init) => {
      const path = new URL(url).pathname;
      paths.push(path);
      if (path.endsWith("/exchange")) {
        assert.deepEqual(JSON.parse(init.body), { personal_token: "pt-fixture" });
        return new Response(JSON.stringify({ token: "job-initial", refresh_token: "refresh-fixture", expires_in: 3600 }), { status: 200 });
      }
      assert.deepEqual(JSON.parse(init.body), { refresh_token: "refresh-fixture" });
      return new Response(JSON.stringify({ token: "job-refreshed", refresh_token: "refresh-next", expires_in: 3600 }), { status: 200 });
    },
    1000,
  );
  try {
    const auth = { mode: "pat", env_var: envVar };
    assert.equal(await manager.getAccessToken(auth, "initial"), "job-initial");
    assert.equal(await manager.getAccessToken(auth, "unauthorized"), "job-refreshed");
    assert.deepEqual(paths, ["/api/v1/jobToken/exchange", "/api/v1/jobToken/refresh"]);
  } finally {
    if (previous === undefined) delete process.env[envVar];
    else process.env[envVar] = previous;
  }
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

test("direct OpenAI transport preserves exact model, tools, usage, and lifecycle events", async () => {
  const envVar = "CPA_TEST_QODER_DIRECT_TOKEN";
  process.env[envVar] = "jt-fixture";
  const calls = [];
  const adapter = new DirectOpenAIAdapter({
    endpoint: "https://direct.example.test/model/v1/chat/completions",
    modelsEndpoint: "https://direct.example.test/model/v1/models",
    authEndpoint: "https://openapi.example.test",
    tokenMode: "bearer",
    models: [],
    fetchImpl: async (url, init = {}) => {
      calls.push({ url: String(url), init });
      if (String(url).endsWith("/models")) {
        return new Response(JSON.stringify({ data: [{ id: "qfmodel", display_name: "Qwen3.8-Flash", is_enabled: true }] }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response([
        'data: {"choices":[{"delta":{"role":"assistant"}}]}',
        'data: {"choices":[{"delta":{"content":"ok"}}]}',
        'data: {"choices":[{"delta":{"reasoning_content":"trace"}}]}',
        'data: {"choices":[{"delta":{},"finish_reason":"stop"}],"raw_usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}',
        "data: [DONE]",
        "",
      ].join("\n"), { status: 200, headers: { "content-type": "text/event-stream" } });
    },
  });
  try {
    const models = await adapter.models({ auth: { mode: "pat", env_var: envVar }, models_endpoint: "https://direct.example.test/model/v1/models" });
    assert.deepEqual(models.map((model) => model.id), ["qfmodel"]);
    const params = {
      ...startParams(),
      auth: { mode: "pat", env_var: envVar, transport: "direct_openai" },
      chat_request: {
        model: "client-alias",
        messages: [{ role: "user", content: "hello" }],
        stream: false,
        tools: [{ type: "function", function: { name: "probe", parameters: { type: "object" } } }],
      },
    };
    const events = [];
    let terminal;
    const completed = new Promise((resolve) => { terminal = resolve; });
    await adapter.start(params, async (event) => {
      events.push(event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.cancelled") terminal(event);
    });
    await completed;
    assert.deepEqual(events.map((event) => event.type), [
      "session.created", "turn.started", "message.delta", "reasoning.delta", "usage.updated", "turn.completed",
    ]);
    const inference = calls.find((call) => call.url.endsWith("/chat/completions"));
    assert.ok(inference);
    assert.equal(inference.init.headers.Authorization, "Bearer jt-fixture");
    assert.equal(calls.some((call) => call.url.endsWith("/jobToken/exchange")), false);
    const body = JSON.parse(inference.init.body);
    assert.equal(body.model, "qfmodel");
    assert.equal(body.stream, true);
    assert.equal(body.stream_options.include_usage, true);
    assert.deepEqual(body.tools[0].function.parameters, { type: "object" });
    assert.equal(body.metadata.context.request_id, params.request_id);
    const second = {
      ...params,
      request_id: "request-2",
      turn_id: "turn-2",
      chat_request: { ...params.chat_request, model: "qfmodel" },
    };
    const secondEvents = [];
    let secondTerminal;
    const secondCompleted = new Promise((resolve) => { secondTerminal = resolve; });
    await adapter.start(second, async (event) => {
      secondEvents.push(event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.cancelled") secondTerminal(event);
    });
    await secondCompleted;
    assert.equal(secondEvents[0].type, "turn.started");
    assert.equal(secondEvents.some((event) => event.type === "session.created"), false);
    await adapter.close(params.execution_session_id);
  } finally {
    delete process.env[envVar];
    await adapter.shutdown();
  }
});

test("direct OpenAI transport exchanges PAT and retries one unauthorized response with refresh token", async () => {
  const envVar = "CPA_TEST_QODER_DIRECT_PAT";
  process.env[envVar] = "pt-fixture";
  const calls = [];
  let inferenceCalls = 0;
  const adapter = new DirectOpenAIAdapter({
    endpoint: "https://direct.example.test/model/v1/chat/completions",
    openAPIEndpoint: "https://openapi.example.test",
    models: [{ id: "qfmodel", display_name: "Qwen3.8-Flash" }],
    fetchImpl: async (url, init = {}) => {
      calls.push({ url: String(url), init });
      if (String(url).endsWith("/jobToken/exchange")) {
        return new Response(JSON.stringify({ token: "jt-one", refresh_token: "jrt-one", expires_in: 86400000 }), { status: 200 });
      }
      if (String(url).endsWith("/jobToken/refresh")) {
        return new Response(JSON.stringify({ token: "jt-two", refresh_token: "jrt-two", expires_in: 86400000 }), { status: 200 });
      }
      inferenceCalls += 1;
      if (inferenceCalls === 1) return new Response("unauthorized", { status: 401 });
      return new Response('data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n', {
        status: 200,
        headers: { "content-type": "text/event-stream" },
      });
    },
  });
  try {
    const params = {
      ...startParams(),
      auth: { mode: "pat", env_var: envVar, transport: "direct_openai" },
      chat_request: { model: "qfmodel", messages: [{ role: "user", content: "hello" }] },
    };
    const events = [];
    let terminal;
    const completed = new Promise((resolve) => { terminal = resolve; });
    await adapter.start(params, async (event) => {
      events.push(event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.cancelled") terminal(event);
    });
    await completed;
    assert.equal(events.at(-1).type, "turn.completed");
    const inference = calls.filter((call) => call.url.endsWith("/chat/completions"));
    assert.equal(inference.length, 2);
    assert.equal(inference[0].init.headers.Authorization, "Bearer jt-one");
    assert.equal(inference[1].init.headers.Authorization, "Bearer jt-two");
  } finally {
    delete process.env[envVar];
    await adapter.shutdown();
  }
});

test("direct OpenAI transport aborts the upstream request on cancel", async () => {
  const envVar = "CPA_TEST_QODER_DIRECT_CANCEL_TOKEN";
  process.env[envVar] = "pt-fixture";
  const adapter = new DirectOpenAIAdapter({
    endpoint: "https://direct.example.test/model/v1/chat/completions",
    openAPIEndpoint: "https://openapi.example.test",
    models: [{ id: "qfmodel" }],
    fetchImpl: async (url, init = {}) => {
      if (String(url).endsWith("/jobToken/exchange")) {
        return new Response(JSON.stringify({ token: "jt-cancel", expires_in: 86400 }), { status: 200 });
      }
      return await new Promise((_resolve, reject) => {
      init.signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      });
    },
  });
  try {
    const params = {
      ...startParams(),
      auth: { mode: "pat", env_var: envVar, transport: "direct_openai" },
      chat_request: { model: "qfmodel", messages: [{ role: "user", content: "hello" }] },
    };
    const events = [];
    let terminal;
    const completed = new Promise((resolve) => { terminal = resolve; });
    const turn = await adapter.start(params, async (event) => {
      events.push(event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.cancelled") terminal(event);
    });
    await turn.cancel();
    const finalEvent = await completed;
    assert.equal(finalEvent.type, "turn.cancelled");
    assert.equal(events.at(-1).payload.code, "request_cancelled");
  } finally {
    delete process.env[envVar];
    await adapter.shutdown();
  }
});

test("direct OpenAI transport projects client tool-call deltas into AgentEventV1", async () => {
  const envVar = "CPA_TEST_QODER_DIRECT_TOOL_TOKEN";
  process.env[envVar] = "pt-fixture";
  const adapter = new DirectOpenAIAdapter({
    endpoint: "https://direct.example.test/model/v1/chat/completions",
    openAPIEndpoint: "https://openapi.example.test",
    models: [{ id: "qfmodel" }],
    fetchImpl: async (url) => {
      if (String(url).endsWith("/jobToken/exchange")) {
        return new Response(JSON.stringify({ token: "jt-tool", expires_in: 86400 }), { status: 200 });
      }
      return new Response([
        'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"probe","arguments":"{\\"x\\":1}"}}]}}]}',
        "data: [DONE]",
        "",
      ].join("\n"), { status: 200, headers: { "content-type": "text/event-stream" } });
    },
  });
  try {
    const params = {
      ...startParams(),
      auth: { mode: "pat", env_var: envVar, transport: "direct_openai" },
      chat_request: { model: "qfmodel", messages: [{ role: "user", content: "call probe" }], tools: [{ type: "function", function: { name: "probe" } }] },
    };
    const events = [];
    let terminal;
    const completed = new Promise((resolve) => { terminal = resolve; });
    await adapter.start(params, async (event) => {
      events.push(event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.cancelled") terminal(event);
    });
    await completed;
    assert.deepEqual(events.map((event) => event.type), [
      "session.created", "turn.started", "tool.started", "tool.updated", "tool.completed", "turn.completed",
    ]);
    assert.equal(events.find((event) => event.type === "tool.updated").payload.partial_json, '{"x":1}');
  } finally {
    delete process.env[envVar];
    await adapter.shutdown();
  }
});

test("direct OpenAI transport rejects a display-name or guessed model alias", async () => {
  const envVar = "CPA_TEST_QODER_DIRECT_MODEL_TOKEN";
  process.env[envVar] = "pt-fixture";
  const adapter = new DirectOpenAIAdapter({
    endpoint: "https://direct.example.test/model/v1/chat/completions",
    openAPIEndpoint: "https://openapi.example.test",
    models: [{ id: "qfmodel", display_name: "Qwen3.8-Flash" }],
    fetchImpl: async () => { throw new Error("network must not be called for an unknown model"); },
  });
  try {
    const params = {
      ...startParams(),
      model: "Qwen3.8-Flash",
      auth: { mode: "pat", env_var: envVar, transport: "direct_openai" },
      chat_request: { model: "Qwen3.8-Flash", messages: [{ role: "user", content: "hello" }] },
    };
    let terminal;
    const completed = new Promise((resolve) => { terminal = resolve; });
    await adapter.start(params, async (event) => {
      if (event.type === "turn.failed") terminal(event);
    });
    const result = await completed;
    assert.equal(result.payload.code, "unsupported_model");
  } finally {
    delete process.env[envVar];
    await adapter.shutdown();
  }
});
