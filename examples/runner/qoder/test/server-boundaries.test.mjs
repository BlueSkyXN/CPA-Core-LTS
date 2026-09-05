import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import test from 'node:test';
import { RunnerServer, runJSONLServer } from '../dist/server.js';

async function within(promise) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('stdin handling hung')), 1000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

for (const trailingNewline of [true, false]) {
  test(`oversized frame closes a still-open stdin pipe (newline=${trailingNewline})`, async () => {
    const input = new PassThrough();
    const output = new PassThrough();
    let raw = '';
    output.on('data', (chunk) => { raw += chunk; });
    let closed = false;
    const adapter = {
      transport: 'direct_openai',
      shutdown: async () => { closed = true; },
    };
    const run = runJSONLServer(new RunnerServer(adapter, output), input, 128);
    input.write(JSON.stringify({
      protocol_version: 1,
      id: 'real-id',
      params: { id: 'nested-id', body: 'x'.repeat(512) },
      method: 'handshake',
    }) + (trailingNewline ? '\n' : ''));
    try {
      await within(run);
    } finally {
      input.destroy();
    }
    const frame = JSON.parse(raw.trim());
    assert.equal(frame.id, 'real-id');
    assert.equal(frame.ok, false);
    assert.equal(frame.error.code, 'frame_too_large');
    assert.equal(closed, true);
  });
}

test('top-level malformed JSON values produce a protocol error, not a server crash', async () => {
  const output = new PassThrough();
  let raw = '';
  output.on('data', (chunk) => { raw += chunk; });
  const server = new RunnerServer({ shutdown: async () => {} }, output);
  await server.handle(null);
  const frame = JSON.parse(raw.trim());
  assert.equal(frame.ok, false);
  assert.equal(frame.error.code, 'invalid_request');
  await server.close();
});
