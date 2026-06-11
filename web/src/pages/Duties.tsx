import { useEffect, useState, type FormEvent } from 'react'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { Duty, OutputActionType } from '../api/types'
import Card from '../components/Card'
import ConfirmButton from '../components/ConfirmButton'
import Modal from '../components/Modal'
import Table from '../components/Table'
import { fmtDate } from '../lib/format'
import { toast } from '../lib/toast'

const TRIGGER_KINDS = ['manual', 'cron', 'event-subscription', 'continuous']

interface DutyForm {
  name: string
  role: string
  description: string
  trigger_kinds: string[]
  required_tools: string // comma-separated in the form
  prompt: string
  output_actions: OutputActionType[]
  config_schema: string // JSON text in the form
}

function emptyForm(): DutyForm {
  return {
    name: '',
    role: '',
    description: '',
    trigger_kinds: ['manual'],
    required_tools: '',
    prompt: '',
    output_actions: [],
    config_schema: '',
  }
}

function formFromDuty(d: Duty): DutyForm {
  return {
    name: d.name,
    role: d.role,
    description: d.description,
    trigger_kinds: d.trigger_kinds ?? [],
    required_tools: (d.required_tools ?? []).join(', '),
    prompt: d.prompt,
    output_actions: d.output_actions ?? [],
    config_schema: d.config_schema ? JSON.stringify(d.config_schema, null, 2) : '',
  }
}

function DutyModal({ duty, onClose, onSaved }: { duty: Duty | null; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<DutyForm>(duty ? formFromDuty(duty) : emptyForm())
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const set = <K extends keyof DutyForm>(key: K, value: DutyForm[K]) => setForm((f) => ({ ...f, [key]: value }))

  const toggleKind = (kind: string) =>
    set(
      'trigger_kinds',
      form.trigger_kinds.includes(kind) ? form.trigger_kinds.filter((k) => k !== kind) : [...form.trigger_kinds, kind],
    )

  const setAction = (i: number, field: keyof OutputActionType, value: string) =>
    set(
      'output_actions',
      form.output_actions.map((a, j) => (j === i ? { ...a, [field]: value } : a)),
    )

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    let configSchema: Record<string, unknown> | null = null
    if (form.config_schema.trim()) {
      try {
        configSchema = JSON.parse(form.config_schema) as Record<string, unknown>
      } catch {
        setError('config schema must be valid JSON')
        return
      }
    }

    const body = {
      name: form.name,
      role: form.role,
      description: form.description,
      trigger_kinds: form.trigger_kinds,
      required_tools: form.required_tools
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
      prompt: form.prompt,
      output_actions: form.output_actions.filter((a) => a.plugin && a.action),
      config_schema: configSchema,
    }

    setBusy(true)
    try {
      if (duty) {
        await api.patch(`/api/v1/duties/${duty.id}`, body)
      } else {
        await api.post('/api/v1/duties', body)
      }
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  return (
    <Modal title={duty ? `Edit duty: ${duty.name}` : 'New duty'} onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Name</span>
          <input value={form.name} onChange={(e) => set('name', e.target.value)} autoFocus />
        </label>
        <label className="field">
          <span>Role category</span>
          <input value={form.role} onChange={(e) => set('role', e.target.value)} placeholder="e.g. engineering" />
        </label>
        <label className="field">
          <span>Description</span>
          <input value={form.description} onChange={(e) => set('description', e.target.value)} />
        </label>
        <div className="field">
          <span className="dim" style={{ fontSize: 12 }}>
            Trigger kinds
          </span>
          <div className="row wrap">
            {TRIGGER_KINDS.map((kind) => (
              <label key={kind} className="row" style={{ width: 'auto', gap: 4 }}>
                <input
                  type="checkbox"
                  style={{ width: 'auto' }}
                  checked={form.trigger_kinds.includes(kind)}
                  onChange={() => toggleKind(kind)}
                />
                {kind}
              </label>
            ))}
          </div>
        </div>
        <label className="field">
          <span>Required tools (comma-separated)</span>
          <input value={form.required_tools} onChange={(e) => set('required_tools', e.target.value)} placeholder="bash, files" />
        </label>
        <label className="field">
          <span>Prompt template</span>
          <textarea className="mono" rows={8} value={form.prompt} onChange={(e) => set('prompt', e.target.value)} />
        </label>
        <div className="field">
          <span className="dim" style={{ fontSize: 12 }}>
            Output actions
          </span>
          {form.output_actions.map((a, i) => (
            <div key={i} className="row mb">
              <input placeholder="plugin" value={a.plugin} onChange={(e) => setAction(i, 'plugin', e.target.value)} />
              <input placeholder="action" value={a.action} onChange={(e) => setAction(i, 'action', e.target.value)} />
              <button
                type="button"
                className="small danger"
                onClick={() => set('output_actions', form.output_actions.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            </div>
          ))}
          <button type="button" className="small" onClick={() => set('output_actions', [...form.output_actions, { plugin: '', action: '' }])}>
            + add action
          </button>
        </div>
        <label className="field mt">
          <span>Config schema (JSON, optional)</span>
          <textarea className="mono" rows={4} value={form.config_schema} onChange={(e) => set('config_schema', e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !form.name}>
            {busy ? 'Saving…' : 'Save duty'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

export default function Duties() {
  const { isAdmin } = useSession()
  const [duties, setDuties] = useState<Duty[]>([])
  const [editing, setEditing] = useState<Duty | null>(null)
  const [creating, setCreating] = useState(false)

  const load = () => {
    api.get<Duty[]>('/api/v1/duties').then(
      (d) => setDuties(d ?? []),
      () => toast('error', 'failed to load duties'),
    )
  }
  useEffect(load, [])

  const remove = async (d: Duty) => {
    try {
      await api.del(`/api/v1/duties/${d.id}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <div className="row between mb">
        <h1>Duties</h1>
        {isAdmin && (
          <button className="primary" onClick={() => setCreating(true)}>
            New duty
          </button>
        )}
      </div>

      <Card>
        <Table
          columns={[
            {
              header: 'Name',
              render: (d: Duty) =>
                isAdmin ? (
                  <a
                    href="#edit"
                    onClick={(e) => {
                      e.preventDefault()
                      setEditing(d)
                    }}
                  >
                    {d.name}
                  </a>
                ) : (
                  <strong>{d.name}</strong>
                ),
            },
            { header: 'Role', render: (d: Duty) => d.role || '—' },
            { header: 'Description', render: (d: Duty) => d.description || '—' },
            { header: 'Triggers', render: (d: Duty) => (d.trigger_kinds ?? []).join(', ') || '—' },
            { header: 'Tools', render: (d: Duty) => (d.required_tools ?? []).join(', ') || '—' },
            { header: 'Updated', render: (d: Duty) => fmtDate(d.updated_at) },
            {
              header: '',
              render: (d: Duty) =>
                isAdmin ? <ConfirmButton label="Delete" onConfirm={() => remove(d)} /> : null,
            },
          ]}
          rows={duties}
          rowKey={(d) => d.id}
          empty="No duties defined."
        />
      </Card>

      {(creating || editing) && (
        <DutyModal
          duty={editing}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
          onSaved={() => {
            setCreating(false)
            setEditing(null)
            load()
          }}
        />
      )}
    </>
  )
}
