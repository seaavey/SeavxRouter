import assert from 'node:assert/strict'
import test from 'node:test'
import { accountCounts, providerModels } from '../src/features/connections/provider-data.ts'

test('counts only ready accounts as active and accepts missing accounts', () => {
  assert.deepEqual(accountCounts(null), { total: 0, active: 0, unavailable: 0 })
  assert.deepEqual(accountCounts([{ provider: 'qoder', status: 'ready' }, { provider: 'qoder', status: 'limited' }]), { total: 2, active: 1, unavailable: 1 })
})

test('selects models by exact owner rather than slash-containing model IDs', () => {
  const models = [{ id: 'vendor/model', owned_by: 'caffeine' }, { id: 'caffeine/wrong', owned_by: 'other' }]
  assert.deepEqual(providerModels(models, 'caffeine'), [models[0]])
  assert.deepEqual(providerModels(null, 'unknown'), [])
})
