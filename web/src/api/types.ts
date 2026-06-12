// TypeScript mirrors of the /api/v1 JSON wire format (snake_case, see
// internal/domain/types.go). Optional pointer fields are `| null`.

export interface BackendRef {
  name: string
  model?: string
  effort?: string
}

export interface Agent {
  id: string
  name: string
  role: string
  system_prompt: string
  default_backend: BackendRef
  enabled: boolean
  avatar_url: string | null
  hired_at: string | null
  created_at: string
  updated_at: string
}

export interface OutputActionType {
  plugin: string
  action: string
}

export interface Duty {
  id: string
  name: string
  role: string
  description: string
  trigger_kinds: string[] | null
  prompt: string
  required_tools: string[] | null
  output_actions: OutputActionType[] | null
  config_schema: Record<string, unknown> | null
  backend: BackendRef | null
  created_at: string
  updated_at: string
}

export interface TriggerConfig {
  kind: string
  schedule?: string
  filter?: Record<string, unknown>
}

export interface OutputBinding {
  plugin: string
  action: string
  params: Record<string, unknown> | null
  for_each?: string
}

export interface Assignment {
  id: string
  name: string
  agent_id: string
  duty_id: string
  enabled: boolean
  trigger: TriggerConfig
  outputs: OutputBinding[] | null
  config: Record<string, unknown> | null
  backend: BackendRef | null
  task_prompt_override: string | null
  extra_instructions: string | null
  created_at: string
  updated_at: string
}

export interface LLMResult {
  status: number
  summary: string
  output: Record<string, unknown> | null
  transcript: string
  tokens: number
  cost: number
}

export interface OutputDelivery {
  plugin: string
  action: string
  params: Record<string, unknown> | null
  status: string
  error?: string
}

export type RunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'skipped'

export interface Run {
  id: string
  assignment_id: string
  agent_id: string
  duty_id: string
  trigger_kind: string
  event_id: string | null
  rendered_system_prompt: string
  rendered_prompt: string
  llm_result: LLMResult | null
  outputs_delivered: OutputDelivery[] | null
  status: RunStatus
  tokens: number
  cost: number
  started_at: string
  finished_at: string | null
  error: string | null
}

export interface FleetEvent {
  id: string
  source_plugin: string
  event_type: string
  payload_raw: unknown
  payload_norm: Record<string, unknown> | null
  identity: string
  dedup_key: string
  status: 'pending' | 'dispatched'
  received_at: string
  dispatched_at: string | null
}

export interface AgentStats {
  agent_id: string
  total_runs: number
  runs_last_30d: number
  success_rate: number
  skip_rate: number
  total_tokens: number
  total_cost_usd: number
  tokens_last_30d: number
  cost_last_30d_usd: number
  outputs_delivered: number
  outputs_last_30d: number
  avg_run_duration_s: number
  last_run_at: string | null
}

// GET /api/v1/agents/{id} returns this envelope (stats is null when the
// stats query failed — detail still loads).
export interface AgentDetailResponse {
  agent: Agent
  stats: AgentStats | null
}

export interface BackendView {
  name: string
  kind: string
  auth_mode: string
  model?: string
  default_effort?: string
}

export interface SecretInfo {
  name: string
  encrypted: boolean
}

export interface User {
  id: string
  username: string
  role: 'admin' | 'viewer'
  created_at: string
  updated_at: string
}

export interface Me {
  username: string
  role: 'admin' | 'viewer'
}

// SSE payload from GET /api/v1/stream (unnamed `data:` messages).
export interface StreamMsg {
  event: 'run_started' | 'run_finished'
  id: string
  assignment_id: string
  agent_id: string
  duty_id: string
  trigger_kind: string
  status: RunStatus
  tokens: number
  cost: number
}
