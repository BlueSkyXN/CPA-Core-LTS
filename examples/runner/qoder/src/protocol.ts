import { Writable } from "node:stream";

import type { RunnerEventFrame, RunnerResponse } from "./types.js";

export const DEFAULT_MAX_FRAME_BYTES = 1024 * 1024;
export const DEFAULT_MAX_QUEUE_FRAMES = 128;
export const DEFAULT_MAX_QUEUE_BYTES = 4 * 1024 * 1024;

type Frame = RunnerResponse | RunnerEventFrame;

export class ProtocolError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly retryable = false,
  ) {
    super(message);
    this.name = "ProtocolError";
  }
}

export class BoundedFrameWriter {
  private readonly queue: Array<{ raw: string; bytes: number; resolve: () => void; reject: (error: Error) => void }> = [];
  private queuedBytes = 0;
  private draining = false;
  private closed = false;

  constructor(
    private readonly output: Writable,
    private readonly maxFrames = DEFAULT_MAX_QUEUE_FRAMES,
    private readonly maxBytes = DEFAULT_MAX_QUEUE_BYTES,
    private readonly maxFrameBytes = DEFAULT_MAX_FRAME_BYTES,
  ) {}

  write(frame: Frame): Promise<void> {
    if (this.closed) {
      return Promise.reject(new ProtocolError("writer_closed", "runner output is closed"));
    }
    const raw = `${JSON.stringify(frame)}\n`;
    const bytes = Buffer.byteLength(raw);
    if (bytes > this.maxFrameBytes) {
      return Promise.reject(new ProtocolError("frame_too_large", "runner output frame exceeds the configured limit"));
    }
    if (this.queue.length >= this.maxFrames || this.queuedBytes + bytes > this.maxBytes) {
      return Promise.reject(new ProtocolError("queue_full", "runner output queue is full", true));
    }
    return new Promise<void>((resolve, reject) => {
      this.queue.push({ raw, bytes, resolve, reject });
      this.queuedBytes += bytes;
      void this.flush();
    });
  }

  close(): void {
    this.closed = true;
    const error = new ProtocolError("writer_closed", "runner output is closed");
    for (const item of this.queue.splice(0)) {
      item.reject(error);
    }
    this.queuedBytes = 0;
  }

  private async flush(): Promise<void> {
    if (this.draining) return;
    this.draining = true;
    try {
      while (!this.closed && this.queue.length > 0) {
        const item = this.queue.shift()!;
        this.queuedBytes -= item.bytes;
        try {
          if (!this.output.write(item.raw)) {
            await new Promise<void>((resolve, reject) => {
              const onDrain = () => {
                cleanup();
                resolve();
              };
              const onError = (error: Error) => {
                cleanup();
                reject(error);
              };
              const cleanup = () => {
                this.output.off("drain", onDrain);
                this.output.off("error", onError);
              };
              this.output.once("drain", onDrain);
              this.output.once("error", onError);
            });
          }
          item.resolve();
        } catch (error) {
          item.reject(error instanceof Error ? error : new Error("runner output failed"));
        }
      }
    } finally {
      this.draining = false;
    }
  }
}

export function safeError(error: unknown): ProtocolError {
  if (error instanceof ProtocolError) return error;
  const candidate = error as { code?: unknown; exitCode?: unknown; name?: unknown } | null;
  if (candidate?.exitCode === 41) {
    return new ProtocolError("auth_expired", "Qoder authentication was rejected");
  }
  if (candidate?.code === "auth_access_token_env_var_not_configured") {
    return new ProtocolError("auth_not_configured", "Qoder PAT environment source is not configured");
  }
  if (candidate?.name === "ProtocolVersionMismatchError") {
    return new ProtocolError("sdk_cli_version_mismatch", "Qoder SDK and CLI protocol versions are incompatible");
  }
  return new ProtocolError("runner_error", "Qoder runner operation failed", true);
}

export function redactStderr(text: string, secrets: string[]): string {
  let safe = String(text);
  for (const secret of secrets) {
    if (secret) safe = safe.split(secret).join("[REDACTED_SECRET]");
  }
  safe = safe
    .replace(/(authorization\s*[:=]\s*bearer\s+)[^\s,;]+/gi, "$1[REDACTED_SECRET]")
    .replace(/((?:access[_-]?token|api[_-]?key|secret|password)\s*[:=]\s*)[^\s,;]+/gi, "$1[REDACTED_SECRET]");
  return safe.slice(0, 2000);
}
