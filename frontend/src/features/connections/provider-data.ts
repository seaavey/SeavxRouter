import type { Model, ProviderAccount } from '../../lib/api'

export function accountCounts(accounts: ProviderAccount[] | null) {
  const total = accounts?.length ?? 0
  const active = accounts?.filter(account => account.status === 'ready').length ?? 0
  return { total, active, unavailable: total - active }
}

export function providerModels(models: Model[] | null, provider: string) {
  return (models ?? []).filter(model => model.owned_by === provider)
}
