import { useState } from 'react'
import { faviconURL } from './favicon'

const icons: Record<string, string> = {
  antigravity: 'ico', codex: 'ico', codebuddy: 'svg', qoder: 'svg',
  kiro: 'ico', verboo: 'ico', nvidia: 'ico', groq: 'ico',
  cloudflare: 'ico', opencode: 'ico', caffeine: 'png', coarena: 'ico',
  lyber: 'png', respan: 'ico',
}

export function providerName(id: string) {
  return id.charAt(0).toUpperCase() + id.slice(1)
}

export function ProviderIdentity({ provider, baseURL }: { provider: string; baseURL?: string }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-3">
      <ProviderIcon key={`${provider}:${baseURL}`} provider={provider} baseURL={baseURL} />
      <span className="truncate">{providerName(provider)}</span>
    </span>
  )
}

function ProviderIcon({ provider, baseURL }: { provider: string; baseURL?: string }) {
  const [failed, setFailed] = useState(false)
  const extension = icons[provider]
  const source = faviconURL(baseURL) ?? (extension ? `/providers/${provider}.${extension}` : undefined)
  if (!source || failed) {
    return <span aria-hidden className="grid size-5 shrink-0 place-items-center rounded bg-muted text-xs text-muted-foreground">{providerName(provider).charAt(0)}</span>
  }
  return <img src={source} referrerPolicy="no-referrer" alt="" aria-hidden width={20} height={20} className="size-5 shrink-0 rounded-sm object-contain" onError={() => setFailed(true)} />
}
