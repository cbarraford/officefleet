import { useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { BackendView, FleetEvent, SecretInfo, User } from '../api/types'
import Badge from '../components/Badge'
import Card from '../components/Card'
import ConfirmButton from '../components/ConfirmButton'
import StatusPill from '../components/StatusPill'
import Table from '../components/Table'
import { fmtDate, fmtDateTime } from '../lib/format'
import { toast } from '../lib/toast'

const TABS = ['backends', 'secrets', 'users', 'events'] as const
type Tab = (typeof TABS)[number]

function BackendsTab() {
  const [backends, setBackends] = useState<BackendView[]>([])
  useEffect(() => {
    api.get<BackendView[]>('/api/v1/backends').then(
      (b) => setBackends(b ?? []),
      () => toast('error', 'failed to load backends'),
    )
  }, [])
  return (
    <Card>
      <p className="dim">Backends are defined in fleet.yaml (read-only here).</p>
      <Table
        columns={[
          { header: 'Name', render: (b: BackendView) => <strong>{b.name}</strong> },
          { header: 'Kind', render: (b: BackendView) => b.kind },
          { header: 'Auth', render: (b: BackendView) => b.auth_mode },
          { header: 'Model', render: (b: BackendView) => b.model ?? '—' },
          { header: 'Effort', render: (b: BackendView) => b.default_effort ?? '—' },
        ]}
        rows={backends}
        rowKey={(b) => b.name}
        empty="No backends configured."
      />
    </Card>
  )
}

function SecretsTab({ isAdmin }: { isAdmin: boolean }) {
  const [secrets, setSecrets] = useState<SecretInfo[]>([])
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [error, setError] = useState('')

  const load = () => {
    api.get<SecretInfo[]>('/api/v1/secrets').then(
      (s) => setSecrets(s ?? []),
      () => toast('error', 'failed to load secrets'),
    )
  }
  useEffect(load, [])

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api.put(`/api/v1/secrets/${encodeURIComponent(name)}`, { value })
      setName('')
      setValue('')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  const remove = async (s: SecretInfo) => {
    try {
      await api.del(`/api/v1/secrets/${encodeURIComponent(s.name)}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <Card className="mb">
        <p className="dim">Values are write-only: they are never displayed after saving.</p>
        <Table
          columns={[
            { header: 'Name', render: (s: SecretInfo) => <span className="mono">{s.name}</span> },
            {
              header: 'Storage',
              render: (s: SecretInfo) =>
                s.encrypted ? <Badge text="encrypted" kind="ok" /> : <Badge text="plaintext" kind="warn" />,
            },
            {
              header: '',
              render: (s: SecretInfo) => (isAdmin ? <ConfirmButton label="Delete" onConfirm={() => remove(s)} /> : null),
            },
          ]}
          rows={secrets}
          rowKey={(s) => s.name}
          empty="No secrets stored."
        />
      </Card>
      {isAdmin && (
        <Card title="Set secret">
          <form onSubmit={save}>
            <div className="row">
              <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
              <input placeholder="value" type="password" autoComplete="new-password" value={value} onChange={(e) => setValue(e.target.value)} />
              <button className="primary" type="submit" disabled={!name || !value}>
                Save
              </button>
            </div>
            {error && <div className="form-error">{error}</div>}
          </form>
        </Card>
      )}
    </>
  )
}

function UsersTab({ isAdmin, myUsername }: { isAdmin: boolean; myUsername: string }) {
  const [users, setUsers] = useState<User[]>([])
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer')
  const [error, setError] = useState('')

  const load = () => {
    api.get<User[]>('/api/v1/users').then(
      (u) => setUsers(u ?? []),
      () => toast('error', 'failed to load users'),
    )
  }
  useEffect(load, [])

  const create = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api.post('/api/v1/users', { username, password, role })
      setUsername('')
      setPassword('')
      setRole('viewer')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'create failed')
    }
  }

  const remove = async (u: User) => {
    try {
      await api.del(`/api/v1/users/${encodeURIComponent(u.username)}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <Card className="mb">
        <Table
          columns={[
            { header: 'Username', render: (u: User) => <strong>{u.username}</strong> },
            { header: 'Role', render: (u: User) => u.role },
            { header: 'Created', render: (u: User) => fmtDate(u.created_at) },
            {
              header: '',
              render: (u: User) =>
                isAdmin ? (
                  <ConfirmButton
                    label="Delete"
                    onConfirm={() => remove(u)}
                    disabled={u.username === myUsername}
                    title={u.username === myUsername ? 'you cannot delete your own account' : undefined}
                  />
                ) : null,
            },
          ]}
          rows={users}
          rowKey={(u) => u.id}
          empty="No users."
        />
      </Card>
      {isAdmin && (
        <Card title="Create user">
          <form onSubmit={create}>
            <div className="row">
              <input placeholder="username" value={username} onChange={(e) => setUsername(e.target.value)} />
              <input placeholder="password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              <select value={role} onChange={(e) => setRole(e.target.value as 'admin' | 'viewer')} style={{ width: 130 }}>
                <option value="viewer">viewer</option>
                <option value="admin">admin</option>
              </select>
              <button className="primary" type="submit" disabled={!username || !password}>
                Create
              </button>
            </div>
            {error && <div className="form-error">{error}</div>}
          </form>
        </Card>
      )}
    </>
  )
}

function EventsTab({ isAdmin, highlight }: { isAdmin: boolean; highlight: string | null }) {
  const [events, setEvents] = useState<FleetEvent[]>([])

  const load = () => {
    api.get<FleetEvent[]>('/api/v1/events?limit=50').then(
      (e) => setEvents(e ?? []),
      () => toast('error', 'failed to load events'),
    )
  }
  useEffect(load, [])

  const replay = async (ev: FleetEvent) => {
    try {
      await api.post(`/api/v1/events/${ev.id}/replay`)
      toast('info', 'event requeued')
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'replay failed')
    }
  }

  return (
    <Card>
      <Table
        columns={[
          { header: 'Source', render: (e: FleetEvent) => e.source_plugin },
          { header: 'Type', render: (e: FleetEvent) => e.event_type },
          { header: 'Status', render: (e: FleetEvent) => <StatusPill status={e.status} /> },
          { header: 'Received', render: (e: FleetEvent) => fmtDateTime(e.received_at) },
          { header: 'Dedup key', render: (e: FleetEvent) => <span className="mono dim">{e.dedup_key}</span> },
          {
            header: '',
            render: (e: FleetEvent) =>
              isAdmin ? (
                <button className="small" onClick={() => replay(e)}>
                  Replay
                </button>
              ) : null,
          },
        ]}
        rows={events}
        rowKey={(e) => e.id}
        rowClass={(e) => (e.id === highlight ? 'highlight' : '')}
        empty="No events received."
      />
    </Card>
  )
}

export default function Settings() {
  const { me, isAdmin } = useSession()
  const [params, setParams] = useSearchParams()
  const raw = params.get('tab')
  const tab: Tab = TABS.includes(raw as Tab) ? (raw as Tab) : 'backends'

  return (
    <>
      <h1>Settings</h1>
      <div className="tabs">
        {TABS.map((t) => (
          <button key={t} className={t === tab ? 'active' : ''} onClick={() => setParams({ tab: t })}>
            {t}
          </button>
        ))}
      </div>
      {tab === 'backends' && <BackendsTab />}
      {tab === 'secrets' && <SecretsTab isAdmin={isAdmin} />}
      {tab === 'users' && <UsersTab isAdmin={isAdmin} myUsername={me.username} />}
      {tab === 'events' && <EventsTab isAdmin={isAdmin} highlight={params.get('highlight')} />}
    </>
  )
}
