import assert from 'node:assert/strict';
import test from 'node:test';
import { setImmediate as nextTick } from 'node:timers/promises';
import { DirectOpenAIAdapter } from '../dist/direct.js';

const encoder = new TextEncoder();
const tokenEnv = 'CPA_QODER_TEST_DIRECT_LIFECYCLE';
const params = () => ({
  request_id: 'request-1', execution_session_id: 'session-1', turn_id: 'turn-1',
  provider: 'qoder', auth_id: 'auth-1', auth_index: 'index-1', model: 'fixture',
  auth: { mode: 'access_token', env_var: tokenEnv },
  permission_policy: { default: 'deny', rules: [] },
  chat_request: { messages: [{ role: 'user', content: 'test' }] },
});
async function within(promise, ms = 700) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('operation did not settle')), ms);
    })]);
  } finally { clearTimeout(timer); }
}
function setup(t, fetchImpl, extra = {}) {
  const previous = process.env[tokenEnv];
  process.env[tokenEnv] = 'fixture-token-not-a-real-secret';
  const adapter = new DirectOpenAIAdapter({
    endpoint: 'https://example.test/chat', models: [{ id: 'fixture' }],
    fetchImpl, ...extra,
  });
  t.after(async () => {
    await adapter.shutdown();
    if (previous === undefined) delete process.env[tokenEnv];
    else process.env[tokenEnv] = previous;
  });
  return adapter;
}
async function start(adapter) {
  const events = [];
  let resolve;
  const terminal = new Promise((r) => { resolve = r; });
  const turn = await adapter.start(params(), async (event) => {
    events.push(event);
    if (['turn.completed', 'turn.failed', 'turn.cancelled', 'session.closed'].includes(event.type)) resolve(event);
  });
  return { events, terminal, turn };
}
function stalledResponse(t) {
  let output;
  const state = { canceled: false, signal: undefined };
  state.fetch = async (_url, init) => {
    state.signal = init.signal;
    const body = new ReadableStream({
      start(controller) {
        output = controller;
        init.signal?.addEventListener('abort', () => controller.error(new Error('aborted')), { once: true });
      },
      cancel() { state.canceled = true; },
    });
    return new Response(body, { headers: { 'content-type': 'text/event-stream' } });
  };
  t.after(() => { try { output?.error(new Error('test cleanup')); } catch {} });
  return state;
}

test('nested Qoder SSE completion is a successful terminal event', async (t) => {
  const body = 'data: {"body":"{\\"choices\\":[{\\"delta\\":{\\"content\\":\\"OK\\"}}]}"}\n\n' +
    'data: {"body":"[DONE]","statusCodeValue":200}\n\n';
  const adapter = setup(t, async () => new Response(body, { headers: { 'content-type': 'text/event-stream' } }));
  const run = await start(adapter);
  assert.equal((await within(run.terminal)).type, 'turn.completed');
  assert.deepEqual(run.events.filter((e) => e.type === 'message.delta').map((e) => e.payload.text), ['OK']);
});

test('tool argument fragments preserve whitespace inside JSON strings', async (t) => {
  const fragments = ['{"q":"hello ', 'world"}'];
  const data = fragments.map((argumentsPart, index) => 'data: ' + JSON.stringify({ choices: [{
    delta: { tool_calls: [{ index: 0, ...(index === 0 ? { id: 'call-1' } : {}),
      function: { ...(index === 0 ? { name: 'lookup' } : {}), arguments: argumentsPart } }] },
  }] }) + '\n\n').join('') + 'data: [DONE]\n\n';
  const adapter = setup(t, async () => new Response(data));
  const run = await start(adapter);
  await within(run.terminal);
  const raw = run.events.filter((e) => e.type === 'tool.updated').map((e) => e.payload.partial_json).join('');
  assert.deepEqual(JSON.parse(raw), { q: 'hello world' });
});

test('cancel after headers interrupts a stalled body read', async (t) => {
  const stalled = stalledResponse(t);
  const run = await start(setup(t, stalled.fetch));
  await nextTick();
  await run.turn.cancel();
  assert.equal((await within(run.terminal)).type, 'turn.cancelled');
  assert.equal(stalled.signal.aborted, true);
});

test('turn deadline remains active after response headers', async (t) => {
  const stalled = stalledResponse(t);
  const run = await start(setup(t, stalled.fetch, { requestTimeoutMs: 1000 }));
  const terminal = await within(run.terminal, 2000);
  assert.equal(terminal.type, 'turn.failed');
  assert.equal(terminal.payload.code, 'direct_timeout');
});

test('cancel also interrupts model discovery before inference starts', async (t) => {
  const stalled = stalledResponse(t);
  const run = await start(setup(t, stalled.fetch, { models: [], modelsEndpoint: 'https://example.test/models' }));
  await nextTick();
  await run.turn.cancel();
  assert.equal((await within(run.terminal)).type, 'turn.cancelled');
  assert.equal(stalled.signal.aborted, true);
});

test('DONE cancels the remaining upstream body instead of only releasing its lock', async (t) => {
  let canceled = false;
  const body = new ReadableStream({
    start(c) { c.enqueue(encoder.encode('data: [DONE]\n\n')); },
    cancel() { canceled = true; },
  });
  const run = await start(setup(t, async () => new Response(body)));
  assert.equal((await within(run.terminal)).type, 'turn.completed');
  await nextTick();
  assert.equal(canceled, true);
});

test('native HTTP fetch closes upstream connection after cancellation', async (t) => {
  const { createServer } = await import('node:http');
  let resolveClosed;
  const closed = new Promise((resolve) => { resolveClosed = resolve; });
  const server = createServer((_request, response) => {
    response.writeHead(200, { 'content-type': 'text/event-stream' });
    response.write('data: {"choices":[{"delta":{"content":"started"}}]}\n\n');
    response.once('close', resolveClosed);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const adapter = setup(t, fetch, { endpoint: `http://127.0.0.1:${server.address().port}/chat` });
  let resolveFirst;
  const first = new Promise((resolve) => { resolveFirst = resolve; });
  let resolveTerminal;
  const terminal = new Promise((resolve) => { resolveTerminal = resolve; });
  const turn = await adapter.start(params(), async (event) => {
    if (event.type === 'message.delta') resolveFirst();
    if (event.type.startsWith('turn.') && event.type !== 'turn.started') resolveTerminal(event);
  });
  await within(first, 2000);
  // Let the consumer enter its next, blocked body read before canceling.
  await nextTick();
  await turn.cancel();
  assert.equal((await within(terminal, 2000)).type, 'turn.cancelled');
  await within(closed, 2000);
});

test('upstream finish reason and reasoning survive the Runner protocol', async (t) => {
  const data = 'data: ' + JSON.stringify({ choices: [{ index: 0, delta: { reasoning_content: 'think', content: 'answer' }, finish_reason: 'length' }] }) + '\n\ndata: [DONE]\n\n';
  const run = await start(setup(t, async () => new Response(data)));
  const terminal = await within(run.terminal);
  assert.equal(terminal.payload.finish_reason, 'length');
  assert.equal(run.events.find((e) => e.type === 'reasoning.delta').payload.text, 'think');
});

test('multiple choices are rejected before a request is sent', async (t) => {
  let calls = 0;
  const adapter = setup(t, async () => { calls++; return new Response('data: [DONE]\n\n'); });
  const request = params(); request.chat_request.n = 2;
  await assert.rejects(adapter.start(request, async () => {}), (e) => e.code === 'invalid_request');
  assert.equal(calls, 0);
});

test('tool identity supplied in a later delta is emitted once', async (t) => {
  const calls = [ { index: 0, function: {} }, { index: 0, id: 'late', function: { name: 'lookup', arguments: '{}' } }, { index: 0, id: 'late', function: { name: 'lookup', arguments: '' } } ];
  const data = calls.map((call) => 'data: ' + JSON.stringify({choices:[{delta:{tool_calls:[call]}}]}) + '\n\n').join('') + 'data: [DONE]\n\n';
  const run = await start(setup(t, async () => new Response(data)));
  await within(run.terminal);
  const updates = run.events.filter((e) => e.type === 'tool.updated');
  assert.equal(updates.length, 1);
  assert.equal(updates[0].payload.tool_call_id, 'late');
  assert.equal(updates[0].payload.name, 'lookup');
});
