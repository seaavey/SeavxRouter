import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeBaseURL, validateAPIKey, validateBaseURL, validateProviderName } from '../src/features/connections/provider-sdk.ts'

test('provider names follow the backend rule that makes them a model prefix', () => {
  assert.equal(validateProviderName('myrouter'), undefined)
  assert.equal(validateProviderName(''), 'Name this provider.')
  assert.ok(validateProviderName('9start'))
  assert.ok(validateProviderName('has space'))
  assert.ok(validateProviderName('a'.repeat(33)))
  assert.equal(validateProviderName('a'.repeat(32)), undefined)
})

test('base URLs require http(s), a host, and no query or fragment', () => {
  assert.equal(validateBaseURL('https://api.example.com/v1'), undefined)
  assert.equal(validateBaseURL('http://127.0.0.1:9101'), undefined)
  assert.ok(validateBaseURL('ftp://api.example.com'))
  assert.ok(validateBaseURL('api.example.com'))
  assert.ok(validateBaseURL('https://api.example.com/v1?x=1'))
  assert.ok(validateBaseURL('https://api.example.com/v1#top'))
  assert.ok(validateBaseURL(''))
})

test('API keys must be present and bounded', () => {
  assert.equal(validateAPIKey('sk-test'), undefined)
  assert.ok(validateAPIKey('   '))
  assert.ok(validateAPIKey('k'.repeat(4097)))
})

test('the stored base URL drops trailing slashes only', () => {
  assert.equal(normalizeBaseURL(' https://api.example.com/v1/ '), 'https://api.example.com/v1')
  assert.equal(normalizeBaseURL('http://127.0.0.1:9101'), 'http://127.0.0.1:9101')
})
