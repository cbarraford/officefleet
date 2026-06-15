import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
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

function AssignmentModal({
  agentId,
  duties,
  existing,
  onClose,
  onSaved,
}: {
  agentId: string
  duties: Duty[]
  existing: Assignment | null // null = create
  onClose: () => void
  onSaved: () => void
}) {
  const [dutyId, setDutyId] = useState(existing?.duty_id ?? duties[0]?.id ?? '')
  const [name, setName] = useState(existing?.name ?? '')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [triggerKind, setTriggerKind] = useState(existing?.trigger.kind ?? 'manual')
  const [schedule, setSchedule] = useState(existing?.trigger.schedule ?? '')
  const [filterText, setFilterText] = useState(
    existing?.trigger.filter ? JSON.stringify(existing.trigger.filter, null, 2) : '',
  )
  const [backend, setBackend] = useState(existing?.backend?.name ?? '')
  const [configText, setConfigText] = useState(existing?.config ? JSON.stringify(existing.config, null, 2) : '')
  const [outputsText, setOutputsText] = useState(existing?.outputs ? JSON.stringify(existing.outputs, null, 2) : '')
  const [taskOverride, setTaskOverride] = useState(existing?.task_prompt_override ?? '')
  const [extra, setExtra] = useState(existing?.extra_instructions ?? '')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    const trigger: Record<string, unknown> = { kind: triggerKind }
    if (triggerKind === 'cron') trigger.schedule = schedule
    if (triggerKind === 'event-subscription') {
      try {
        trigger.filter = filterText ? JSON.parse(filterText) : {}
      } catch {
        setError('filter must be valid JSON')
        return
      }
    }
    let config: unknown = null
    if (configText.trim()) {
      try {
        config = JSON.parse(configText)
      } catch {
        setError('config must be valid JSON')
        return
      }
    }
    let outputs: unknown = null
    if (outputsText.trim()) {
      try {
        outputs = JSON.parse(outputsText)
      } catch {
        setError('outputs must be valid JSON')
        return
      }
    }

    const body: Record<string, unknown> = {
      name,
      enabled,
      trigger,
      backend: backend ? { name: backend } : null,
      config,
      outputs,
      task_prompt_override: taskOverride || null,
      extra_instructions: extra || null,
    }
    if (!existing) {
      body.agent_id = agentId
      body.duty_id = dutyId
    }

    setBusy(true)
    try {
      if (existing) await api.patch(`/api/v1/assignments/${existing.id}`, body)
      else await api.post('/api/v1/assignments', body)
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  return (
    <Modal title={existing ? 'Edit assignment' : 'New assignment'} onClose={onClose}>
      <form onSubmit={submit}>
        {!existing && (
          <label className="field">
            <span>Duty</span>
            <select value={dutyId} onChange={(e) => setDutyId(e.target.value)}>
              {duties.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="field">
          <span>Name (purpose; unique per agent+duty)</span>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. on-open" />
        </label>
        <label className="field">
          <span>Trigger</span>
          <select value={triggerKind} onChange={(e) => setTriggerKind(e.target.value)}>
            <option value="manual">manual</option>
            <option value="cron">cron</option>
            <option value="event-subscription">event-subscription</option>
          </select>
        </label>
        {triggerKind === 'cron' && (
          <label className="field">
            <span>Schedule (cron)</span>
            <input className="mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} placeholder="0 9 * * 1-5" />
          </label>
        )}
        {triggerKind === 'event-subscription' && (
          <label className="field">
            <span>Filter (JSON)</span>
            <textarea
              className="mono"
              rows={4}
              value={filterText}
              onChange={(e) => setFilterText(e.target.value)}
              placeholder='{"source":"gitlab","event_type":"mr_opened"}'
            />
          </label>
        )}
        <label className="field">
          <span>Backend override (name, optional)</span>
          <input value={backend} onChange={(e) => setBackend(e.target.value)} />
        </label>
        <label className="field">
          <span>Config (JSON)</span>
          <textarea className="mono" rows={4} value={configText} onChange={(e) => setConfigText(e.target.value)} />
        </label>
        <label className="field">
          <span>Outputs (JSON)</span>
          <textarea className="mono" rows={5} value={outputsText} onChange={(e) => setOutputsText(e.target.value)} />
        </label>
        <label className="field">
          <span>Task prompt override (optional)</span>
          <textarea rows={3} value={taskOverride} onChange={(e) => setTaskOverride(e.target.value)} />
        </label>
        <label className="field">
          <span>Extra instructions (optional)</span>
          <textarea rows={3} value={extra} onChange={(e) => setExtra(e.target.value)} />
        </label>
        <label className="row">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

function StateModal({ assignment, onClose }: { assignment: Assignment; onClose: () => void }) {
  const [state, setState] = useState<Record<string, string> | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    api.get<Record<string, string>>(`/api/v1/assignments/${assignment.id}/state`).then(
      (s) => setState(s ?? {}),
      (err) => setError(err instanceof ApiError ? err.message : 'failed to load state'),
    )
  }, [assignment.id])

  const entries = state ? Object.entries(state) : []
  return (
    <Modal title="Assignment state / memory" onClose={onClose}>
      {error && <div className="form-error">{error}</div>}
      {!state && !error && <div className="dim">Loading…</div>}
      {state && entries.length === 0 && <div className="dim">No state stored for this assignment.</div>}
      {entries.length > 0 && (
        <dl className="kv">
          {entries.map(([k, v]) => (
            <Fragment key={k}>
              <dt className="mono">{k}</dt>
              <dd className="mono">{v}</dd>
            </Fragment>
          ))}
        </dl>
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
  const [editing, setEditing] = useState<Assignment | 'new' | null>(null)
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

  const deleteAssignment = async (a: Assignment) => {
    if (!window.confirm(`Delete the "${dutyName(a.duty_id)}" assignment?`)) return
    try {
      await api.del(`/api/v1/assignments/${a.id}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
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
            <button className="small primary" onClick={() => setEditing('new')} disabled={duties.length === 0}>
              New assignment
            </button>
          </div>
        )}
        <Table
          columns={[
            { header: 'Duty', render: (a: Assignment) => dutyName(a.duty_id) },
            { header: 'Name', render: (a: Assignment) => <span className="dim mono">{a.name || '—'}</span> },
            {
              header: 'Trigger',
              render: (a: Assignment) => (
                <>
                  {a.trigger.kind} <span className="dim mono">{triggerSummary(a)}</span>
                </>
              ),
            },
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
              render: (a: Assignment) => (
                <div className="row">
                  <button className="small" onClick={() => setStateAssignment(a)}>
                    State
                  </button>
                  {isAdmin && (
                    <>
                      <button className="small" onClick={() => setEditing(a)}>
                        Edit
                      </button>
                      <button className="small" onClick={() => setRunningAssignment(a)}>
                        Run now
                      </button>
                      <button className="small danger" onClick={() => deleteAssignment(a)}>
                        Delete
                      </button>
                    </>
                  )}
                </div>
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
      {editing && (
        <AssignmentModal
          agentId={agent.id}
          duties={duties}
          existing={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            load()
          }}
        />
      )}
      {stateAssignment && <StateModal assignment={stateAssignment} onClose={() => setStateAssignment(null)} />}
      {openRun && <RunDrawer run={openRun} onClose={() => setOpenRun(null)} />}
    </>
  )
}
