import assert from 'node:assert/strict';
import test from 'node:test';
import { QoderSDKAdapter } from '../dist/qoder.js';

const assistant = (...content) => ({ type: 'assistant', message: { content } });
const text = (value) => ({ type: 'text', text: value });
const partial = (event) => ({ type: 'stream_event', event });
const result = (value, usage) => ({ type: 'result', subtype: 'success', is_error: false, result: value, usage });

async function project(messages) {
  // Exercise SDK message handling without starting a CLI or contacting a vendor.
  const adapter = new QoderSDKAdapter('/fixture/not-executed', '/tmp');
  const events = [];
  const session = {
    initialized: true,
    current: {
      params: { request_id: 'r', execution_session_id: 's', turn_id: 't', auth_id: 'a', auth_index: '1' },
      emit: async (event) => events.push(event),
      sequence: 0, started: true, canceled: false, terminal: false,
      hasTextOutput: false, partialContent: new Map(), toolIndexes: new Set(),
    },
  };
  for (const message of messages) await adapter.handleMessage(session, message);
  assert.equal(events.at(-1).type, 'turn.completed');
  assert.deepEqual(events.map((event) => event.sequence), events.map((_, i) => i + 1));
  return events;
}

function output(events, kind = 'message.delta') {
  return events.filter((event) => event.type === kind).map((event) => event.payload.text).join('');
}

test('SDK full assistant and result emit the answer only once', async () => {
  const events = await project([assistant(text('FINAL')), result('FINAL')]);
  assert.equal(output(events), 'FINAL');
});

test('SDK metadata-only partial does not suppress the complete answer', async () => {
  const events = await project([
    partial({ type: 'message_start' }), assistant(text('FINAL')), result('FINAL'),
  ]);
  assert.equal(output(events), 'FINAL');
});

test('SDK reasoning-only partial preserves final text without duplicating reasoning', async () => {
  const events = await project([
    partial({ type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'think' } }),
    assistant({ type: 'thinking', thinking: 'think' }, text('FINAL')), result('FINAL'),
  ]);
  assert.equal(output(events), 'FINAL');
  assert.equal(output(events, 'reasoning.delta'), 'think');
});

test('SDK complete message fills only the missing streamed suffix', async () => {
  const events = await project([
    partial({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'hel' } }),
    assistant(text('hello')), result('hello'),
  ]);
  assert.equal(output(events), 'hello');
  assert.deepEqual(events.filter((e) => e.type === 'message.delta').map((e) => e.payload.text), ['hel', 'lo']);
});

test('SDK partial tracking is scoped to each assistant message in a tool turn', async () => {
  const events = await project([
    partial({ type: 'message_start' }),
    partial({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Checking. ' } }),
    partial({ type: 'content_block_start', index: 1, content_block: { type: 'tool_use', id: 'tool-1', name: 'Read' } }),
    partial({ type: 'content_block_stop', index: 1 }),
    assistant(text('Checking. '), { type: 'tool_use', id: 'tool-1', name: 'Read' }),
    assistant(text('FINAL')), result('FINAL'),
  ]);
  assert.equal(output(events), 'Checking. FINAL');
  assert.equal(events.filter((e) => e.type === 'tool.started').length, 1);
});

test('SDK result-only answer remains a fallback after empty deltas', async () => {
  const events = await project([
    partial({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: '' } }), result('FINAL'),
  ]);
  assert.equal(output(events), 'FINAL');
});

test('SDK usage retains independent cache counts and inclusive input totals', async () => {
  const events = await project([result('FINAL', {
    input_tokens: 10, output_tokens: 12, cache_read_input_tokens: 90, cache_creation_input_tokens: 20,
  })]);
  assert.deepEqual(events.find((e) => e.type === 'usage.updated').payload, {
    input_tokens: 120, output_tokens: 12, total_tokens: 132,
    cache_read_tokens: 90, cache_creation_tokens: 20,
    provenance: 'provider_reported_unverified',
  });
});

test('SDK absent cache counts stay absent and explicit zero stays zero', async () => {
  for (const extra of [{}, { cache_read_input_tokens: 0, cache_creation_input_tokens: 0 }]) {
    const events = await project([result('FINAL', { input_tokens: 10, output_tokens: 2, ...extra })]);
    const usage = events.find((e) => e.type === 'usage.updated').payload;
    assert.equal(usage.input_tokens, 10);
    assert.equal(usage.total_tokens, 12);
    assert.equal(usage.cache_read_tokens, extra.cache_read_input_tokens);
    assert.equal(usage.cache_creation_tokens, extra.cache_creation_input_tokens);
  }
});
