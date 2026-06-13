import type { Assignment, BackendRef, OutputBinding, TriggerConfig } from '../api/types'

export interface AssignmentForm {
  agentID: string
  dutyID: string
  enabled: boolean
  triggerKind: string
  schedule: string
  filterJson: string
  outputsJson: string
  configJson: string
  backendName: string
  backendModel: string
  backendEffort: string
  taskPromptOverride: string
  extraInstructions: string
}

export interface AssignmentPayload {
  agent_id?: string
  duty_id?: string
  enabled: boolean
  trigger: TriggerConfig
  outputs: OutputBinding[]
  config: Record<string, unknown>
  backend: BackendRef | null
  task_prompt_override: string | null
  extra_instructions: string | null
}

function prettyJSON(value: unknown, fallback: string): string {
  if (value == null) return fallback
  return JSON.stringify(value, null, 2)
}

function parseObject(text: string, label: string): Record<string, unknown> {
  const trimmed = text.trim()
  if (!trimmed) return {}
  const parsed = JSON.parse(trimmed) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object`)
  }
  return parsed as Record<string, unknown>
}

function parseOutputs(text: string): OutputBinding[] {
  const trimmed = text.trim()
  if (!trimmed) return []
  const parsed = JSON.parse(trimmed) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('outputs must be a JSON array')
  }
  return parsed as OutputBinding[]
}

export function emptyAssignmentForm(agentID: string, dutyID = ''): AssignmentForm {
  return {
    agentID,
    dutyID,
    enabled: true,
    triggerKind: 'manual',
    schedule: '',
    filterJson: '{}',
    outputsJson: '[]',
    configJson: '{}',
    backendName: '',
    backendModel: '',
    backendEffort: '',
    taskPromptOverride: '',
    extraInstructions: '',
  }
}

export function assignmentToForm(assignment: Assignment): AssignmentForm {
  return {
    agentID: assignment.agent_id,
    dutyID: assignment.duty_id,
    enabled: assignment.enabled,
    triggerKind: assignment.trigger.kind || 'manual',
    schedule: assignment.trigger.schedule ?? '',
    filterJson: prettyJSON(assignment.trigger.filter, '{}'),
    outputsJson: prettyJSON(assignment.outputs, '[]'),
    configJson: prettyJSON(assignment.config, '{}'),
    backendName: assignment.backend?.name ?? '',
    backendModel: assignment.backend?.model ?? '',
    backendEffort: assignment.backend?.effort ?? '',
    taskPromptOverride: assignment.task_prompt_override ?? '',
    extraInstructions: assignment.extra_instructions ?? '',
  }
}

export function assignmentFormToPayload(form: AssignmentForm, includeIDs: boolean): AssignmentPayload {
  const trigger: TriggerConfig = { kind: form.triggerKind }
  if (form.triggerKind === 'cron') {
    trigger.schedule = form.schedule.trim()
  }
  if (form.triggerKind === 'event-subscription') {
    trigger.filter = parseObject(form.filterJson, 'filter')
  }

  const backendName = form.backendName.trim()
  const backend = backendName
    ? {
        name: backendName,
        ...(form.backendModel.trim() ? { model: form.backendModel.trim() } : {}),
        ...(form.backendEffort.trim() ? { effort: form.backendEffort.trim() } : {}),
      }
    : null

  const payload: AssignmentPayload = {
    enabled: form.enabled,
    trigger,
    outputs: parseOutputs(form.outputsJson),
    config: parseObject(form.configJson, 'config'),
    backend,
    task_prompt_override: form.taskPromptOverride.trim() ? form.taskPromptOverride : null,
    extra_instructions: form.extraInstructions.trim() ? form.extraInstructions : null,
  }
  if (includeIDs) {
    payload.agent_id = form.agentID
    payload.duty_id = form.dutyID
  }
  return payload
}
