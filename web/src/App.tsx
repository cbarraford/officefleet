import { createContext, useContext, useEffect, useState } from 'react'
import { NavLink, Outlet } from 'react-router-dom'
import { api } from './api/client'
import type { Me } from './api/types'
import Toasts from './components/Toasts'

interface Session {
  me: Me
  isAdmin: boolean
}

const SessionContext = createContext<Session | null>(null)

// useSession is only rendered under <App/>, which never renders children
// before /me resolves — the assertion is safe.
export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession outside <App/>')
  return s
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null)

  useEffect(() => {
    // A 401 here triggers the client's onUnauthorized redirect to /login.
    api.get<Me>('/api/v1/me').then(setMe, () => {})
  }, [])

  const logout = async () => {
    try {
      await api.post('/api/v1/logout')
    } finally {
      window.location.assign('/login')
    }
  }

  if (!me) return null // brief blank while /me resolves (or redirects)

  return (
    <SessionContext.Provider value={{ me, isAdmin: me.role === 'admin' }}>
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">OfficeFleet</div>
          <nav>
            <NavLink to="/" end>
              Dashboard
            </NavLink>
            <NavLink to="/agents">Agents</NavLink>
            <NavLink to="/duties">Duties</NavLink>
            <NavLink to="/settings">Settings</NavLink>
          </nav>
          <div className="session">
            <div>
              {me.username} <span className="dim">({me.role})</span>
            </div>
            <button className="small mt" onClick={logout}>
              Log out
            </button>
          </div>
        </aside>
        <main className="main">
          <Outlet />
        </main>
      </div>
      <Toasts />
    </SessionContext.Provider>
  )
}
