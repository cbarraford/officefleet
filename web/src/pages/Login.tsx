import { useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ApiError, api } from '../api/client'
import Card from '../components/Card'

// Only same-site relative paths are honored as return-to targets. Reject
// protocol-relative (`//`) and backslash forms (browsers normalize `\` to
// `/`, so `/\evil.com` would become `//evil.com`).
function safeNext(raw: string | null): string {
  if (raw && raw.startsWith('/') && !raw.startsWith('//') && !raw.includes('\\')) return raw
  return '/'
}

export default function Login() {
  const [params] = useSearchParams()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.post('/api/v1/login', { username, password })
      // Full navigation (not router navigate): App refetches /me on load.
      window.location.assign(safeNext(params.get('next')))
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'login failed')
    }
  }

  return (
    <div className="login-wrap">
      <Card title="Sign in to OfficeFleet" className="login-card">
        <form onSubmit={submit}>
          <label className="field">
            <span>Username</span>
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
          </label>
          <label className="field">
            <span>Password</span>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          {error && <div className="form-error">{error}</div>}
          <button className="primary" type="submit" disabled={busy || !username || !password}>
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
      </Card>
    </div>
  )
}
