import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { Agent, AgentStats, BackendView } from '../api/types'
import AvatarBubble from '../components/AvatarBubble'
import Badge from '../components/Badge'
import Card from '../components/Card'
import Modal from '../components/Modal'
import { fmtDate, fmtPercent } from '../lib/format'
import { toast } from '../lib/toast'

// Lazy per-card stats cache: each card fetches once per page lifetime.
const statsCache = new Map<string, AgentStats>()

function StatsStrip({ agentID }: { agentID: string }) {
  const [stats, setStats] = useState<AgentStats | null>(statsCache.get(agentID) ?? null)

  useEffect(() => {
    if (statsCache.has(agentID)) return
    api.get<AgentStats>(`/api/v1/agents/${agentID}/stats`).then(
      (s) => {
        statsCache.set(agentID, s)
        setStats(s)
      },
      () => {}, // stats are decorative on the grid; the card still renders
    )
  }, [agentID])

  if (!stats) return <div className="dim">…</div>
  return (
    <div className="dim">
      {stats.runs_last_30d} runs 30d · {fmtPercent(stats.success_rate)} success · {stats.outputs_last_30d} outputs 30d
    </div>
  )
}

function CreateAgentModal({ backends, onClose, onCreated }: { backends: BackendView[]; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [role, setRole] = useState('')
  const [systemPrompt, setSystemPrompt] = useState('')
  const [backend, setBackend] = useState(backends[0]?.name ?? '')
  const [hiredAt, setHiredAt] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name,
        role,
        system_prompt: systemPrompt,
        default_backend: { name: backend },
      }
      if (hiredAt) body.hired_at = hiredAt
      await api.post('/api/v1/agents', body)
      onCreated()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'create failed')
    }
  }

  return (
    <Modal title="New agent" onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </label>
        <label className="field">
          <span>Role</span>
          <input value={role} onChange={(e) => setRole(e.target.value)} placeholder="e.g. Code Reviewer" />
        </label>
        <label className="field">
          <span>System prompt (persona)</span>
          <textarea rows={5} value={systemPrompt} onChange={(e) => setSystemPrompt(e.target.value)} />
        </label>
        <label className="field">
          <span>Default backend</span>
          <select value={backend} onChange={(e) => setBackend(e.target.value)}>
            {backends.map((b) => (
              <option key={b.name} value={b.name}>
                {b.name} ({b.kind})
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>Hire date</span>
          <input type="date" value={hiredAt} onChange={(e) => setHiredAt(e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !name || !backend}>
            {busy ? 'Hiring…' : 'Hire agent'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

export default function Agents() {
  const { isAdmin } = useSession()
  const [agents, setAgents] = useState<Agent[]>([])
  const [backends, setBackends] = useState<BackendView[]>([])
  const [creating, setCreating] = useState(false)

  const load = () => {
    Promise.all([api.get<Agent[]>('/api/v1/agents'), api.get<BackendView[]>('/api/v1/backends')]).then(
      ([a, b]) => {
        setAgents(a ?? [])
        setBackends(b ?? [])
      },
      () => toast('error', 'failed to load agents'),
    )
  }
  useEffect(load, [])

  const toggleEnabled = async (agent: Agent) => {
    try {
      await api.patch(`/api/v1/agents/${agent.id}`, { enabled: !agent.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  return (
    <>
      <div className="row between mb">
        <h1>Agents</h1>
        {isAdmin && (
          <button className="primary" onClick={() => setCreating(true)}>
            New agent
          </button>
        )}
      </div>

      <div className="grid cards">
        {agents.map((a) => (
          <Card key={a.id}>
            <div className="row mb">
              <AvatarBubble name={a.name} url={a.avatar_url} size={48} />
              <div>
                <Link to={`/agents/${a.id}`}>
                  <strong>{a.name}</strong>
                </Link>
                <div className="dim">{a.role || '—'}</div>
              </div>
              <div className="spacer" />
              {!a.enabled && <Badge text="Paused" kind="warn" />}
            </div>
            <div className="dim mb">hired {fmtDate(a.hired_at)}</div>
            <StatsStrip agentID={a.id} />
            {isAdmin && (
              <div className="mt">
                <button className="small" onClick={() => toggleEnabled(a)}>
                  {a.enabled ? 'Pause' : 'Resume'}
                </button>
              </div>
            )}
          </Card>
        ))}
        {agents.length === 0 && <div className="empty">No agents yet — hire one.</div>}
      </div>

      {creating && (
        <CreateAgentModal
          backends={backends}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            load()
          }}
        />
      )}
    </>
  )
}
