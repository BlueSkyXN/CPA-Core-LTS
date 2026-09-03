export const RUNNER_PROTOCOL_VERSION = 1;
export const RUNNER_NAME = "cpa-qoder-runner";
export const RUNNER_VERSION = "0.1.0";
export const QODER_SDK_VERSION = "1.0.10";

export type QoderTransport = "sdk_cli" | "direct_openai";

export type AuthSpec =
  | { mode: "pat"; env_var: string; account_id?: string; transport?: QoderTransport }
  | { mode: "access_token"; env_var: string; account_id?: string; transport?: QoderTransport }
  | { mode: "local_cli"; profile_id?: string };

export type PermissionRule = {
  tool_name: string;
  behavior: "allow" | "deny" | "allow_with_modified_input" | "cancel_turn";
  modified_input?: Record<string, unknown>;
};

export type FixedPermissionPolicy = {
  default: "deny" | "cancel_turn";
  rules?: PermissionRule[];
};

export type QoderInputBlock =
  | { type: "text"; text: string }
  | {
      type: "image";
      source:
        | { type: "base64"; media_type: "image/png" | "image/jpeg" | "image/gif" | "image/webp"; data: string }
        | { type: "url"; url: string };
    };

export type FixedMcpServerConfig =
  | { type?: "stdio"; command: string; args?: string[]; env?: Record<string, string> }
  | { type: "sse" | "http"; url: string; headers?: Record<string, string> };

export type Correlation = {
  request_id: string;
  execution_session_id: string;
  turn_id: string;
  provider: "qoder";
  auth_id?: string;
  auth_index?: string;
};

export type StartParams = Correlation & {
  prompt: string;
  system_prompt?: string;
  content?: QoderInputBlock[];
  model: string;
  auth: AuthSpec;
  permission_policy: FixedPermissionPolicy;
  skills?: string[];
  setting_sources?: Array<"user" | "project" | "local">;
  allowed_tools?: string[];
  disallowed_tools?: string[];
  mcp_servers?: Record<string, FixedMcpServerConfig>;
  /** Original bounded Chat Completions object, preserved for direct transport. */
  chat_request?: Record<string, unknown>;
};

export type ModelsParams = {
  auth: AuthSpec;
  cache_ttl_ms?: number;
  models_endpoint?: string;
  models_json?: string;
};

export type ReadinessParams = {
  auth?: AuthSpec;
};

export type CancelParams = {
  request_id: string;
  execution_session_id: string;
};

export type CloseParams = {
  execution_session_id: string;
};

export type RunnerRequest = {
  protocol_version: number;
  id: string;
  method: "handshake" | "readiness" | "models" | "start" | "cancel" | "close" | "shutdown";
  params?: unknown;
};

export type RunnerError = {
  code: string;
  message: string;
  retryable?: boolean;
};

export type RunnerResponse = {
  protocol_version: number;
  type: "response";
  id: string;
  ok: boolean;
  result?: unknown;
  error?: RunnerError;
};

export type AgentEventType =
  | "session.created"
  | "turn.started"
  | "message.delta"
  | "reasoning.delta"
  | "tool.started"
  | "tool.updated"
  | "tool.completed"
  | "permission.required"
  | "usage.updated"
  | "warning"
  | "turn.completed"
  | "turn.failed"
  | "turn.cancelled"
  | "session.closed";

export type AgentEventV1 = Correlation & {
  schema_version: 1;
  type: AgentEventType;
  sequence: number;
  timestamp: string;
  payload?: unknown;
};

export type RunnerEventFrame = {
  protocol_version: number;
  type: "event";
  request_id: string;
  event: AgentEventV1;
};

export type ModelRecord = {
  id: string;
  display_name: string;
  description?: string;
  source?: string;
  is_default?: boolean;
  is_enabled?: boolean;
  is_reasoning?: boolean;
  is_vl?: boolean;
  max_input_tokens?: number;
  max_output_tokens?: number;
  reasoning_efforts?: string[];
  default_reasoning_effort?: string;
  supports_disabled?: boolean;
  available_context_windows?: number[];
  default_context_window?: number;
};

export interface ActiveTurn {
  cancel(): Promise<void>;
}

export interface QoderAdapter {
  readonly transport?: QoderTransport;
  readiness(auth?: AuthSpec): Promise<{ auth_ready: boolean; message: string }>;
  models(params: ModelsParams): Promise<ModelRecord[]>;
  start(params: StartParams, emit: (event: AgentEventV1) => Promise<void>): Promise<ActiveTurn>;
  close(executionSessionID: string): Promise<void>;
  shutdown(): Promise<void>;
}
