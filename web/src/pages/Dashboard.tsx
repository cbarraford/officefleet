import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { connectStream } from '../api/sse'
import type { Agent, Duty, Run, StreamMsg } from '../api/types'
import Card from '../components/Card'
import StatusPill from '../components/StatusPill'
import Table from '../components/Table'
import { fmtDateTime, isToday } from '../lib/format'
import { toast } from '../lib/toast'

const FEED_CAP = 50

interface FeedItem {
  msg: StreamMsg
  at: Date
}

export default function Dashboard() {
  const [runs, setRuns] = useState<Run[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [duties, setDuties] = useState<Duty[]>([])
  const [feed, setFeed] = useState<FeedItem[]>([])
  const [connected, setConnected] = useState(true)
  const [loadError, setLoadError] = useState(false)

  const load = useCallback(() => {
    setLoadError(false)
    Promise.all([
      api.get<Run[]>('/api/v1/runs?limit=50'),
      api.get<Agent[]>('/api/v1/agents'),
      api.get<Duty[]>('/api/v1/duties'),
    ]).then(
      ([r, a, d]) => {
        setRuns(r ?? [])
        setAgents(a ?? [])
        setDuties(d ?? [])
      },
      () => {
        setLoadError(true)
        toast('error', 'failed to load dashboard data')
      },
    )
  }, [])

  // Keep `load` reachable from the stream callbacks without resubscribing.
  const loadRef = useRef(load)
  loadRef.current = load

  useEffect(() => {
    load()
    return connectStream({
      onMessage: (msg) => {
        setFeed((f) => [{ msg, at: new Date() }, ...f].slice(0, FEED_CAP))
        if (msg.event === 'run_finished') loadRef.current()
      },
      onStatus: setConnected,
      onReconnect: () => loadRef.current(),
    })
  }, [load])

  const agentName = useMemo(() => {
    const m = new Map(agents.map((a) => [a.id, a.name]))
    return (id: string) => m.get(id) ?? id.slice(0, 8)
  }, [agents])

  const dutyName = useMemo(() => {
    const m = new Map(duties.map((d) => [d.id, d.name]))
    return (id: string) => m.get(id) ?? id.slice(0, 8)
  }, [duties])

  const sortedRuns = useMemo(
    () => [...runs].sort((a, b) => b.started_at.localeCompare(a.started_at)),
    [runs],
  )

  const activeAgents = agents.filter((a) => a.enabled).length
  const runsToday = runs.filter((r) => isToday(r.started_at)).length
  const failuresToday = runs.filter((r) => r.status === 'failed' && isToday(r.started_at)).length

  return (
    <>
      <div className="row between mb">
        <h1>Dashboard</h1>
        {!connected && <span className="reconnecting">reconnecting…</span>}
        {loadError && (
          <button className="small" onClick={load}>
            Retry
          </button>
        )}
      </div>

      <div className="grid cols-3 mb">
        <Card className="stat">
          <div className="num">{activeAgents}</div>
          <div className="label">active agents</div>
        </Card>
        <Card className="stat">
          <div className="num">{runsToday}</div>
          <div className="label">runs today</div>
        </Card>
        <Card className="stat">
          <div className="num">{failuresToday}</div>
          <div className="label">failures today</div>
        </Card>
      </div>

      <div className="grid cols-3">
        <Card title="Live activity" className="mb">
          <div className="feed">
            {feed.length === 0 && <div className="empty">Waiting for runs…</div>}
            {feed.map((f, i) => (
              <div key={`${f.msg.id}-${f.msg.event}-${i}`} className="item">
                <StatusPill status={f.msg.status} /> {agentName(f.msg.agent_id)} ·{' '}
                {dutyName(f.msg.duty_id)} <span className="dim">({f.msg.trigger_kind})</span>
                <div className="when">{fmtDateTime(f.at.toISOString())}</div>
              </div>
            ))}
          </div>
        </Card>

        <Card title="Recent runs" className="mb" style={{ gridColumn: 'span 2' }}>
          <Table
            columns={[
              { header: 'Status', render: (r: Run) => <StatusPill status={r.status} /> },
              {
                header: 'Agent',
                render: (r: Run) => <Link to={`/agents/${r.agent_id}`}>{agentName(r.agent_id)}</Link>,
              },
              { header: 'Duty', render: (r: Run) => dutyName(r.duty_id) },
              { header: 'Trigger', render: (r: Run) => r.trigger_kind },
              { header: 'Started', render: (r: Run) => fmtDateTime(r.started_at) },
              { header: 'Tokens', render: (r: Run) => String(r.tokens) },
            ]}
            rows={sortedRuns}
            rowKey={(r) => r.id}
            empty="No runs yet."
          />
        </Card>
      </div>
    </>
  )
}
