import { describe, expect, it } from 'vitest'
import { assignmentFormToPayload, assignmentToForm, emptyAssignmentForm } from './assignmentEditor'
import type { Assignment } from '../api/types'

describe('assignment editor helpers', () => {
  it('serializes a complete assignment create payload', () => {
    const form = emptyAssignmentForm('agent-1', 'duty-1')
    form.enabled = true
    form.triggerKind = 'event-subscription'
    form.schedule = ''
    form.filterJson = '{"source":"gitlab","event_type":"merge_request"}'
    form.outputsJson = '[{"plugin":"gitlab","action":"comment","params":{"path":"notes"}}]'
    form.configJson = '{"severity":"high"}'
    form.backendName = 'claude-prod'
    form.backendModel = 'opus'
    form.backendEffort = 'high'
    form.taskPromptOverride = 'Review this merge request.'
    form.extraInstructions = 'Be concise.'

    expect(assignmentFormToPayload(form, true)).toEqual({
      agent_id: 'agent-1',
      duty_id: 'duty-1',
      enabled: true,
      trigger: { kind: 'event-subscription', filter: { source: 'gitlab', event_type: 'merge_request' } },
      outputs: [{ plugin: 'gitlab', action: 'comment', params: { path: 'notes' } }],
      config: { severity: 'high' },
      backend: { name: 'claude-prod', model: 'opus', effort: 'high' },
      task_prompt_override: 'Review this merge request.',
      extra_instructions: 'Be concise.',
    })
  })

  it('round trips an existing assignment into editable form fields', () => {
    const assignment: Assignment = {
      id: 'a1',
      agent_id: 'agent-1',
      duty_id: 'duty-1',
      enabled: false,
      trigger: { kind: 'cron', schedule: '0 9 * * 1-5' },
      outputs: [{ plugin: 'email', action: 'send', params: null }],
      config: { channel: 'ops' },
      backend: { name: 'claude-prod' },
      task_prompt_override: null,
      extra_instructions: 'Escalate blockers.',
      created_at: '',
      updated_at: '',
    }

    const form = assignmentToForm(assignment)

    expect(form.triggerKind).toBe('cron')
    expect(form.schedule).toBe('0 9 * * 1-5')
    expect(JSON.parse(form.outputsJson)).toEqual([{ plugin: 'email', action: 'send', params: null }])
    expect(JSON.parse(form.configJson)).toEqual({ channel: 'ops' })
    expect(form.backendName).toBe('claude-prod')
    expect(form.extraInstructions).toBe('Escalate blockers.')
  })
})
