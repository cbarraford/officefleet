import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
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
          <button className="small" onClick={toggleAgent}>
            {agent.enabled ? 'Pause' : 'Resume'}
          </button>
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
                  <button className="small" onClick={() => setRunningAssignment(a)}>
                    Run now
                  </button>
                ) : null,
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
      {openRun && <RunDrawer run={openRun} onClose={() => setOpenRun(null)} />}
    </>
  )
}
