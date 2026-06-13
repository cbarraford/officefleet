import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { AgentDetailResponse, Assignment, Duty, Run } from '../api/types'
import AvatarBubble from '../components/AvatarBubble'
import Badge from '../components/Badge'
import Card from '../components/Card'
import JsonView from '../components/JsonView'
import Modal from '../components/Modal'
import StatusPill from '../components/StatusPill'
import Table from '../components/Table'
import { fmtCost, fmtDate, fmtDateTime, fmtPercent } from '../lib/format'
import { toast } from '../lib/toast'
import { assignmentFormToPayload, assignmentToForm, emptyAssignmentForm, type AssignmentForm } from './assignmentEditor'

function triggerSummary(a: Assignment): string {
  switch (a.trigger.kind) {
    case 'cron':
      return a.trigger.schedule ?? ''
    case 'event-subscription':
      return a.trigger.filter ? JSON.stringify(a.trigger.filter) : 'any event'
    default:
      return '' // manual/continuous: the kind itself says it all
  }
}

function RunNowModal({ assignment, onClose, onRan }: { assignment: Assignment; onClose: () => void; onRan: () => void }) {
  const [paramsText, setParamsText] = useState('{}')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    let params: Record<string, unknown>
    try {
      params = JSON.parse(paramsText || '{}') as Record<string, unknown>
    } catch {
      setError('params must be valid JSON')
      return
    }
    setBusy(true)
    try {
      await api.post(`/api/v1/assignments/${assignment.id}/run`, { params })
      onRan()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'run failed to start')
    }
  }

  return (
    <Modal title="Run now" onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Params (JSON)</span>
          <textarea className="mono" rows={6} value={paramsText} onChange={(e) => setParamsText(e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Starting…' : 'Run'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

function AssignmentEditorModal({
  agentID,
  duties,
  assignment,
  onClose,
  onSaved,
}: {
  agentID: string
  duties: Duty[]
  assignment: Assignment | null
  onClose: () => void
  onSaved: () => void
}) {
  const [form, setForm] = useState<AssignmentForm>(() =>
    assignment ? assignmentToForm(assignment) : emptyAssignmentForm(agentID, duties[0]?.id ?? ''),
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const selectedDuty = duties.find((d) => d.id === form.dutyID)
  const triggerKinds = selectedDuty?.trigger_kinds?.length ? selectedDuty.trigger_kinds : ['manual', 'cron', 'event-subscription', 'continuous']

  const setField = <K extends keyof AssignmentForm>(key: K, value: AssignmentForm[K]) => {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    let payload: ReturnType<typeof assignmentFormToPayload>
    try {
      payload = assignmentFormToPayload(form, !assignment)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'invalid assignment fields')
      return
    }
    setBusy(true)
    try {
      if (assignment) {
        await api.patch(`/api/v1/assignments/${assignment.id}`, payload)
      } else {
        await api.post('/api/v1/assignments', payload)
      }
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  const deleteAssignment = async () => {
    if (!assignment) return
    if (!confirmDelete) {
      setConfirmDelete(true)
      return
    }
    setBusy(true)
    try {
      await api.del(`/api/v1/assignments/${assignment.id}`)
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <Modal title={assignment ? 'Edit assignment' : 'New assignment'} onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Duty</span>
          <select value={form.dutyID} onChange={(e) => setField('dutyID', e.target.value)} disabled={!!assignment}>
            <option value="">Select duty</option>
            {duties.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        <div className="row mb">
          <label className="row" style={{ width: 'auto', gap: 6 }}>
            <input
              type="checkbox"
              style={{ width: 'auto' }}
              checked={form.enabled}
              onChange={(e) => setField('enabled', e.target.checked)}
            />
            Enabled
          </label>
        </div>
        <label className="field">
          <span>Trigger</span>
          <select value={form.triggerKind} onChange={(e) => setField('triggerKind', e.target.value)}>
            {triggerKinds.map((kind) => (
              <option key={kind} value={kind}>
                {kind}
              </option>
            ))}
          </select>
        </label>
        {form.triggerKind === 'cron' && (
          <label className="field">
            <span>Cron schedule</span>
            <input value={form.schedule} onChange={(e) => setField('schedule', e.target.value)} placeholder="0 9 * * 1-5" />
          </label>
        )}
        {form.triggerKind === 'event-subscription' && (
          <label className="field">
            <span>Event filter (JSON)</span>
            <textarea className="mono" rows={5} value={form.filterJson} onChange={(e) => setField('filterJson', e.target.value)} />
          </label>
        )}
        <label className="field">
          <span>Outputs (JSON array)</span>
          <textarea className="mono" rows={5} value={form.outputsJson} onChange={(e) => setField('outputsJson', e.target.value)} />
        </label>
        <label className="field">
          <span>Config (JSON object)</span>
          <textarea className="mono" rows={4} value={form.configJson} onChange={(e) => setField('configJson', e.target.value)} />
        </label>
        <div className="row">
          <label className="field" style={{ flex: 1 }}>
            <span>Backend</span>
            <input value={form.backendName} onChange={(e) => setField('backendName', e.target.value)} placeholder="default" />
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span>Model</span>
            <input value={form.backendModel} onChange={(e) => setField('backendModel', e.target.value)} />
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span>Effort</span>
            <input value={form.backendEffort} onChange={(e) => setField('backendEffort', e.target.value)} />
          </label>
        </div>
        <label className="field">
          <span>Task prompt override</span>
          <textarea rows={4} value={form.taskPromptOverride} onChange={(e) => setField('taskPromptOverride', e.target.value)} />
        </label>
        <label className="field">
          <span>Extra instructions</span>
          <textarea rows={4} value={form.extraInstructions} onChange={(e) => setField('extraInstructions', e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !form.dutyID}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          {assignment && (
            <button type="button" className={`danger ${confirmDelete ? 'confirming' : ''}`} disabled={busy} onClick={deleteAssignment}>
              {confirmDelete ? 'Confirm?' : 'Delete'}
            </button>
          )}
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

function parseInspectorValue(text: string): unknown {
  const trimmed = text.trim()
  if (!trimmed) return ''
  try {
    return JSON.parse(trimmed)
  } catch {
    return text
  }
}

function AssignmentStateModal({ assignment, isAdmin, onClose }: { assignment: Assignment; isAdmin: boolean; onClose: () => void }) {
  const [stateData, setStateData] = useState<Record<string, unknown> | null>(null)
  const [memory, setMemory] = useState<unknown[] | null>(null)
  const [stateKey, setStateKey] = useState('')
  const [stateValue, setStateValue] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(() => {
    Promise.all([
      api.get<Record<string, unknown>>(`/api/v1/assignments/${assignment.id}/state`),
      api.get<unknown[]>(`/api/v1/assignments/${assignment.id}/memory`),
    ]).then(
      ([s, m]) => {
        setStateData(s ?? {})
        setMemory(m ?? [])
      },
      (err) => setError(err instanceof ApiError ? err.message : 'failed to load state'),
    )
  }, [assignment.id])

  useEffect(load, [load])

  const saveState = async (e: FormEvent) => {
    e.preventDefault()
    if (!stateKey.trim()) return
    setBusy(true)
    setError('')
    try {
      await api.put(`/api/v1/assignments/${assignment.id}/state/${encodeURIComponent(stateKey.trim())}`, {
        value: parseInspectorValue(stateValue),
      })
      setStateKey('')
      setStateValue('')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'save failed')
    } finally {
      setBusy(false)
    }
  }

  const deleteState = async (key: string) => {
    setBusy(true)
    setError('')
    try {
      await api.del(`/api/v1/assignments/${assignment.id}/state/${encodeURIComponent(key)}`)
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'delete failed')
    } finally {
      setBusy(false)
    }
  }

  const appendMemory = async (e: FormEvent) => {
    e.preventDefault()
    if (!note.trim()) return
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/assignments/${assignment.id}/memory`, { note: parseInspectorValue(note) })
      setNote('')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'append failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Assignment state" onClose={onClose}>
      {error && <div className="form-error">{error}</div>}
      <JsonView label="State" value={stateData ?? {}} open />
      {stateData &&
        Object.keys(stateData).map((key) => (
          <div className="row mb" key={key}>
            <span className="mono">{key}</span>
            <div className="spacer" />
            {isAdmin && (
              <button className="small danger" disabled={busy} onClick={() => deleteState(key)}>
                Delete
              </button>
            )}
          </div>
        ))}
      {isAdmin && (
        <form onSubmit={saveState} className="mb">
          <div className="row">
            <input placeholder="state key" value={stateKey} onChange={(e) => setStateKey(e.target.value)} />
            <input placeholder="value or JSON" value={stateValue} onChange={(e) => setStateValue(e.target.value)} />
            <button className="primary" type="submit" disabled={busy || !stateKey.trim()}>
              Save
            </button>
          </div>
        </form>
      )}
      <JsonView label="Memory" value={memory ?? []} open />
      {isAdmin && (
        <form onSubmit={appendMemory}>
          <label className="field">
            <span>Append memory note</span>
            <textarea className="mono" rows={4} value={note} onChange={(e) => setNote(e.target.value)} />
          </label>
          <button className="primary" type="submit" disabled={busy || !note.trim()}>
            Append
          </button>
        </form>
      )}
    </Modal>
  )
}

function RunDrawer({ run, onClose }: { run: Run; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="drawer">
      <div className="row between mb">
        <h2>
          Run <span className="mono dim">{run.id.slice(0, 8)}</span> <StatusPill status={run.status} />
        </h2>
        <button className="small" onClick={onClose}>
          Close
        </button>
      </div>
      <dl className="kv mb">
        <dt>Trigger</dt>
        <dd>{run.trigger_kind}</dd>
        <dt>Started</dt>
        <dd>{fmtDateTime(run.started_at)}</dd>
        <dt>Finished</dt>
        <dd>{fmtDateTime(run.finished_at)}</dd>
        <dt>Tokens / cost</dt>
        <dd>
          {run.tokens} / {fmtCost(run.cost)}
        </dd>
        {run.event_id && (
          <>
            <dt>Event</dt>
            <dd>
              <Link to={`/settings?tab=events&highlight=${run.event_id}`}>{run.event_id.slice(0, 8)}…</Link>
            </dd>
          </>
        )}
        {run.error && (
          <>
            <dt>Error</dt>
            <dd className="form-error">{run.error}</dd>
          </>
        )}
      </dl>
      {run.llm_result?.summary && (
        <Card title="Summary" className="mb">
          {run.llm_result.summary}
        </Card>
      )}
      <JsonView label="Rendered system prompt" text={run.rendered_system_prompt} />
      <JsonView label="Rendered task prompt" text={run.rendered_prompt} />
      {run.llm_result && <JsonView label="Transcript" text={run.llm_result.transcript} />}
      {run.llm_result?.output && <JsonView label="LLM output" value={run.llm_result.output} />}
      {run.outputs_delivered && run.outputs_delivered.length > 0 && (
        <JsonView label={`Outputs delivered (${run.outputs_delivered.length})`} value={run.outputs_delivered} />
      )}
    </div>
  )
}

export default function AgentDetail() {
  const { id } = useParams<{ id: string }>()
  const { isAdmin } = useSession()
  const [detail, setDetail] = useState<AgentDetailResponse | null>(null)
  const [assignments, setAssignments] = useState<Assignment[]>([])
  const [duties, setDuties] = useState<Duty[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [statusFilter, setStatusFilter] = useState('')
  const [runningAssignment, setRunningAssignment] = useState<Assignment | null>(null)
  const [editingAssignment, setEditingAssignment] = useState<Assignment | null | undefined>(undefined)
  const [stateAssignment, setStateAssignment] = useState<Assignment | null>(null)
  const [openRun, setOpenRun] = useState<Run | null>(null)

  const load = useCallback(() => {
    if (!id) return
    Promise.all([
      api.get<AgentDetailResponse>(`/api/v1/agents/${id}`),
      api.get<Assignment[]>('/api/v1/assignments'),
      api.get<Duty[]>('/api/v1/duties'),
    ]).then(
      ([d, asg, du]) => {
        setDetail(d)
        setAssignments((asg ?? []).filter((a) => a.agent_id === id))
        setDuties(du ?? [])
      },
      () => toast('error', 'failed to load agent'),
    )
  }, [id])

  const loadRuns = useCallback(() => {
    if (!id) return
    const status = statusFilter ? `&status=${statusFilter}` : ''
    api.get<Run[]>(`/api/v1/runs?agent_id=${id}&limit=50${status}`).then(
      (r) => setRuns(r ?? []),
      () => toast('error', 'failed to load runs'),
    )
  }, [id, statusFilter])

  useEffect(load, [load])
  useEffect(loadRuns, [loadRuns])

  const dutyName = useMemo(() => {
    const m = new Map(duties.map((d) => [d.id, d.name]))
    return (dutyID: string) => m.get(dutyID) ?? dutyID.slice(0, 8)
  }, [duties])

  const sortedRuns = useMemo(
    () => [...runs].sort((a, b) => b.started_at.localeCompare(a.started_at)),
    [runs],
  )

  const toggleAgent = async () => {
    if (!detail) return
    try {
      await api.patch(`/api/v1/agents/${detail.agent.id}`, { enabled: !detail.agent.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  const toggleAssignment = async (a: Assignment) => {
    try {
      await api.patch(`/api/v1/assignments/${a.id}`, { enabled: !a.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  const fileInput = useRef<HTMLInputElement>(null)

  const regenerateAvatar = async () => {
    if (!detail) return
    try {
      await api.post(`/api/v1/agents/${detail.agent.id}/avatar/regenerate`)
      toast('info', 'avatar generating — refreshing shortly…')
      window.setTimeout(load, 5000) // §6.1: async generation; poll once after ~5s
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'regenerate failed')
    }
  }

  const uploadAvatar = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // allow re-selecting the same file later
    if (!file || !detail) return
    if (file.type !== 'image/png') {
      toast('error', 'avatar must be a PNG')
      return
    }
    if (file.size > 1024 * 1024) {
      toast('error', 'avatar must be at most 1 MiB')
      return
    }
    try {
      await api.putRaw(`/api/v1/agents/${detail.agent.id}/avatar`, file, 'image/png')
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'upload failed')
    }
  }

  if (!detail) return <h1 className="dim">Loading…</h1>
  const { agent, stats } = detail

  return (
    <>
      <div className="row mb">
        <AvatarBubble name={agent.name} url={agent.avatar_url} size={64} />
        <div>
          <h1 style={{ marginBottom: 2 }}>{agent.name}</h1>
          <div className="dim">
            {agent.role || '—'} · hired {fmtDate(agent.hired_at)}
          </div>
        </div>
        <div className="spacer" />
        {!agent.enabled && <Badge text="Paused" kind="warn" />}
        {isAdmin && (
          <>
            <button className="small" onClick={regenerateAvatar}>
              Regenerate avatar
            </button>
            <button className="small" onClick={() => fileInput.current?.click()}>
              Upload avatar
            </button>
            <input ref={fileInput} type="file" accept="image/png" style={{ display: 'none' }} onChange={uploadAvatar} />
            <button className="small" onClick={toggleAgent}>
              {agent.enabled ? 'Pause' : 'Resume'}
            </button>
          </>
        )}
      </div>

      {stats && (
        <div className="grid cols-3 mb">
          <Card className="stat">
            <div className="num">{stats.runs_last_30d}</div>
            <div className="label">runs 30d ({stats.total_runs} total)</div>
          </Card>
          <Card className="stat">
            <div className="num">{fmtPercent(stats.success_rate)}</div>
            <div className="label">success · skip {fmtPercent(stats.skip_rate)}</div>
          </Card>
          <Card className="stat">
            <div className="num">{fmtCost(stats.cost_last_30d_usd)}</div>
            <div className="label">
              cost 30d · {stats.tokens_last_30d} tokens · {stats.outputs_last_30d} outputs
            </div>
          </Card>
        </div>
      )}

      <Card title="Assignments" className="mb">
        {isAdmin && (
          <div className="row mb">
            <button className="primary" onClick={() => setEditingAssignment(null)}>
              New assignment
            </button>
          </div>
        )}
        <Table
          columns={[
            { header: 'Duty', render: (a: Assignment) => dutyName(a.duty_id) },
            {
              header: 'Trigger',
              render: (a: Assignment) => (
                <>
                  {a.trigger.kind} <span className="dim mono">{triggerSummary(a)}</span>
                </>
              ),
            },
            { header: 'Backend', render: (a: Assignment) => a.backend?.name ?? 'default' },
            {
              header: 'Enabled',
              render: (a: Assignment) =>
                isAdmin ? (
                  <button className="small" onClick={() => toggleAssignment(a)}>
                    {a.enabled ? 'On' : 'Off'}
                  </button>
                ) : (
                  <span>{a.enabled ? 'On' : 'Off'}</span>
                ),
            },
            {
              header: '',
              render: (a: Assignment) =>
                isAdmin ? (
                  <div className="row wrap">
                    <button className="small" onClick={() => setEditingAssignment(a)}>
                      Edit
                    </button>
                    <button className="small" onClick={() => setStateAssignment(a)}>
                      State
                    </button>
                    <button className="small" onClick={() => setRunningAssignment(a)}>
                      Run now
                    </button>
                  </div>
                ) : (
                  <button className="small" onClick={() => setStateAssignment(a)}>
                    State
                  </button>
                ),
            },
          ]}
          rows={assignments}
          rowKey={(a) => a.id}
          empty="No duties assigned."
        />
      </Card>

      <Card title="Run history">
        <div className="row mb">
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} style={{ width: 180 }}>
            <option value="">all statuses</option>
            <option value="queued">queued</option>
            <option value="running">running</option>
            <option value="succeeded">succeeded</option>
            <option value="failed">failed</option>
            <option value="skipped">skipped</option>
          </select>
        </div>
        <Table
          columns={[
            { header: 'Status', render: (r: Run) => <StatusPill status={r.status} /> },
            { header: 'Duty', render: (r: Run) => dutyName(r.duty_id) },
            { header: 'Trigger', render: (r: Run) => r.trigger_kind },
            { header: 'Started', render: (r: Run) => fmtDateTime(r.started_at) },
            { header: 'Tokens', render: (r: Run) => String(r.tokens) },
            { header: 'Cost', render: (r: Run) => fmtCost(r.cost) },
          ]}
          rows={sortedRuns}
          rowKey={(r) => r.id}
          onRowClick={setOpenRun}
          empty="No runs match."
        />
      </Card>

      {runningAssignment && (
        <RunNowModal
          assignment={runningAssignment}
          onClose={() => setRunningAssignment(null)}
          onRan={() => {
            setRunningAssignment(null)
            loadRuns()
          }}
        />
      )}
      {editingAssignment !== undefined && (
        <AssignmentEditorModal
          agentID={agent.id}
          duties={duties}
          assignment={editingAssignment}
          onClose={() => setEditingAssignment(undefined)}
          onSaved={() => {
            setEditingAssignment(undefined)
            load()
          }}
        />
      )}
      {stateAssignment && <AssignmentStateModal assignment={stateAssignment} isAdmin={isAdmin} onClose={() => setStateAssignment(null)} />}
      {openRun && <RunDrawer run={openRun} onClose={() => setOpenRun(null)} />}
    </>
  )
}
