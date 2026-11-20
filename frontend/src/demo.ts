import type { Model, Provider, RequestLog, TenantKey, Usage } from './api'

const volumes = [86, 120, 98, 152, 131, 112, 183, 139, 162, 121, 205, 179, 145, 192, 167, 210, 185, 138, 235, 197, 158, 203, 176, 240, 208, 231, 196, 255, 224, 187]

export function demoUsage(days: number): Usage {
  const data = volumes.slice(-days).map((requests, index, values) => {
    const date = new Date()
    date.setUTCDate(date.getUTCDate() - (values.length - 1 - index))
    return { key: date.toISOString().slice(0, 10), requests, total_tokens: requests * 817, cost_usd: requests * 0.00135 }
  })
  const totals = data.reduce((sum, row) => ({
    requests: sum.requests + row.requests,
    total_tokens: sum.total_tokens + row.total_tokens,
    cost_usd: sum.cost_usd + row.cost_usd,
  }), { requests: 0, total_tokens: 0, cost_usd: 0 })
  return { totals, data }
}

export const demoModels: Model[] = [
  { id: 'codex/gpt-5', owned_by: 'codex' },
  { id: 'groq/llama-3.3-70b', owned_by: 'groq' },
  { id: 'qoder/sonus', owned_by: 'qoder' },
]

export const demoProviders: Provider[] = demoModels.map(model => ({
  provider: model.owned_by!,
  default_model: model.id,
  accounts: [{ account_id: 'workspace', status: 'ready' }],
}))

export const demoLogs: RequestLog[] = [0, 1, 2, 0, 1, 0, 2, 1].map((model, index) => ({
  id: String(1042 - index),
  timestamp: new Date(Date.now() - (index + 1) * 180000).toISOString(),
  model: demoModels[model].id,
  provider: demoModels[model].owned_by!,
  endpoint: index === 3 ? 'responses' : 'chat',
  status: index === 5 ? 429 : 200,
  duration_ms: [1420, 680, 2150, 910, 740, 56, 1940, 810][index],
  usage: { total_tokens: [1248, 836, 2104, 964, 782, 0, 1657, 980][index] },
}))

export const demoKeys: TenantKey[] = [
  { id: 1, name: 'Development', prefix: 'sk-svr-dev', budget_usd: 20, limit_token: 0 },
  { id: 2, name: 'Staging', prefix: 'sk-svr-stg', budget_usd: 5, limit_token: 500000 },
]
