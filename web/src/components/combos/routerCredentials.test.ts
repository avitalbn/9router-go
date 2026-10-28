import assert from 'node:assert'
import { describe, it } from 'node:test'
import type { ProviderConnection, ProviderNode } from '../../api/client'
import {
  providerHasCredentials,
  resolveRouterProvider,
  routerCredentialWarning,
  routerHasCredentials,
} from './routerCredentials'

function conn(provider: string, isActive: number, extra: Partial<ProviderConnection> = {}): ProviderConnection {
  return {
    id: `${provider}-${isActive}`,
    provider,
    authType: 'apikey',
    name: null,
    email: null,
    priority: 1,
    isActive,
    data: '{}',
    createdAt: '',
    updatedAt: '',
    ...extra,
  }
}

describe('routerCredentials', () => {
  it('has credentials when the provider has an active connection', () => {
    assert.strictEqual(routerHasCredentials('openrouter/typesafe/jev-1.13', [conn('openrouter', 1)]), true)
    assert.strictEqual(routerCredentialWarning('openrouter/typesafe/jev-1.13', [conn('openrouter', 1)]), null)
  })

  it('lacks credentials when the provider only has inactive connections', () => {
    const connections = [conn('openrouter', 0), conn('claude', 1)]
    assert.strictEqual(routerHasCredentials('openrouter/typesafe/jev-1.13', connections), false)
  })

  it('lacks credentials when the provider has no connection at all', () => {
    assert.strictEqual(routerHasCredentials('openrouter/typesafe/jev-1.13', [conn('claude', 1)]), false)
    assert.strictEqual(routerHasCredentials('openrouter/typesafe/jev-1.13', []), false)
  })

  it('warns with the provider named in the message', () => {
    assert.strictEqual(
      routerCredentialWarning('openrouter/typesafe/jev-1.13', []),
      'No credentials for openrouter: System One routing is skipped and the combo uses its saved order. Connect openrouter or pick another router model.'
    )
  })

  it('treats a registry no-auth provider as credentialed without a connection', () => {
    assert.strictEqual(routerHasCredentials('opencode/big-pickle', []), true)
    assert.strictEqual(providerHasCredentials('gemini-web', []), true)
  })

  it('does not trust the catalog noAuth flag over the registry', () => {
    // devin-cli is catalog noAuth but absent from the Go registry, so the
    // gateway logs "no credentials" for it.
    assert.strictEqual(providerHasCredentials('devin-cli', []), false)
  })

  it('resolves an alias prefix to its provider before checking connections', () => {
    assert.strictEqual(resolveRouterProvider('ag/gemini-3-pro'), 'antigravity')
    assert.strictEqual(routerHasCredentials('ag/gemini-3-pro', [conn('antigravity', 1)]), true)
    assert.strictEqual(routerHasCredentials('ag/gemini-3-pro', [conn('antigravity', 0)]), false)
    assert.strictEqual(routerHasCredentials('oc/jev-1.13-free', []), true)
  })

  it('falls back to the default router model when empty', () => {
    assert.strictEqual(resolveRouterProvider(''), 'opencode')
    assert.strictEqual(resolveRouterProvider('   '), 'opencode')
    assert.strictEqual(routerHasCredentials('', []), true)
    assert.strictEqual(routerHasCredentials(undefined, []), true)
  })

  it('resolves a custom provider node prefix to the node id', () => {
    const nodes = [{ id: 'openai-compatible-chat-abc', type: 'openai-compatible', name: 'Mine', prefix: 'mine' }] as ProviderNode[]
    assert.strictEqual(resolveRouterProvider('mine/model-x', [], nodes), 'openai-compatible-chat-abc')
    assert.strictEqual(routerHasCredentials('mine/model-x', [], nodes), false)
    assert.strictEqual(
      routerHasCredentials('mine/model-x', [conn('openai-compatible-chat-abc', 1)], nodes),
      true
    )
  })

  it('does not judge a router string that has no provider prefix', () => {
    assert.strictEqual(resolveRouterProvider('my-model-alias'), null)
    assert.strictEqual(routerHasCredentials('my-model-alias', []), true)
  })
})
