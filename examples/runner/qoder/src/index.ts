#!/usr/bin/env node
import process from "node:process";

import { ProtocolError } from "./protocol.js";
import { QoderSDKAdapter } from "./qoder.js";
import { RunnerServer, runJSONLServer } from "./server.js";

type CLIOptions = {
  cliPath: string;
  cwd: string;
  maxQueueFrames: number;
};

function parseArgs(args: string[]): CLIOptions {
  let cliPath = "";
  let cwd = process.cwd();
  let maxQueueFrames = 128;
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--stdio") continue;
    if (arg === "--cli-path") cliPath = String(args[++index] ?? "");
    else if (arg === "--cwd") cwd = String(args[++index] ?? "");
    else if (arg === "--max-queue-frames") maxQueueFrames = Number(args[++index]);
    else if (arg === "--version") {
      process.stdout.write("cpa-qoder-runner 0.1.0\n");
      process.exit(0);
    } else throw new ProtocolError("invalid_argument", `unsupported runner argument: ${arg}`);
  }
  if (!cliPath) throw new ProtocolError("cli_path_required", "--cli-path is required; bundled Qoder CLI fallback is disabled");
  if (!Number.isInteger(maxQueueFrames) || maxQueueFrames < 1 || maxQueueFrames > 4096) {
    throw new ProtocolError("invalid_argument", "--max-queue-frames must be between 1 and 4096");
  }
  return { cliPath, cwd, maxQueueFrames };
}

async function main(): Promise<void> {
  const options = parseArgs(process.argv.slice(2));
  const adapter = new QoderSDKAdapter(options.cliPath, options.cwd);
  const server = new RunnerServer(adapter, process.stdout, options.maxQueueFrames);
  await runJSONLServer(server, process.stdin);
}

main().catch((error) => {
  const message = error instanceof ProtocolError ? error.message : "Qoder runner failed";
  process.stderr.write(`${message}\n`);
  process.exitCode = 1;
});
