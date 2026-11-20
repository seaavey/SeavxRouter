import { useEffect, useState, type FormEvent } from 'react'
import { Waypoints } from 'lucide-react'
import { api } from './api'
import { Dashboard } from './app'
import { Button } from './components/ui/button'

interface Session {
  authenticated: boolean
  must_change: boolean
  setup_required?: boolean
}

export function App() {
  const [demo, setDemo] = useState(() => new URLSearchParams(location.search).get('demo') === '1')
  const [session, setSession] = useState<Session | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [setupToken, setSetupToken] = useState('')

  useEffect(() => {
    if (demo) return
    const controller = new AbortController()
    void api<Session>('/dashboard/api/session', { signal: controller.signal }).then(setSession).catch(() => {
      if (!controller.signal.aborted) {
        setSession({ authenticated: false, must_change: false })
        setError('Start the router with dashboard login enabled to connect.')
      }
    })
    const expire = () => {
      setSession(previous => ({ authenticated: false, must_change: false, setup_required: previous?.setup_required || previous?.must_change }))
      setError('Your session ended. Sign in again.')
    }
    window.addEventListener('dashboard-session-expired', expire)
    return () => {
      controller.abort()
      window.removeEventListener('dashboard-session-expired', expire)
    }
  }, [demo])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const password = String(form.get('password') ?? '')
    if (session?.must_change && password !== form.get('confirmation')) {
      setError('The passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const endpoint = session?.must_change ? 'password' : 'login'
      const next = await api<Session>(`/dashboard/api/${endpoint}`, {
        method: 'POST',
        body: JSON.stringify({ password, setup_token: setupToken }),
      })
      if (!next.must_change) setSetupToken('')
      setSession(next)
    } catch (error) {
      setError(error instanceof Error ? error.message : 'Could not sign in.')
    } finally {
      setBusy(false)
    }
  }

  async function exit() {
    if (demo) {
      const url = new URL(location.href)
      url.searchParams.delete('demo')
      history.replaceState(null, '', url)
      setSession(null)
      setDemo(false)
      return
    }
    await api('/dashboard/api/logout', { method: 'POST' })
    setSession({ authenticated: false, must_change: false })
  }

  if (demo || (session?.authenticated && !session.must_change)) {
    return <Dashboard demo={demo} onExit={exit} />
  }

  const setup = session?.must_change
  return <main className="auth-screen">
    <section className="panel auth-panel">
      <div className="brand"><span className="brand-mark"><Waypoints size={19} /></span>SeavxRouter</div>
      <h1>{setup ? 'Set a new password' : 'Sign in'}</h1>
      <p>{setup ? 'Replace the default password before opening the dashboard.' : 'Use your dashboard password to continue.'}</p>
      {session === null ? <p role="status">Checking your session…</p> : <form onSubmit={submit} key={setup ? 'setup' : 'login'}>
        <label>{setup ? 'New password' : 'Password'}
          <input name="password" type="password" required minLength={setup ? 12 : undefined} autoComplete={setup ? 'new-password' : 'current-password'} />
        </label>
        {setup && !setupToken && <p>Enter the setup token again to finish setup.</p>}
        {(setup || session.setup_required) && <label>Setup token
          <input name="setup_token" type="password" required autoComplete="off" value={setupToken} onChange={event => setSetupToken(event.target.value)} />
        </label>}
        {setup && <label>Confirm new password<input name="confirmation" type="password" required minLength={12} autoComplete="new-password" /></label>}
        {error && <p className="auth-error" role="alert">{error}</p>}
        <Button type="submit" disabled={busy}>{busy ? 'Please wait…' : setup ? 'Save password' : 'Sign in'}</Button>
      </form>}
      {!setup && <Button variant="ghost" disabled={busy} onClick={() => { setError(''); setDemo(true) }}>View demo</Button>}
    </section>
  </main>
}
