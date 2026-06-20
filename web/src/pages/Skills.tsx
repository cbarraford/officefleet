import { useEffect, useState, type FormEvent } from 'react'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { Skill, OutputActionType } from '../api/types'
import Card from '../components/Card'
import ConfirmButton from '../components/ConfirmButton'
import Modal from '../components/Modal'
import Table from '../components/Table'
import { fmtDate } from '../lib/format'
import { toast } from '../lib/toast'

const TRIGGER_KINDS = ['manual', 'cron', 'event-subscription', 'continuous']

interface SkillForm {
  name: string
  role: string
  description: string
  trigger_kinds: string[]
  required_tools: string // comma-separated in the form
  prompt: string
  output_actions: OutputActionType[]
  config_schema: string // JSON text in the form
}

function emptyForm(): SkillForm {
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

function formFromSkill(d: Skill): SkillForm {
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

function SkillModal({ skill, onClose, onSaved }: { skill: Skill | null; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<SkillForm>(skill ? formFromSkill(skill) : emptyForm())
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const set = <K extends keyof SkillForm>(key: K, value: SkillForm[K]) => setForm((f) => ({ ...f, [key]: value }))

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
      if (skill) {
        await api.patch(`/api/v1/skills/${skill.id}`, body)
      } else {
        await api.post('/api/v1/skills', body)
      }
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  return (
    <Modal title={skill ? `Edit skill: ${skill.name}` : 'New skill'} onClose={onClose}>
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
            {busy ? 'Saving…' : 'Save skill'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

export default function Skills() {
  const { isAdmin } = useSession()
  const [skills, setSkills] = useState<Skill[]>([])
  const [editing, setEditing] = useState<Skill | null>(null)
  const [creating, setCreating] = useState(false)

  const load = () => {
    api.get<Skill[]>('/api/v1/skills').then(
      (d) => setSkills(d ?? []),
      () => toast('error', 'failed to load skills'),
    )
  }
  useEffect(load, [])

  const remove = async (d: Skill) => {
    try {
      await api.del(`/api/v1/skills/${d.id}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <div className="row between mb">
        <h1>Skills</h1>
        {isAdmin && (
          <button className="primary" onClick={() => setCreating(true)}>
            New skill
          </button>
        )}
      </div>

      <Card>
        <Table
          columns={[
            {
              header: 'Name',
              render: (d: Skill) =>
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
            { header: 'Role', render: (d: Skill) => d.role || '—' },
            { header: 'Description', render: (d: Skill) => d.description || '—' },
            { header: 'Triggers', render: (d: Skill) => (d.trigger_kinds ?? []).join(', ') || '—' },
            { header: 'Tools', render: (d: Skill) => (d.required_tools ?? []).join(', ') || '—' },
            { header: 'Updated', render: (d: Skill) => fmtDate(d.updated_at) },
            {
              header: '',
              render: (d: Skill) =>
                isAdmin ? <ConfirmButton label="Delete" onConfirm={() => remove(d)} /> : null,
            },
          ]}
          rows={skills}
          rowKey={(d) => d.id}
          empty="No skills defined."
        />
      </Card>

      {(creating || editing) && (
        <SkillModal
          skill={editing}
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
