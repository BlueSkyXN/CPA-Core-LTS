#!/usr/bin/env node
import process from "node:process";

import { ProtocolError } from "./protocol.js";
import { DirectOpenAIAdapter } from "./direct.js";
import { QoderSDKAdapter } from "./qoder.js";
import { RunnerServer, runJSONLServer } from "./server.js";
import type { ModelRecord, QoderTransport } from "./types.js";

type CLIOptions = {
  transport: QoderTransport;
  cliPath: string;
  cwd: string;
  maxQueueFrames: number;
  directEndpoint: string;
  directModelsEndpoint: string;
  directAuthEndpoint: string;
  directTokenMode: "auto" | "bearer" | "pat_exchange";
  openAPIEndpoint: string;
  openAPIUserAgent: string;
  directModels: ModelRecord[];
};

function parseArgs(args: string[]): CLIOptions {
  let transport: QoderTransport = "sdk_cli";
  let cliPath = "";
  let cwd = process.cwd();
  let maxQueueFrames = 128;
  let directEndpoint = "";
  let directModelsEndpoint = "";
  let directAuthEndpoint = "";
  let directTokenMode: CLIOptions["directTokenMode"] = "auto";
  let openAPIEndpoint = "";
  let openAPIUserAgent = "qoder/1.1.40";
  let directModels: ModelRecord[] = [];
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--stdio") continue;
    if (arg === "--transport") {
      const value = String(args[++index] ?? "");
      if (value !== "sdk_cli" && value !== "direct_openai") throw new ProtocolError("invalid_argument", "--transport must be sdk_cli or direct_openai");
      transport = value;
    } else if (arg === "--cli-path") cliPath = String(args[++index] ?? "");
    else if (arg === "--cwd") cwd = String(args[++index] ?? "");
    else if (arg === "--max-queue-frames") maxQueueFrames = Number(args[++index]);
    else if (arg === "--direct-endpoint") directEndpoint = String(args[++index] ?? "");
    else if (arg === "--direct-models-endpoint") directModelsEndpoint = String(args[++index] ?? "");
    else if (arg === "--direct-auth-endpoint") directAuthEndpoint = String(args[++index] ?? "");
    else if (arg === "--direct-token-mode") {
      const value = String(args[++index] ?? "");
      if (value !== "auto" && value !== "bearer" && value !== "pat_exchange") throw new ProtocolError("invalid_argument", "--direct-token-mode must be auto, bearer, or pat_exchange");
      directTokenMode = value;
    }
    else if (arg === "--openapi-endpoint") openAPIEndpoint = String(args[++index] ?? "");
    else if (arg === "--openapi-user-agent") openAPIUserAgent = String(args[++index] ?? "");
    else if (arg === "--direct-models-json") {
      const value = String(args[++index] ?? "");
      try {
        const parsed = JSON.parse(value);
        if (!Array.isArray(parsed)) throw new Error("array required");
        directModels = parsed as ModelRecord[];
      } catch {
        throw new ProtocolError("invalid_argument", "--direct-models-json must be a JSON array");
      }
    }
    else if (arg === "--version") {
      process.stdout.write("cpa-qoder-runner 0.1.0\n");
      process.exit(0);
    } else throw new ProtocolError("invalid_argument", `unsupported runner argument: ${arg}`);
  }
  if (transport === "sdk_cli" && !cliPath) throw new ProtocolError("cli_path_required", "--cli-path is required for sdk_cli; bundled Qoder CLI fallback is disabled");
  if (transport === "direct_openai" && !directEndpoint) throw new ProtocolError("direct_endpoint_required", "--direct-endpoint is required for direct_openai");
  if (directAuthEndpoint && openAPIEndpoint && directAuthEndpoint.replace(/\/+$/, "") !== openAPIEndpoint.replace(/\/+$/, "")) throw new ProtocolError("direct_auth_config", "--direct-auth-endpoint and --openapi-endpoint must match");
  if (!Number.isInteger(maxQueueFrames) || maxQueueFrames < 1 || maxQueueFrames > 4096) {
    throw new ProtocolError("invalid_argument", "--max-queue-frames must be between 1 and 4096");
  }
  return { transport, cliPath, cwd, maxQueueFrames, directEndpoint, directModelsEndpoint, directAuthEndpoint, directTokenMode, openAPIEndpoint, openAPIUserAgent, directModels };
}

async function main(): Promise<void> {
  const options = parseArgs(process.argv.slice(2));
  const adapter = options.transport === "direct_openai"
    ? new DirectOpenAIAdapter({
      endpoint: options.directEndpoint,
      modelsEndpoint: options.directModelsEndpoint || undefined,
      authEndpoint: options.directAuthEndpoint || undefined,
      tokenMode: options.directTokenMode,
      openAPIEndpoint: options.openAPIEndpoint || undefined,
      openAPIUserAgent: options.openAPIUserAgent,
      models: options.directModels,
    })
    : new QoderSDKAdapter(options.cliPath, options.cwd, {
      openAPIEndpoint: options.openAPIEndpoint || undefined,
      openAPIUserAgent: options.openAPIUserAgent,
    });
  const server = new RunnerServer(adapter, process.stdout, options.maxQueueFrames);
  await runJSONLServer(server, process.stdin);
}

main().catch((error) => {
  const message = error instanceof ProtocolError ? error.message : "Qoder runner failed";
  process.stderr.write(`${message}\n`);
  process.exitCode = 1;
});
