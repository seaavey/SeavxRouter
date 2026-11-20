import { useEffect, useEffectEvent, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Activity, ArrowDownToLine, Box, ChevronDown, ExternalLink, KeyRound, Layers3, LayoutDashboard, List, Plus, RefreshCw, Search, LogOut, Waypoints } from 'lucide-react'
import { animate } from 'motion/mini'
import { api, type Model, type Provider, type RequestLog, type TenantKey, type Usage } from './api'
import { demoKeys, demoLogs, demoModels, demoProviders, demoUsage } from './demo'
import { Button } from './components/ui/button'
import { Modal } from './components/ui/dialog'

const pages = {
  Overview: { icon: LayoutDashboard, description: 'Monitor request volume, token usage, and cost across your gateway.' },
  Requests: { icon: List, description: 'Inspect the most recent requests handled by your gateway.' },
  Providers: { icon: Layers3, description: 'Check provider accounts and their routing status.' },
  Models: { icon: Box, description: 'Browse the model IDs your gateway exposes.' },
  'API Keys': { icon: KeyRound, description: 'Manage tenant credentials and monthly limits.' },
}

type Page = keyof typeof pages
type ModalName = 'request' | 'create' | 'revoke'
type Result<T> = { value?: T; error?: string }
const number = new Intl.NumberFormat('en-US')
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
const dollars = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 })
const time = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })
const dayLabel = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })

async function result<T>(request: Promise<T>): Promise<Result<T>> {
  try {
    return { value: await request }
  } catch (error) {
    return { error: error instanceof Error ? error.message : 'Could not reach the router.' }
  }
}

function Status({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'ready' | 'limited' }) {
  return <span className={`status ${tone}`}><span />{children}</span>
}

function Notice({ children }: { children: ReactNode }) {
  return <div className="notice">{children}</div>
}

function ModelName({ name }: { name: string }) {
  return <span className="model-name"><Box size={14} />{name || '—'}</span>
}

function RequestTable({ rows, onOpen }: { rows: RequestLog[]; onOpen: (row: RequestLog) => void }) {
  if (!rows.length) return <Notice>No matching requests.</Notice>
  return <div className="table-scroll"><table>
    <thead><tr><th>Model</th><th>Status</th><th>Latency</th><th>Tokens</th><th>Time</th></tr></thead>
    <tbody>{rows.map(row => <tr key={row.id}>
      <td><button className="row-link" onClick={() => onOpen(row)}><ModelName name={row.model} /></button></td>
      <td><Status tone={row.status < 400 ? 'ready' : 'limited'}>{row.status}</Status></td>
      <td>{number.format(row.duration_ms)} ms</td><td>{number.format(row.usage.total_tokens)}</td>
      <td>{time.format(new Date(row.timestamp))}</td>
    </tr>)}</tbody>
  </table></div>
}

function UsageChart({ usage, days }: { usage: Usage; days: number }) {
  const byDay = new Map(usage.data?.map(row => [row.key, row.requests]))
  const end = new Date()
  end.setUTCHours(0, 0, 0, 0)
  const buckets = Array.from({ length: days }, (_, index) => {
    const date = new Date(end)
    date.setUTCDate(end.getUTCDate() - (days - 1 - index))
    return { label: dayLabel.format(date), requests: byDay.get(date.toISOString().slice(0, 10)) ?? 0 }
  })
  const step = Math.max(1, Math.ceil(Math.max(...buckets.map(bucket => bucket.requests)) / 4))
  return <div className="chart" role="group" aria-label="Daily requests in UTC">
    <div className="axis">{[4, 3, 2, 1, 0].map(mark => <span key={mark}>{compact.format(mark * step)}</span>)}</div>
    <div className="plot">
      <div className="grid-lines" aria-hidden="true">{[0, 1, 2, 3, 4].map(line => <i key={line} />)}</div>
      <div className="bars">{buckets.map(bucket => <div className="bar-cell" key={bucket.label} title={`${bucket.label} · ${number.format(bucket.requests)} requests`} aria-label={`${bucket.label}, ${bucket.requests} requests`}>
        <div className="bar" style={{ height: `${bucket.requests / (4 * step) * 100}%` }} />
      </div>)}</div>
      <div className="date-axis">{buckets.filter((_, i) => i === 0 || i === days - 1 || (days === 30 && (i === 10 || i === 20))).map(bucket => <span key={bucket.label}>{bucket.label}</span>)}</div>
    </div>
  </div>
}

export function Dashboard({ demo, onExit }: { demo: boolean; onExit: () => Promise<void> }) {
  const [page, setPage] = useState<Page>('Overview')
  const [exitError, setExitError] = useState('')
  const [exiting, setExiting] = useState(false)
  const [days, setDays] = useState(7)
  const [refresh, setRefresh] = useState(0)
  const [filter, setFilter] = useState('')
  const [offset, setOffset] = useState(0)
  const [modal, setModal] = useState<ModalName | null>(null)
  const [selectedLog, setSelectedLog] = useState<RequestLog | null>(null)
  const [selectedKey, setSelectedKey] = useState<TenantKey | null>(null)
  const [createdKey, setCreatedKey] = useState('')
  const [actionError, setActionError] = useState('')
  const [saving, setSaving] = useState(false)
  const [detail, setDetail] = useState<Result<{ response?: unknown }> | null>(null)
  const [loading, setLoading] = useState(true)
  const [online, setOnline] = useState(false)
  const [streamStatus, setStreamStatus] = useState('Connecting')
  const watchRequests = !demo && (page === 'Overview' || (page === 'Requests' && offset === 0))
  const [usage, setUsage] = useState<Result<Usage>>({})
  const [logs, setLogs] = useState<Result<RequestLog[]>>({})
  const [models, setModels] = useState<Result<Model[]>>({})
  const [providers, setProviders] = useState<Result<Provider[]>>({})
  const [keys, setKeys] = useState<Result<TenantKey[]>>({})
  const main = useRef<HTMLElement>(null)

  useEffect(() => {
    if (!main.current || matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const animation = animate(main.current, { opacity: [0, 1], transform: ['translateY(4px)', 'translateY(0)'] }, { duration: 0.18 })
    return () => animation.cancel()
  }, [page])

  useEffect(() => {
    if (demo) return
    const controller = new AbortController()
    const options = { signal: controller.signal }
    const start = new Date()
    start.setUTCHours(0, 0, 0, 0)
    start.setUTCDate(start.getUTCDate() - (days - 1))
    async function load() {
      setLoading(true)
      const health = result(api('/healthz', options)).then(state => {
        if (!controller.signal.aborted) setOnline(!state.error)
      })
      const tasks: Promise<void>[] = [health]
      if (page === 'Overview') tasks.push(result(api<Usage>(`/dashboard/api/usage?group_by=day&start=${encodeURIComponent(start.toISOString())}`, options)).then(state => {
        if (!controller.signal.aborted) setUsage(state)
      }))
      if (page === 'Overview' || page === 'Requests') tasks.push(result(api<{ data: RequestLog[] }>(`/dashboard/api/logging?limit=${page === 'Overview' ? 5 : 26}&offset=${page === 'Overview' ? 0 : offset}`, options).then(value => value.data)).then(state => {
        if (!controller.signal.aborted) setLogs(state)
      }))
      if (page === 'Providers') tasks.push(result(api<{ data: Provider[] }>('/dashboard/api/quota', options).then(value => value.data ?? [])).then(state => {
        if (!controller.signal.aborted) setProviders(state)
      }))
      if (page === 'Models') tasks.push(result(api<{ data: Model[] }>('/dashboard/api/models', options).then(value => value.data ?? [])).then(state => {
        if (!controller.signal.aborted) setModels(state)
      }))
      if (page === 'API Keys') tasks.push(result(api<{ keys: TenantKey[] | null }>('/dashboard/api/keys', options).then(value => value.keys ?? [])).then(state => {
        if (!controller.signal.aborted) setKeys(state)
      }))
      await Promise.all(tasks)
      if (!controller.signal.aborted) setLoading(false)
    }
    void load()
    return () => controller.abort()
  }, [days, demo, offset, page, refresh])

  const refreshFromStream = useEffectEvent(() => {
    if (loading) return false
    setRefresh(previous => previous + 1)
    return true
  })

  useEffect(() => {
    if (!watchRequests) return

    setStreamStatus('Connecting')
    let dirty = false
    const controller = new AbortController()
    const stream = new EventSource('/dashboard/api/logging/stream')
    stream.addEventListener('ready', () => {
      setStreamStatus('Live')
      // The router does not replay events after a disconnect.
      dirty = true
    })
    stream.addEventListener('log', () => { dirty = true })
    stream.onerror = () => {
      setStreamStatus(stream.readyState === EventSource.CLOSED ? 'Live updates unavailable' : 'Reconnecting')
      void api<{ authenticated: boolean }>('/dashboard/api/session', { signal: controller.signal }).then(session => {
        if (!session.authenticated && !controller.signal.aborted) {
          window.dispatchEvent(new Event('dashboard-session-expired'))
        }
      }).catch(() => {})
    }

    // Batch bursts and wait for any in-flight snapshot before refreshing.
    const timer = window.setInterval(() => {
      if (dirty && refreshFromStream()) dirty = false
    }, 2000)

    return () => {
      controller.abort()
      stream.close()
      window.clearInterval(timer)
    }
  }, [watchRequests])

  useEffect(() => {
    if (modal !== 'request' || !selectedLog || demo) return
    const controller = new AbortController()
    void result(api<{ response?: unknown }>(`/dashboard/api/logging/${encodeURIComponent(selectedLog.id)}`, { signal: controller.signal })).then(state => {
      if (!controller.signal.aborted) setDetail(state)
    })
    return () => controller.abort()
  }, [demo, modal, selectedLog])

  function navigate(next: Page) {
    if (next !== page && (next === 'Overview' || next === 'Requests')) setLogs({})
    setPage(next)
    setFilter('')
    setOffset(0)
  }

  function open(name: ModalName) {
    setActionError('')
    setCreatedKey('')
    setModal(name)
  }

  function openRequest(row: RequestLog) {
    setSelectedLog(row)
    setDetail(null)
    open('request')
  }

  async function exit() {
    setExiting(true)
    setExitError('')
    try {
      await onExit()
    } catch (error) {
      setExitError(error instanceof Error ? error.message : 'Could not sign out.')
    } finally {
      setExiting(false)
    }
  }

  async function manageKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSaving(true)
    setActionError('')
    try {
      if (modal === 'create') {
        const form = new FormData(event.currentTarget)
        const created = await api<{ key: string }>('/dashboard/api/keys', {
          method: 'POST',
          body: JSON.stringify({
            name: String(form.get('name')).trim(),
            budget_usd: Number(form.get('budget')),
            limit_token: Number(form.get('tokens')),
          }),
        })
        setCreatedKey(created.key)
      } else if (selectedKey) {
        await api(`/dashboard/api/keys/${selectedKey.id}`, { method: 'DELETE' })
        setModal(null)
      }
      setRefresh(previous => previous + 1)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not update the key.')
    } finally {
      setSaving(false)
    }
  }

  const currentUsage = demo ? { value: demoUsage(days) } : usage
  const currentLogs = demo ? { value: demoLogs } : logs
  const currentModels = demo ? { value: demoModels } : models
  const currentProviders = demo ? { value: demoProviders } : providers
  const currentKeys = demo ? { value: demoKeys } : keys
  const query = filter.trim().toLowerCase()
  const requestRows = (currentLogs.value ?? []).slice(0, page === 'Overview' ? 5 : 25).filter(row =>
    `${row.model} ${row.provider} ${row.status}`.toLowerCase().includes(query),
  )
  const modelRows = (currentModels.value ?? []).filter(model => model.id.toLowerCase().includes(query))

  function render<T>(state: Result<T>, content: (value: T) => ReactNode) {
    if (state.error) return <Notice>{state.error}</Notice>
    if (state.value !== undefined) return content(state.value)
    return <Notice>Loading router data…</Notice>
  }

  return <>
    <header className="shell">
      <div className="topbar frame">
        <button className="brand" onClick={() => navigate('Overview')}><span className="brand-mark"><Waypoints size={19} /></span>SeavxRouter</button>
        <span className="workspace"><span className="separator">/</span> Workspace</span>
        <div className="top-actions">
          <Status tone={demo ? 'neutral' : online ? 'ready' : 'neutral'}>{demo ? 'Demo data' : online ? 'Router online' : loading ? 'Connecting' : 'Router offline'}</Status>
          <Button variant="outline" className="connection" disabled={exiting} onClick={exit}><LogOut size={15} />{demo ? 'Exit demo' : 'Sign out'}</Button>
        </div>
      </div>
      <nav className="navigation frame" aria-label="Dashboard">
        {(Object.keys(pages) as Page[]).map(item => {
          const Icon = pages[item].icon
          return <button key={item} onClick={() => navigate(item)} aria-current={page === item ? 'page' : undefined}>
            <Icon size={15} />{item}
          </button>
        })}
      </nav>
    </header>

    <main className="frame main" ref={main}>
      <div className="page-heading">
        <div><h1>{page}</h1><p>{pages[page].description}</p></div>
        <div className="actions">
          {page === 'Overview' && <label className="select"><select aria-label="Usage period" value={days} onChange={event => { setUsage({}); setDays(Number(event.target.value)) }}>
            <option value={7}>Last 7 days</option><option value={30}>Last 30 days</option>
          </select><ChevronDown size={14} /></label>}
          {page === 'API Keys' && <Button disabled={demo || !keys.value} onClick={() => open('create')}><Plus size={16} /> Create key</Button>}
          {!demo && <Button variant="outline" aria-label="Refresh data" title="Refresh data" disabled={loading} onClick={() => setRefresh(previous => previous + 1)}><RefreshCw size={15} /></Button>}
        </div>
      </div>

      {exitError && <div className="offline" role="alert">{exitError}</div>}
      {!demo && !loading && !online && <div className="offline">The router is unavailable.</div>}

      {page === 'Overview' && <>
        <div className="metrics">
          {[
            { title: 'Total requests', value: currentUsage.value && number.format(currentUsage.value.totals.requests), icon: Activity, caption: 'Requests handled' },
            { title: 'Total tokens', value: currentUsage.value && compact.format(currentUsage.value.totals.total_tokens), icon: Layers3, caption: 'Tokens recorded' },
            { title: 'Estimated cost', value: currentUsage.value && dollars.format(currentUsage.value.totals.cost_usd), icon: ArrowDownToLine, caption: 'Across all providers' },
          ].map(metric => <section className="metric panel" key={metric.title}>
            <div>{metric.title}<metric.icon size={16} /></div>
            <strong>{metric.value ?? '—'}</strong><p>{metric.caption}</p>
          </section>)}
        </div>
        <section className="panel chart-panel">
          <div className="panel-heading"><div><h2>Request volume</h2><p>Daily requests across all providers · UTC</p></div><span className="legend"><i /> Requests</span></div>
          {render(currentUsage, value => <UsageChart usage={value} days={days} />)}
        </section>
        <section className="panel">
          <div className="panel-heading"><div><div className="flex flex-wrap items-center gap-2"><h2>Recent requests</h2>{watchRequests && <Status tone={streamStatus === 'Live' ? 'ready' : 'neutral'}>{streamStatus}</Status>}</div><p>Latest activity across your gateway.</p></div><Button variant="outline" onClick={() => navigate('Requests')}>View requests</Button></div>
          {render(currentLogs, () => <RequestTable rows={requestRows} onOpen={openRequest} />)}
        </section>
      </>}

      {(page === 'Requests' || page === 'Models') && <section className="panel">
        <div className="panel-heading flex-wrap"><label className="search"><Search size={15} /><input aria-label={`Filter ${page.toLowerCase()}`} placeholder={page === 'Requests' ? 'Filter this page…' : 'Find a model…'} value={filter} onChange={event => setFilter(event.target.value)} /></label>
          {page === 'Requests' && !demo && <Status tone={watchRequests && streamStatus === 'Live' ? 'ready' : 'neutral'}>{watchRequests ? streamStatus : 'Live updates paused'}</Status>}
        </div>
        {page === 'Requests' ? render(currentLogs, value => <>
          <RequestTable rows={requestRows} onOpen={openRequest} />
          {!demo && (offset > 0 || value.length > 25) && <div className="pagination"><Button variant="outline" disabled={offset === 0 || loading} onClick={() => { setLogs({}); setOffset(previous => previous - 25) }}>Previous</Button><Button variant="outline" disabled={value.length <= 25 || loading} onClick={() => { setLogs({}); setOffset(previous => previous + 25) }}>Next</Button></div>}
        </>) : render(currentModels, () => modelRows.length ? <div className="table-scroll"><table><thead><tr><th>Model ID</th><th>Owner</th></tr></thead><tbody>{modelRows.map(model => <tr key={model.id}><td><ModelName name={model.id} /></td><td>{model.owned_by || '—'}</td></tr>)}</tbody></table></div> : <Notice>No matching models.</Notice>)}
      </section>}

      {page === 'Providers' && render(currentProviders, value => value.length ? <div className="provider-grid">{value.map(provider => <section className="panel" key={provider.provider}>
        <div className="panel-heading"><div><h2>{provider.provider}</h2><p>{provider.default_model || 'Provider accounts'}</p></div><Layers3 size={18} /></div>
        {(provider.accounts ?? []).map((account, index) => <div className="account" key={index}><span>{account.email || account.account_id || `Account ${index + 1}`}</span><Status tone={account.status === 'ready' ? 'ready' : 'limited'}>{account.status}</Status></div>)}
        {!provider.accounts?.length && <Notice>No accounts configured.</Notice>}
      </section>)}</div> : <div className="panel"><Notice>No providers configured.</Notice></div>)}

      {page === 'API Keys' && <section className="panel">{render(currentKeys, value => value.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Prefix</th><th>Monthly budget</th><th>Token limit</th><th>Status</th><th>Action</th></tr></thead>
        <tbody>{value.map(key => <tr key={key.id}><td>{key.name}</td><td><code>{key.prefix}…</code></td><td>{key.budget_usd ? dollars.format(key.budget_usd) : 'Unlimited'}</td><td>{key.limit_token ? number.format(key.limit_token) : 'Unlimited'}</td><td><Status tone={key.revoked_at ? 'neutral' : 'ready'}>{key.revoked_at ? 'Revoked' : 'Active'}</Status></td><td>{!key.revoked_at && <Button variant="ghost" disabled={demo} onClick={() => { setSelectedKey(key); open('revoke') }}>Revoke</Button>}</td></tr>)}</tbody>
      </table></div> : <Notice>No tenant keys created.</Notice>)}</section>}

      <footer><span>SeavxRouter</span><a href="https://github.com/seaavey/SeavxRouter" target="_blank" rel="noreferrer">GitHub <ExternalLink size={13} /></a></footer>
    </main>

    {modal === 'request' && selectedLog && <Modal title={`Request #${selectedLog.id}`} description={selectedLog.model || 'Request details'} onClose={() => setModal(null)}>
      <dl className="detail-list"><dt>Endpoint</dt><dd>{selectedLog.endpoint}</dd><dt>Status</dt><dd>{selectedLog.status}</dd><dt>Latency</dt><dd>{number.format(selectedLog.duration_ms)} ms</dd><dt>Tokens</dt><dd>{number.format(selectedLog.usage.total_tokens)}</dd></dl>
      {!demo && <><h3>Response</h3>{detail ? render(detail, value => <pre>{value.response == null ? 'No response body recorded.' : typeof value.response === 'string' ? value.response : JSON.stringify(value.response, null, 2)}</pre>) : <Notice>Loading response…</Notice>}</>}
    </Modal>}
    {modal === 'create' && <Modal title={createdKey ? 'Key created' : 'Create tenant key'} description={createdKey ? 'Copy the full key now. The router only shows it once.' : 'Set monthly limits for requests using this key. Use zero for unlimited.'} onClose={() => { if (!saving) { setModal(null); setCreatedKey('') } }}>
      {createdKey ? <label>API key<input readOnly value={createdKey} onFocus={event => event.currentTarget.select()} /></label> : <form onSubmit={manageKey}>
        <label>Name<input name="name" required /></label>
        <label>Monthly budget (USD)<input name="budget" type="number" min="0" step="0.01" defaultValue="0" required /></label>
        <label>Monthly token limit<input name="tokens" type="number" min="0" step="1" defaultValue="0" required /></label>
        {actionError && <p role="alert">{actionError}</p>}
        <div className="dialog-actions"><Button type="submit" disabled={saving}>{saving ? 'Creating…' : 'Create key'}</Button></div>
      </form>}
    </Modal>}
    {modal === 'revoke' && selectedKey && <Modal title="Revoke tenant key" description={`Requests using ${selectedKey.name} will lose access immediately.`} onClose={() => { if (!saving) setModal(null) }}>
      <form onSubmit={manageKey}>
        {actionError && <p role="alert">{actionError}</p>}
        <div className="dialog-actions"><Button variant="outline" disabled={saving} onClick={() => setModal(null)}>Cancel</Button><Button type="submit" disabled={saving}>{saving ? 'Revoking…' : 'Revoke key'}</Button></div>
      </form>
    </Modal>}
  </>
}
